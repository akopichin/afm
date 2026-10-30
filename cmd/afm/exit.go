package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/akopichin/afm/pkg/orchestrator"
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

// Committed failure wins over a later signal; both are runtime outcomes, so neither prints usage.
func runExitError(signalCtx context.Context, runErr error, runDir string) error {
	var failed *orchestrator.StageFailedError
	if errors.As(runErr, &failed) {
		fmt.Fprintf(os.Stderr, "afm: %s (run dir: %s)\n", singleLineRunError(failed.Error()), singleLineRunError(runDir))
		printAdditionalRunErrors(runErr)
		return &ExitError{Code: 1, Silent: true}
	}
	if signalCtx.Err() != nil && errors.Is(runErr, context.Canceled) {
		fmt.Fprintln(os.Stderr, "afm: interrupted — run `afm run` again to resume")
		return &ExitError{Code: interruptedExitCode, Silent: true}
	}
	return fmt.Errorf("run: %w", runErr)
}

// Keep joined persistence errors visible without repeating the primary failure or cancellation.
func printAdditionalRunErrors(err error) {
	if err == nil || err == context.Canceled {
		return
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, inner := range joined.Unwrap() {
			printAdditionalRunErrors(inner)
		}
		return
	}
	var failed *orchestrator.StageFailedError
	if errors.As(err, &failed) {
		printAdditionalRunErrors(errors.Unwrap(err))
		return
	}
	fmt.Fprintf(os.Stderr, "afm: %s\n", singleLineRunError(err.Error()))
}

func singleLineRunError(message string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, message)
}
