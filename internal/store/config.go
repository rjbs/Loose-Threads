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

// LocalCollection is the collection with no account, where projects land
// when no route claims them.  It always exists.
const LocalCollection = "local"

const configFile = "config.yaml"

// Config is the store's config.yaml: the JMAP server, the collections
// that may be synced with accounts on it, and the rules that route a new
// project into a collection.
type Config struct {
	Server      string                      `yaml:"server,omitempty"`
	Collections map[string]CollectionConfig `yaml:"collections,omitempty"`
	Routes      []Route                     `yaml:"routes,omitempty"`
}

// CollectionConfig describes one collection.  A collection with no
// account is never synced.  Server, if set, overrides the store's: it is
// the URL of the JMAP Session resource.
type CollectionConfig struct {
	Server  string `yaml:"server,omitempty"`
	Account string `yaml:"account,omitempty"`
}

// Endpoint returns the session URL and account id the named collection
// syncs with, and whether it syncs at all.
func (c *Config) Endpoint(collection string) (server, account string, ok bool) {
	if c == nil {
		return "", "", false
	}
	cc, found := c.Collections[collection]
	if !found || cc.Account == "" {
		return "", "", false
	}
	server = cc.Server
	if server == "" {
		server = c.Server
	}
	return server, cc.Account, server != ""
}

// Synced lists the collections that sync, sorted.
func (c *Config) Synced() []string {
	var names []string
	for name := range c.Collections {
		if _, _, ok := c.Endpoint(name); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
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

// Collections lists collection names: local, every configured one, and
// every one holding something in the cache, sorted with local first.
func (s *Store) Collections() ([]string, error) {
	seen := map[string]bool{LocalCollection: true}
	for name := range s.config.Collections {
		seen[name] = true
	}
	rs, err := s.db.Query(`SELECT DISTINCT collection FROM objects`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	for rs.Next() {
		var name string
		if err := rs.Scan(&name); err != nil {
			return nil, err
		}
		seen[name] = true
	}
	if err := rs.Err(); err != nil {
		return nil, err
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
