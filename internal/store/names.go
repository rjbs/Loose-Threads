package store

import (
	"database/sql"
	"fmt"
	"hash/fnv"
	"path"
	"strings"

	"github.com/rjbs/loosethreads/internal/patch"
)

// A project's TaskList carries a name and a color for people to see.
// lt picks both when it creates the list, and never sets them again on
// its own, so either can be changed afterward, in lt or in another
// client, and stays changed.

// DefaultName is the name a new project's list gets: for an id made from
// a remote, the id without its host ("rjbs/Loose-Threads"); for a path,
// its last element; otherwise the id itself.
func DefaultName(id string) string {
	if strings.HasPrefix(id, "/") {
		return path.Base(id)
	}
	if host, rest, ok := strings.Cut(id, "/"); ok && strings.Contains(host, ".") && rest != "" {
		return rest
	}
	return id
}

// palette is the colors DefaultColor picks among: middling tones, apart
// in hue, that read on light and dark backgrounds alike.
var palette = []string{
	"#4e79a7", // blue
	"#f28e2b", // orange
	"#e15759", // red
	"#76b7b2", // teal
	"#59a14f", // green
	"#edc948", // yellow
	"#b07aa1", // purple
	"#ff9da7", // pink
	"#9c755f", // brown
	"#499894", // dark teal
	"#d37295", // raspberry
	"#8cd17d", // light green
}

// DefaultColor is the color a new project's list gets, a hex RGB value
// chosen by hashing the id, so every machine picks the same one.
func DefaultColor(id string) string {
	h := fnv.New32a()
	h.Write([]byte(id))
	return palette[h.Sum32()%uint32(len(palette))]
}

// RenameProject sets the name of a project's list.
func (s *Store) RenameProject(id, name string) error {
	if name == "" {
		return fmt.Errorf("store: a project's name cannot be empty")
	}
	return s.tx(func(tx *sql.Tx) error {
		lists, err := projectLists(tx, id)
		if err != nil {
			return err
		}
		if len(lists) == 0 {
			return fmt.Errorf("%w: project %q", ErrNotFound, id)
		}
		for _, l := range lists {
			v := patch.Clone(l.local)
			v["name"] = name
			if err := setLocal(tx, l, v); err != nil {
				return err
			}
		}
		return nil
	})
}

// DefaultsChange is a change ApplyProjectDefaults made, or would make,
// to one project.
type DefaultsChange struct {
	Project string
	Name    string // the new name, or empty if the name was kept
	Color   string // the new color, or empty if the color was kept
}

// ApplyProjectDefaults gives each project the default name if its name is
// missing or is just its id, and the default color if it has none.  It
// leaves alone any name or color that was chosen, and with dryRun it
// changes nothing, only reporting what it would do.  It is for lists made
// before lt picked names and colors.
func (s *Store) ApplyProjectDefaults(dryRun bool) ([]DefaultsChange, error) {
	var changes []DefaultsChange
	apply := func(q queryer, tx *sql.Tx) error {
		lists, err := queryRows(q, `type = ? AND project IS NOT NULL AND local IS NOT NULL ORDER BY project, key`, typeTaskList)
		if err != nil {
			return err
		}
		for _, l := range lists {
			id, _ := l.local[propProject].(string)
			c := DefaultsChange{Project: id}
			v := patch.Clone(l.local)
			if name, _ := v["name"].(string); name == "" || name == id {
				if def := DefaultName(id); def != name {
					c.Name, v["name"] = def, def
				}
			}
			if color, _ := v["color"].(string); color == "" {
				c.Color = DefaultColor(id)
				v["color"] = c.Color
			}
			if c.Name == "" && c.Color == "" {
				continue
			}
			changes = append(changes, c)
			if tx != nil {
				if err := setLocal(tx, l, v); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if dryRun {
		return changes, apply(s.db, nil)
	}
	return changes, s.tx(func(tx *sql.Tx) error { return apply(tx, tx) })
}
