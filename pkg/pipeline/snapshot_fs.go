package pipeline

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
)

// errLink is what openComponent reports for a symlink or, on Windows, any reparse point.
var errLink = errors.New("symlink")

// beforeOpenComponent lets a test change the tree just before a path element is opened.
var beforeOpenComponent = func(rel, prefix string) {}

// openNoFollow opens rel one path element at a time, each relative to the directory opened just before it. A
// symlink anywhere on the path, even one swapped in only for a moment, fails the open instead of redirecting it.
func openNoFollow(root *os.Root, rel string) (*os.File, error) {
	if !fs.ValidPath(rel) {
		return nil, &fs.PathError{Op: "open", Path: rel, Err: fs.ErrInvalid}
	}
	f, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return f, nil
	}
	names := strings.Split(rel, "/")
	for i, name := range names {
		prefix := strings.Join(names[:i+1], "/")
		beforeOpenComponent(rel, prefix)
		next, err := openComponent(f, name)
		f.Close()
		if errors.Is(err, errLink) {
			return nil, fmt.Errorf("%s is a symlink; symlinks are not supported in a package", prefix)
		}
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: prefix, Err: err}
		}
		f = next
	}
	return f, nil
}

// noFollowFS is a package tree seen through openNoFollow, so fs.WalkDir neither follows nor lists through a symlink.
type noFollowFS struct{ root *os.Root }

func (n noFollowFS) Open(name string) (fs.File, error) { return openNoFollow(n.root, name) }

func (n noFollowFS) Stat(name string) (fs.FileInfo, error) {
	f, err := openNoFollow(n.root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}

// ReadDir lists the opened directory with Readdir, which stats each entry relative to it; a lazy DirEntry.Info
// would look the entry up again by path.
func (n noFollowFS) ReadDir(name string) ([]fs.DirEntry, error) {
	f, err := openNoFollow(n.root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	infos, err := f.Readdir(-1)
	if err != nil {
		return nil, err
	}
	entries := make([]fs.DirEntry, len(infos))
	for i, info := range infos {
		entries[i] = fs.FileInfoToDirEntry(info)
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}
