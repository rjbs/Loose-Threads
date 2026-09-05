package main

import (
	"fmt"
)

func init() {
	register(&command{"move-project", "move this project into another collection", runMoveProject})
}

func runMoveProject(args []string) error {
	fs := newFlagSet("move-project", "COLLECTION")
	projectID := fs.String("project", "", "project id (default: derived from the working directory)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fs.Usage()
		return fmt.Errorf("exactly one COLLECTION argument is required")
	}

	c, err := openWorkspace(*projectID, false)
	if err != nil {
		return err
	}
	from := c.project.Collection
	p, err := c.store.MoveProject(c.project.ID, pos[0])
	if err != nil {
		return err
	}
	if from == p.Collection {
		fmt.Printf("%s is already in %s\n", p.ID, p.Collection)
		return nil
	}
	fmt.Printf("%s: %s -> %s\n", p.ID, from, p.Collection)
	return nil
}
