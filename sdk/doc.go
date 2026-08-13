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
//	run, err := client.Start(ctx, "flow.yaml", "/path/to/target/project")
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	for {
//		status, err := run.Status(ctx)
//		if err != nil {
//			log.Fatal(err)
//		}
//		if status.Done || status.Failed {
//			break
//		}
//		time.Sleep(2 * time.Second)
//	}
//
//	if err := run.Wait(ctx); err != nil {
//		log.Fatal(err)
//	}
//	if err := run.Cleanup(); err != nil {
//		log.Fatal(err)
//	}
//
// Scope for v1: only file-path flow definitions; only autonomous
// (agents: [auto]) or script/planning-gated stages — interactive
// (question/answer) stages are not answerable through this package. Errors
// from Approve/Retry/Revise carry the afm dashboard's response text but are
// not typed/sentinel errors. Cleanup is always explicit — this package never
// deletes a run's isolated directory on its own.
package afmsdk
