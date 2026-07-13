Domain: static asset inventory of the afm dashboard (React/Vite/TypeScript SPA), for the `pkg/web` cell that embeds them.

## Asset layout

```
pkg/web/dashboard/
├── index.html          # root HTML page (built by Vite: references ./assets/index-<hash>.js, mounts #root)
├── index.dev.html      # source dev template for index.html (restored into index.html before build)
├── assets/             # Vite build output (index-<hash>.js; hash in name, recreated each build)
├── style.css           # dashboard styles (default novacorps theme)
├── style-goga.css      # goga theme styles (linked server-side when theme=goga, see pkg/server)
├── quarium-logo.png    # quarium logo in the goga theme header
├── favicon.svg         # browser tab icon
├── src/                # React/Vite/TypeScript SPA sources (entry: src/main.tsx → src/app/App); see src/ cells
└── app.js              # legacy vanilla-JS client (behavioural parity rewritten into src/); still embedded
                        # by pkg/web via //go:embed dashboard/*, but no longer the active entry point
```

The active dashboard is a React/Vite/TypeScript SPA whose sources live in `src/` (entry `src/main.tsx` →
`App`). Vite (`vite build`) compiles them into `index.html` + `assets/index-<hash>.js` at the dashboard
root. Markdown rendering uses the npm dependency `markdown-it` (imported in
`src/components/plan-panel/markdown.ts`); the old vendored `markdown-it.min.js` was removed.

## Build pipeline (npm scripts)

- `restore-index` (`node scripts/restore-index.js`) — copies `index.dev.html` → `index.html`. Required
  before `dev` and `build`: Vite with `outDir='.'` overwrites `index.html` with the built version, so
  without restoration a rebuild would fail (`clean:assets` deletes `assets/` and Vite loses the entry).
- `clean:assets` (`rm -rf assets`) — clears hashed assets before a build so they don't accumulate and
  bloat the embedded binary.
- `build` = `restore-index && clean:assets && vite build` — emits `index.html` + `assets/index-<hash>.js`
  into the dashboard root.

## How `pkg/web` consumes this directory

`pkg/web/embed.go` embeds this directory with `//go:embed dashboard/*` and re-roots it with `fs.Sub`
so consumers see the same paths as before the split (`index.html`, not `dashboard/index.html`):

```go
//go:embed dashboard/*
var embedded embed.FS

var FS, _ = fs.Sub(embedded, "dashboard")
```

The rewrite changed only how the dashboard is built (Vite instead of hand-written files); the embed
contract is unchanged. Because the build keeps `index.html` + `assets/*` at the dashboard root, `pkg/web`
needs no edits. Any new file added to this directory becomes part of the embedded bundle automatically —
no change is needed in `pkg/web` unless the directory name itself changes.
