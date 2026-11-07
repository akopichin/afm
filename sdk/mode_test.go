package afmsdk

import (
	"reflect"
	"testing"
)

func TestRunArgs_ByMode(t *testing.T) {
	base := []string{"run", "--dir", "/w", "--port", "4000"}
	cases := []struct {
		mode Mode
		want []string
	}{
		{"", append(append([]string{}, base...), "/f.yaml")},
		{ModeDashboard, append(append([]string{}, base...), "/f.yaml")},
		{ModeCore, append(append([]string{}, base...), "--mode", "core", "/f.yaml")},
	}
	for _, tc := range cases {
		c, err := New(Config{Binary: "/bin/true", Mode: tc.mode})
		if err != nil {
			t.Fatalf("New(%q): %v", tc.mode, err)
		}
		if got := c.runArgs("/w", "/f.yaml", 4000); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("mode %q: argv = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

func TestNew_RejectsUnknownMode(t *testing.T) {
	if _, err := New(Config{Binary: "/bin/true", Mode: "bogus"}); err == nil {
		t.Fatal("New accepted an unknown mode")
	}
}
