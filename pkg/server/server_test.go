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

func TestServerServesMarkdownIt(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/markdown-it.min.js", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /markdown-it.min.js: ожидался 200, получен %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "markdownit") {
		t.Error("в теле ответа нет глобала markdownit")
	}
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
	if !strings.Contains(body, `href="style.css"`) {
		t.Error("default тема должна ссылаться на style.css")
	}
	if !strings.Contains(body, `class="theme-novacorps"`) {
		t.Error("default тема должна ставить class theme-novacorps")
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
	if !strings.Contains(body, `href="style-goga.css"`) {
		t.Error("goga тема должна ссылаться на style-goga.css")
	}
	if !strings.Contains(body, `class="theme-goga"`) {
		t.Error("goga тема должна ставить class theme-goga")
	}
	if strings.Contains(body, "theme-novacorps") {
		t.Error("goga тема не должна содержать theme-novacorps")
	}
	if strings.Contains(body, `href="style.css"`) {
		t.Error("goga тема не должна ссылаться на style.css")
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
