package main

import "fmt"

// ExitError is a generic exit-code transport for RunE: returning one makes
// main() terminate the process with Code, without any RunE calling os.Exit
// itself (that would break Cobra composition — a subcommand invoked via
// cmd.Execute() in a test, or nested under another command, must be able to
// observe the returned error instead of having the whole test process die).
//
// This is deliberately generic and NOT specific to any one command (unlike
// docker.SubprocessExitError, which only ever carries a container's exit
// code): any RunE that needs a non-1 exit code for a non-error-looking
// outcome (e.g. `afm check`/`afm report` on a corrupt usage ledger — real
// data, just degraded, exit 3) returns this instead.
type ExitError struct {
	// Code is the process exit code main() passes to os.Exit.
	Code int
	// Silent suppresses main()'s usual "Error: ..." + usage printing. Root
	// already sets SilenceErrors/SilenceUsage so Cobra itself never prints
	// on its own; Silent controls only main()'s OWN fallback printing for a
	// non-nil ExecuteC() error. Set it when the RunE already wrote a
	// precise, contextual message to stderr itself before returning.
	Silent bool
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}
