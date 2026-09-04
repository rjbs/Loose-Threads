package main

import (
	"fmt"

	"github.com/rjbs/loosethreads/internal/project"
	"github.com/rjbs/loosethreads/internal/store"
	"github.com/rjbs/loosethreads/internal/tui"
)

func init() {
	register(&command{"browse", "browse threads in a terminal UI", runBrowse})
}

func runBrowse(args []string) error {
	fs := newFlagSet("browse", "")
	projectID := fs.String("project", "", "project to start on (default: derived from the working directory)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		fs.Usage()
		return fmt.Errorf("browse takes no arguments")
	}

	s, err := store.Open("")
	if err != nil {
		return err
	}
	start := *projectID
	if start == "" {
		if id, err := project.Identify("."); err == nil {
			start = id.ID
		}
	}
	m, err := tui.New(s, start)
	if err != nil {
		return err
	}
	return tui.Run(m)
}
