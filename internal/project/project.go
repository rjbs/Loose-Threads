// Package project derives a stable project identity from a working
// directory, preferring git remotes and falling back to filesystem paths.
package project

import (
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
)

// Identity is a project id together with a note on where it came from.
type Identity struct {
	ID     string
	Source string // "remote:github", "remote:origin", "gitroot", or "path"
}

// IsPath reports whether id is a filesystem path rather than a normalized
// remote, meaning it was derived from the git root or working directory
// because no recognized remote existed.  Such an identity changes as soon
// as a remote is added, orphaning threads recorded under it.
func IsPath(id string) bool { return strings.HasPrefix(id, "/") }

// PathWarning is the text shown when a project is identified by path.
const PathWarning = "this project is identified by its checkout path because the repository has no github, gitbox, or origin remote; " +
	"add a remote before recording many threads, or they will be orphaned when the identity changes"

// RemotePrecedence lists remote names in the order they are consulted.
var RemotePrecedence = []string{"github", "gitbox", "origin"}

// Identify derives the project identity for dir.  The first of these that
// exists wins: a remote from RemotePrecedence, the git root, or the
// absolute path of dir itself.
func Identify(dir string) (Identity, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Identity{}, err
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return Identity{}, err
	}

	root, err := gitRoot(abs)
	if err != nil {
		return Identity{ID: abs, Source: "path"}, nil
	}

	for _, name := range RemotePrecedence {
		u, err := remoteURL(root, name)
		if err != nil {
			continue
		}
		return Identity{ID: NormalizeRemote(u), Source: "remote:" + name}, nil
	}

	return Identity{ID: root, Source: "gitroot"}, nil
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitRoot(dir string) (string, error) {
	root, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	if root == "" {
		return "", errors.New("project: not in a git work tree")
	}
	return root, nil
}

func remoteURL(root, name string) (string, error) {
	u, err := git(root, "remote", "get-url", name)
	if err != nil {
		return "", err
	}
	if u == "" {
		return "", fmt.Errorf("project: remote %q has no url", name)
	}
	return u, nil
}

// NormalizeRemote reduces a git remote URL to "host/path", dropping the
// scheme, user, port, and a trailing ".git", so that the ssh and https
// forms of the same repository compare equal.  The host is lowercased;
// the path is not, since some hosts are case-sensitive.  A remote that is
// a local filesystem path is returned cleaned, without a host.
func NormalizeRemote(raw string) string {
	raw = strings.TrimSpace(raw)

	var host, path string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil {
			return raw
		}
		host, path = u.Hostname(), u.Path
	case isScpLike(raw):
		i := strings.Index(raw, ":")
		host, path = raw[:i], raw[i+1:]
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
	default:
		return strings.TrimSuffix(filepath.Clean(raw), ".git")
	}

	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimSuffix(path, "/")
	host = strings.ToLower(host)

	if host == "" {
		return "/" + path
	}
	if path == "" {
		return host
	}
	return host + "/" + path
}

// isScpLike reports whether raw looks like "[user@]host:path", which git
// treats as ssh.  A path with a slash before the first colon is a local
// path (git's own rule), and a Windows drive letter is not a host.
func isScpLike(raw string) bool {
	i := strings.Index(raw, ":")
	if i <= 0 {
		return false
	}
	if strings.Contains(raw[:i], "/") {
		return false
	}
	return true
}
