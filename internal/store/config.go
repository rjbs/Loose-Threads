package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// LocalCollection is the collection with no remote, where projects land
// when no route claims them.  It always exists.
const LocalCollection = "local"

const configFile = "config.yaml"

// Config is the store's config.yaml: the collections that may be synced,
// the rules that route a new project into one, and the identity sync
// commits are made with.
type Config struct {
	Author      *Author                     `yaml:"author,omitempty"`
	Collections map[string]CollectionConfig `yaml:"collections,omitempty"`
	Routes      []Route                     `yaml:"routes,omitempty"`
}

// CollectionConfig describes one collection.  A collection with no
// remote is never synced.  Author, if set, overrides the store's.
type CollectionConfig struct {
	Remote string  `yaml:"remote,omitempty"`
	Author *Author `yaml:"author,omitempty"`
}

// Author is a git identity for sync commits.
type Author struct {
	Name  string `yaml:"name"`
	Email string `yaml:"email"`
}

// AuthorFor returns the identity sync commits in the named collection
// should carry: the collection's, else the store's, else nil (meaning
// let git decide).
func (c *Config) AuthorFor(collection string) *Author {
	if c == nil {
		return nil
	}
	if cc, ok := c.Collections[collection]; ok && cc.Author != nil {
		return cc.Author
	}
	return c.Author
}

// Route sends projects whose id matches a glob to a collection.  "*"
// matches any run of characters, including "/"; "?" matches one.
type Route struct {
	Match      string `yaml:"match"`
	Collection string `yaml:"collection"`
}

func (s *Store) configPath() string { return filepath.Join(s.Root, configFile) }

func (s *Store) loadConfig() error {
	data, err := os.ReadFile(s.configPath())
	if errors.Is(err, os.ErrNotExist) {
		s.config = &Config{}
		return nil
	}
	if err != nil {
		return err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return fmt.Errorf("store: %s: %w", s.configPath(), err)
	}
	for _, r := range c.Routes {
		if _, err := globRegexp(r.Match); err != nil {
			return fmt.Errorf("store: %s: route %q: %w", s.configPath(), r.Match, err)
		}
	}
	s.config = &c
	return nil
}

// Config returns the loaded configuration.  Callers may modify it and
// then SaveConfig.
func (s *Store) Config() *Config { return s.config }

// SaveConfig writes config.yaml.
func (s *Store) SaveConfig() error {
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(s.config)
	if err != nil {
		return err
	}
	return writeAtomic(s.configPath(), data)
}

// Route returns the collection a project with the given id belongs in:
// the first matching route's, else local.
func (c *Config) Route(id string) string {
	for _, r := range c.Routes {
		re, err := globRegexp(r.Match)
		if err != nil {
			continue
		}
		if re.MatchString(id) {
			return r.Collection
		}
	}
	return LocalCollection
}

// Remote returns the remote of the named collection, or "".
func (c *Config) Remote(collection string) string {
	if c == nil || c.Collections == nil {
		return ""
	}
	return c.Collections[collection].Remote
}

func globRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range glob {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// CollectionDir returns the directory of a collection.
func (s *Store) CollectionDir(name string) string {
	return filepath.Join(s.Root, name)
}

// Collections lists collection names: local, every configured one, and
// every directory under the root, sorted with local first.
func (s *Store) Collections() ([]string, error) {
	seen := map[string]bool{LocalCollection: true}
	for name := range s.config.Collections {
		seen[name] = true
	}
	entries, err := os.ReadDir(s.Root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			seen[e.Name()] = true
		}
	}
	var names []string
	for n := range seen {
		if n != LocalCollection {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return append([]string{LocalCollection}, names...), nil
}

// migrateFlat moves project directories that sit directly under the root
// (the layout before collections existed) into the local collection.
func (s *Store) migrateFlat() error {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		old := filepath.Join(s.Root, e.Name())
		if _, err := os.Stat(filepath.Join(old, projectFile)); err != nil {
			continue
		}
		local := s.CollectionDir(LocalCollection)
		if err := os.MkdirAll(local, 0o755); err != nil {
			return err
		}
		if err := os.Rename(old, filepath.Join(local, e.Name())); err != nil {
			return fmt.Errorf("store: migrating %s into %s: %w", old, LocalCollection, err)
		}
	}
	return nil
}
