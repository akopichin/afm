# Agent Logging Design

**Date:** 2026-04-16  
**Status:** Approved

## Problem

Currently `RunAgent` and `RunPlanning` dump raw claude stream-json to `.log` files. These are unreadable — you cannot tell what an agent did, when it started, or when it finished.

## Goal

Every agent run produces two files:
- `<agent>.log` — human-readable: start banner, timestamped actions, end banner
- `<agent>.jsonl` — raw stream-json from claude (for debugging)

## Design

### 1. `progress.Logger` — new methods

Add `startTime time.Time` field to `Logger` (set in `NewLogger`).

```go
// LogStart writes a start banner with agent type, stage name, and timestamp.
func (l *Logger) LogStart(agentType, stageName string)

// LogAction writes a timestamped action line.
func (l *Logger) LogAction(toolName, detail string)

// LogEnd writes a completion/failure banner with duration.
func (l *Logger) LogEnd(err error)
```

Log format:
```
=== implementation agent | stage: auth-module | started: 2026-04-16 14:30:01 ===
14:30:02  text  I'll analyze the plan and implement the auth module...
14:30:05  Read  pkg/auth/auth.go
14:30:08  Write pkg/auth/jwt.go
14:30:11  Bash  go build ./...
14:30:14  Bash  git add . && git commit -m "feat: jwt auth"
=== completed | 2026-04-16 14:30:14 | duration: 13s ===
```

On error:
```
=== FAILED | 2026-04-16 14:30:14 | duration: 13s | idle timeout after 30m ===
```

### 2. Stream-json parsing in executor

Extend existing `streamEvent` structs to include `tool_use` blocks in assistant messages.

Parsing rules per event:

| claude event | logged as |
|---|---|
| `assistant` + `content[].type == "text"` | `text  <first 100 chars>` |
| `assistant` + `tool_use` name=`Write`/`Edit` | `Write  <file path from input>` |
| `assistant` + `tool_use` name=`Bash` | `Bash   <command, truncated to 80 chars>` |
| `assistant` + `tool_use` name=`Read` | `Read   <file path from input>` |
| other tool_use | `<ToolName>  <summary of input>` |
| `result` subtype=`success/error` | ignored (covered by LogEnd) |
| anything else | ignored |

Every raw line also goes to `.jsonl` (derived as `strings.TrimSuffix(logFile, ".log") + ".jsonl"`).

### 3. Updated executor signatures

```go
func (e *Executor) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error
func (e *Executor) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error
```

`LogStart` is called before `e.run()`, `LogEnd` is deferred after.

### 4. Orchestrator call sites

```go
// implementation
e.exec.RunAgent(ctx, "implementation", s.Name, prompt, logFile)

// review
e.exec.RunAgent(ctx, "review", s.Name, reviewPrompt, reviewLog)

// planning
e.exec.RunPlanning(ctx, s.Name, prompt, outFile, logFile)

// summary
e.exec.RunAgent(ctx, "summary", "all stages", prompt, logFile)
```

### 5. File layout per stage

```
runs/2026-04-16_001/auth-module/
  planning.log       ← human-readable
  planning.jsonl     ← raw stream
  implementation.log
  implementation.jsonl
  review.log         ← only if review agent enabled
  review.jsonl
runs/2026-04-16_001/
  summary.log
  summary.jsonl
```

## What does NOT change

- `e.run()` internal signature — unchanged
- `NewLogger` API — unchanged (startTime set internally)
- No new packages, no new interfaces
- `.log` files still append on restart (existing separator logic)
