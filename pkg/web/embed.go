package web

import (
	"embed"
	"io/fs"
)

// Внедряем только то, что реально раздаётся дашбордом: точку входа index.html,
// собранный React-бандл (assets/) и статические стили/иконки/логотип.
//
// node_modules/, src/, public/, scripts/, конфиги и прочее намеренно исключены:
// вариант «dashboard/*» утягивал бы в бинарь ~96 МБ зависимостей и исходников
// фронтенда. Набор путей = набор публичных веб-путей дашборда.
//
//go:embed dashboard/index.html dashboard/favicon.svg dashboard/quarium-logo.png dashboard/skins dashboard/assets
var embedded embed.FS

// FS serves the dashboard assets rooted at their original web paths
// (index.html, skins/novacorps/index.css, ...), independent of the dashboard/ subdirectory on disk.
var FS, _ = fs.Sub(embedded, "dashboard")
