package web_test

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/akopichin/afm/pkg/web"
)

// TestEmbed_ServesOriginalWebPathsAfterDirMove locks in the post-split contract:
// after the dashboard assets moved into pkg/web/dashboard/, web.FS must still serve
// them at their original web paths (index.html, style.css, ...). fs.Sub(embedded,
// "dashboard") re-roots the embed so consumers stay unaware of the on-disk
// subdirectory.
func TestEmbed_ServesOriginalWebPathsAfterDirMove(t *testing.T) {
	assets := []string{
		"index.html",
		"style.css",
		"app.js",
		"markdown-it.min.js",
		"favicon.svg",
	}

	for _, name := range assets {
		info, err := fs.Stat(web.FS, name)
		if err != nil {
			t.Errorf("web.FS should serve %q at its original root path, got %v", name, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("web.FS: asset %q is empty", name)
		}
	}

	// The dashboard/ prefix must not leak into web.FS: fs.Sub must have re-rooted it.
	if _, err := fs.Stat(web.FS, "dashboard/index.html"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("web.FS should not expose the dashboard/ prefix after re-rooting, got %v", err)
	}
}
