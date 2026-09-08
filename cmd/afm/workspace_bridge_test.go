package main

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server/workspace"
	"github.com/akopichin/afm/pkg/state"
)

// fakeWorkspace is a minimal workspace.FS stub for testing
// workspaceResolveFile/workspaceCurrentFileSHA in isolation, without a real
// Linux openat2-backed fsImpl (workspace.New degrades to zero roots on
// non-Linux — see pkg/server/workspace/access_other.go — so it can't
// exercise Read() at all on a macOS dev host). Only Read is exercised by the
// two closures under test; the rest of the interface is implemented just to
// satisfy workspace.FS.
type fakeWorkspace struct {
	files map[string]workspace.File // key: rootID+"/"+relPath
	err   error
}

func (f *fakeWorkspace) Roots() []workspace.RootView { return nil }

func (f *fakeWorkspace) List(context.Context, string, string, string) (workspace.Page, error) {
	return workspace.Page{}, workspace.ErrNotFound
}

func (f *fakeWorkspace) Reference(context.Context, string, string) (workspace.Reference, error) {
	return workspace.Reference{}, workspace.ErrNotFound
}

func (f *fakeWorkspace) Read(_ context.Context, rootID, relPath string) (workspace.File, error) {
	if f.err != nil {
		return workspace.File{}, f.err
	}
	file, ok := f.files[rootID+"/"+relPath]
	if !ok {
		return workspace.File{}, workspace.ErrNotFound
	}
	return file, nil
}

func (f *fakeWorkspace) Diff(context.Context, string, string) (workspace.Diff, error) {
	return workspace.Diff{}, workspace.ErrDiffUnavailable
}

func (f *fakeWorkspace) Changes(context.Context, string, workspace.ChangeMode) (workspace.ChangeList, error) {
	return workspace.ChangeList{}, nil
}

func (f *fakeWorkspace) Search(context.Context, string, string) (workspace.SearchResult, error) {
	return workspace.SearchResult{}, nil
}

func (f *fakeWorkspace) Close() error { return nil }

// TestWorkspaceResolveFile_FileNote проверяет happy-path для ноты без
// привязки к строке: Abs собирается из rootContainerPaths + File.Path,
// DisplayPath/Reference берутся из File как есть, ContentSHA считается через
// state.FileContentSHA от реального содержимого.
func TestWorkspaceResolveFile_FileNote(t *testing.T) {
	ws := &fakeWorkspace{files: map[string]workspace.File{
		"project/main.go": {
			Path:        "main.go",
			DisplayPath: "project/main.go",
			Reference:   `[AFM file: "/workspace/main.go"]`,
			Content:     "package main\n\nfunc main() {}\n",
		},
	}}
	resolve := workspaceResolveFile(ws, map[string]string{"project": "/workspace"})

	rf, ok := resolve("project", "main.go", nil)
	if !ok {
		t.Fatal("resolve: got ok=false, want true")
	}
	if want := "/workspace/main.go"; rf.Abs != want {
		t.Errorf("Abs: got %q, want %q", rf.Abs, want)
	}
	if want := "project/main.go"; rf.DisplayPath != want {
		t.Errorf("DisplayPath: got %q, want %q", rf.DisplayPath, want)
	}
	if want := `[AFM file: "/workspace/main.go"]`; rf.Reference != want {
		t.Errorf("Reference: got %q, want %q", rf.Reference, want)
	}
	wantSHA := state.FileContentSHA([]byte("package main\n\nfunc main() {}\n"))
	if rf.ContentSHA != wantSHA {
		t.Errorf("ContentSHA: got %q, want %q", rf.ContentSHA, wantSHA)
	}
	// Не построчная нота — LineText/InRange не выставляются.
	if rf.LineText != "" || rf.InRange {
		t.Errorf("LineText/InRange: got (%q, %t), want (\"\", false) for a file-level note", rf.LineText, rf.InRange)
	}
}

// TestWorkspaceResolveFile_LineNote_InRange проверяет извлечение конкретной
// 1-индексированной строки из содержимого файла.
func TestWorkspaceResolveFile_LineNote_InRange(t *testing.T) {
	ws := &fakeWorkspace{files: map[string]workspace.File{
		"project/main.go": {
			Path:        "main.go",
			DisplayPath: "project/main.go",
			Reference:   `[AFM file: "/workspace/main.go"]`,
			Content:     "line one\nline two\nline three\n",
		},
	}}
	resolve := workspaceResolveFile(ws, map[string]string{"project": "/workspace"})

	line := 2
	rf, ok := resolve("project", "main.go", &line)
	if !ok {
		t.Fatal("resolve: got ok=false, want true")
	}
	if !rf.InRange {
		t.Fatal("InRange: got false, want true (line 2 exists)")
	}
	if want := "line two"; rf.LineText != want {
		t.Errorf("LineText: got %q, want %q", rf.LineText, want)
	}
}

// TestWorkspaceResolveFile_LineNote_OutOfRange покрывает ErrStaleLine-путь
// AddNote: запрошенная строка не попадает в текущий файл (файл стал короче).
func TestWorkspaceResolveFile_LineNote_OutOfRange(t *testing.T) {
	ws := &fakeWorkspace{files: map[string]workspace.File{
		"project/main.go": {
			Path:    "main.go",
			Content: "only one line\n",
		},
	}}
	resolve := workspaceResolveFile(ws, map[string]string{"project": "/workspace"})

	line := 5
	rf, ok := resolve("project", "main.go", &line)
	if !ok {
		t.Fatal("resolve: got ok=false, want true (file itself resolves fine)")
	}
	if rf.InRange {
		t.Error("InRange: got true, want false (line 5 doesn't exist)")
	}
}

// TestWorkspaceResolveFile_TrailingNewlineLineCount — регрессия на off-by-one
// из-за завершающего "\n": почти любой реальный файл на диске оканчивается
// переводом строки, и наивный strings.Split(content, "\n") на "a\nb\nc\nd\n"
// даёт 5 элементов (["a","b","c","d",""]), а не 4 реальные строки — фантомный
// последний элемент. Из-за этого запрос строки N+1 (на единицу за пределом
// файла — ровно тот случай, когда пользователь удалил ОТМЕЧЕННУЮ строку и
// файл стал короче на одну строку) ошибочно репортился как InRange=true с
// пустым LineText, вместо InRange=false — что глушило ErrStaleLine в
// AddNote и детект дрифта переставал работать для этого частного случая.
// Здесь 4 реальные строки: N=4 должен резолвиться, N+1=5 — нет.
func TestWorkspaceResolveFile_TrailingNewlineLineCount(t *testing.T) {
	ws := &fakeWorkspace{files: map[string]workspace.File{
		"project/main.go": {
			Path:    "main.go",
			Content: "a\nb\nc\nd\n",
		},
	}}
	resolve := workspaceResolveFile(ws, map[string]string{"project": "/workspace"})

	lastReal := 4
	rf, ok := resolve("project", "main.go", &lastReal)
	if !ok {
		t.Fatal("resolve: got ok=false, want true")
	}
	if !rf.InRange {
		t.Fatal("InRange: got false, want true for the last real line (4)")
	}
	if want := "d"; rf.LineText != want {
		t.Errorf("LineText: got %q, want %q", rf.LineText, want)
	}

	phantom := 5
	rf, ok = resolve("project", "main.go", &phantom)
	if !ok {
		t.Fatal("resolve: got ok=false, want true (file itself resolves fine)")
	}
	if rf.InRange {
		t.Errorf("InRange: got true, want false — line 5 is the phantom trailing-newline element, not a real line (LineText=%q)", rf.LineText)
	}
}

// TestWorkspaceResolveFile_WorkspaceError покрывает ErrStaleContent-путь
// AddNote: любая ошибка workspace (файл удалён, слишком большой, бинарный,
// симлинк, ...) должна репортиться единообразно как ok=false, а не паниковать
// или различаться по типу ошибки — вызывающий код (AddNote) уже сворачивает
// это в единый ErrStaleContent.
func TestWorkspaceResolveFile_WorkspaceError(t *testing.T) {
	ws := &fakeWorkspace{err: workspace.ErrTooLarge}
	resolve := workspaceResolveFile(ws, map[string]string{"project": "/workspace"})

	rf, ok := resolve("project", "huge.bin", nil)
	if ok {
		t.Fatal("resolve: got ok=true, want false on workspace error")
	}
	if rf != (orchestrator.ResolvedFile{}) {
		t.Errorf("resolve: got non-zero ResolvedFile %+v on error, want zero value", rf)
	}
}

// TestWorkspaceCurrentFileSHA mirrors the ResolveFile coverage above for the
// simpler CurrentFileSHA closure used by renderReviewFeedback drift-detection.
func TestWorkspaceCurrentFileSHA(t *testing.T) {
	ws := &fakeWorkspace{files: map[string]workspace.File{
		"project/main.go": {Content: "hello\n"},
	}}
	currentSHA := workspaceCurrentFileSHA(ws)

	sha, ok := currentSHA("project", "main.go")
	if !ok {
		t.Fatal("currentSHA: got ok=false, want true")
	}
	if want := state.FileContentSHA([]byte("hello\n")); sha != want {
		t.Errorf("sha: got %q, want %q", sha, want)
	}

	if _, ok := currentSHA("project", "missing.go"); ok {
		t.Error("currentSHA(missing): got ok=true, want false")
	}
}
