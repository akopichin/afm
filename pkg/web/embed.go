package web

import "embed"

//go:embed index.html style.css app.js markdown-it.min.js favicon.svg
var FS embed.FS
