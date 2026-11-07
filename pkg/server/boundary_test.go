package server

import (
	"os/exec"
	"strings"
	"testing"
)

const webPkg = "github.com/akopichin/afm/pkg/web"

func listDeps(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", pkg, err, out)
	}
	return strings.Fields(string(out))
}

// API собирается без Web UI: UI подключает launcher, а не сервер.
func TestBoundary_ServerDoesNotImportWeb(t *testing.T) {
	for _, dep := range listDeps(t, ".") {
		if dep == webPkg || strings.HasPrefix(dep, webPkg+"/") {
			t.Errorf("pkg/server depends on %s", dep)
		}
	}
}

// Бинарник всё равно содержит UI: его подключает cmd/afm.
func TestBoundary_BinaryImportsWeb(t *testing.T) {
	for _, dep := range listDeps(t, "../../cmd/afm") {
		if dep == webPkg {
			return
		}
	}
	t.Errorf("cmd/afm does not depend on %s", webPkg)
}
