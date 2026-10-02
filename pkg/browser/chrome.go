package browser

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ownerMarkerName is the per-profile marker that records which afm process owns a
// Chrome profile dir, so a later start can safely sweep only provably-dead-owned
// dirs left behind by a SIGKILL (which skips Close).
const ownerMarkerName = "owner.json"

type ownerMarker struct {
	PID   int    `json:"pid"`
	Epoch string `json:"epoch"`
}

// chromeConn is a launched Chrome reachable over the CDP pipe (fds 3/4).
type chromeConn struct {
	cmd        *exec.Cmd
	toChrome   *os.File // we write CDP requests here (browser reads fd 3)
	fromChrome *os.File // we read CDP messages here (browser writes fd 4)
	profileDir string
}

// launchChrome starts a dedicated Chrome with --remote-debugging-pipe (never a
// TCP port) in its own profile dir. The pipe fds are passed as the child's fd 3
// (input) and fd 4 (output). The process is put in its own group so the whole
// tree can be killed on teardown.
func launchChrome(chromePath, profileDir string, headless bool) (*chromeConn, error) {
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return nil, fmt.Errorf("browser: profile dir: %w", err)
	}
	// fd 3: afm writes, browser reads.
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("browser: input pipe: %w", err)
	}
	// fd 4: browser writes, afm reads.
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, fmt.Errorf("browser: output pipe: %w", err)
	}

	args := []string{
		"--remote-debugging-pipe",
		"--user-data-dir=" + profileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-sync",
		"about:blank",
	}
	if headless {
		args = append(args, "--headless=new")
	}
	cmd := exec.Command(chromePath, args...)
	cmd.ExtraFiles = []*os.File{inR, outW} // → child fd 3 and fd 4
	setPgid(cmd)

	if err := cmd.Start(); err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, fmt.Errorf("browser: launch chrome: %w", err)
	}
	// Close the child's ends in the parent; keep our ends.
	inR.Close()
	outW.Close()

	return &chromeConn{cmd: cmd, toChrome: inW, fromChrome: outR, profileDir: profileDir}, nil
}

// close terminates the owned Chrome process group and closes the pipes.
func (c *chromeConn) close() {
	if c == nil {
		return
	}
	if c.cmd != nil && c.cmd.Process != nil {
		killProcessGroup(c.cmd)
	}
	if c.toChrome != nil {
		c.toChrome.Close()
	}
	if c.fromChrome != nil {
		c.fromChrome.Close()
	}
}

// writeOwnerMarker records ownership of a profile dir for the stale-profile sweep.
func writeOwnerMarker(profileDir string, pid int, epoch string) error {
	data, err := json.Marshal(ownerMarker{PID: pid, Epoch: epoch})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(profileDir, ownerMarkerName), data, 0o600)
}

// sweepStaleProfiles removes profile dirs under parent whose owner process is no
// longer alive. Dirs without a readable owner marker are left untouched
// (conservative): this is the honest counterpart to the SIGKILL limitation — a
// best-effort sweep on the next start, never a watchdog.
func sweepStaleProfiles(parent string, isAlive func(int) bool) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(parent, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, ownerMarkerName))
		if err != nil {
			continue // no readable marker → not provably ours → leave it
		}
		var m ownerMarker
		if json.Unmarshal(data, &m) != nil || m.PID <= 0 {
			continue
		}
		if !isAlive(m.PID) {
			_ = os.RemoveAll(dir)
		}
	}
}
