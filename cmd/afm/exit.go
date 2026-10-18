package main

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// ExitError is a generic exit-code transport for RunE: returning one makes main terminate the process with
// Code, without any RunE calling os.Exit itself.
type ExitError struct {
	// Code is the process exit code main() passes to os.Exit.
	Code int
	// Silent suppresses main's usual "Error: ..." + usage printing. Set it when the RunE already wrote a
	// precise, contextual message to stderr itself before returning.
	Silent bool
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}

// interruptedExitCode is the conventional exit code of a process stopped by SIGINT (128 + 2).
const interruptedExitCode = 130

// runExitError turns orch.Run's error into the command's. A run stopped by Ctrl+C (signalCtx cancelled) is not
// a usage error: it prints one line on how to resume and exits 130, without cobra's usage text.
func runExitError(signalCtx context.Context, runErr error) error {
	if signalCtx.Err() != nil && errors.Is(runErr, context.Canceled) {
		fmt.Fprintln(os.Stderr, "afm: interrupted — run `afm run` again to resume")
		return &ExitError{Code: interruptedExitCode, Silent: true}
	}
	return fmt.Errorf("run: %w", runErr)
}
