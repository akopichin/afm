Domain: static asset inventory of the afm dashboard (React/Vite/TypeScript SPA), for the `pkg/web` cell that embeds them.

## Asset layout

```
pkg/web/dashboard/
├── index.html          # root HTML page (built by Vite: references ./assets/index-<hash>.js, mounts #root)
├── index.dev.html      # source dev template for index.html (restored into index.html before build)
├── assets/             # Vite build output (index-<hash>.js; hash in name, recreated each build)
├── skins/              # skin system: skins/base/*.css (shared structural partials, one per
│                       # dashboard component) + skins/<name>/index.css (novacorps/goga —
│                       # tokens for data-theme=dark/light + decor, @import base) + optional
│                       # skins/<name>/favicon.svg OR favicon.png override + optional
│                       # skins/<name>/title.txt (first line becomes <title>). goga ships its
│                       # own skins/goga/quarium-logo.png (referenced as the .logo
│                       # background-image in skins/goga/index.css) and skins/goga/favicon.png
│                       # (a duplicate of the same file, under the favicon convention name) +
│                       # skins/goga/title.txt = "QArium". Server picks the active skin
│                       # (theme:/skin_dir: config, skin_dir fully overrides) and rewrites
│                       # index.html's stylesheet href/body class/favicon href+type/title
│                       # accordingly (see pkg/server/server.go)
├── quarium-logo.png    # unused legacy asset at this root path (kept embedded, not referenced by
│                       # any component) — NOT the same reference as skins/goga/quarium-logo.png
│                       # above, which is a separate copy that IS used by the goga skin
├── favicon.svg         # browser tab icon
├── src/                # React/Vite/TypeScript SPA sources (entry: src/main.tsx → src/app/App); see src/ cells
└── app.js              # legacy vanilla-JS client (behavioural parity rewritten into src/); kept on
                        # disk for reference but NOT embedded (embed.go's explicit file list omits it)
                        # and not the active entry point
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

`pkg/web/embed.go` embeds an explicit file list (not a `dashboard/*` wildcard — that would pull in
node_modules/src/public and bloat the binary) and re-roots it with `fs.Sub` so consumers see the same
paths as before the split (`index.html`, not `dashboard/index.html`):

```go
//go:embed dashboard/index.html dashboard/favicon.svg dashboard/quarium-logo.png dashboard/skins dashboard/assets
var embedded embed.FS

var FS, _ = fs.Sub(embedded, "dashboard")
```

The rewrite changed only how the dashboard is built (Vite instead of hand-written files); the embed
contract is unchanged. Because the build keeps `index.html` + `assets/*` at the dashboard root, `pkg/web`
needs no edits. Any new file added to this directory becomes part of the embedded bundle automatically —
no change is needed in `pkg/web` unless the directory name itself changes.
