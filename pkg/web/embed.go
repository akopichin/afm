package web

import (
	"embed"
	"io/fs"
)

// Явный список assets исключает исходники и node_modules из бинарника.
//
//go:embed dashboard/index.html dashboard/favicon.svg dashboard/quarium-logo.png dashboard/skins dashboard/assets dashboard/side-agent.css dashboard/chat-composer.css
var embedded embed.FS

// FS serves the dashboard assets rooted at their original web paths
// (index.html, skins/novacorps/index.css, ...), independent of the dashboard/ subdirectory on disk.
var FS, _ = fs.Sub(embedded, "dashboard")
