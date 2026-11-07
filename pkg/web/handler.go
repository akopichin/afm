package web

import (
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/contracts/config"
)

// themeCustom — активный skin_dir (не значение EffectiveTheme, поэтому не в
// pkg/config; goga/novacorps/graphite берутся из config.Theme*).
const themeCustom = "custom"

// Имена файлов внутри директории скина (встроенной или skin_dir).
const skinIndexFile = "index.css"

// skinFaviconCandidates — имена favicon-файлов скина в порядке поиска;
// расширение первого найденного определяет <link type="..."> (svg/png).
var skinFaviconCandidates = []string{"favicon.svg", "favicon.png"}

const skinTitleFile = "title.txt"

const (
	mimeSVG = "image/svg+xml"
	mimePNG = "image/png"
)

// defaultFaviconHref — общий дефолтный favicon, используется, когда у активного
// скина нет своего favicon.svg/favicon.png.
const defaultFaviconHref = "./favicon.svg"

// customSkinRoute — префикс маршрута, отдающего skin_dir с диска.
const customSkinRoute = "/skins/custom/"

// dashboardCSP — Content-Security-Policy для документа дашборда.
const dashboardCSP = "default-src 'self'; " +
	"img-src 'self' data: blob:; " +
	"style-src 'self' 'unsafe-inline'; " +
	"script-src 'self' 'sha256-lZhgtiuhrcX+JCIbk+3Pi4xUGHjw107eJYKPorMPpdQ='; " +
	"connect-src 'self' ws: wss:; " +
	"font-src 'self' data:; " +
	"base-uri 'self'; object-src 'none'; frame-ancestors 'none'"

// HandlerConfig задаёт оформление дашборда: Theme — встроенный скин, SkinDir — каталог своего скина.
type HandlerConfig struct {
	Theme   string
	SkinDir string
}

// handler отдаёт встроенный Web UI дашборда.
type handler struct {
	indexBytes []byte       // предподготовленный index.html (с заменами скина/favicon)
	fileServer http.Handler // отдаёт встроенную статику (skins/, assets, ...)
}

// NewHandler возвращает http.Handler с дашбордом: index.html с выбранным скином и статика из FS.
func NewHandler(cfg HandlerConfig) http.Handler {
	h := &handler{fileServer: http.FileServer(http.FS(FS))}

	skinName := builtinSkinName(cfg.Theme)
	skinHref := skinHrefFor(skinName)
	faviconHref, faviconMimeType, faviconFound := embeddedFavicon(skinName)
	if !faviconFound {
		faviconHref, faviconMimeType = defaultFaviconHref, mimeSVG
	}
	titleText := ""
	if data, err := fs.ReadFile(FS, "skins/"+skinName+"/"+skinTitleFile); err == nil {
		titleText = strings.TrimSpace(string(data))
	}

	// skin_dir полностью подменяет активный скин: нужен index.css внутри директории, иначе — предупреждение и
	// fallback на встроенный скин.
	var customSkinServer http.Handler
	if cfg.SkinDir != "" {
		if _, err := os.Stat(filepath.Join(cfg.SkinDir, skinIndexFile)); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skin_dir %q has no %s, using skin %q\n", cfg.SkinDir, skinIndexFile, skinName)
		} else {
			customSkinServer = http.FileServer(http.FS(os.DirFS(cfg.SkinDir)))
			skinName = themeCustom
			skinHref = skinHrefFor(themeCustom)
			faviconHref, faviconMimeType = defaultFaviconHref, mimeSVG
			if href, mime, ok := findSkinFavicon("./skins/custom", func(name string) bool {
				_, err := os.Stat(filepath.Join(cfg.SkinDir, name))
				return err == nil
			}); ok {
				faviconHref, faviconMimeType = href, mime
			}
			titleText = ""
			if data, err := os.ReadFile(filepath.Join(cfg.SkinDir, skinTitleFile)); err == nil {
				titleText = strings.TrimSpace(string(data))
			}
		}
	}

	// Предподготовка index.html: подменяем ссылку на CSS, класс body, favicon и, если у скина есть title.txt,
	// <title>. Замены строковые — сами файлы скина отдаст fileServer/customSkinServer позже.
	indexBytes, err := fs.ReadFile(FS, "index.html")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: read embedded index.html: %v\n", err)
	} else {
		indexBytes = bytes.ReplaceAll(indexBytes,
			[]byte(`href="`+skinHrefFor(config.ThemeGraphite)+`"`), []byte(`href="`+skinHref+`"`))
		indexBytes = bytes.ReplaceAll(indexBytes,
			[]byte(`class="theme-`+config.ThemeGraphite+`"`), []byte(`class="theme-`+skinName+`"`))
		indexBytes = bytes.ReplaceAll(indexBytes,
			[]byte(`type="`+mimeSVG+`" href="`+defaultFaviconHref+`"`),
			[]byte(`type="`+faviconMimeType+`" href="`+faviconHref+`"`))
		if titleText != "" {
			indexBytes = bytes.ReplaceAll(indexBytes,
				[]byte(`<title>afm Dashboard</title>`), []byte(`<title>`+titleText+`</title>`))
		}
		h.indexBytes = indexBytes
	}

	mux := http.NewServeMux()
	if customSkinServer != nil {
		// custom skin CSS — тоже фиксированное имя, не кэшируем (см. serveStatic).
		mux.Handle(customSkinRoute, http.StripPrefix(customSkinRoute, noStore(customSkinServer)))
	}
	mux.HandleFunc("/", h.serveStatic)
	return mux
}

// faviconMIME возвращает MIME-тип favicon по имени файла (расширению).
func faviconMIME(name string) string {
	if strings.HasSuffix(name, ".png") {
		return mimePNG
	}
	return mimeSVG
}

// skinHrefFor возвращает относительный href на стиль встроенного скина по имени.
func skinHrefFor(name string) string {
	return "./skins/" + name + "/" + skinIndexFile
}

// findSkinFavicon ищет favicon скина через statFn (embed или диск) по skinFaviconCandidates. Возвращает
// относительный href, MIME и true, если нашёл; иначе false — вызывающий использует дефолт.
func findSkinFavicon(base string, statFn func(name string) bool) (href, mime string, found bool) {
	for _, name := range skinFaviconCandidates {
		if statFn(name) {
			return base + "/" + name, faviconMIME(name), true
		}
	}
	return "", "", false
}

// builtinSkinName нормализует Theme до имени встроенного скина: "goga", "novacorps" или дефолт "graphite"
// (сюда же попадает устаревший алиас "coffee", пустое и неизвестные значения).
func builtinSkinName(theme string) string {
	switch theme {
	case config.ThemeGoga:
		return config.ThemeGoga
	case config.ThemeNovacorps:
		return config.ThemeNovacorps
	default:
		return config.ThemeGraphite
	}
}

// embeddedFavicon ищет favicon встроенного скина среди skinFaviconCandidates
// (skins/<name>/favicon.svg или favicon.png в embed).
func embeddedFavicon(skinName string) (href, mime string, found bool) {
	return findSkinFavicon("./skins/"+skinName, func(name string) bool {
		_, err := fs.Stat(FS, "skins/"+skinName+"/"+name)
		return err == nil
	})
}

func noStore(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
		h.ServeHTTP(w, r)
	})
}

// serveStatic отдаёт index.html для "/" и "/index.html", остальную статику делегирует на FileServer.
func (h *handler) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "/index.html" {
		h.serveIndex(w, r)
		return
	}
	// Скины отдаются с ФИКСИРОВАННЫМИ именами, в отличие от контент-хэшированного JS-бандла. no-store
	// заставляет браузер всегда брать свежий CSS; для локального дашборда это дёшево.
	if strings.HasPrefix(r.URL.Path, "/skins/") {
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
	}
	h.fileServer.ServeHTTP(w, r)
}

// serveIndex отдаёт предподготовленный index.html. Если embed-чтение не удалось
// (indexBytes пуст), fallback на FileServer — защита от регрессии embed.
func (h *handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	// index.html тоже с фиксированным именем и подменяется на лету (скин/ favicon) — не должен кэшироваться,
	// иначе браузер отдаст старую ссылку на assets/skins после пересборки.
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	w.Header().Set("Content-Security-Policy", dashboardCSP)
	if len(h.indexBytes) == 0 {
		h.fileServer.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(h.indexBytes)
}
