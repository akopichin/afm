package scripts_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunbot(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("runbot requires bash")
	}
	script, err := os.ReadFile("../runbot.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, fileToken, fileChat, token, users, chat, wantToken, wantUsers, wantError string
		childExit                                                                      int
	}{
		{name: "notifier files with CRLF", fileToken: "file-token\r\n", fileChat: "123456789\r\n", wantToken: "file-token", wantUsers: "123456789"},
		{name: "environment overrides files", fileToken: "file-token", fileChat: "123456789", token: "env-token", users: "123,456", wantToken: "env-token", wantUsers: "123,456"},
		{name: "chat environment overrides file", fileToken: "file-token", fileChat: "123456789", chat: "987654321", wantToken: "file-token", wantUsers: "987654321"},
		{name: "explicit users allow a group notifier", fileToken: "file-token", fileChat: "-100123456789", users: "123,456", wantToken: "file-token", wantUsers: "123,456"},
		{name: "missing token", fileChat: "123456789", wantError: "set TELEGRAM_BOT_TOKEN"},
		{name: "missing user ID", fileToken: "file-token", wantError: "private-chat user ID"},
		{name: "group is not a user", fileToken: "file-token", fileChat: "-100123456789", wantError: "private-chat user ID"},
		{name: "child exit status preserved", token: "env-token", users: "123", wantToken: "env-token", wantUsers: "123", childExit: 23},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			checkout := filepath.Join(dir, "checkout with spaces")
			moduleDir := filepath.Join(checkout, "telegram-bot")
			secretDir := filepath.Join(dir, "secrets")
			mockBin := filepath.Join(dir, "bin")
			for _, path := range []string{moduleDir, secretDir, mockBin} {
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for name, content := range map[string]string{"telegram-bot-token": tt.fileToken, "telegram-chat-id": tt.fileChat} {
				if content != "" {
					if err := os.WriteFile(filepath.Join(secretDir, name), []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			scriptPath := filepath.Join(checkout, "runbot.sh")
			if err := os.WriteFile(scriptPath, script, 0o755); err != nil {
				t.Fatal(err)
			}
			stub := `#!/bin/sh
set -eu
[ "$TELEGRAM_BOT_TOKEN" = "$RUNBOT_TEST_TOKEN" ]
[ "$TELEGRAM_ALLOWED_USER_IDS" = "$RUNBOT_TEST_USERS" ]
[ "$(pwd -P)" = "$RUNBOT_TEST_DIR" ]
[ "$#" -eq 6 ]
[ "$1" = run ] && [ "$2" = . ]
[ "$3" = --port ] && [ "$4" = 8081 ]
[ "$5" = --store ] && [ "$6" = "$RUNBOT_TEST_STORE" ]
exit "$RUNBOT_TEST_EXIT"
`
			if err := os.WriteFile(filepath.Join(mockBin, "go"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			physicalDir, err := filepath.EvalSymlinks(moduleDir)
			if err != nil {
				t.Fatal(err)
			}
			storePath := filepath.Join(dir, "state with spaces.json")
			cmd := exec.Command("bash", scriptPath, "--port", "8081", "--store", storePath)
			cmd.Dir = dir // The caller does not need to be in the checkout.
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "TELEGRAM_") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env,
				"PATH="+mockBin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"AFM_TELEGRAM_SECRETS_DIR="+secretDir,
				"TELEGRAM_BOT_TOKEN="+tt.token, "TELEGRAM_ALLOWED_USER_IDS="+tt.users, "TELEGRAM_CHAT_ID="+tt.chat,
				"RUNBOT_TEST_TOKEN="+tt.wantToken, "RUNBOT_TEST_USERS="+tt.wantUsers,
				"RUNBOT_TEST_DIR="+physicalDir, "RUNBOT_TEST_STORE="+storePath, fmt.Sprintf("RUNBOT_TEST_EXIT=%d", tt.childExit),
			)
			out, err := cmd.CombinedOutput()
			wantExit := tt.childExit
			if tt.wantError != "" {
				wantExit = 1
				if !strings.Contains(string(out), tt.wantError) {
					t.Fatalf("expected %q, got %s", tt.wantError, out)
				}
			}
			gotExit := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatal(err)
				}
				gotExit = exitErr.ExitCode()
			}
			if gotExit != wantExit {
				t.Fatalf("exit code = %d, want %d: %s", gotExit, wantExit, out)
			}
			if strings.Contains(string(out), "file-token") || strings.Contains(string(out), "env-token") {
				t.Fatal("launcher exposed a token in its output")
			}
		})
	}
}
