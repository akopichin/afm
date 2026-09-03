package workspace

import "context"

// fsImpl is the concrete FS backend. It keeps one open directory handle per
// root (byID) and preserves the declared order (order) for stable Roots()
// output. When the platform or kernel lacks openat2, byID is simply empty and
// every lookup fails cleanly — the browser capability is off, nothing panics.
type fsImpl struct {
	byID  map[string]*rootHandle
	order []string
}

// New opens a secure filesystem backend over the given roots.
//
// On Linux with openat2 (kernel ≥ 5.6) it opens a directory fd per root and
// records it. A root whose path can't be opened (missing mount) is skipped. If
// openat2 itself is unavailable — ENOSYS on an old kernel, or any non-Linux OS
// where the probe returns "unsupported" — New degrades gracefully: it returns a
// backend with zero roots and no error, so the rest of the server runs normally
// with the file browser simply disabled.
func New(roots []Root) (FS, error) {
	fs := &fsImpl{byID: map[string]*rootHandle{}}
	for _, r := range roots {
		dirf, err := openRootDir(r.Path)
		if err != nil {
			continue // missing mount / unsupported OS → skip this root
		}
		if err := probeOpenat2Supported(dirf); err != nil {
			// ENOSYS (kernel < 5.6) or unsupported OS → capability off entirely.
			_ = closeFD(dirf)
			return &fsImpl{byID: map[string]*rootHandle{}}, nil
		}
		fs.order = append(fs.order, r.ID)
		fs.byID[r.ID] = &rootHandle{root: r, dirf: dirf}
	}
	return fs, nil
}

// resolve is the single lookup+validation gate every read path shares (List,
// Read, Reference, Diff). It maps a rootID to its handle and validates relPath,
// returning the handle and the cleaned relative path. Tasks 7-9 build on this
// and must not re-implement lookup or validation.
func (fs *fsImpl) resolve(rootID, relPath string) (*rootHandle, string, error) {
	h, ok := fs.byID[rootID]
	if !ok {
		return nil, "", ErrInvalidRootOrPath
	}
	clean, err := validateRelPath(relPath)
	if err != nil {
		return nil, "", err
	}
	return h, clean, nil
}

// Roots returns the wire view of every open root, in declaration order. The
// absolute Path is deliberately never exposed.
func (fs *fsImpl) Roots() []RootView {
	views := make([]RootView, 0, len(fs.order))
	for _, id := range fs.order {
		r := fs.byID[id].root
		views = append(views, RootView{
			ID:            r.ID,
			Label:         r.Label,
			Kind:          r.Kind,
			MountReadOnly: r.MountReadOnly,
		})
	}
	return views
}

// Close releases every open root directory fd.
func (fs *fsImpl) Close() error {
	var firstErr error
	for _, h := range fs.byID {
		if err := closeFD(h.dirf); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// List is implemented in list.go. Read and Reference are implemented in
// content.go.

// Diff is a stub until Task 9 implements it. It exists now only so fsImpl
// satisfies the FS interface.

func (fs *fsImpl) Diff(context.Context, string, string) (Diff, error) {
	return Diff{}, ErrNotFound
}
