package web_test

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/web"
)

// TestEmbed_ServesReactDashboardAtRootPaths фиксирует контракт встраивания
// React-дашборда: после миграции с vanilla (app.js + markdown-it.min.js) на
// React/Vite раздаётся точка входа index.html, собранный бандл в assets/ и
// статические стили/иконки. markdown-it теперь упакован внутрь бандла, поэтому
// отдельный markdown-it.min.js больше не раздаётся.
//
// fs.Sub(embedded, "dashboard") ре-рутит embed: потребители работают с корневыми
// веб-путями (index.html, skins/<name>/index.css, assets/...) и не знают про
// on-disk подкаталог.
func TestEmbed_ServesReactDashboardAtRootPaths(t *testing.T) {
	files := []string{
		"index.html",
		"skins/novacorps/index.css",
		"skins/goga/index.css",
		"favicon.svg",
		"quarium-logo.png",
	}

	for _, name := range files {
		info, err := fs.Stat(web.FS, name)
		if err != nil {
			t.Errorf("web.FS should serve %q at its original root path, got %v", name, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("web.FS: asset %q is empty", name)
		}
	}

	// Бандл React-сборки обязан присутствовать и быть непустым.
	entries, err := fs.ReadDir(web.FS, "assets")
	if err != nil {
		t.Fatalf("web.FS should expose the built assets/ directory, got %v", err)
	}
	var hasJS bool
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".js") && !e.IsDir() {
			hasJS = true
			break
		}
	}
	if !hasJS {
		t.Errorf("web.FS: assets/ must contain the built JS bundle, got %d entries", len(entries))
	}

	// Префикс dashboard/ не должен утекать в web.FS — fs.Sub должен был ре-рутить.
	if _, err := fs.Stat(web.FS, "dashboard/index.html"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("web.FS should not expose the dashboard/ prefix after re-rooting, got %v", err)
	}
}
