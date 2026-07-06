package web

import (
	"embed"
	"io/fs"
)

//go:embed dashboard/*
var embedded embed.FS

// FS serves the dashboard assets rooted at their original web paths
// (index.html, style.css, ...), independent of the dashboard/ subdirectory on disk.
var FS, _ = fs.Sub(embedded, "dashboard")
