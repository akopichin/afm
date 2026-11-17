//go:build unix

package serve

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/akopichin/afm/pkg/runregistry"
)

// The test binary doubles as the afm that the host spawns: TestMain hands control to fakeAFM when the
// environment says so, so tests run real child processes without a built afm.
const fakeEnv = "SERVE_FAKE_AFM"

func init() {
	if os.Getenv(fakeEnv) == "1" {
		os.Exit(fakeAFM(os.Args[1:]))
	}
}

// fakeReport is what the fake child writes about itself to SERVE_FAKE_OUT.
type fakeReport struct {
	Args     []string `json:"args"`
	Env      []string `json:"env"`
	Cwd      string   `json:"cwd"`
	PID      int      `json:"pid"`
	Sid      int      `json:"sid"`
	StdinEOF bool     `json:"stdin_eof"`
	Task     string   `json:"task"`
}

// SERVE_FAKE_BEHAVIOR: hold (default), crash (exit 3), silent, finish (exit 0 soon), unready, stubborn (unready,
// ignores SIGINT), early (record without endpoint), docker-orphan (Docker record that outlives the process).
func fakeAFM(args []string) int {
	flags := map[string]string{}
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "--") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			flags[args[i]] = args[i+1]
			i++
		}
	}
	report := fakeReport{Args: args, Env: os.Environ(), PID: os.Getpid(), Sid: mustSid()}
	report.Cwd, _ = os.Getwd()
	buf := make([]byte, 1)
	_, err := os.Stdin.Read(buf)
	report.StdinEOF = err != nil
	if path, ok := strings.CutPrefix(flags["--task"], "@"); ok {
		data, _ := os.ReadFile(path)
		report.Task = string(data)
	}
	if out := os.Getenv("SERVE_FAKE_OUT"); out != "" {
		data, _ := json.Marshal(report)
		f, _ := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		_, _ = f.Write(append(data, '\n'))
		_ = f.Close()
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	switch os.Getenv("SERVE_FAKE_BEHAVIOR") {
	case "crash":
		return 3
	case "silent":
		<-stop
		return 0
	case "docker-orphan":
		marker, _ := runregistry.ProcessStart(os.Getpid())
		_ = runregistry.Publish(os.Getenv("SERVE_FAKE_REGISTRY"), runregistry.Record{
			InstanceID: flags["--instance-id"], LaunchID: flags["--launch-id"], PID: os.Getpid(), ProcessStart: marker,
			Mode: flags["--mode"], Docker: true, APIURL: "http://127.0.0.1:1", RunID: "fake-1", Ready: true,
			StartedAt: time.Now(), UpdatedAt: time.Now(),
		})
		return 0
	case "early":
		marker, _ := runregistry.ProcessStart(os.Getpid())
		_ = runregistry.Publish(os.Getenv("SERVE_FAKE_REGISTRY"), runregistry.Record{
			InstanceID: flags["--instance-id"], LaunchID: flags["--launch-id"], PID: os.Getpid(), ProcessStart: marker,
			Mode: flags["--mode"], StartedAt: time.Now(), UpdatedAt: time.Now(),
		})
		<-stop
		return 0
	}
	behavior := os.Getenv("SERVE_FAKE_BEHAVIOR")
	ready := behavior != "unready" && behavior != "stubborn"
	if behavior == "stubborn" {
		signal.Ignore(os.Interrupt)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 4
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/runtime", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Runtime{ProtocolVersion: 1, Mode: flags["--mode"], RunID: "fake-1", Ready: ready, InstanceID: flags["--instance-id"]})
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	regDir := os.Getenv("SERVE_FAKE_REGISTRY")
	marker, _ := runregistry.ProcessStart(os.Getpid())
	_ = runregistry.Publish(regDir, runregistry.Record{
		InstanceID: flags["--instance-id"], LaunchID: flags["--launch-id"], PID: os.Getpid(), ProcessStart: marker,
		Mode: flags["--mode"], APIURL: "http://" + ln.Addr().String(), RunID: "fake-1", Ready: ready,
		RunsDir: os.Getenv("SERVE_FAKE_RUNS_DIR"), StartedAt: time.Now(), UpdatedAt: time.Now(),
	})
	defer func() { _ = runregistry.Remove(regDir, flags["--instance-id"]) }()
	if behavior == "finish" {
		time.Sleep(300 * time.Millisecond)
	} else {
		<-stop
	}
	_ = srv.Shutdown(context.Background())
	return 0
}

func mustSid() int {
	sid, err := unix.Getsid(0)
	if err != nil {
		return -1
	}
	return sid
}

func readReports(t *testing.T, path string) []fakeReport {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []fakeReport
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r fakeReport
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("report line %q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}
