package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/accounting"
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

// TestServer_ConfigAcceptsAccountant — контракт: Config имеет поле Accountant
// (локальный интерфейс server.Accountant, которому структурно удовлетворяет
// *accounting.Accountant), и New(Config{...Accountant...}) компилируется и
// пробрасывает cfg.Accountant в s.accountant.
func TestServer_ConfigAcceptsAccountant(t *testing.T) {
	// Compile-time: конкретный *accounting.Accountant удовлетворяет локальному
	// интерфейсу Accountant — именно это Server.New передаст в UsageHandler.
	var _ Accountant = (*accounting.Accountant)(nil)

	srv := New(Config{
		Port:       0,
		RunDir:     t.TempDir(),
		Accountant: &stubAccountant{},
	})
	if srv == nil {
		t.Fatal("New вернул nil")
	}
	if srv.accountant == nil {
		t.Error("s.accountant не пробрасывается из cfg.Accountant")
	}
}

// TestServer_UsageRouteWired — маршрут /api/usage действительно зарегистрирован в
// Server.New: запрос через полный mux Server.Handler() доходит до UsageHandler и
// возвращает JSON-агрегаты, а не 404. Подтверждает связку route→handler, а не
// только работу UsageHandler в изоляции (этот путь отличается от тестов
// usage_handler_test.go, где хендлер вызывается напрямую).
func TestServer_UsageRouteWired(t *testing.T) {
	want := []accounting.UsageAggregate{
		{StageID: "design", TimeBucket: "2026-07-07T10:00:00Z", Metric: "tokens", Value: 1200},
	}
	srv := New(Config{
		Port:       0,
		RunDir:     t.TempDir(),
		Accountant: &stubAccountant{result: want},
	})
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/usage?metric=tokens", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (маршрут /api/usage должен быть зарегистрирован)", w.Code)
	}
	var got []accounting.UsageAggregate
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v (body=%q)", err, w.Body.String())
	}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("body: got %+v, want %+v", got, want)
	}
}

func TestServer_IndexDefaultTheme(t *testing.T) {
	srv := New(Config{Accountant: &stubAccountant{}})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	// После миграции на React/Vite index.html ссылается на стиль и бандл
	// относительными путями с префиксом "./" (href="./style.css",
	// src="./assets/index-<hash>.js"), тело body несёт class="theme-novacorps"
	// и точку монтирования <div id="root">.
	if !strings.Contains(body, `href="./style.css"`) {
		t.Error("default тема должна ссылаться на ./style.css")
	}
	if !strings.Contains(body, `class="theme-novacorps"`) {
		t.Error("default тема должна ставить class theme-novacorps")
	}
	if !strings.Contains(body, `id="root"`) {
		t.Error("index должен содержать точку монтирования React (#root)")
	}
	if strings.Contains(body, "style-goga") {
		t.Error("default тема не должна ссылаться на style-goga.css")
	}
}

func TestServer_IndexGogaTheme(t *testing.T) {
	srv := New(Config{Theme: themeGoga, Accountant: &stubAccountant{}})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	// БОД-класс темы подменяется сервером корректно (server.go заменяет
	// class="theme-novacorps" → class="theme-goga") — это рабочая часть
	// theme-switching, её проверяем строго.
	if !strings.Contains(body, `class="theme-goga"`) {
		t.Error("goga тема должна ставить class theme-goga")
	}
	if strings.Contains(body, "theme-novacorps") {
		t.Error("goga тема не должна содержать theme-novacorps")
	}
	// CSS-своп: server.go подменяет href="./style.css" → href="./style-goga.css"
	// (Vite собирает относительные пути). Проверяем строго.
	if !strings.Contains(body, `href="./style-goga.css"`) {
		t.Error("goga тема должна ссылаться на ./style-goga.css")
	}
	if strings.Contains(body, `href="./style.css"`) {
		t.Error("goga тема не должна ссылаться на дефолтный ./style.css")
	}
}

func TestServer_ServesGogaStylesheet(t *testing.T) {
	srv := New(Config{Theme: "goga", Accountant: &stubAccountant{}})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/style-goga.css", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /style-goga.css: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "--mint") {
		t.Error("style-goga.css должен определять CSS-токен --mint")
	}
}
