package web

import "embed"

//go:embed index.html style.css app.js favicon.svg
var FS embed.FS
