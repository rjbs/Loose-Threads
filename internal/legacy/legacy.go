// Package legacy reads the Markdown-file store that Loose Threads used
// before JMAP storage: a root holding collections, each holding one
// directory per project with a project.yaml and one Markdown file per
// thread.  It exists only so that lt migrate can import such a store, and
// it never writes.
package legacy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/rjbs/loosethreads/internal/thread"
)

const (
	projectFile = "project.yaml"
	threadExt   = ".md"
)

// Project is one project directory and the threads in it.
type Project struct {
	ID         string `yaml:"id"`
	Name       string `yaml:"name,omitempty"`
	Collection string `yaml:"-"`
	Dir        string `yaml:"-"`

	Threads []*thread.Thread `yaml:"-"`
}

// Read loads every project under root, in every collection, with its
// threads.  Directories directly under root that hold a project.yaml
// (the layout from before collections) are read as the "local"
// collection.  Thread files that fail to parse are returned in errs
// rather than aborting the read.
func Read(root string) (projects []*Project, errs []error, err error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, projectFile)); err == nil {
			p, perrs, err := readProject(dir, "local")
			if err != nil {
				return nil, nil, err
			}
			projects, errs = append(projects, p), append(errs, perrs...)
			continue
		}
		subs, err := os.ReadDir(dir)
		if err != nil {
			return nil, nil, err
		}
		for _, s := range subs {
			pdir := filepath.Join(dir, s.Name())
			if !s.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(pdir, projectFile)); err != nil {
				continue
			}
			p, perrs, err := readProject(pdir, e.Name())
			if err != nil {
				return nil, nil, err
			}
			projects, errs = append(projects, p), append(errs, perrs...)
		}
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].Collection != projects[j].Collection {
			return projects[i].Collection < projects[j].Collection
		}
		return projects[i].ID < projects[j].ID
	})
	return projects, errs, nil
}

func readProject(dir, collection string) (*Project, []error, error) {
	data, err := os.ReadFile(filepath.Join(dir, projectFile))
	if err != nil {
		return nil, nil, err
	}
	var p Project
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", filepath.Join(dir, projectFile), err)
	}
	if p.ID == "" {
		return nil, nil, fmt.Errorf("%s: missing id", filepath.Join(dir, projectFile))
	}
	p.Dir, p.Collection = dir, collection

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, threadExt) {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		t, err := thread.Parse(data)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		t.ID = strings.TrimSuffix(name, threadExt)
		p.Threads = append(p.Threads, t)
	}
	return &p, errs, nil
}
