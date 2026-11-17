package adapters

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCodexQuotaWarnings(t *testing.T) {
	for _, name := range []string{"python3", "jq", "bash"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s unavailable", name)
		}
	}
	for _, tc := range []struct {
		name, quota, verify, extension string
		warnings, exit                 int
	}{
		{name: "captured weekly quota", quota: "captured", warnings: 2},
		{name: "legacy verify", quota: "captured", verify: "1", warnings: 2},
		{name: "streamed helper", quota: "captured", verify: "1", extension: "1", warnings: 2},
		{name: "healthy", quota: "healthy"},
		{name: "threshold", quota: "threshold", warnings: 2},
		{name: "fully used", quota: "full", warnings: 2},
		{name: "no subscription", quota: "null"},
		{name: "unsupported", quota: "unsupported"},
		{name: "malformed", quota: "malformed"},
		{name: "invalid percent", quota: "invalid"},
		{name: "secondary quota", quota: "secondary", warnings: 2},
		{name: "timeout", quota: "timeout"},
		{name: "agent failure", quota: "captured", warnings: 2, exit: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			fake := filepath.Join(dir, "codex")
			script := `#!/usr/bin/env python3
import json, os, sys, time
if sys.argv[1] == 'app-server':
    mode=os.environ['QUOTA_CASE']
    if mode == 'timeout':
        time.sleep(60)
        sys.exit(1)
    if mode == 'unsupported': sys.exit(1)
    if mode == 'malformed':
        print('invalid json', flush=True)
        sys.exit(0)
    assert json.loads(sys.stdin.readline())['method'] == 'initialize'
    print(json.dumps({'id':1,'result':{}}), flush=True)
    assert json.loads(sys.stdin.readline())['method'] == 'initialized'
    assert json.loads(sys.stdin.readline())['method'] == 'account/rateLimits/read'
    result=json.load(open(os.environ['QUOTA_FIXTURE']))
    if mode == 'healthy': result['rateLimits']['primary']['usedPercent']=89
    if mode == 'threshold': result['rateLimits']['primary']['usedPercent']=90
    if mode == 'full': result['rateLimits']['primary']['usedPercent']=100
    if mode == 'invalid': result['rateLimits']['primary']['usedPercent']='98'
    if mode == 'null': result={'rateLimits': None}
    if mode == 'secondary':
        result['rateLimits']['secondary']={'usedPercent':95,'windowDurationMins':300,'resetsAt':1795021515}
        result['rateLimits']['primary']=None
    print(json.dumps({'id':2,'result':result}), flush=True)
    time.sleep(60)
else:
    if '--help' in sys.argv:
        print('usage: exec')
    else:
        sys.stdout.write(open(os.environ['EXEC_FIXTURE']).read())
        sys.exit(int(os.environ['AGENT_EXIT']))
`
			if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", filepath.Join(cwd, "codex-as-claude.sh"), "-p", "offline fixture")
			// Browser and hook settings from a developer shell must not affect this fixture.
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if strings.HasPrefix(key, "CODEX_") || strings.HasPrefix(key, "AFM_") {
					continue
				}
				cmd.Env = append(cmd.Env, entry)
			}
			cmd.Env = append(cmd.Env, "CODEX_BIN="+fake, "CODEX_MODEL=fixture-model", "CODEX_VERIFY="+tc.verify, "AFM_STREAM_EXTENSION="+tc.extension, "QUOTA_CASE="+tc.quota, "QUOTA_FIXTURE="+filepath.Join(cwd, "testdata/codex-quota.json"), "EXEC_FIXTURE="+filepath.Join(cwd, "testdata/codex-exec.jsonl"), "AGENT_EXIT="+strconv.Itoa(tc.exit))
			started := time.Now()
			stdout, err := cmd.Output()
			code := 0
			if err != nil {
				exit, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			if code != tc.exit {
				t.Fatalf("exit=%d, want %d: %v", code, tc.exit, err)
			}
			if time.Since(started) > 60*time.Second {
				t.Fatal("quota probe exceeded its deadline")
			}
			if tc.extension == "1" {
				var first map[string]any
				line, _, _ := strings.Cut(string(stdout), "\n")
				if err := json.Unmarshal([]byte(line), &first); err != nil || first["subtype"] != "afm_stream" {
					t.Fatalf("missing leading stream negotiation: %s", line)
				}
			}
			warnings := 0
			var last map[string]any
			for _, line := range strings.Split(strings.TrimSpace(string(stdout)), "\n") {
				var event map[string]any
				if err := json.Unmarshal([]byte(line), &event); err != nil {
					t.Fatalf("invalid stream: %s", line)
				}
				last = event
				if event["type"] != "rate_limit_event" {
					continue
				}
				warnings++
				info := event["rate_limit_info"].(map[string]any)
				wantWindow, wantUsage := "weekly", 0.98
				if tc.quota == "threshold" {
					wantUsage = 0.90
				}
				if tc.quota == "full" {
					wantUsage = 1
				}
				if tc.quota == "secondary" {
					wantWindow, wantUsage = "five_hour", 0.95
				}
				if info["status"] != "allowed_warning" || info["rateLimitType"] != wantWindow || info["utilization"] != wantUsage || info["resetsAt"] != float64(1795021515) {
					t.Fatalf("wrong warning: %#v", info)
				}
			}
			if warnings != tc.warnings {
				t.Fatalf("warnings=%d, want %d; stdout=%s", warnings, tc.warnings, stdout)
			}
			if last["type"] != "result" {
				t.Fatalf("missing terminal result: %#v", last)
			}
			wantSubtype := "success"
			if tc.exit != 0 {
				wantSubtype = "error_during_execution"
			}
			if last["subtype"] != wantSubtype {
				t.Fatalf("wrong terminal result: %#v", last)
			}
		})
	}
}
