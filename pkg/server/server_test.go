package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
)

func TestServerRouteStages(t *testing.T) {
	srv, _ := setupTestServer(t)

	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/api/stages/s1/plan", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound {
		t.Error("route should match /api/stages/s1/plan")
	}
}

// TestServerServesReactBundle пришёл на смену TestServerServesMarkdownIt.
// После миграции дашборда с vanilla JS на React/Vite отдельный публичный файл
// markdown-it.min.js удалён — markdown-it упакован внутрь собранного бандла
// assets/index-<hash>.js. Хэш меняется от сборки к сборке, поэтому проверяем
// не точное имя, а контракт: каталог /assets/ раздаётся и содержит непустой
// .js-бандл, который сервер отдаёт с 200.
func TestServerServesReactBundle(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	// Шаг 1. GET /assets/ через fileServer возвращает HTML-листинг каталога
	// (http.FileServer отдаёт directory listing), значит каталог существует.
	dirReq := httptest.NewRequest("GET", "/assets/", nil)
	dirW := httptest.NewRecorder()
	handler.ServeHTTP(dirW, dirReq)
	if dirW.Code != http.StatusOK {
		t.Fatalf("GET /assets/: ожидался 200, получен %d", dirW.Code)
	}
	if !strings.Contains(dirW.Body.String(), ".js") {
		t.Fatalf("GET /assets/: в листинге нет .js-бандла (body=%q)", dirW.Body.String())
	}

	// Шаг 2. Извлечь имя бандла из листинга и запросить его напрямую.
	bundleName := extractFirstJSBundle(dirW.Body.String())
	if bundleName == "" {
		t.Fatal("не удалось извлечь имя .js-бандла из листинга /assets/")
	}
	bundleReq := httptest.NewRequest("GET", "/assets/"+bundleName, nil)
	bundleW := httptest.NewRecorder()
	handler.ServeHTTP(bundleW, bundleReq)
	if bundleW.Code != http.StatusOK {
		t.Fatalf("GET /assets/%s: ожидался 200, получен %d", bundleName, bundleW.Code)
	}
	if bundleW.Body.Len() == 0 {
		t.Errorf("GET /assets/%s: тело бандла пустое", bundleName)
	}
}

// extractFirstJSBundle достаёт первое имя вида index-<hash>.js из HTML-листинга
// http.FileServer (формат "<a href="index-HASH.js">"). Возвращает "" если не нашёл.
func extractFirstJSBundle(listing string) string {
	const marker = `href="`
	start := strings.Index(listing, marker)
	for start != -1 {
		start += len(marker)
		end := strings.IndexByte(listing[start:], '"')
		if end == -1 {
			break
		}
		name := listing[start : start+end]
		if strings.HasSuffix(name, ".js") {
			return name
		}
		next := strings.Index(listing[start:], marker)
		if next == -1 {
			break
		}
		start = start + next
	}
	return ""
}

// TestServer_ConfigAcceptsAccountant и TestServer_UsageRouteWired удалены вместе
// с роутом /api/usage и пакетом accounting (см. task-2-brief).

func TestServer_IndexDefaultTheme(t *testing.T) {
	srv := New(Config{})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `href="./skins/coffee/index.css"`) {
		t.Error("default скин должен ссылаться на ./skins/coffee/index.css")
	}
	if !strings.Contains(body, `class="theme-coffee"`) {
		t.Error("default скин должен ставить class theme-coffee")
	}
	if !strings.Contains(body, `href="./favicon.svg"`) {
		t.Error("default скин должен использовать общий favicon.svg")
	}
	if !strings.Contains(body, `id="root"`) {
		t.Error("index должен содержать точку монтирования React (#root)")
	}
	if strings.Contains(body, "skins/goga") || strings.Contains(body, "skins/custom") {
		t.Error("default скин не должен ссылаться на goga/custom")
	}
}

func TestServer_IndexGogaTheme(t *testing.T) {
	srv := New(Config{Theme: config.ThemeGoga})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `class="theme-goga"`) {
		t.Error("goga скин должен ставить class theme-goga")
	}
	if strings.Contains(body, "theme-coffee") {
		t.Error("goga скин не должен содержать theme-coffee")
	}
	if !strings.Contains(body, `href="./skins/goga/index.css"`) {
		t.Error("goga скин должен ссылаться на ./skins/goga/index.css")
	}
	if strings.Contains(body, `href="./skins/coffee/index.css"`) {
		t.Error("goga скин не должен ссылаться на дефолтный coffee")
	}
}

func TestServer_IndexNovacorpsTheme(t *testing.T) {
	srv := New(Config{Theme: config.ThemeNovacorps})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `class="theme-novacorps"`) {
		t.Error("novacorps скин должен ставить class theme-novacorps")
	}
	if strings.Contains(body, "theme-coffee") {
		t.Error("novacorps скин не должен содержать theme-coffee")
	}
	if !strings.Contains(body, `href="./skins/novacorps/index.css"`) {
		t.Error("novacorps скин должен ссылаться на ./skins/novacorps/index.css")
	}
	if strings.Contains(body, `href="./skins/coffee/index.css"`) {
		t.Error("novacorps скин не должен ссылаться на дефолтный coffee")
	}
}

func TestServer_ServesGogaStylesheet(t *testing.T) {
	srv := New(Config{Theme: config.ThemeGoga})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/skins/goga/index.css", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /skins/goga/index.css: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "--mint") {
		t.Error("skins/goga/index.css должен определять CSS-токен --mint")
	}
}

func TestServer_ServesBaseSkinPartial(t *testing.T) {
	srv := New(Config{})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/skins/base/header.css", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /skins/base/header.css: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), ".logo") {
		t.Error("skins/base/header.css должен содержать структурные правила .logo")
	}
}

func TestServer_SkinDirOverridesTheme(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.css"), []byte(`:root[data-theme="dark"]{--mint:#123456;}`), 0644); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{Theme: config.ThemeGoga, SkinDir: dir})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `href="./skins/custom/index.css"`) {
		t.Error("skin_dir должен побеждать theme: ожидался href на ./skins/custom/index.css")
	}
	if !strings.Contains(body, `class="theme-custom"`) {
		t.Error("skin_dir должен ставить class theme-custom")
	}

	cssReq := httptest.NewRequest("GET", "/skins/custom/index.css", nil)
	cssW := httptest.NewRecorder()
	handler.ServeHTTP(cssW, cssReq)
	if cssW.Code != http.StatusOK {
		t.Fatalf("GET /skins/custom/index.css: got %d, want 200", cssW.Code)
	}
	if !strings.Contains(cssW.Body.String(), "#123456") {
		t.Error("GET /skins/custom/index.css должен отдавать содержимое из SkinDir, а не embed")
	}
}

func TestServer_SkinDirMissingIndexFallsBack(t *testing.T) {
	dir := t.TempDir() // пустая директория, index.css нет

	srv := New(Config{Theme: config.ThemeGoga, SkinDir: dir})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `href="./skins/goga/index.css"`) {
		t.Error("без index.css в skin_dir должен быть fallback на встроенный скин из theme")
	}
	if !strings.Contains(body, `class="theme-goga"`) {
		t.Error("fallback должен ставить class theme-goga, не theme-custom")
	}

	cssReq := httptest.NewRequest("GET", "/skins/custom/index.css", nil)
	cssW := httptest.NewRecorder()
	handler.ServeHTTP(cssW, cssReq)
	if cssW.Code != http.StatusNotFound {
		t.Errorf("/skins/custom/ не должен монтироваться при невалидном skin_dir: got %d, want 404", cssW.Code)
	}
}

func TestServer_SkinDirCustomFavicon(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.css"), []byte(`:root[data-theme="dark"]{--mint:#000;}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "favicon.svg"), []byte("<svg></svg>"), 0644); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{SkinDir: dir})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `href="./skins/custom/favicon.svg"`) {
		t.Error("skin_dir со своим favicon.svg должен переопределять ссылку на иконку")
	}
}

func TestServer_SkinDirWithoutFaviconUsesDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.css"), []byte(`:root[data-theme="dark"]{--mint:#000;}`), 0644); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{SkinDir: dir})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `href="./favicon.svg"`) {
		t.Error("skin_dir без favicon.svg должен оставлять дефолтную иконку")
	}
}

func TestServer_IndexGogaTheme_TitleAndFavicon(t *testing.T) {
	srv := New(Config{Theme: config.ThemeGoga})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `<title>QArium</title>`) {
		t.Error("goga скин должен подставлять <title>QArium</title> из title.txt")
	}
	if strings.Contains(body, `<title>afm Dashboard</title>`) {
		t.Error("goga скин не должен оставлять дефолтный <title>")
	}
	if !strings.Contains(body, `type="image/png" href="./skins/goga/favicon.png"`) {
		t.Error("goga скин должен использовать растровый favicon.png с type=image/png")
	}
}

func TestServer_ServesGogaLogoAsset(t *testing.T) {
	srv := New(Config{Theme: config.ThemeGoga})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/skins/goga/quarium-logo.png", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /skins/goga/quarium-logo.png: got %d, want 200", w.Code)
	}
	if w.Body.Len() == 0 {
		t.Error("quarium-logo.png должен быть непустым")
	}
}

func TestServer_SkinDirCustomTitle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.css"), []byte(`:root[data-theme="dark"]{--mint:#000;}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "title.txt"), []byte("My Skin"), 0644); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{SkinDir: dir})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `<title>My Skin</title>`) {
		t.Error("skin_dir с title.txt должен подставлять свой <title>")
	}
}

func TestServer_SkinDirWithoutTitleKeepsDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.css"), []byte(`:root[data-theme="dark"]{--mint:#000;}`), 0644); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{SkinDir: dir})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `<title>afm Dashboard</title>`) {
		t.Error("skin_dir без title.txt должен оставлять дефолтный <title>")
	}
}
