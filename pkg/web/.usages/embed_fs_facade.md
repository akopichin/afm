Domain: serving the embedded dashboard assets over HTTP. Audience: `pkg/server`.

## Serving the dashboard root

```go
mux.Handle("/", http.FileServer(http.FS(web.FS)))
```

`web.FS` is rooted at the same relative paths the dashboard files always had (`index.html`,
`style.css`, `app.js`, `markdown-it.min.js`, `favicon.svg`) — consumers do not need to account for the
`pkg/web/dashboard/` on-disk subdirectory; that detail is fully contained within `pkg/web`.

No other API is exposed — `pkg/web` is a pure embed wrapper, not a templating or routing layer.
