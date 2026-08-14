// Package afmsdk lets a Go service launch, track, and control an afm flow
// run as a subprocess.
//
// A Client starts "afm run" with its own isolated state directory and
// dashboard port per run, then talks to that run's dashboard API
// (GET /api/status, POST /api/stages/<id>/{approve,retry,revise}) to observe
// progress and manage stages while the flow is alive. This package has no
// dependency on afm's own Go packages — it only requires the afm binary to be
// installed and reachable (via PATH or Config.Binary).
//
// Typical use:
//
//	client, err := afmsdk.New(afmsdk.Config{MaxConcurrent: 4})
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	runCtx, cancel := context.WithCancel(ctx)
//	defer cancel()
//
//	run, err := client.Start(runCtx, "flow.yaml", "/path/to/target/project")
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	for {
//		status, err := run.Status(runCtx)
//		if err != nil {
//			log.Fatal(err)
//		}
//		if status.Done {
//			break
//		}
//		if status.Failed {
//			// afm keeps the subprocess alive on failure so a stage can be
//			// retried; here we just give up and shut it down instead, since
//			// Wait only returns once the process exits or ctx is cancelled.
//			cancel()
//			break
//		}
//		time.Sleep(2 * time.Second)
//	}
//
//	if err := run.Wait(runCtx); err != nil {
//		log.Fatal(err)
//	}
//	if err := run.Cleanup(); err != nil {
//		log.Fatal(err)
//	}
//
// Reconnecting after a restart of the calling process: persist Run.Dir(),
// Run.Port(), and Run.PID() somewhere durable (e.g. a database row) when you
// start a run, then call Client.Attach with those three values to get a new
// *Run handle for it — Status/Approve/Retry/Revise work exactly as before.
// Wait and Cleanup return an error on an attached Run, since this package
// never held the *exec.Cmd needed to manage its lifecycle across a restart.
//
//	attached, err := client.Attach(ctx, savedDir, savedPort, savedPID)
//	if errors.Is(err, afmsdk.ErrProcessDead) {
//		// the afm subprocess didn't survive the restart either — nothing to
//		// reconnect to.
//	}
//
// Scope for v1: only file-path flow definitions; only autonomous
// (agents: [auto]) or script/planning-gated stages — interactive
// (question/answer) stages are not answerable through this package. Errors
// from Approve/Retry/Revise carry the afm dashboard's response text but are
// not typed/sentinel errors. Cleanup is always explicit — this package never
// deletes a run's isolated directory on its own.
package afmsdk
