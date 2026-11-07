package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/rjbs/loosethreads/internal/legacy"
	"github.com/rjbs/loosethreads/internal/store"
)

func init() {
	register(&command{"migrate", "import threads from the old Markdown-file store", runMigrate})
}

// runMigrate imports every thread from a Markdown-file store into the
// cache.  Threads keep their ids, which become their uids, so running it
// again skips what is already there, and threads imported on two
// machines are recognized as the same when they sync.
func runMigrate(args []string) error {
	fs := newFlagSet("migrate", "")
	from := fs.String("from", "", "root of the old store (default: the store root itself)")
	dryRun := fs.Bool("n", false, "report what would be imported without importing it")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		fs.Usage()
		return fmt.Errorf("migrate takes no arguments")
	}

	s, err := store.Open("")
	if err != nil {
		return err
	}
	defer s.Close()
	root := *from
	if root == "" {
		root = s.Root
	}

	projects, errs, err := legacy.Read(root)
	if err != nil {
		return err
	}
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "warning: skipped %v\n", e)
	}
	if len(projects) == 0 {
		fmt.Printf("no Markdown-file projects found under %s\n", root)
		return nil
	}

	cfg := s.Config()
	total := 0
	for _, lp := range projects {
		// Keep a project in the collection it was in, when that name is
		// still configured; otherwise let the routing rules place it.
		collection := lp.Collection
		if _, ok := cfg.Collections[collection]; !ok && collection != store.LocalCollection {
			collection = cfg.Route(lp.ID)
		}

		var p *store.Project
		if !*dryRun {
			if p, err = s.ProjectIn(lp.ID, lp.Name, collection); err != nil {
				return err
			}
			collection = p.Collection
		} else if existing, err := s.LookupProject(lp.ID); err == nil {
			p, collection = existing, existing.Collection
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}

		imported, present := 0, 0
		for _, t := range lp.Threads {
			if p != nil {
				if _, err := s.Get(p, t.ID); err == nil {
					present++
					continue
				}
			}
			if !*dryRun {
				if err := s.Create(p, t, t.Created); err != nil {
					return fmt.Errorf("%s %s: %w", lp.ID, t.ID, err)
				}
			}
			imported++
		}
		total += imported
		fmt.Printf("%s [%s]: %d imported", lp.ID, collection, imported)
		if present > 0 {
			fmt.Printf(", %d already present", present)
		}
		fmt.Println()
	}
	if *dryRun {
		fmt.Printf("%d threads would be imported\n", total)
	} else {
		fmt.Printf("%d threads imported; run lt sync to send them to the server\n", total)
	}
	return nil
}
