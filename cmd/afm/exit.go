package main

import "fmt"

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
