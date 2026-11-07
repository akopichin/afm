package web

import (
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/config"
)

func TestHandler_ServesReactBundle(t *testing.T) {
	handler := NewHandler(HandlerConfig{})

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

func TestHandler_IndexDefaultTheme(t *testing.T) {
	handler := NewHandler(HandlerConfig{})

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `href="./skins/graphite/index.css"`) {
		t.Error("default скин должен ссылаться на ./skins/graphite/index.css")
	}
	if !strings.Contains(body, `class="theme-graphite"`) {
		t.Error("default скин должен ставить class theme-graphite")
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

// permanentStylesheets — структурный CSS, который доставляется ПОСТОЯННОЙ ссылкой мимо скина: skin_dir
// заменяет основной stylesheet, а модалка бокового агента и чат-композеры должны оставаться свёрстанными.
// marker — правило, которое обязано быть в файле.
var permanentStylesheets = []struct{ path, marker string }{
	{"side-agent.css", ".side-agent-modal"},
	{"chat-composer.css", ".chat-composer-row"},
}

// Постоянные ссылки есть в index (их добавляет index.dev.html, сборка переносит в index.html), а файлы
// реально отдаются: HTML-only проверка не поймала бы 404 из-за отсутствия файла в //go:embed.
func TestHandler_PermanentStylesheetsLinkedAndServed(t *testing.T) {
	handler := NewHandler(HandlerConfig{})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	if !strings.Contains(body, `href="./skins/graphite/index.css"`) {
		t.Error("index должен сохранять активную skin-ссылку")
	}
	for _, css := range permanentStylesheets {
		if !strings.Contains(body, `href="./`+css.path+`"`) {
			t.Errorf("index должен содержать постоянную ссылку на /%s", css.path)
		}
		cssW := httptest.NewRecorder()
		handler.ServeHTTP(cssW, httptest.NewRequest("GET", "/"+css.path, nil))
		if cssW.Code != http.StatusOK {
			t.Fatalf("GET /%s: got %d, want 200 (файл обязан быть в //go:embed)", css.path, cssW.Code)
		}
		if !strings.Contains(cssW.Body.String(), css.marker) {
			t.Errorf("/%s должен содержать структурные правила %s", css.path, css.marker)
		}
	}
}

// При custom skin постоянные таблицы стилей всё равно подключены и отдаются из web.FS.
func TestHandler_PermanentStylesheetsSurviveSkinDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.css"), []byte(`:root{--x:1;}`), 0644); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(HandlerConfig{SkinDir: dir})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	if !strings.Contains(body, `href="./skins/custom/index.css"`) {
		t.Error("skin_dir должен давать custom skin-ссылку")
	}
	for _, css := range permanentStylesheets {
		if !strings.Contains(body, `href="./`+css.path+`"`) {
			t.Errorf("ссылка /%s должна оставаться и для skin_dir", css.path)
		}
		cssW := httptest.NewRecorder()
		handler.ServeHTTP(cssW, httptest.NewRequest("GET", "/"+css.path, nil))
		if cssW.Code != http.StatusOK {
			t.Fatalf("GET /%s под skin_dir: got %d, want 200", css.path, cssW.Code)
		}
	}
}

func TestHandler_IndexGogaTheme(t *testing.T) {
	handler := NewHandler(HandlerConfig{Theme: config.ThemeGoga})

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
	if strings.Contains(body, "theme-graphite") {
		t.Error("goga скин не должен содержать theme-graphite")
	}
	if !strings.Contains(body, `href="./skins/goga/index.css"`) {
		t.Error("goga скин должен ссылаться на ./skins/goga/index.css")
	}
	if strings.Contains(body, `href="./skins/graphite/index.css"`) {
		t.Error("goga скин не должен ссылаться на дефолтный graphite")
	}
}

func TestHandler_IndexNovacorpsTheme(t *testing.T) {
	handler := NewHandler(HandlerConfig{Theme: config.ThemeNovacorps})

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
	if strings.Contains(body, "theme-graphite") {
		t.Error("novacorps скин не должен содержать theme-graphite")
	}
	if !strings.Contains(body, `href="./skins/novacorps/index.css"`) {
		t.Error("novacorps скин должен ссылаться на ./skins/novacorps/index.css")
	}
	if strings.Contains(body, `href="./skins/graphite/index.css"`) {
		t.Error("novacorps скин не должен ссылаться на дефолтный graphite")
	}
}

func TestHandler_ServesGogaStylesheet(t *testing.T) {
	handler := NewHandler(HandlerConfig{Theme: config.ThemeGoga})

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

func TestHandler_ServesBaseSkinPartial(t *testing.T) {
	handler := NewHandler(HandlerConfig{})

	req := httptest.NewRequest("GET", "/skins/base/header.css", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /skins/base/header.css: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), ".global-header") {
		t.Error("skins/base/header.css должен содержать структурные правила .global-header")
	}
}

// Скины отдаются с фиксированными именами и меняются между пересборками — браузер не должен кэшировать
// старый CSS. Проверяем no-store и на CSS скина, и на index.html.
func TestHandler_SkinsAndIndexAreNotCached(t *testing.T) {
	handler := NewHandler(HandlerConfig{})

	for _, path := range []string{"/skins/base/header.css", "/skins/graphite/index.css", "/", "/index.html"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("GET %s: Cache-Control=%q, ожидался no-store", path, cc)
		}
	}
}

func TestHandler_SkinDirOverridesTheme(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.css"), []byte(`:root[data-theme="dark"]{--mint:#123456;}`), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(HandlerConfig{Theme: config.ThemeGoga, SkinDir: dir})

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

func TestHandler_SkinDirMissingIndexFallsBack(t *testing.T) {
	dir := t.TempDir() // пустая директория, index.css нет

	handler := NewHandler(HandlerConfig{Theme: config.ThemeGoga, SkinDir: dir})

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

func TestHandler_SkinDirCustomFavicon(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.css"), []byte(`:root[data-theme="dark"]{--mint:#000;}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "favicon.svg"), []byte("<svg></svg>"), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(HandlerConfig{SkinDir: dir})

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `href="./skins/custom/favicon.svg"`) {
		t.Error("skin_dir со своим favicon.svg должен переопределять ссылку на иконку")
	}
}

func TestHandler_SkinDirWithoutFaviconUsesDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.css"), []byte(`:root[data-theme="dark"]{--mint:#000;}`), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(HandlerConfig{SkinDir: dir})

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `href="./favicon.svg"`) {
		t.Error("skin_dir без favicon.svg должен оставлять дефолтную иконку")
	}
}

func TestHandler_IndexGogaTheme_TitleAndFavicon(t *testing.T) {
	handler := NewHandler(HandlerConfig{Theme: config.ThemeGoga})

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `<title>Goga</title>`) {
		t.Error("goga скин должен подставлять <title>Goga</title> из title.txt")
	}
	if strings.Contains(body, `<title>afm Dashboard</title>`) {
		t.Error("goga скин не должен оставлять дефолтный <title>")
	}
	if !strings.Contains(body, `type="image/png" href="./skins/goga/favicon.png"`) {
		t.Error("goga скин должен использовать растровый favicon.png с type=image/png")
	}
}

func TestHandler_ServesGogaLogoAsset(t *testing.T) {
	handler := NewHandler(HandlerConfig{Theme: config.ThemeGoga})

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

func TestHandler_SkinDirCustomTitle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.css"), []byte(`:root[data-theme="dark"]{--mint:#000;}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "title.txt"), []byte("My Skin"), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(HandlerConfig{SkinDir: dir})

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `<title>My Skin</title>`) {
		t.Error("skin_dir с title.txt должен подставлять свой <title>")
	}
}

func TestHandler_SkinDirWithoutTitleKeepsDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.css"), []byte(`:root[data-theme="dark"]{--mint:#000;}`), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(HandlerConfig{SkinDir: dir})

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `<title>afm Dashboard</title>`) {
		t.Error("skin_dir без title.txt должен оставлять дефолтный <title>")
	}
}

func TestServeIndex_SetsCSP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	NewHandler(HandlerConfig{}).ServeHTTP(w, req)

	got := w.Header().Get("Content-Security-Policy")
	if got == "" {
		t.Fatal("serveIndex did not set Content-Security-Policy")
	}
	for _, want := range []string{
		"default-src 'self'",
		"img-src 'self' data: blob:",
		"connect-src 'self' ws: wss:",
		"object-src 'none'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CSP missing %q; got %q", want, got)
		}
	}
}

func TestDashboardCSP_InlineScriptHashMatches(t *testing.T) {
	indexBytes, err := fs.ReadFile(FS, "index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	m := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindSubmatch(indexBytes)
	if m == nil {
		t.Fatal("no inline <script> found in index.html")
	}
	sum := sha256.Sum256(m[1])
	want := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
	if !strings.Contains(dashboardCSP, want) {
		t.Errorf("dashboardCSP is missing the inline-script hash.\n  computed: %q\n  update the script-src hash in dashboardCSP (handler.go)", want)
	}
}
