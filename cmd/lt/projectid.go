package main

import (
	"fmt"

	"github.com/rjbs/loosethreads/internal/project"
	"github.com/rjbs/loosethreads/internal/store"
)

func init() {
	register(&command{"project-id", "print the project identity of a directory", runProjectID})
}

func runProjectID(args []string) error {
	fs := newFlagSet("project-id", "[DIR]")
	verbose := fs.Bool("v", false, "also print the source of the identity and the store directory")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	dir := "."
	switch len(pos) {
	case 0:
	case 1:
		dir = pos[0]
	default:
		fs.Usage()
		return fmt.Errorf("at most one DIR argument is allowed")
	}

	id, err := project.Identify(dir)
	if err != nil {
		return err
	}
	if !*verbose {
		fmt.Println(id.ID)
		return nil
	}
	fmt.Printf("id:     %s\nsource: %s\ndir:    %s\n", id.ID, id.Source, store.DirName(id.ID))
	return nil
}
