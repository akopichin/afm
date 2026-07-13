# General Conventions

Mandatory rules for all JavaScript code in this project. Strict deterministic specification.

## Engineering Principles

Code in this repository MUST prioritize:
- readable, explicit code
- predictable, straightforward control flow
- stable abstractions
- operational stability and maintainability

---

# Development

## Constraints

- Compatible with ECMAScript 2024 (ES2024) and ES2025 features only
- Use ESM (`import`/`export`) as the module system — CommonJS is **STRICTLY** forbidden
- Set `"type": "module"` in `package.json`
- Node.js 22+ runtime for full ES2024/ES2025 support

## Imports

RULES:
1. Use ESM `import`/`export` syntax exclusively — `require()` and `module.exports` are **STRICTLY** forbidden
2. Use named exports as the default. Default exports are allowed only for the main module entity
3. Group imports in order: Node.js built-ins → external packages → internal modules
4. Use import aliases only to resolve naming conflicts
5. Use `node:` prefix for all Node.js built-in imports

```javascript
// Valid: ESM named imports
import { readFile } from 'node:fs/promises'
import { EventEmitter } from 'node:events'
import { z } from 'zod'
import { helper } from './utils.js'

// Valid: default export for main entity
export default class ApiClient { /* ... */ }

// Forbidden: CommonJS
const fs = require('node:fs')
module.exports = { something }
```

All subsequent import patterns MUST follow ESM syntax only.

## Asynchronous Programming

RULES:
1. Use `async`/`await` for all asynchronous operations — creating new continuation-style async APIs (callback-based async control flow) is **STRICTLY** forbidden. Callbacks are allowed as required adapters for host/library APIs (e.g., `EventEmitter`, `stream`, `Array.prototype.map`)
2. Use top-level `await` only at ESM module top level. Use `await` inside `async` functions normally
3. Document async function return types with `@returns {Promise<T>}` in JSDoc

```javascript
// Valid
async function fetchUser(id) {
  const response = await fetch(`/api/users/${id}`)
  return response.json()
}

// Forbidden: continuation-style async API
function fetchUser(id, callback) {
  fetch(`/api/users/${id}`).then(data => callback(data))
}

// Valid: callbacks as required adapters
emitter.on('data', (chunk) => process(chunk))
items.map((item) => transform(item))
```

Use `Promise.all()` for concurrent independent operations. Use `Promise.allSettled()` when individual failures must not short-circuit.

Use `Promise.try()` (ES2025) to wrap synchronous throws into a rejected promise in async chains.

## Error Handling

RULES:
1. Use custom error classes extending `Error`
2. Errors MUST include contextual information via `cause` and custom properties
3. Never catch and swallow errors silently
4. Error classification MUST use `instanceof` — comparing error messages is **STRICTLY** forbidden

```javascript
class AppError extends Error {
  constructor(message, { cause, context } = {}) {
    super(message, { cause })
    this.name = this.constructor.name
    this.context = context
  }
}

class NotFoundError extends AppError {}
class ValidationError extends AppError {}
```

Every caught error MUST be either re-thrown, wrapped with context, or explicitly handled. Empty `catch` blocks are forbidden.

```javascript
// Valid: wrap with context
try {
  await saveUser(user)
} catch (error) {
  throw new AppError('failed to save user', {
    cause: error,
    context: { userId: user.id },
  })
}

// Forbidden: silent catch
try {
  await saveUser(user)
} catch (error) {}
```

## Dependency Injection

Use constructor-based dependency injection. Dependencies MUST be passed as explicit parameters.

```javascript
class UserService {
  #repository
  #logger

  constructor({ repository, logger }) {
    this.#repository = repository
    this.#logger = logger
  }
}
```

Allowed DI patterns:
- constructor injection with explicit parameters
- factory functions returning configured instances
- manual wiring

Forbidden:
- global singletons accessed from within functions
- service locator patterns
- runtime DI containers with reflection

## Null Safety

RULES:
1. Use optional chaining (`?.`) for property access on nullable values
2. Use nullish coalescing (`??`) for default values — `||` for defaults is **STRICTLY** forbidden
3. Use `null` to represent the explicit absence of a value. Accept `undefined` at JS/API boundaries (missing properties, omitted arguments, no explicit return)

```javascript
// Valid
const name = user?.profile?.name ?? 'unknown'
const port = config?.port ?? 3000

// Forbidden
const name = user && user.profile && user.profile.name || 'unknown'
const port = config.port || 3000
```

## Logging

RULES:
1. Use structured logging as the default
2. Logger MUST be injected, not imported as a global

Operational logs **MUST**:
- include contextual metadata
- be machine-readable
- support filtering and aggregation

```javascript
logger.info('user created', { userId: user.id, email: user.email })
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

## Code Formatting

All code MUST be formatted with Prettier.

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

Write functions as flat sequences of steps. Extract logic into named helpers instead of nesting beyond two levels.

Use private class fields (`#field`) for encapsulation. The `_field` convention is **STRICTLY** forbidden.

```javascript
class Service {
  #repository

  constructor(repository) {
    this.#repository = repository
  }
}
```

## Documentation

All exported functions, methods, and classes MUST have JSDoc comments.

```javascript
/**
 * Creates a new user in the system.
 *
 * @param {string} name - The user's full name
 * @param {UserConfig} config - User configuration
 * @returns {Promise<User>} The created user
 * @throws {ValidationError} When input is invalid
 */
export async function createUser(name, config) {}
```

JSDoc rules:
- `@param` with type annotations for all parameters
- `@returns` with type annotations for return values
- `@throws` for documented exceptions
- Allowed types: `string`, `number`, `boolean`, `Array<T>`, `Object<string, T>`, `T|null`, `Promise<T>`
- Forbidden types: `Function`, `Object` without generics, `any`
- Avoid rest parameters in public API signatures. When used, document with `@param {...string} args`

Comments SHOULD explain intent, invariants, and non-obvious decisions.
Comments SHOULD NOT restate code behavior.

## Naming

- Functions and variables: `camelCase`
- Classes: `PascalCase`
- Constants: `SCREAMING_SNAKE_CASE` for true constants (`const MAX_RETRIES = 3`), `camelCase` for const-declared values (`const userConfig = {}`)
- File names: `kebab-case` for modules (`user-service.js`)
- Private class members: `#` prefix only (`#field`, `#method()`)

## Dependencies

All dependencies **MUST** be declared in `package.json` with a minimum version specified.

---

# Testing

## Constraints

- Test code must be compatible with ES2024 and above

## Tools

- Jest — running tests and assertions
- ESLint — linting test code

Jest with strict ESM requires `--experimental-vm-modules` and an ESM-compatible configuration. When Jest mocking limitations with ESM become blocking, consider `node:test` (available in Node 22+) as an alternative.

## Running Tests

- Run all commands from the project root directory

## Test Structure

Tests mirror the source code structure **directly**:
- `src/module/service.js` → `src/module/service.test.js`
- Test files reside alongside the code they test
- Shared test helpers are placed in `test/utils/`
- Shared test fixtures are placed in `test/fixtures/`
- Integration tests for external dependencies are placed in `tests/integration/`

## Naming

- Files: `<module>.test.js`
- Functions: `test('<Component> <scenario>')` (e.g., `test('createUser succeeds with valid input')`)
- Grouping: `describe('<Component>')`

## Test Types

- **Unit** — every exported function/method/class, main scenario and typical data
- **Edge cases**:
  - Empty inputs: `null`, `undefined`, `""`, `[]`, `{}`
  - Boundary values: `0`, negative, very large
  - Invalid types
  - Expected errors via `expect(() => fn()).toThrow(ErrorClass)`
- **Integration** — only for interaction between modules or external services

## Mocks

- Pure logic — no mocks
- External dependencies — Jest mock functions (`jest.fn()`) or manual test doubles
- Mock at the module boundary, not at the implementation level

Mock only at external boundaries. Keep business logic tests mock-free.

## Miscellaneous

- Use self-documenting test names. Keep comments minimal.
- Skip integration tests with unavailable external dependencies via `describe.skip` or `test.skip`

## Dependencies

All test dependencies **MUST** be added to `package.json` in `devDependencies`.

---

# Validation Commands

All validation commands MUST pass in CI. ES2024+ compatibility required.

| Purpose                  | Command                               |
|--------------------------|---------------------------------------|
| Format code              | `npx prettier --write .`              |
| Check formatting         | `npx prettier --check .`              |
| Run tests                | `npx jest`                            |
| Run a specific test      | `npx jest <path>`                     |
| Run tests with coverage  | `npx jest --coverage`                 |
| Lint                     | `npx eslint src/`                     |
| Facade check             | `node -e "import('./src/index.js')"`  |
