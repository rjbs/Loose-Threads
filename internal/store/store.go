// Package store manages the on-disk layout of Loose Threads: a root
// directory holding one flat directory per project, each with a
// project.yaml and one Markdown file per thread.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/rjbs/loosethreads/internal/thread"
)

// EnvRoot names the environment variable that overrides the store root.
const EnvRoot = "LOOSETHREADS_HOME"

const (
	projectFile = "project.yaml"
	threadExt   = ".md"
)

var ErrNotFound = errors.New("store: not found")

// Store is a handle on one store root.  The root need not exist yet.
type Store struct {
	Root string
}

// Project is a project directory within the store.
type Project struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name,omitempty"`
	Dir  string `yaml:"-"` // absolute path of the project directory
}

// DisplayName returns the project's name, or its id when it has none.
func (p Project) DisplayName() string {
	if p.Name != "" {
		return p.Name
	}
	return p.ID
}

// DefaultRoot returns $LOOSETHREADS_HOME, or the XDG data directory
// default of ~/.local/share/loosethreads.
func DefaultRoot() (string, error) {
	if r := os.Getenv(EnvRoot); r != "" {
		return r, nil
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "loosethreads"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "loosethreads"), nil
}

// Open returns a Store for root, or for DefaultRoot when root is empty.
func Open(root string) (*Store, error) {
	if root == "" {
		var err error
		if root, err = DefaultRoot(); err != nil {
			return nil, err
		}
	}
	return &Store{Root: root}, nil
}

var slugJunk = regexp.MustCompile(`[^a-z0-9.]+`)

// DirName returns the directory name for a project id: a lowercased,
// hyphenated slug for humans, plus six hex digits of SHA-256 of the exact
// id for uniqueness.
func DirName(id string) string {
	slug := strings.Trim(slugJunk.ReplaceAllString(strings.ToLower(id), "-"), "-")
	sum := sha256.Sum256([]byte(id))
	hash := hex.EncodeToString(sum[:3])
	if slug == "" {
		return hash
	}
	return slug + "-" + hash
}

// ProjectDir returns the directory a project with the given id has, or
// would have, in the store.
func (s *Store) ProjectDir(id string) string {
	return filepath.Join(s.Root, DirName(id))
}

// Project returns the project with the given id, creating its directory
// and project.yaml if they do not exist.
func (s *Store) Project(id string) (*Project, error) {
	dir := s.ProjectDir(id)
	p, err := readProject(dir)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	p = &Project{ID: id, Dir: dir}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := s.SaveProject(p); err != nil {
		return nil, err
	}
	return p, nil
}

// LookupProject returns the project with the given id, or ErrNotFound.
func (s *Store) LookupProject(id string) (*Project, error) {
	p, err := readProject(s.ProjectDir(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: project %q", ErrNotFound, id)
	}
	return p, err
}

// SaveProject writes project.yaml.
func (s *Store) SaveProject(p *Project) error {
	data, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(p.Dir, projectFile), data)
}

func readProject(dir string) (*Project, error) {
	data, err := os.ReadFile(filepath.Join(dir, projectFile))
	if err != nil {
		return nil, err
	}
	var p Project
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("store: %s: %w", filepath.Join(dir, projectFile), err)
	}
	if p.ID == "" {
		return nil, fmt.Errorf("store: %s: missing id", filepath.Join(dir, projectFile))
	}
	p.Dir = dir
	return &p, nil
}

// Projects lists every project in the store, sorted by display name.
// Directories without a project.yaml are ignored.
func (s *Store) Projects() ([]*Project, error) {
	entries, err := os.ReadDir(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var ps []*Project
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := readProject(filepath.Join(s.Root, e.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool {
		return ps[i].DisplayName() < ps[j].DisplayName()
	})
	return ps, nil
}

// ThreadPath returns the file path for a thread id within p.
func (s *Store) ThreadPath(p *Project, id string) string {
	return filepath.Join(p.Dir, id+threadExt)
}

// Threads lists every thread in p, sorted by id (which is to say, by
// creation date).  Files that fail to parse are returned in errs rather
// than aborting the listing, so one bad hand edit does not hide the rest.
func (s *Store) Threads(p *Project) (threads []*thread.Thread, errs []error, err error) {
	entries, err := os.ReadDir(p.Dir)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, threadExt) {
			continue
		}
		id := strings.TrimSuffix(name, threadExt)
		t, err := s.Get(p, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		threads = append(threads, t)
	}
	sort.Slice(threads, func(i, j int) bool { return threads[i].ID < threads[j].ID })
	return threads, errs, nil
}

// Get reads one thread.
func (s *Store) Get(p *Project, id string) (*thread.Thread, error) {
	path := s.ThreadPath(p, id)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: thread %q", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	t, err := thread.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	t.ID = id
	return t, nil
}

// Create assigns t a fresh id and writes it into p.  It retries on the
// unlikely id collision.
func (s *Store) Create(p *Project, t *thread.Thread, now time.Time) error {
	if t.Created.IsZero() {
		t.Created = now
	}
	for range 10 {
		id := thread.NewID(now)
		path := s.ThreadPath(p, id)
		if _, err := os.Lstat(path); err == nil {
			continue
		}
		t.ID = id
		return s.Save(p, t)
	}
	return errors.New("store: could not find an unused thread id")
}

// Save writes t to its file in p, atomically.
func (s *Store) Save(p *Project, t *thread.Thread) error {
	if t.ID == "" {
		return errors.New("store: cannot save a thread without an id")
	}
	data, err := t.Marshal()
	if err != nil {
		return err
	}
	return writeAtomic(s.ThreadPath(p, t.ID), data)
}

// writeAtomic writes data to a temporary file beside path and renames it
// into place, so readers never see a partial file.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
