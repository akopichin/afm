package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTask_Sources(t *testing.T) {
	file := filepath.Join(t.TempDir(), "task.txt")
	if err := os.WriteFile(file, []byte("  from file \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte("  from url\n"))
	}))
	defer srv.Close()

	tests := []struct {
		name  string
		value string
		stdin string
		want  string
	}{
		{name: "literal text", value: "  fix the bug\n", want: "  fix the bug\n"},
		{name: "file", value: "@" + file, want: "  from file \n"},
		{name: "stdin", value: "@-", stdin: "\nfrom stdin\n\n", want: "\nfrom stdin\n\n"},
		{name: "url", value: srv.URL + "/task.md", want: "  from url\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loadTask(t.Context(), tt.value, strings.NewReader(tt.stdin))
			if err != nil || got != tt.want {
				t.Fatalf("loadTask = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestLoadTask_Refusals(t *testing.T) {
	dir := t.TempDir()
	huge := filepath.Join(dir, "huge.txt")
	if err := os.WriteFile(huge, []byte(strings.Repeat("a", maxTaskBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	badUTF8 := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(badUTF8, []byte{0xff, 0xfe, 'a'}, 0o600); err != nil {
		t.Fatal(err)
	}
	notText := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("x"))
	}))
	defer notText.Close()
	notFound := httptest.NewServer(http.NotFoundHandler())
	defer notFound.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
	}))
	defer redirect.Close()
	bigBody := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("a", maxTaskBytes+1)))
	}))
	defer bigBody.Close()

	tests := []struct {
		name  string
		value string
		stdin string
		want  string
	}{
		{name: "empty", value: "", want: "empty"},
		{name: "blank", value: " \n", want: "empty"},
		{name: "blank stdin", value: "@-", stdin: " \n\t", want: "empty"},
		{name: "empty stdin", value: "@-", want: "empty"},
		{name: "missing file", value: "@" + filepath.Join(dir, "none"), want: "read task file"},
		{name: "oversized file", value: "@" + huge, want: "larger than"},
		{name: "oversized stdin", value: "@-", stdin: strings.Repeat("a", maxTaskBytes+1), want: "larger than"},
		{name: "oversized literal", value: strings.Repeat("a", maxTaskBytes+1), want: "larger than"},
		{name: "invalid utf-8", value: "@" + badUTF8, want: "UTF-8"},
		{name: "userinfo", value: "https://user:pass@example.com/t", want: "credentials"}, //nolint:gosec // G101: deliberate fake credentials for the rejection test
		{name: "not text", value: notText.URL + "/t", want: "text/*"},
		{name: "http error", value: notFound.URL + "/t", want: "404"},
		{name: "redirect to file", value: redirect.URL + "/t", want: "redirect"},
		{name: "oversized url body", value: bigBody.URL + "/t", want: "larger than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadTask(t.Context(), tt.value, strings.NewReader(tt.stdin))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("loadTask error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

// Nested net/http diagnostics embed the full URL (a malformed Location, a dial error): none of them may reach the error.
func TestLoadTask_FetchErrorsNeverLeakSecrets(t *testing.T) {
	badLocation := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://user:PASSWORD@host/%zz?token=SECRET")
		w.WriteHeader(http.StatusFound)
	}))
	defer badLocation.Close()
	credRedirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://user:PASSWORD@example.com/t?token=SECRET", http.StatusFound)
	}))
	defer credRedirect.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path+"?token=SECRET", http.StatusFound)
	}))
	defer loop.Close()

	for name, url := range map[string]string{
		"malformed Location":  badLocation.URL + "/t?token=SECRET",
		"credential redirect": credRedirect.URL + "/t?token=SECRET",
		"dial error":          closedURL + "/t?token=SECRET",
		"redirect loop":       loop.URL + "/t?token=SECRET",
		"bad URL":             "http://exa mple.com/t?token=SECRET",
		"bad port":            "http://127.0.0.1:99999/t?token=SECRET",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadTask(t.Context(), url, strings.NewReader(""))
			if err == nil {
				t.Fatal("loadTask succeeded")
			}
			for _, secret := range []string{"SECRET", "PASSWORD", "%zz"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaks %q: %v", secret, err)
				}
			}
		})
	}
}

// The query often carries a token: it must not reach error text.
func TestLoadTask_URLErrorsMaskQuery(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/task?token=SECRET"
	srv.Close()

	_, err := loadTask(t.Context(), url, strings.NewReader(""))
	if err == nil {
		t.Fatal("loadTask of a closed server succeeded")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error leaks the query: %v", err)
	}

	ok := httptest.NewServer(http.NotFoundHandler())
	defer ok.Close()
	_, err = loadTask(t.Context(), ok.URL+"/task?token=SECRET", strings.NewReader(""))
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("status error = %v, want one without the query", err)
	}
}

func TestRunTaskFile(t *testing.T) {
	runDir := t.TempDir()
	if got, err := readRunTask(runDir); err != nil || got != "" {
		t.Fatalf("readRunTask of a run without a task = %q, %v", got, err)
	}
	if err := writeRunTask(runDir, "do it"); err != nil {
		t.Fatal(err)
	}
	if got, err := readRunTask(runDir); err != nil || got != "do it" {
		t.Fatalf("readRunTask = %q, %v; want %q", got, err, "do it")
	}
	info, err := os.Stat(filepath.Join(runDir, taskFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("task file mode = %o, want 600", perm)
	}
}

func TestPromptWithTask(t *testing.T) {
	tests := []struct{ prompt, task, want string }{
		{prompt: "flow\n", task: " task\n", want: "flow\n\n task\n"},
		{prompt: "", task: "task\n", want: "task\n"},
		{prompt: "", task: "", want: ""},
		{prompt: "flow", task: "", want: "flow"},
		{prompt: "", task: "task", want: "task"},
		{prompt: "flow", task: "task", want: "flow\n\ntask"},
	}
	for _, tt := range tests {
		if got := promptWithTask(tt.prompt, tt.task); got != tt.want {
			t.Errorf("promptWithTask(%q, %q) = %q, want %q", tt.prompt, tt.task, got, tt.want)
		}
	}
}

// promptLog is where the fake agent of prompt.yaml records every prompt it was given.
func (fx *modeFixture) usePromptLog() string {
	fx.t.Helper()
	path := filepath.Join(fx.root, "prompts.log")
	fx.env = append(fx.env, "AFM_IT_PROMPT_LOG="+path)
	return path
}

const promptEndMarker = "--- end ---\n"

// promptsIn returns the prompts recorded so far, one element per agent invocation.
// A record counts only once the agent wrote its end marker.
func promptsIn(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var prompts []string
	for _, part := range strings.Split(string(raw), "### ")[1:] {
		if strings.HasSuffix(part, promptEndMarker) {
			prompts = append(prompts, part)
		}
	}
	return prompts
}

func TestRunTask_ReachesAgentAndRunDir(t *testing.T) {
	fx := newModeFixture(t, modeCore, "prompt.yaml", "")
	log := fx.usePromptLog()
	child := fx.start("--task", "build the thing")
	runID := fx.waitReady(child)
	fx.waitStage("build", "done")

	prompts := promptsIn(log)
	if len(prompts) != 1 || !strings.Contains(prompts[0], "flow instructions") || !strings.Contains(prompts[0], "build the thing") {
		t.Fatalf("agent prompts = %q, want one with the flow prompt and the task", prompts)
	}
	taskPath := filepath.Join(fx.runDir(runID), taskFileName)
	if raw, err := os.ReadFile(taskPath); err != nil || string(raw) != "build the thing" {
		t.Fatalf("task file = %q, %v", raw, err)
	}
	if info, err := os.Stat(taskPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("task file mode = %v, %v; want 600", info, err)
	}
}

// A run interrupted before it finished resumes with its task even though the second start has no --task.
func TestRunTask_ResumeKeepsTask(t *testing.T) {
	fx := newModeFixture(t, modeCore, "prompt.yaml", "")
	log := fx.usePromptLog()
	fx.gateImplementation()
	first := fx.start("--task", "build the thing")
	runID := fx.waitReady(first)
	waitFor(t, "the first agent prompt", func() bool { return len(promptsIn(log)) == 1 })
	first.interrupt(t)

	writeFile(t, filepath.Join(fx.releaseDir, "build"), "")
	second := fx.start()
	if got := fx.waitReady(second); got != runID {
		t.Fatalf("second start opened run %s, want the resumed %s", got, runID)
	}
	fx.waitStage("build", "done")

	prompts := promptsIn(log)
	if len(prompts) != 2 || !strings.Contains(prompts[1], "build the thing") || !strings.Contains(prompts[1], "flow instructions") {
		t.Fatalf("agent prompts = %q, want the resumed run to carry the task", prompts)
	}
}

// Every repeat cycle gets the task, and each new run directory records it.
func TestRunTask_RepeatCarriesTask(t *testing.T) {
	fx := newModeFixture(t, modeCore, "prompt-repeat.yaml", "")
	log := fx.usePromptLog()
	child := fx.start("--task", "build the thing")
	fx.waitReady(child)
	child.waitOutput(t, "flow \"core-prompt-repeat\" completed")
	waitFor(t, "two cycles", func() bool { return len(promptsIn(log)) == 2 })

	for i, prompt := range promptsIn(log) {
		if !strings.Contains(prompt, "build the thing") {
			t.Errorf("cycle %d prompt lacks the task: %q", i+1, prompt)
		}
	}
	matches, err := filepath.Glob(filepath.Join(fx.root, ".afm", "runs", "*", taskFileName))
	if err != nil || len(matches) != 2 {
		t.Errorf("task files = %v, %v; want one per cycle", matches, err)
	}
}

// A refused --task stops before any run directory or stage exists.
func TestRunTask_RefusalsCreateNothing(t *testing.T) {
	big := filepath.Join(t.TempDir(), "big.txt")
	if err := os.WriteFile(big, []byte(strings.Repeat("a", maxTaskBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		task string
		want string
	}{
		{name: "empty", task: "", want: "the task is empty"},
		{name: "oversized", task: "@" + big, want: "larger than"},
		{name: "userinfo", task: "https://user:pw@example.com/task", want: "credentials"}, //nolint:gosec // G101: deliberate fake credentials for the rejection test
		{name: "missing file", task: "@/does/not/exist", want: "read task file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newCycleFixture(t)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("AFM_EXIT_ON_FAIL", "")
			t.Setenv("AFM_DIR", "")
			t.Setenv("AFM_WORKTREES", "0")
			t.Setenv("AFM_USE_DOCKER", "0")
			t.Setenv("AFM_BROWSER", "0")
			fx.write("flow.yaml", fx.flowYAML(flowSpec{}))

			args := []string{"run", fx.flowPath, "--dir", fx.root, "--task", tt.task}
			var code int
			_, stderr := captureOutput(t, func() { code = runMain(args) })
			if code == 0 {
				t.Fatal("runMain exit code = 0, want non-zero")
			}
			if !strings.Contains(stderr, tt.want) {
				t.Fatalf("stderr = %q, want %q", stderr, tt.want)
			}
			if _, err := os.Stat(filepath.Join(fx.root, ".afm", "runs")); !os.IsNotExist(err) {
				t.Errorf("runs directory exists after a refused task (stat err = %v)", err)
			}
			if _, err := os.Stat(fx.tickLog); !os.IsNotExist(err) {
				t.Errorf("stage script ran: tick marker exists (stat err = %v)", err)
			}
		})
	}
}

func TestWithoutTaskArg(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{name: "none", in: "run flow.yaml --port 8080", want: "run flow.yaml --port 8080"},
		{name: "separate value", in: "run --task secret flow.yaml", want: "run flow.yaml"},
		{name: "equals value", in: "run --task=secret flow.yaml", want: "run flow.yaml"},
		{name: "last position", in: "run flow.yaml --task", want: "run flow.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.Join(withoutTaskArg(strings.Fields(tt.in)), " ")
			if got != tt.want {
				t.Errorf("withoutTaskArg(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestWriteTaskSnapshot(t *testing.T) {
	root := t.TempDir()
	path, err := writeTaskSnapshot(root, "the task")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, filepath.Join(root, ".afm")) {
		t.Errorf("snapshot %s is outside the mounted afm root", path)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "the task" {
		t.Errorf("snapshot = %q, %v", raw, err)
	}
	if got, err := loadTask(t.Context(), "@"+path, strings.NewReader("")); err != nil || got != "the task" {
		t.Errorf("loadTask of the snapshot = %q, %v", got, err)
	}
}

func TestWriteTaskSnapshot_KeepsTextExactly(t *testing.T) {
	const task = "\n  keep me  \n\n"
	path, err := writeTaskSnapshot(t.TempDir(), task)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := loadTask(t.Context(), "@"+path, strings.NewReader("")); err != nil || got != task {
		t.Errorf("loadTask of the snapshot = %q, %v; want %q", got, err, task)
	}
}

// task.md must hold the supplied text byte for byte, not a trimmed copy.
func TestRunTask_RunDirKeepsSuppliedText(t *testing.T) {
	runDir := t.TempDir()
	const supplied = "  keep\n\n"
	task, err := loadTask(t.Context(), supplied, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareRunTask(runDir, task); err != nil {
		t.Fatal(err)
	}
	if got, err := readRunTask(runDir); err != nil || got != supplied {
		t.Fatalf("task.md = %q, %v; want %q", got, err, supplied)
	}
}
