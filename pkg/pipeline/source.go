package pipeline

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// SourceKind says where a package comes from.
type SourceKind string

const (
	SourceLocal SourceKind = "local"
	SourceGit   SourceKind = "git"
)

// Source is a parsed --pipeline argument. For a local package Location is the absolute directory; for Git it is
// the repository URL, Subdir the package directory inside it and Ref the requested branch, tag or commit.
type Source struct {
	Kind     SourceKind `json:"kind"`
	Location string     `json:"location"`
	Subdir   string     `json:"subdir,omitempty"`
	Ref      string     `json:"ref,omitempty"`
}

// Identity is the key that ties a run to its source across restarts. The ref is not part of it: a later run of the
// same repository and subdirectory is the same pipeline at another version.
func (s Source) Identity() string {
	if s.Kind == SourceLocal {
		return "local:" + s.Location
	}
	return "git:" + s.Location + "//" + s.Subdir
}

var errURLCredentials = errors.New("pipeline source: credentials in a URL are not supported; use a git credential helper or an ssh key")

// ParseSource tells a local directory from a Git source. A relative local path is resolved against cwd.
func ParseSource(arg, cwd string) (Source, error) {
	if strings.TrimSpace(arg) == "" {
		return Source{}, errors.New("pipeline source is empty")
	}
	if isLocalArg(arg, cwd) {
		return parseLocal(arg, cwd)
	}
	return parseGit(arg)
}

func isLocalArg(arg, cwd string) bool {
	if strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, ".") || strings.HasPrefix(arg, "~") {
		return true
	}
	if info, err := os.Stat(filepath.Join(cwd, arg)); err == nil && info.IsDir() {
		return true
	}
	if strings.Contains(arg, "://") || strings.HasPrefix(arg, "git@") {
		return false
	}
	host, _, hasPath := strings.Cut(arg, "/")
	return !hasPath || !strings.Contains(host, ".")
}

func parseLocal(arg, cwd string) (Source, error) {
	if arg == "~" || strings.HasPrefix(arg, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return Source{}, fmt.Errorf("pipeline source: resolve home directory: %w", err)
		}
		arg = filepath.Join(home, strings.TrimPrefix(arg, "~"))
	}
	if !filepath.IsAbs(arg) {
		arg = filepath.Join(cwd, arg)
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return Source{}, fmt.Errorf("pipeline source: %w", err)
	}
	return Source{Kind: SourceLocal, Location: abs}, nil
}

// parseGit reads <repo>[//<subdir>][@<ref>]. The repo is host/path, https://, http://, ssh://, file:// or git@host:path.
func parseGit(arg string) (Source, error) {
	repo, ref, subdir := splitGit(arg)
	if strings.HasPrefix(repo, "-") {
		return Source{}, errors.New("pipeline source: a repository must not start with '-'")
	}
	normalized, err := normalizeRepo(repo)
	if err != nil {
		return Source{}, err
	}
	if subdir != "" {
		clean := path.Clean(subdir)
		if path.IsAbs(clean) || clean == parentDir || strings.HasPrefix(clean, "../") {
			return Source{}, fmt.Errorf("pipeline source: subdirectory %q leaves the repository", subdir)
		}
		subdir = strings.TrimPrefix(clean, "./")
		if subdir == "." {
			subdir = ""
		}
	}
	if strings.HasPrefix(ref, "-") {
		return Source{}, errors.New("pipeline source: a ref must not start with '-'")
	}
	return Source{Kind: SourceGit, Location: normalized, Subdir: subdir, Ref: ref}, nil
}

// splitGit cuts the ref and subdirectory off the repository part. '@' and '//' only separate parts after the host,
// because the scheme (://) and an ssh user (git@) contain them too.
func splitGit(arg string) (repo, ref, subdir string) {
	pathStart := 0
	switch {
	case strings.Contains(arg, "://"):
		_, after, _ := strings.Cut(arg, "://")
		pathStart = len(arg) - len(after)
		if i := strings.Index(after, "/"); i >= 0 {
			pathStart += i
		} else {
			pathStart = len(arg)
		}
	case strings.HasPrefix(arg, "git@"):
		if i := strings.Index(arg, ":"); i >= 0 {
			pathStart = i
		}
	default:
		if i := strings.Index(arg, "/"); i >= 0 {
			pathStart = i
		} else {
			pathStart = len(arg)
		}
	}
	head, tail := arg[:pathStart], arg[pathStart:]
	if i := strings.LastIndex(tail, "@"); i >= 0 {
		ref = tail[i+1:]
		tail = tail[:i]
	}
	if i := strings.Index(tail, "//"); i >= 0 {
		subdir = tail[i+2:]
		tail = tail[:i]
	}
	return head + tail, ref, subdir
}

const (
	schemeSSH  = "ssh"
	schemeFile = "file"
)

func normalizeRepo(repo string) (string, error) {
	switch {
	case strings.HasPrefix(repo, "git@"):
		return strings.TrimSuffix(repo, "/"), nil
	case !strings.Contains(repo, "://"):
		if strings.Contains(strings.SplitN(repo, "/", 2)[0], "@") {
			return "", errURLCredentials
		}
		repo = "https://" + repo
	default:
		// already a URL with a scheme
	}
	u, err := url.Parse(repo)
	if err != nil {
		return "", errors.New("pipeline source: malformed repository URL")
	}
	switch u.Scheme {
	case "https", "http", schemeFile:
		if u.User != nil {
			return "", errURLCredentials
		}
	case schemeSSH:
		// A bare user (ssh://git@host) is a login name; a password would reach git's argv and errors.
		if _, hasPassword := u.User.Password(); hasPassword {
			return "", errURLCredentials
		}
	default:
		return "", fmt.Errorf("pipeline source: unsupported URL scheme %q", u.Scheme)
	}
	if u.Host == "" && u.Scheme != schemeFile {
		return "", errors.New("pipeline source: repository URL has no host")
	}
	return strings.TrimSuffix(repo, "/"), nil
}
