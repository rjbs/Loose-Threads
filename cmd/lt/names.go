package main

import (
	"fmt"
	"strings"

	"github.com/rjbs/loosethreads/internal/store"
)

func init() {
	register(&command{"rename-project", "set this project's display name", runRenameProject})
	register(&command{"project-defaults", "give projects the default name and color where they have none of their own", runProjectDefaults})
}

func runRenameProject(args []string) error {
	fs := newFlagSet("rename-project", "NAME")
	projectID := fs.String("project", "", "project id (default: derived from the working directory)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		fs.Usage()
		return fmt.Errorf("a NAME is required")
	}
	name := strings.Join(pos, " ")

	c, err := openWorkspace(*projectID, false)
	if err != nil {
		return err
	}
	if err := c.store.RenameProject(c.project.ID, name); err != nil {
		return err
	}
	fmt.Printf("%s: named %q\n", c.project.ID, name)
	syncInBackground(c.store, c.project)
	return nil
}

func runProjectDefaults(args []string) error {
	fs := newFlagSet("project-defaults", "")
	dryRun := fs.Bool("n", false, "report what would change, but change nothing")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		fs.Usage()
		return fmt.Errorf("project-defaults takes no arguments")
	}

	s, err := store.Open("")
	if err != nil {
		return err
	}
	defer s.Close()
	changes, err := s.ApplyProjectDefaults(*dryRun)
	if err != nil {
		return err
	}
	for _, c := range changes {
		var what []string
		if c.Name != "" {
			what = append(what, fmt.Sprintf("name %q", c.Name))
		}
		if c.Color != "" {
			what = append(what, "color "+c.Color)
		}
		fmt.Printf("%s: %s\n", c.Project, strings.Join(what, ", "))
	}
	switch {
	case len(changes) == 0:
		fmt.Println("every project has a name and color; nothing to do")
	case *dryRun:
		fmt.Printf("%d projects would change\n", len(changes))
	default:
		syncAllInBackground(s)
	}
	return nil
}
