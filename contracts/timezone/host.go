// Package timezone определяет часовой пояс хоста (имя IANA и текущее смещение от UTC),
// который dashboard использует для отображения времени сообщений. Пакет разделяют
// docker-транспорт (хост передаёт значения в контейнер) и сервер (/api/status).
package timezone

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Info — часовой пояс хоста: имя IANA (пустое, когда зону определить не удалось)
// и текущее смещение от UTC в минутах со знаком. Пустое имя НЕ означает UTC:
// смещение остаётся реальным смещением хоста.
type Info struct {
	Name          string
	OffsetMinutes int
}

// Env-переменные docker-транспорта: хост кладёт сюда результат Host() при re-exec
// в контейнер. Значения не секретны и передаются обычным `-e KEY=VALUE`.
const (
	EnvHostTimeZone         = "AFM_HOST_TIME_ZONE"
	EnvHostUTCOffsetMinutes = "AFM_HOST_UTC_OFFSET_MINUTES"
	EnvHostZoneFile         = "AFM_HOST_ZONE_FILE"
	ContainerZoneFile       = "/tmp/afm-host-zoneinfo"
)

// localtimePath — тестовый шов: путь к /etc/localtime.
var localtimePath = "/etc/localtime"

// ianaNameRe — синтаксическая форма IANA-имени ("UTC", "Asia/Tokyo", "Etc/GMT+8"):
// сегменты из букв, цифр, "_", "+", "-", разделённые "/". Проверка формы, не tzdata.
var ianaNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+)*$`)

// Host возвращает часовой пояс хоста: явный $TZ имеет приоритет над
// /etc/localtime. Пустой $TZ означает UTC; без $TZ имя берётся из
// zoneinfo-симлинка /etc/localtime. Если имя неизвестно, остаётся текущее
// смещение time.Local.
func Host() Info {
	if tz, set := os.LookupEnv("TZ"); set {
		tz = strings.TrimPrefix(tz, ":")
		if tz == "" {
			return Info{Name: "UTC"}
		}
		if filepath.IsAbs(tz) {
			if resolved, err := filepath.EvalSymlinks(tz); err == nil {
				if info, ok := fromZoneinfoPath(resolved); ok {
					return info
				}
			}
		} else if info, ok := fromName(tz); ok {
			return info
		}
		return currentOffset()
	}
	if target, err := os.Readlink(localtimePath); err == nil {
		if info, ok := fromZoneinfoPath(target); ok {
			return info
		}
	}
	return currentOffset()
}

// HostZoneFile сохраняет проверенные правила зоны в отдельной копии, когда
// имя IANA определить нельзя. Docker монтирует копию, а не исходный TZ-файл:
// пользовательский путь может указывать на приватный файл произвольного типа.
func HostZoneFile() string {
	path := localtimePath
	if tz, set := os.LookupEnv("TZ"); set {
		path = strings.TrimPrefix(tz, ":")
		if !filepath.IsAbs(path) {
			return ""
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	const maxZoneFileSize = 1 << 20
	data, err := io.ReadAll(io.LimitReader(f, maxZoneFileSize+1))
	if err != nil || len(data) > maxZoneFileSize {
		return ""
	}
	if _, err := time.LoadLocationFromTZData("host", data); err != nil {
		return ""
	}
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	cacheDir := filepath.Join(cacheRoot, "afm", "host-zoneinfo")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	copyPath := filepath.Join(cacheDir, fmt.Sprintf("%x.tzif", sum))
	copyFile, err := os.CreateTemp(cacheDir, ".tzif-*")
	if err != nil {
		return ""
	}
	defer func() { _ = os.Remove(copyFile.Name()) }()
	if _, err := copyFile.Write(data); err != nil {
		_ = copyFile.Close()
		return ""
	}
	if err := copyFile.Chmod(0o644); err != nil {
		_ = copyFile.Close()
		return ""
	}
	if err := copyFile.Close(); err != nil {
		return ""
	}
	if err := os.Rename(copyFile.Name(), copyPath); err != nil {
		return ""
	}
	return copyPath
}

func fromZoneinfoPath(path string) (Info, bool) {
	if _, name, found := strings.Cut(path, "zoneinfo/"); found {
		return fromName(strings.TrimLeft(name, "/"))
	}
	return Info{}, false
}

func currentOffset() Info {
	_, offSec := time.Now().Zone()
	return Info{OffsetMinutes: offSec / 60}
}

// fromName валидирует IANA-имя по tzdata хоста и снимает текущее смещение зоны.
func fromName(name string) (Info, bool) {
	name = strings.TrimPrefix(name, "posix/")
	if !ianaNameRe.MatchString(name) || name == "Local" || name == "Factory" ||
		name == "posixrules" || name == "leapseconds" ||
		strings.HasPrefix(name, "right/") || strings.HasPrefix(name, "SystemV/") {
		return Info{}, false
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return Info{}, false
	}
	_, offSec := time.Now().In(loc).Zone()
	return Info{Name: name, OffsetMinutes: offSec / 60}, true
}

// FromTransport читает часовой пояс из env docker-транспорта. Если обе основные
// переменные отсутствуют, возвращает локальную зону. Имя и смещение независимы:
// синтаксически корректное IANA-имя принимается без tzdata в контейнере, а
// валидное смещение сохраняется даже при отсутствующем или битом имени.
func FromTransport() Info {
	return fromTransportAt(time.Now())
}

func fromTransportAt(at time.Time) Info {
	_, hasName := os.LookupEnv(EnvHostTimeZone)
	_, hasOffset := os.LookupEnv(EnvHostUTCOffsetMinutes)
	if !hasName && !hasOffset {
		return Host()
	}
	var info Info
	if name := os.Getenv(EnvHostTimeZone); ianaNameRe.MatchString(name) {
		info.Name = name
	}
	if raw := os.Getenv(EnvHostUTCOffsetMinutes); raw != "" {
		if off, err := strconv.Atoi(raw); err == nil {
			info.OffsetMinutes = off
		}
	}
	// A named zone can change its offset during a long run. Prefer container
	// tzdata when available; keep the transported value if rules are absent.
	if info.Name != "" {
		if loc, err := time.LoadLocation(info.Name); err == nil {
			_, offSec := at.In(loc).Zone()
			info.OffsetMinutes = offSec / 60
			return info
		}
	}
	if path := os.Getenv(EnvHostZoneFile); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			if loc, err := time.LoadLocationFromTZData("host", data); err == nil {
				_, offSec := at.In(loc).Zone()
				info.OffsetMinutes = offSec / 60
			}
		}
	}
	return info
}
