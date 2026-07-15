package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	srv := New(Config{Theme: themeGoga})
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
	srv := New(Config{Theme: "goga"})
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
