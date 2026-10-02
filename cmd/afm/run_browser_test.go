package main

import (
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/browser"
	"github.com/akopichin/afm/pkg/config"
)

func stubBrowserPreflight(t *testing.T, st config.BrowserStatus, err error) {
	t.Helper()
	prev := browserPreflightFn
	t.Cleanup(func() { browserPreflightFn = prev })
	browserPreflightFn = func(config.Config, browser.BrowserMode) (config.BrowserStatus, error) {
		return st, err
	}
}

func withDockerExecSeam(t *testing.T, fn func(argv []string) (string, int)) {
	t.Helper()
	prev := dockerProtocolProbe
	t.Cleanup(func() { dockerProtocolProbe = prev })
	dockerProtocolProbe = fn
}

func managerStillListening(m *browser.Manager) bool {
	c, err := net.Dial("tcp", m.Addr())
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func argvAfterImage(argv []string, image string) []string {
	for i, a := range argv {
		if a == image {
			return argv[i+1:]
		}
	}
	return nil
}

func TestStartBrowser_NativeOwnsAndCleansUp(t *testing.T) {
	stubBrowserPreflight(t, config.BrowserStatus{Available: true}, nil)
	m, d, cleanup, err := startBrowser(config.Config{}, browser.BrowserModeNative)
	if err != nil || m == nil {
		t.Fatalf("want live manager, err=%v", err)
	}
	if !strings.HasPrefix(d.Endpoint, "127.0.0.1:") {
		t.Fatalf("native endpoint must be loopback, got %q", d.Endpoint)
	}
	if d.Epoch == "" {
		t.Fatal("startBrowser must generate a non-empty epoch")
	}
	if d.AppURL != "http://127.0.0.1:18080" { // native: app on host port directly, no publish
		t.Fatalf("native app URL wrong: %q", d.AppURL)
	}
	if !managerStillListening(m) {
		t.Fatal("manager must be serving before cleanup")
	}
	cleanup()
	if managerStillListening(m) {
		t.Fatal("cleanup must close the manager")
	}
}

func TestStartBrowser_OffWhenDisabled(t *testing.T) {
	fa := false
	m, _, cleanup, err := startBrowser(config.Config{Browser: config.BrowserConfig{Enabled: &fa}}, browser.BrowserModeNative)
	if err != nil || m != nil {
		t.Fatalf("disabled must be nil manager, got m=%v err=%v", m, err)
	}
	cleanup() // must be a safe no-op
}

func TestPickFreeLoopbackPort_IsBindable(t *testing.T) {
	p, err := pickFreeLoopbackPort()
	if err != nil {
		t.Fatalf("pick failed: %v", err)
	}
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		t.Fatalf("picked port not bindable: %v", err)
	}
	_ = l.Close()
}

func TestDescriptorFromEnv_OnlyInContainer(t *testing.T) {
	t.Setenv("AFM_BROWSER_ENDPOINT", "host.docker.internal:5001")
	t.Setenv("AFM_BROWSER_TRANSPORT_TOKEN", "tok")
	t.Setenv("AFM_BROWSER_APP_URL", "http://127.0.0.1:54321")
	t.Setenv("AFM_BROWSER_EPOCH", "ep1")
	if d := descriptorFromEnv(false); d != nil {
		t.Fatal("host must not build a transport descriptor from env")
	}
	d := descriptorFromEnv(true)
	if d == nil || d.StageEndpoint("build") != "ws://host.docker.internal:5001/session/ep1/build" {
		t.Fatalf("in-container descriptor wrong: %+v", d)
	}
	if d.Token != "tok" || d.AppURL != "http://127.0.0.1:54321" {
		t.Fatalf("descriptor fields wrong: %+v", d)
	}
}

func TestProbeImageBrowserProtocol_Compatibility(t *testing.T) {
	var gotArgv []string
	withDockerExecSeam(t, func(argv []string) (string, int) {
		gotArgv = argv
		return fmt.Sprintf("%d\n", browser.TransportProtocolVersion), 0
	})
	v, err := probeImageBrowserProtocol("img")
	if err != nil || v != browser.TransportProtocolVersion {
		t.Fatalf("matching image must be compatible: v=%d err=%v", v, err)
	}
	// Entrypoint prepends `afm`, so the probe must pass ONLY `browser
	// protocol-version` — never a leading `afm` (which would become `afm afm …`).
	if a := argvAfterImage(gotArgv, "img"); !reflect.DeepEqual(a, []string{"browser", "protocol-version"}) {
		t.Fatalf("probe must run exactly `browser protocol-version` (no leading afm), got %q", a)
	}
}

func TestProbeImageBrowserProtocol_IncompatibleSeverity(t *testing.T) {
	stubBrowserPreflight(t, config.BrowserStatus{Available: true}, nil)

	// Older image: different integer → EnableOn aborts with a clear error.
	withDockerExecSeam(t, func([]string) (string, int) { return "0\n", 0 })
	tr := true
	onCfg := config.Config{Browser: config.BrowserConfig{Enabled: &tr}, Docker: config.DockerConfig{Image: "img"}}
	if _, _, _, err := startBrowser(onCfg, browser.BrowserModeDocker); err == nil || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("version mismatch under EnableOn must be a clear hard error, got %v", err)
	}

	// Older image with no subcommand → non-zero exit; EnableAuto degrades.
	withDockerExecSeam(t, func([]string) (string, int) { return "unknown command \"protocol-version\"\n", 1 })
	autoCfg := config.Config{Docker: config.DockerConfig{Image: "img"}} // nil enabled → auto
	m, _, _, err := startBrowser(autoCfg, browser.BrowserModeDocker)
	if err != nil || m != nil {
		t.Fatalf("auto + incompatible image must degrade silently, got m=%v err=%v", m, err)
	}
}

func TestStartBrowser_DockerFreshAndResumedGetFreshEpoch(t *testing.T) {
	stubBrowserPreflight(t, config.BrowserStatus{Available: true}, nil)
	withDockerExecSeam(t, func([]string) (string, int) {
		return fmt.Sprintf("%d\n", browser.TransportProtocolVersion), 0
	})
	cfg := config.Config{Docker: config.DockerConfig{Image: "img"}}
	m1, d1, cl1, err := startBrowser(cfg, browser.BrowserModeDocker)
	if err != nil || m1 == nil {
		t.Fatalf("docker start: %v", err)
	}
	defer cl1()
	m2, d2, cl2, err := startBrowser(cfg, browser.BrowserModeDocker)
	if err != nil || m2 == nil {
		t.Fatalf("docker start 2: %v", err)
	}
	defer cl2()
	if d1.Epoch == "" || d2.Epoch == "" || d1.Epoch == d2.Epoch {
		t.Fatalf("each start must get a fresh epoch: %q vs %q", d1.Epoch, d2.Epoch)
	}
	if !strings.HasPrefix(d1.Endpoint, "host.docker.internal:") {
		t.Fatalf("docker endpoint must be host.docker.internal, got %q", d1.Endpoint)
	}
	if !strings.HasPrefix(d1.AppURL, "http://127.0.0.1:") {
		t.Fatalf("docker app URL must be a host loopback publish, got %q", d1.AppURL)
	}
}
