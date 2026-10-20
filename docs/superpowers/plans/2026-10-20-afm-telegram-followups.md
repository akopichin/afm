# Optional AFM Telegram Integration Implementation Plan

> **For agentic workers:** This is a separate future plan. Do not implement it as part of `dev-plan.md` or the current bot task. Review the contract with the user before scheduling AFM core work.

**Goal:** Add server-side AFM contracts that would let an external Telegram bot register automatically and reject writes to a replaced run or changed stage atomically.

**Architecture:** AFM exposes a stable ordinary-run identity, opt-in loopback registration, and conditional stage mutations. The standalone bot remains a separate binary and uses the enhanced SDK when this plan is approved later.

**Tech Stack:** Go, existing AFM server/orchestrator/state packages and SDK.

**Spec:** The current bot task is in `$AFM_STAGE_DIR/task.md`. Its latest instruction forbids AFM code changes now. The API limits and expected fallback are recorded in `dev-plan.md`.

## Global Constraints

- Future scope only: `cmd/afm`, `pkg/server`, `pkg/orchestrator`, SDK, and docs may change after separate approval. The current bot implementation must work without these changes.
- Preserve legacy dashboard requests that omit new preconditions. Keep secrets out of argv/logs; bind local integration endpoints to loopback.
- Read subsystem `AGENTS.md` before editing. Do not raise the Go version. Test state replay and cleanup semantics where touched.

## Review Focus

- Port reuse must never identify a new run as the old one; cover with a server/SDK test.
- Conditional stage actions must reject a stale generation at the durable transition, not only at HTTP preflight; cover with an orchestrator race test.
- Bot absence must not fail an AFM run; cover with a startup/retry test.

---

### Task 1: Stable identity for ordinary runs (server writer)

**Files:** Modify `pkg/server/handlers.go`, relevant status tests, `sdk/status.go` and SDK tests.

**Depends on:** A reviewed definition of the persisted run ID. **Produces:** `GET /api/status.run_id` for every run, including runs without a side agent; a run keeps the same ID across AFM process restarts.

- [ ] Add a failing test for ordinary and side-agent status, plus two runs reusing one port.
- [ ] Run `go test ./pkg/server -run 'Status|SideAgent' -count=1`; expect red.
- [ ] Return the persisted run identity and decode it in the SDK without changing existing field meaning for side agents.
- [ ] Run `go test ./pkg/server -run 'Status|SideAgent' -count=1` and `make sdk-test`; expect pass.

### Task 2: Conditional stage decisions (orchestrator writer)

**Files:** Modify `pkg/server/handlers.go`, `pkg/server/stageview.go`, `pkg/orchestrator/control_api.go`, related tests, and SDK control methods.

**Depends on:** Task 1. **Produces:** stage generation in status and optional `expected_stage_seq` on approve, revise, pause, and continue. A mismatch returns 409 without a mutation.

- [ ] Add failing tests where a stage changes after GET but before POST, including replay and duplicate callback cases.
- [ ] Run `go test ./pkg/server ./pkg/orchestrator -run 'Conditional|StageSeq' -count=1`; expect red.
- [ ] Check the expected generation atomically at the durable transition. Preserve behavior for requests that omit it.
- [ ] Run `go test ./pkg/server ./pkg/orchestrator` and `make sdk-test`; expect pass.

### Task 3: Opt-in AFM registration (CLI writer)

**Files:** Create `cmd/afm/run_telegram.go` and tests; modify `cmd/afm/run.go` and setup docs.

**Depends on:** Task 1. **Produces:** a documented opt-in setting for same-host AFM runs to announce their ID/dashboard port to the bot listener, retrying while active without failing the run when the bot is absent.

- [ ] Add failing `httptest` coverage for default-off behavior, late bot start, cancellation, dashboard-disabled configuration, and clean shutdown.
- [ ] Run `go test ./cmd/afm -run TelegramRegistration -count=1`; expect red.
- [ ] Register after dashboard startup and stop the retry worker before resource cleanup; do not send secrets or arbitrary dashboard URLs.
- [ ] Run `go test ./cmd/afm -run TelegramRegistration -count=1` and `make test`; expect pass.

## Review decisions (out of scope)

- Docker/cross-host bridge design and authentication are not specified by the current same-host bot task. They need a separate transport/security decision before implementation.
