package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/akopichin/afm/pkg/runregistry"
)

const (
	listenHost      = "127.0.0.1"
	shutdownTimeout = 5 * time.Second
	retentionEvery  = 30 * time.Second
	headerReadLimit = 10 * time.Second
	DefaultPort     = 8100
	mcpPath         = "/mcp"
)

// Options configures one host process.
type Options struct {
	// StateDir is ~/.afm/serve unless set; RegistryDir is ~/.afm/active-runs unless set.
	StateDir    string
	RegistryDir string
	Config      Config
	// Binary is the afm executable that children run.
	Binary  string
	Env     []string
	Groups  HostGroups
	Port    int
	Version string
	// Log receives one line per notable event; nil discards.
	Log io.Writer
}

// App is a running host: the state lock, the launcher, the gateway and the HTTP listener. Closing it releases
// the lock and stops serving; the runs it started or attached keep running.
type App struct {
	opts      Options
	host      *Host
	listener  net.Listener
	launcher  *Launcher
	inventory *Inventory
	sessions  *Sessions
	control   *Controller
	gateway   *Gateway
	http      *http.Server
}

// Open takes the state lock, adopts launches left in flight by a previous host, forgets registry records of
// processes that are provably gone, and binds the loopback listener. A second host gets *LockedError.
func Open(opts Options) (*App, error) {
	home, err := os.UserHomeDir()
	if err != nil && (opts.StateDir == "" || opts.RegistryDir == "") {
		return nil, fmt.Errorf("find the home directory: %w", err)
	}
	if opts.StateDir == "" {
		opts.StateDir, _ = DefaultDir()
	}
	if opts.RegistryDir == "" {
		opts.RegistryDir = runregistry.Dir(home)
	}
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	host, err := AcquireHost(opts.StateDir, os.Getpid(), time.Now())
	if err != nil {
		return nil, err
	}
	a := &App{opts: opts, host: host}
	if err := a.assemble(); err != nil {
		host.Close()
		return nil, err
	}
	return a, nil
}

func (a *App) assemble() error {
	opts := a.opts
	discovery := &Discovery{RegistryDir: opts.RegistryDir, Now: time.Now}
	a.inventory = &Inventory{Host: a.host, Discovery: discovery, Now: time.Now}
	a.sessions = &Sessions{}
	a.launcher = NewLauncher(LauncherConfig{
		Host: a.host, Config: opts.Config, Discovery: discovery, Binary: opts.Binary, Env: opts.Env,
		Groups: opts.Groups.Child.Names(),
	})
	a.control = &Controller{Host: a.host, Runs: a.inventory, Status: a.sessions, ReleaseAfter: opts.Config.Retention.ReleaseAfter}
	// Records of exited processes are saved and removed before any launch is watched again, so no watcher sees
	// a half-reaped registry.
	if err := a.reapExited(discovery); err != nil {
		return fmt.Errorf("scan registry: %w", err)
	}
	if err := a.launcher.Recover(); err != nil {
		return fmt.Errorf("recover launches: %w", err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(listenHost, strconv.Itoa(opts.Port)))
	if err != nil {
		a.launcher.Close()
		return fmt.Errorf("listen: %w", err)
	}
	a.listener = listener
	if err := a.host.SetEndpoint("http://" + listener.Addr().String() + mcpPath); err != nil {
		a.launcher.Close()
		_ = listener.Close()
		return err
	}
	a.gateway = NewGateway(GatewayConfig{
		Inventory: a.inventory, Launcher: a.launcher, Controller: a.control, Sessions: a.sessions,
		Groups: opts.Groups, Version: opts.Version,
	})
	mux := http.NewServeMux()
	mux.Handle(mcpPath, a.gateway.Handler())
	// No WriteTimeout: MCP responses stream for as long as a forwarded wait lasts.
	a.http = &http.Server{Handler: mux, ReadHeaderTimeout: headerReadLimit}
	return nil
}

// reapExited saves what every record and its answering endpoint show about the runs of owned processes (a live one
// may exit before anything else looks), then removes the records of provably gone processes, which would pile up;
// a record whose knowledge could not be saved stays for the next start.
func (a *App) reapExited(d *Discovery) error {
	// Read before the scan, like every observation, so a record is merged against what the host knew before it.
	receipts, err := a.host.Receipts()
	var attached []Attached
	if err == nil {
		attached, err = a.host.LoadAttached()
	}
	if err != nil {
		a.logf("kept every registry record: the host state is unreadable: %v\n", err)
		return nil
	}
	observed, err := d.Scan(context.Background())
	if err != nil {
		return err
	}
	keep := func(obs Observation) error { return a.keepObserved(obs, receipts, attached) }
	for _, obs := range observed {
		// Saved before the recheck: an exiting process may unlink its record first, and the recheck then finds none.
		if err := keep(obs); err != nil {
			a.logf("could not save the runs of %s: %v\n", obs.Record.RunID, err)
			continue
		}
		removed, err := d.Reap(context.Background(), obs, keep)
		switch {
		case err != nil:
			a.logf("kept the record of exited run %s: %v\n", obs.Record.RunID, err)
		case removed:
			a.logf("removed the record of exited run %s\n", obs.Record.RunID)
		default:
			// Not exited, or changed since the scan: a later start looks again.
		}
	}
	return nil
}

// keepObserved saves what a record, and the endpoint that answered for it, say about the runs of a process on the
// receipt of its launch or on its attachment; receipts and attached are what the host knew before it scanned.
func (a *App) keepObserved(obs Observation, receipts []Receipt, attached []Attached) error {
	rec := obs.Record
	if r := receiptFor(receipts, rec.LaunchID); r != nil && recordOfLaunch(*r, rec) {
		_, err := a.host.UpdateReceipt(r.LaunchID, time.Now(), func(cur *Receipt) { cur.bindRecord(rec, obs.Runtime, r.RunID) })
		return err
	}
	if cur := attachedFor(attached, rec.InstanceID); cur != nil {
		_, err := a.inventory.rebind(*cur, cur.RunID, recordBinding(rec, obs.Runtime))
		return err
	}
	return nil
}

func (a *App) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(a.opts.Log, format, args...) // a log that cannot be written is not worth failing for
}

// Endpoint is the URL of the MCP endpoint.
func (a *App) Endpoint() string { return "http://" + a.listener.Addr().String() + mcpPath }

// Serve answers MCP requests until ctx ends, then shuts down gracefully: sessions close, the lock is released
// and the runs are left alone.
func (a *App) Serve(ctx context.Context) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	errs := make(chan error, 1)
	go func() { errs <- a.http.Serve(a.listener) }()
	retentionDone := a.startRetention(ctx)

	var serveErr error
	select {
	case serveErr = <-errs:
	case <-ctx.Done():
	}
	stop()
	<-retentionDone
	// Streams must end first or Shutdown waits for them.
	a.gateway.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := a.http.Shutdown(shutdownCtx)
	a.launcher.Close()
	closeErr := a.host.Close()
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(serveErr, err, closeErr)
}

// startRetention releases finished runs that were held too long; the returned channel closes when it stops.
func (a *App) startRetention(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if a.control.ReleaseAfter <= 0 {
			return
		}
		ticker := time.NewTicker(retentionEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				views, err := a.inventory.List(ctx)
				if err != nil {
					continue
				}
				for _, runID := range a.control.Sweep(ctx, views) {
					a.logf("released finished run %s after %s\n", runID, a.control.ReleaseAfter)
				}
			}
		}
	}()
	return done
}

// Close releases the lock without serving; it is for a host that failed before Serve.
func (a *App) Close() error {
	a.launcher.Close()
	_ = a.listener.Close()
	return a.host.Close()
}
