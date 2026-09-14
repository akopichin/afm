# Go SDK

Need to drive afm from a Go service instead of the CLI — start a flow as a
subprocess, poll its progress, and call approve/retry/revise while it's running (for
example to expose your own HTTP endpoints for watching progress in a browser)?

`afmsdk.Client.Start` spawns `afm run --dir <isolated> --port <picked> <flowPath>` as
a real subprocess — it does not import or embed afm's orchestrator.

See [`sdk/README.md`](https://github.com/akopichin/afm/blob/main/sdk/README.md) for
the `afmsdk` Go module, its API, and the tagging convention it will use once
released.
