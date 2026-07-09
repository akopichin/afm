Domain: static asset inventory of the afm dashboard, for the `pkg/web` cell that embeds them.

## Asset layout

```
pkg/web/dashboard/
├── index.html          # root HTML page
├── style.css           # dashboard styles (default novacorps theme)
├── style-goga.css      # goga theme styles (linked server-side when theme=goga, see pkg/server)
├── quarium-logo.png    # quarium logo in the goga theme header
├── app.js              # client logic: stage/event polling, WebSocket subscription, consumption panel
├── markdown-it.min.js  # third-party markdown renderer (plans/feedback in the UI)
└── favicon.svg         # browser tab icon
```

## How `pkg/web` consumes this directory

`pkg/web/embed.go` embeds this directory with `//go:embed dashboard/*` and re-roots it with `fs.Sub`
so consumers see the same paths as before the split (`index.html`, not `dashboard/index.html`):

```go
//go:embed dashboard/*
var embedded embed.FS

var FS, _ = fs.Sub(embedded, "dashboard")
```

Any new file added to this directory becomes part of the embedded bundle automatically — no change is
needed in `pkg/web` unless the directory name itself changes.
