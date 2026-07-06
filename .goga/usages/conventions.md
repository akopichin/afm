# General Conventions

Mandatory rules for all Go code in this project. Strict deterministic specification.

## Engineering Principles

Code in this repository MUST prioritize:
- readable, explicit code
- predictable, straightforward control flow
- stable abstractions
- operational stability and maintainability

---

# Development

## Constraints

- Compatible with Go 1.23 and above only
- Use `go.mod` for dependency management

## Imports

RULES:
1. Format imports using `goimports` (includes `gofmt`)
2. Optionally use `gci` for strict import grouping
3. Use import aliases only to resolve naming conflicts or when the package name is misleading
4. Use only named imports — dot-imports are **STRICTLY** forbidden

```go
// Valid
import "pkg"

// Forbidden
import . "pkg"
```

All subsequent import patterns MUST follow named imports only.

## Dependency Injection

Use constructor-based dependency injection. Dependencies MUST be passed as explicit constructor parameters.

```go
type Service struct {
    repo Repository
    log  *slog.Logger
}

func NewService(
    repo Repository,
    log *slog.Logger,
) *Service {
    return &Service{
        repo: repo,
        log:  log,
    }
}
```

Allowed DI patterns:
- constructor injection with explicit parameters
- interface-based dependencies
- manual wiring

Use `log/slog` or `zap` instead of:
- service locators
- runtime DI containers
- reflection-heavy frameworks
- implicit dependencies

## Context Usage

`context.Context` MUST:
- be the first function argument
- be propagated through call chains
- respect cancellation and deadlines

```go
func (s *Service) CreateUser(
    ctx context.Context,
    req Request,
) error
```

Context MUST remain a function argument only. Keep context as a short-lived parameter passed through the call stack.

## Error Handling

Use standard Go error handling. Errors MUST:
- be returned explicitly
- preserve the original error chain
- include contextual information

```go
return fmt.Errorf("load user %s: %w", id, err)
```

Use `errors.Is` and `errors.As` for error classification. Use sentinel errors only for stable domain conditions.

Error classification MUST use `errors.Is` and `errors.As`. Compare error types, not error strings.

Every returned error MUST be handled by the caller. Propagate errors up the call chain with `fmt.Errorf` and `%w` wrapping.

## Logging

RULES:
1. Use structured logging via `log/slog` as the default
2. Use `zap` only for services with measured throughput requirements exceeding `slog` capacity
3. Use immutable, injected logger instances

Operational logs **MUST**:
- include contextual metadata
- be machine-readable
- support filtering and aggregation

```go
logger.InfoContext(
    ctx,
    "user created",
    "user_id", user.ID,
    "email", user.Email,
)
```

Formatting:
- lowercase messages
- concise operational wording
- stable log event names

Log levels MUST reflect operational importance and required reaction.

### DEBUG

DEBUG is used for diagnostic information useful during development or incident investigation.

Use DEBUG for:
- intermediate state
- request/response details
- branch decisions
- retries
- external payload previews
- performance diagnostics

### INFO

INFO is used for important normal business operations.

INFO logs SHOULD describe:
- lifecycle events
- significant state transitions
- externally observable operations

### WARN

WARN indicates abnormal but recoverable situations.
The operation continues, but attention MAY be required.

Use WARN when:
- fallback logic is activated
- retryable failures occur
- degraded behavior is detected
- unexpected input is received

### ERROR

ERROR is used when an operation cannot be completed.

Use ERROR when:
- a request fails
- data cannot be persisted
- external dependency prevents operation completion
- invariant violation affects functionality

### Log Content Restrictions

Log messages MUST contain only non-sensitive operational data. Exclude secrets, credentials, tokens, and personal sensitive data from all log output.

## Goroutines and Concurrency

Every goroutine MUST:
- have a lifecycle owner
- terminate predictably
- support cancellation

Concurrency patterns MUST use:
- `errgroup` for managed goroutine groups
- bounded concurrency via semaphore or worker pool
- explicit synchronization primitives
- context cancellation for shutdown

Channels MUST have:
- clear ownership semantics
- explicit producer/consumer responsibilities

Goroutine lifecycle MUST be bounded. Every goroutine requires a parent context and a deterministic exit condition.

## Composition

Prefer explicit composition over inheritance-style embedding.

Use embedding only when the embedded type is a direct behavioral component of the struct. Embedding MUST reflect a "has-a" relationship with direct method delegation.

Composition MUST use flat struct fields for loosely related components.

## Code Formatting

All code MUST be formatted with `goimports` (includes `gofmt`).

Inside function and method bodies, logical blocks are separated by **one blank line**:
- Variable initialization is separated from conditional constructs and loops
- Loops and conditions are separated by a blank line
- Data preparation is separated from its processing
- Processing is separated from returning the result

Style Rules:
- short functions
- early returns
- explicit variable naming
- minimal nesting

Write functions as flat sequences of steps. Extract logic into named helpers instead of nesting conditionals beyond two levels.

## Documentation

All exported functions, methods, types, interfaces, and packages MUST have doc comments.

```go
// CreateUser creates a new user in the system.
func CreateUser(ctx context.Context, req Request) error
```

Comments SHOULD explain intent, invariants, and non-obvious decisions.
Comments SHOULD NOT restate code behavior.

## Dependencies

All dependencies **MUST** be declared in `go.mod` with a minimum version specified.

---

# Testing

## Constraints

- Test code must be compatible with Go 1.23 and above

## Tools

- `testing` — standard test framework
- `testify/assert` — assertions
- `testify/require` — fatal assertions
- `cmp` — deep struct comparison (allowed for complex struct diffs)
- `gomock` — for complex scenarios (allowed, not required)

Prefer hand-written fakes over generated mocks.

## Running Tests

- Run all commands from the module root directory

## Test Structure

Tests mirror the source code structure **directly**:
- `<module>/service.go` → `<module>/service_test.go`
- Test files reside in the same package as the code they test
- Shared test helpers are placed in `<module>/testutil/` or `<module>/testdata/`
- Integration tests for external dependencies are placed in `tests/integration/`

## Naming

- Functions: `Test<Component>_<Scenario>` (e.g., `TestCreateUser_Success`, `TestCreateUser_ValidationError`)
- Table-driven test cases use descriptive `name` field

## Table-Driven Tests

Table-driven tests are the preferred style.

```go
func TestNormalize(t *testing.T) {
    tests := []struct {
        name     string
        input    string
        expected string
    }{
        {
            name:     "trim spaces",
            input:    " test ",
            expected: "test",
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result := Normalize(tt.input)

            require.Equal(t, tt.expected, result)
        })
    }
}
```

## Test Types

- **Unit** — every exported function/method/type, main scenario and typical data
- **Edge cases**:
  - Empty inputs: `nil`, `""`, empty slices, empty maps
  - Boundary values: `0`, negative, very large
  - Invalid types via type assertions
  - Expected errors via `require.Error` or `require.EqualError`
- **Integration** — only for interaction between packages or external services

## Mocking

- Pure logic — no mocks
- External dependencies — hand-written fakes or `gomock`
- Mock at the interface level, not at the implementation level

Generate mocks only for external dependency interfaces. Keep business logic mock-free.

## Concurrency Testing

Concurrency-sensitive code SHOULD include:
- race-condition tests
- cancellation tests
- timeout behavior tests

Run race detector regularly.

## Miscellaneous

- Use self-documenting test names. Keep comments minimal.
- Skip integration tests with unavailable external dependencies via `build tags`

## Dependencies

All test dependencies **MUST** be declared in `go.mod`.

---

# Validation Commands

All validation commands MUST pass in CI. Go 1.23+ compatibility required.

| Purpose                      | Command                           |
|------------------------------|-----------------------------------|
| Format code and imports      | `goimports -w .`                  |
| Run tests                    | `go test ./...`                   |
| Run tests with race detector | `go test -race ./...`             |
| Run integration tests        | `go test -tags=integration ./...` |
| Run benchmarks               | `go test -bench=. ./...`          |
| Lint (includes staticcheck)  | `golangci-lint run`               |
| Vulnerability scan           | `govulncheck ./...`               |

Lint violations require explicit documented justification for any suppression.