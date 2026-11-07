package main

import "testing"

func TestProductDockerImage(t *testing.T) {
	for version, want := range map[string]string{
		"dev": "akopichin/afm:3", "v3.0.0": "akopichin/afm:3",
		"v3.0.0-alpha.1": "akopichin/afm:v3.0.0-alpha.1",
		"3.0.0-beta.2":   "akopichin/afm:v3.0.0-beta.2",
	} {
		if got := productDockerImage(version); got != want {
			t.Errorf("%s: got %s, want %s", version, got, want)
		}
	}
}
