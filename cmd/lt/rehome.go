package main

import (
	"fmt"

	"github.com/rjbs/loosethreads/internal/project"
	"github.com/rjbs/loosethreads/internal/store"
)

func init() {
	register(&command{"rehome", "move threads recorded under this checkout's path to its remote-based identity", runRehome})
}

// runRehome handles the common sequence: threads were recorded while the
// repository had no remote, so they live under a path identity; now a
// remote exists and the threads should follow.
func runRehome(args []string) error {
	fs := newFlagSet("rehome", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		fs.Usage()
		return fmt.Errorf("rehome takes no arguments")
	}

	id, err := project.Identify(".")
	if err != nil {
		return err
	}
	if project.IsPath(id.ID) {
		return fmt.Errorf("this checkout has no github, gitbox, origin, or rjbs remote, so there is no remote-based identity to move threads to")
	}

	paths, err := project.PathIdentities(".")
	if err != nil {
		return err
	}
	s, err := store.Open("")
	if err != nil {
		return err
	}

	total := 0
	for _, from := range paths {
		if _, err := s.LookupProject(from); err != nil {
			continue
		}
		moved, err := s.Rehome(from, id.ID)
		if err != nil {
			return err
		}
		for _, m := range moved {
			fmt.Printf("%s: %s -> %s\n", m, from, id.ID)
		}
		total += len(moved)
	}
	if total == 0 {
		fmt.Println("no threads recorded under a path identity for this checkout; nothing to do")
	}
	return nil
}
