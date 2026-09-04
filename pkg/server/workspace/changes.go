package workspace

import "bytes"

// nameStatusRec is one record of `git diff --name-status -z` (rename/copy
// detection is disabled, so there are never old+new path triples).
//
//nolint:unused // consumed by parseNameStatusZ below; wired into FS.Changes by Task 4
type nameStatusRec struct {
	status byte
	path   string
}

// mapStatus maps a git single-letter status to a ChangeStatus. ok=false for any
// status this feature does not expect (with --no-renames, R/C never appear).
//
//nolint:unused // wired into FS.Changes by Task 4; exercised directly by changes_test.go (linux-only) until then
func mapStatus(s byte) (ChangeStatus, bool) {
	switch s {
	case 'A':
		return ChangeAdded, true
	case 'D':
		return ChangeDeleted, true
	case 'M', 'T', 'U':
		return ChangeModified, true
	default:
		return "", false
	}
}

// parseNameStatusZ parses `git diff --name-status -z` output: NUL-separated
// fields alternating status,path,status,path,… (NOT a "status\tpath" line).
// A dangling final record (status without a path) is an error unless truncated
// is true (byte-cap fired mid-record), in which case the partial tail is
// dropped. An unknown/unmapped status is an error.
//
//nolint:unused // wired into FS.Changes by Task 4; exercised directly by changes_test.go (linux-only) until then
func parseNameStatusZ(data []byte, truncated bool) ([]nameStatusRec, error) {
	fields := splitZ(data)
	// git -z terminates every field with NUL, so complete output ends in NUL
	// (splitZ drops that trailing empty). A non-NUL final byte means the stream
	// was cut mid-field: legal only when the byte-cap fired, and the partial
	// final field must be discarded before pairing.
	if len(data) > 0 && data[len(data)-1] != 0 {
		if !truncated {
			return nil, ErrReadFailed
		}
		if len(fields) > 0 {
			fields = fields[:len(fields)-1]
		}
	}
	recs := make([]nameStatusRec, 0, len(fields)/2)
	for i := 0; i+1 < len(fields); i += 2 {
		st := fields[i]
		if len(st) != 1 {
			return nil, ErrReadFailed
		}
		if _, ok := mapStatus(st[0]); !ok {
			return nil, ErrReadFailed
		}
		recs = append(recs, nameStatusRec{status: st[0], path: string(fields[i+1])})
	}
	// An odd leftover field (a status with no following path) is malformed
	// unless the byte-cap fired.
	if len(fields)%2 != 0 && !truncated {
		return nil, ErrReadFailed
	}
	return recs, nil
}

// parseUntrackedZ parses `git ls-files -z` output: NUL-separated paths.
//
//nolint:unused // wired into FS.Changes by Task 4; exercised directly by changes_test.go (linux-only) until then
func parseUntrackedZ(data []byte) []string {
	fields := splitZ(data)
	// A final field not terminated by NUL is a byte-cap fragment — drop it.
	if len(data) > 0 && data[len(data)-1] != 0 && len(fields) > 0 {
		fields = fields[:len(fields)-1]
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, string(f))
	}
	return out
}

// splitZ splits on NUL and drops the trailing empty element that a
// NUL-terminated stream always produces. Empty fields elsewhere are impossible
// (git never emits an empty path/status).
//
//nolint:unused // consumed by parseNameStatusZ/parseUntrackedZ above; wired into FS.Changes by Task 4
func splitZ(data []byte) [][]byte {
	parts := bytes.Split(data, []byte{0})
	if n := len(parts); n > 0 && len(parts[n-1]) == 0 {
		parts = parts[:n-1]
	}
	return parts
}
