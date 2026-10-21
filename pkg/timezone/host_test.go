package timezone

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setLocaltimePath подменяет путь к /etc/localtime на время теста.
func setLocaltimePath(t *testing.T, path string) {
	t.Helper()
	prev := localtimePath
	localtimePath = path
	t.Cleanup(func() { localtimePath = prev })
}

// unsetTZ убирает $TZ, регистрируя восстановление исходного значения.
func unsetTZ(t *testing.T) {
	t.Helper()
	t.Setenv("TZ", "")
	if err := os.Unsetenv("TZ"); err != nil {
		t.Fatal(err)
	}
}

func TestHost_IANATZEnv(t *testing.T) {
	t.Setenv("TZ", "Asia/Tokyo")

	got := Host()
	if got.Name != "Asia/Tokyo" {
		t.Errorf("Name = %q, want %q", got.Name, "Asia/Tokyo")
	}
	if got.OffsetMinutes != 540 {
		t.Errorf("OffsetMinutes = %d, want 540", got.OffsetMinutes)
	}
}

func TestHost_AbsoluteZoneinfoTZEnv(t *testing.T) {
	zoneFile := "/usr/share/zoneinfo/America/New_York"
	if _, err := os.Stat(zoneFile); err != nil {
		t.Skipf("zone file unavailable: %v", err)
	}
	t.Setenv("TZ", ":"+zoneFile)
	link := filepath.Join(t.TempDir(), "localtime")
	if err := os.Symlink("/usr/share/zoneinfo/Asia/Tokyo", link); err != nil {
		t.Fatal(err)
	}
	setLocaltimePath(t, link)

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	_, offSec := time.Now().In(loc).Zone()
	got := Host()
	if got.Name != "America/New_York" || got.OffsetMinutes != offSec/60 {
		t.Errorf("Host() = %+v, want America/New_York with offset %d", got, offSec/60)
	}
}

func TestHost_AbsoluteSymlinkTZEnv(t *testing.T) {
	zoneFile := "/usr/share/zoneinfo/America/New_York"
	if _, err := os.Stat(zoneFile); err != nil {
		t.Skipf("zone file unavailable: %v", err)
	}
	link := filepath.Join(t.TempDir(), "host-zone")
	if err := os.Symlink(zoneFile, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TZ", link)
	localtime := filepath.Join(t.TempDir(), "localtime")
	if err := os.Symlink("/usr/share/zoneinfo/Asia/Tokyo", localtime); err != nil {
		t.Fatal(err)
	}
	setLocaltimePath(t, localtime)

	got := Host()
	if got.Name != "America/New_York" {
		t.Fatalf("Name = %q, want America/New_York from TZ symlink", got.Name)
	}
	loc, err := time.LoadLocation(got.Name)
	if err != nil {
		t.Fatal(err)
	}
	_, winterOffset := time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC).In(loc).Zone()
	if winterOffset != -5*60*60 {
		t.Errorf("winter offset = %d seconds, want -18000", winterOffset)
	}
}

func TestHost_ExplicitEmptyTZEnv(t *testing.T) {
	t.Setenv("TZ", "")
	link := filepath.Join(t.TempDir(), "localtime")
	if err := os.Symlink("/usr/share/zoneinfo/Europe/Berlin", link); err != nil {
		t.Fatal(err)
	}
	setLocaltimePath(t, link)

	got := Host()
	if got != (Info{Name: "UTC"}) {
		t.Errorf("Host() = %+v, want UTC for explicitly empty TZ", got)
	}
}

func TestHost_CustomAbsoluteTZEnvKeepsRuntimeOffset(t *testing.T) {
	data, err := os.ReadFile("/usr/share/zoneinfo/America/New_York")
	if err != nil {
		t.Skipf("zone file unavailable: %v", err)
	}
	zoneFile := filepath.Join(t.TempDir(), "custom-zone")
	if err := os.WriteFile(zoneFile, data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TZ", zoneFile)
	_, offSec := time.Now().Zone()

	// A custom zone file has no IANA name. /etc/localtime must not replace
	// the explicit TZ override, even when its symlink has a known name.
	otherZone := "Asia/Tokyo"
	if offSec/60 == 540 {
		otherZone = "Pacific/Kiritimati"
	}
	link := filepath.Join(t.TempDir(), "localtime")
	if err := os.Symlink("/usr/share/zoneinfo/"+otherZone, link); err != nil {
		t.Fatal(err)
	}
	setLocaltimePath(t, link)

	got := Host()
	if got.Name != "" || got.OffsetMinutes != offSec/60 {
		t.Errorf("Host() = %+v, want unnamed runtime offset %d", got, offSec/60)
	}
}

func TestHost_LocaltimeSymlink(t *testing.T) {
	unsetTZ(t)
	link := filepath.Join(t.TempDir(), "localtime")
	if err := os.Symlink("/usr/share/zoneinfo/Pacific/Kiritimati", link); err != nil {
		t.Fatal(err)
	}
	setLocaltimePath(t, link)

	got := Host()
	if got.Name != "Pacific/Kiritimati" {
		t.Errorf("Name = %q, want %q", got.Name, "Pacific/Kiritimati")
	}
	if got.OffsetMinutes != 840 {
		t.Errorf("OffsetMinutes = %d, want 840", got.OffsetMinutes)
	}
}

func TestHost_LocaltimeSymlinkWithDoubleSlash(t *testing.T) {
	unsetTZ(t)
	link := filepath.Join(t.TempDir(), "localtime")
	if err := os.Symlink("/usr/share/zoneinfo//America/New_York", link); err != nil {
		t.Fatal(err)
	}
	setLocaltimePath(t, link)

	got := Host()
	if got.Name != "America/New_York" {
		t.Fatalf("Name = %q, want America/New_York", got.Name)
	}
	loc, err := time.LoadLocation(got.Name)
	if err != nil {
		t.Fatal(err)
	}
	_, winterOffset := time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC).In(loc).Zone()
	if winterOffset != -5*60*60 {
		t.Errorf("winter offset = %d seconds, want -18000", winterOffset)
	}
}

func TestHost_PosixZoneNameIsBrowserCompatible(t *testing.T) {
	t.Setenv("TZ", "posix/America/New_York")
	if got := Host().Name; got != "America/New_York" {
		t.Errorf("TZ name = %q, want browser-compatible America/New_York", got)
	}

	unsetTZ(t)
	link := filepath.Join(t.TempDir(), "localtime")
	if err := os.Symlink("/usr/share/zoneinfo/posix/America/New_York", link); err != nil {
		t.Fatal(err)
	}
	setLocaltimePath(t, link)
	if got := Host().Name; got != "America/New_York" {
		t.Errorf("symlink name = %q, want browser-compatible America/New_York", got)
	}
}

func TestHost_GoOnlyZoneNamesAreNotSentToBrowser(t *testing.T) {
	for _, name := range []string{"Local", "right/America/New_York", "SystemV/EST5EDT", "Factory"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TZ", name)
			if got := Host().Name; got != "" {
				t.Errorf("Host().Name = %q, want empty for Go-only zone", got)
			}
		})
	}
}

func TestHost_UnidentifiableLocaltime(t *testing.T) {
	unsetTZ(t)
	file := filepath.Join(t.TempDir(), "localtime")
	if err := os.WriteFile(file, []byte("not a zone"), 0o644); err != nil {
		t.Fatal(err)
	}
	setLocaltimePath(t, file)

	got := Host()
	if got.Name != "" {
		t.Errorf("Name = %q, want empty (unknown zone must not pretend to be known)", got.Name)
	}
	_, offSec := time.Now().Zone()
	if got.OffsetMinutes != offSec/60 {
		t.Errorf("OffsetMinutes = %d, want host offset %d", got.OffsetMinutes, offSec/60)
	}
}

func TestFromTransport_ValidNameAndOffset(t *testing.T) {
	t.Setenv(EnvHostTimeZone, "Asia/Tokyo")
	t.Setenv(EnvHostUTCOffsetMinutes, "540")

	got := FromTransport()
	if got.Name != "Asia/Tokyo" || got.OffsetMinutes != 540 {
		t.Errorf("FromTransport() = %+v, want {Asia/Tokyo 540}", got)
	}
}

func TestFromTransport_ZoneFileUpdatesOffsetAcrossDST(t *testing.T) {
	data, err := os.ReadFile("/usr/share/zoneinfo/America/New_York")
	if err != nil {
		t.Skipf("zone file unavailable: %v", err)
	}
	zoneFile := filepath.Join(t.TempDir(), "host-zone")
	if err := os.WriteFile(zoneFile, data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvHostTimeZone, "")
	t.Setenv(EnvHostUTCOffsetMinutes, "-300")
	t.Setenv("AFM_HOST_ZONE_FILE", zoneFile)

	before := fromTransportAt(time.Date(2026, time.March, 8, 6, 59, 59, 0, time.UTC))
	after := fromTransportAt(time.Date(2026, time.March, 8, 7, 0, 0, 0, time.UTC))
	if before != (Info{OffsetMinutes: -300}) || after != (Info{OffsetMinutes: -240}) {
		t.Errorf("transport offsets around DST boundary before=%+v after=%+v, want -300 then -240", before, after)
	}
}

func TestFromTransport_NamedZoneUpdatesOffsetAcrossDST(t *testing.T) {
	t.Setenv(EnvHostTimeZone, "America/New_York")
	t.Setenv(EnvHostUTCOffsetMinutes, "-300")
	t.Setenv(EnvHostZoneFile, "")

	before := fromTransportAt(time.Date(2026, time.March, 8, 6, 59, 59, 0, time.UTC))
	after := fromTransportAt(time.Date(2026, time.March, 8, 7, 0, 0, 0, time.UTC))
	if before != (Info{Name: "America/New_York", OffsetMinutes: -300}) || after != (Info{Name: "America/New_York", OffsetMinutes: -240}) {
		t.Errorf("named zone offsets around DST before=%+v after=%+v, want -300 then -240", before, after)
	}
}

func TestFromTransport_NamedZoneWithoutRulesKeepsOffset(t *testing.T) {
	t.Setenv(EnvHostTimeZone, "Mars/Olympus_Mons")
	t.Setenv(EnvHostUTCOffsetMinutes, "-330")
	t.Setenv(EnvHostZoneFile, "")
	got := fromTransportAt(time.Date(2026, time.March, 8, 7, 0, 0, 0, time.UTC))
	if got != (Info{Name: "Mars/Olympus_Mons", OffsetMinutes: -330}) {
		t.Errorf("missing zone rules = %+v, want transported offset", got)
	}
}

func TestHostZoneFile_RejectsInvalidTZif(t *testing.T) {
	privateFile := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(privateFile, []byte("private non-zone data"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TZ", privateFile)
	if got := HostZoneFile(); got != "" {
		t.Errorf("HostZoneFile() = %q, want no mountable file for non-TZif content", got)
	}
}

func TestHostZoneFile_ValidatedCopyKeepsDSTRules(t *testing.T) {
	data, err := os.ReadFile("/usr/share/zoneinfo/America/New_York")
	if err != nil {
		t.Skipf("zone file unavailable: %v", err)
	}
	original := filepath.Join(t.TempDir(), "custom-zone")
	if err := os.WriteFile(original, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TZ", original)
	copyPath := HostZoneFile()
	if copyPath == "" || copyPath == original {
		t.Fatalf("HostZoneFile() = %q, want validated separate copy", copyPath)
	}
	if err := os.WriteFile(original, []byte("source replaced after validation"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvHostTimeZone, "")
	t.Setenv(EnvHostUTCOffsetMinutes, "-300")
	t.Setenv(EnvHostZoneFile, copyPath)
	before := fromTransportAt(time.Date(2026, time.March, 8, 6, 59, 59, 0, time.UTC))
	after := fromTransportAt(time.Date(2026, time.March, 8, 7, 0, 0, 0, time.UTC))
	if before.OffsetMinutes != -300 || after.OffsetMinutes != -240 {
		t.Errorf("validated copy DST offsets before=%d after=%d, want -300 and -240", before.OffsetMinutes, after.OffsetMinutes)
	}
}

func TestFromTransport_IANAShapedNameWithoutTzdata(t *testing.T) {
	// Имя синтаксически корректно, но отсутствует в tzdata: контейнер не обязан
	// иметь базу зон, чтобы передать имя дальше в браузер.
	t.Setenv(EnvHostTimeZone, "Mars/Olympus_Mons")
	t.Setenv(EnvHostUTCOffsetMinutes, "-330")

	got := FromTransport()
	if got.Name != "Mars/Olympus_Mons" {
		t.Errorf("Name = %q, want syntactically valid name accepted without tzdata", got.Name)
	}
	if got.OffsetMinutes != -330 {
		t.Errorf("OffsetMinutes = %d, want -330", got.OffsetMinutes)
	}
}

func TestFromTransport_InvalidNameKeepsOffset(t *testing.T) {
	t.Setenv(EnvHostTimeZone, "not a zone!")
	t.Setenv(EnvHostUTCOffsetMinutes, "-330")

	got := FromTransport()
	if got.Name != "" {
		t.Errorf("Name = %q, want empty for a malformed name", got.Name)
	}
	if got.OffsetMinutes != -330 {
		t.Errorf("OffsetMinutes = %d, want -330 (valid offset survives a broken name)", got.OffsetMinutes)
	}
}

func TestFromTransport_InvalidOffsetKeepsNameAndUsesRules(t *testing.T) {
	t.Setenv(EnvHostTimeZone, "Asia/Tokyo")
	t.Setenv(EnvHostUTCOffsetMinutes, "lots")

	got := FromTransport()
	if got.Name != "Asia/Tokyo" || got.OffsetMinutes != 540 {
		t.Errorf("FromTransport() = %+v, want {Asia/Tokyo 540}", got)
	}
}

func TestFromTransport_Empty(t *testing.T) {
	t.Setenv(EnvHostTimeZone, "")
	t.Setenv(EnvHostUTCOffsetMinutes, "")

	got := FromTransport()
	if got != (Info{}) {
		t.Errorf("FromTransport() = %+v, want zero Info", got)
	}
}
