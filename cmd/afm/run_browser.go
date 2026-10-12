package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/akopichin/afm/pkg/browser"
	"github.com/akopichin/afm/pkg/config"
)

// browserPreflightFn resolves host prerequisites (seam for tests).
var browserPreflightFn = func(cfg config.Config, mode browser.BrowserMode) (config.BrowserStatus, error) {
	return cfg.BrowserPreflight(mode)
}

// dockerProtocolProbe runs `docker <argv>` capturing combined output + exit code
// (seam for tests). argv already starts with "run" (no leading "docker").
var dockerProtocolProbe = func(argv []string) (string, int) {
	cmd := exec.Command("docker", argv...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	return string(out), code
}

// startBrowser builds and owns the managed browser Manager for a run. It generates its own random epoch
// (never a run id).
func startBrowser(cfg config.Config, mode browser.BrowserMode) (*browser.Manager, *browser.Descriptor, func(), error) {
	noop := func() {}
	st, err := browserPreflightFn(cfg, mode)
	if err != nil {
		return nil, nil, noop, err // EnableOn hard failure
	}
	if !st.Available {
		return nil, nil, noop, nil // off or auto-degraded
	}

	enable := cfg.Browser.EffectiveEnable()

	// Docker: the pinned image's transport must match the host before we commit.
	if mode == browser.BrowserModeDocker {
		if perr := checkImageProtocol(cfg); perr != nil {
			if enable == config.EnableOn {
				return nil, nil, noop, perr
			}
			fmt.Fprintf(os.Stderr, "warning: browser disabled: %v\n", perr)
			return nil, nil, noop, nil
		}
	}

	epoch, err := browser.NewEpoch()
	if err != nil {
		return nil, nil, noop, err
	}
	mgr, err := browser.New(browser.Options{
		ChromePath: cfg.Browser.ChromePath(),
		Headless:   cfg.Browser.IsHeadless(),
		Epoch:      epoch,
	})
	if err != nil {
		return nil, nil, noop, degradeOrError(enable, err)
	}
	endpoint, appURL, err := browserEndpointAndAppURL(cfg, mode, mgr)
	if err != nil {
		_ = mgr.Close()
		return nil, nil, noop, degradeOrError(enable, err)
	}
	desc := &browser.Descriptor{
		Version:  browser.TransportProtocolVersion,
		Endpoint: endpoint,
		Token:    mgr.Token(),
		AppURL:   appURL,
		Epoch:    epoch,
		Docker:   mode == browser.BrowserModeDocker,
	}
	return mgr, desc, func() { _ = mgr.Close() }, nil
}

// degradeOrError returns err when the capability is explicitly on, else nil (auto
// degrade — a CLI/API-only flow must not fail because Chrome could not start).
func degradeOrError(enable config.EnableState, err error) error {
	if enable == config.EnableOn {
		return err
	}
	fmt.Fprintf(os.Stderr, "warning: browser disabled: %v\n", err)
	return nil
}

// browserEndpointAndAppURL produces the WS endpoint host and the tested-app URL. Native: loopback endpoint
// and the app served on the host port directly.
func browserEndpointAndAppURL(cfg config.Config, mode browser.BrowserMode, mgr *browser.Manager) (string, string, error) {
	_, port, err := net.SplitHostPort(mgr.Addr())
	if err != nil {
		return "", "", fmt.Errorf("browser: manager addr: %w", err)
	}
	if mode == browser.BrowserModeDocker {
		hostPort, perr := pickFreeLoopbackPort()
		if perr != nil {
			return "", "", perr
		}
		return "host.docker.internal:" + port, fmt.Sprintf("http://127.0.0.1:%d", hostPort), nil
	}
	return "127.0.0.1:" + port, fmt.Sprintf("http://127.0.0.1:%d", cfg.Browser.GetAppPort()), nil
}

// checkImageProtocol probes the pinned image's transport protocol and compares it
// to the host's compiled-in version.
func checkImageProtocol(cfg config.Config) error {
	v, err := probeImageBrowserProtocol(cfg.Docker.GetImage())
	if err != nil {
		return err
	}
	if v != browser.TransportProtocolVersion {
		return fmt.Errorf("browser transport protocol mismatch: host %d, image %d — rebuild/pull the matching afm image", browser.TransportProtocolVersion, v)
	}
	return nil
}

// probeImageBrowserProtocol runs `docker run --rm <image> browser protocol-version` — ARGS ONLY, no
// leading "afm": the image ENTRYPOINT already exec's /usr/local/bin/afm "$@".
func probeImageBrowserProtocol(image string) (int, error) {
	out, code := dockerProtocolProbe([]string{"run", "--rm", image, "browser", "protocol-version"})
	if code != 0 {
		return 0, fmt.Errorf("image %q: browser protocol probe failed (exit %d): %s", image, code, strings.TrimSpace(out))
	}
	v, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("image %q: unparseable browser protocol version %q", image, strings.TrimSpace(out))
	}
	return v, nil
}

// pickFreeLoopbackPort returns a currently-free loopback TCP port. The TOCTOU
// window to the real bind is tiny; a late bind failure is a clear startup error.
func pickFreeLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// descriptorFromEnv rebuilds the runtime descriptor from the AFM_BROWSER_* env the host forwarded into the
// container.
func descriptorFromEnv(inContainer bool) *browser.Descriptor {
	if !inContainer {
		return nil
	}
	endpoint := os.Getenv("AFM_BROWSER_ENDPOINT")
	token := os.Getenv("AFM_BROWSER_TRANSPORT_TOKEN")
	epoch := os.Getenv("AFM_BROWSER_EPOCH")
	if endpoint == "" || token == "" || epoch == "" {
		return nil
	}
	return &browser.Descriptor{
		Version:  browser.TransportProtocolVersion,
		Endpoint: endpoint,
		Token:    token,
		AppURL:   os.Getenv("AFM_BROWSER_APP_URL"),
		Epoch:    epoch,
		Docker:   true, // descriptorFromEnv only runs in-container (ReExecedIntoContainer)
	}
}

// portFromURL extracts the TCP port from an http URL (0 if absent/unparseable).
func portFromURL(raw string) int {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	p, _ := strconv.Atoi(u.Port())
	return p
}
