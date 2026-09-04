package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rjbs/loosethreads/internal/editor"
	"github.com/rjbs/loosethreads/internal/project"
	"github.com/rjbs/loosethreads/internal/store"
	"github.com/rjbs/loosethreads/internal/thread"
)

func init() {
	register(&command{"add", "record a new thread", runAdd})
	register(&command{"list", "list threads", runList})
	register(&command{"show", "print one thread", runShow})
	register(&command{"done", "mark a thread done", stateCommand(thread.Done)})
	register(&command{"abandon", "mark a thread abandoned", stateCommand(thread.Abandoned)})
	register(&command{"reopen", "reopen a closed thread", stateCommand(thread.Open)})
	register(&command{"edit", "open a thread in $EDITOR", runEdit})
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func runAdd(args []string) error {
	fs := newFlagSet("add", "TITLE")
	projectID := fs.String("project", "", "project id (default: derived from the working directory)")
	session := fs.String("session", defaultSession(), "session id to record")
	transcript := fs.String("transcript", "", "transcript path to record")
	origin := fs.String("origin", string(defaultOrigin()), "who is adding this: agent or human")
	body := fs.String("body", "", "body text; if absent and stdin is not a terminal, stdin is read")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fs.Usage()
		return fmt.Errorf("exactly one TITLE argument is required")
	}
	title := strings.TrimSpace(pos[0])
	if title == "" {
		return fmt.Errorf("title must not be blank")
	}

	text := *body
	if text == "" && !stdinIsTerminal() {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		text = string(b)
	}
	text = strings.TrimSpace(text)

	c, err := openWorkspace(*projectID, true)
	if err != nil {
		return err
	}
	if project.IsPath(c.project.ID) {
		fmt.Fprintf(os.Stderr, "warning: %s\n", project.PathWarning)
	}

	full := title + "\n"
	if text != "" {
		full += "\n" + text + "\n"
	}
	t := &thread.Thread{
		State:      thread.Open,
		Created:    now(),
		Session:    *session,
		Transcript: *transcript,
		Origin:     thread.Origin(*origin),
		Body:       full,
	}
	if err := c.store.Create(c.project, t, t.Created); err != nil {
		return err
	}
	fmt.Println(t.ID)
	return nil
}

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return true
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func runList(args []string) error {
	fs := newFlagSet("list", "")
	scope := fs.String("scope", "project", "session, project, or all")
	projectID := fs.String("project", "", "project id (default: derived from the working directory)")
	session := fs.String("session", defaultSession(), "session id, for -scope session and for marking")
	allStates := fs.Bool("all-states", false, "include done and abandoned threads")
	asJSON := fs.Bool("json", false, "emit JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		fs.Usage()
		return fmt.Errorf("list takes no arguments")
	}

	switch *scope {
	case "session", "project", "all":
	default:
		return fmt.Errorf("unknown scope %q", *scope)
	}
	if *scope == "session" && *session == "" {
		return fmt.Errorf("-scope session requires a session id (flag or $%s)", envSession)
	}

	s, err := store.Open("")
	if err != nil {
		return err
	}

	var projects []*store.Project
	if *scope == "all" {
		if projects, err = s.Projects(); err != nil {
			return err
		}
	} else {
		c, err := openWorkspace(*projectID, false)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil // a project with no threads has nothing to list
			}
			return err
		}
		projects = []*store.Project{c.project}
	}

	var views []store.View
	for _, p := range projects {
		if p.Mismatched() {
			fmt.Fprintf(os.Stderr, "warning: %s is named for another project but its project.yaml says %q\n", p.Dir, p.ID)
		}
		c := &workspace{store: s, project: p}
		ths, errs, err := s.Threads(p)
		if err != nil {
			return err
		}
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "warning: %v\n", e)
		}
		for _, t := range ths {
			if !*allStates && !t.IsOpen() {
				continue
			}
			if *scope == "session" && t.Session != *session {
				continue
			}
			views = append(views, c.store.View(c.project, t))
		}
	}

	if *asJSON {
		if views == nil {
			views = []store.View{}
		}
		return printJSON(views)
	}

	lastProject := ""
	for _, v := range views {
		if *scope == "all" && v.Project != lastProject {
			if lastProject != "" {
				fmt.Println()
			}
			fmt.Printf("%s\n", v.Project)
			lastProject = v.Project
		}
		mark := " "
		if *session != "" && v.Session == *session {
			mark = "*"
		}
		state := ""
		if v.State != string(thread.Open) {
			state = " [" + v.State + "]"
		}
		fmt.Printf("%s %s%s  %s\n", mark, v.ID, state, v.Title)
	}
	return nil
}

func runShow(args []string) error {
	fs := newFlagSet("show", "THREAD")
	projectID := fs.String("project", "", "project id (default: derived from the working directory)")
	asJSON := fs.Bool("json", false, "emit JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fs.Usage()
		return fmt.Errorf("exactly one THREAD argument is required")
	}

	c, err := openWorkspace(*projectID, false)
	if err != nil {
		return err
	}
	t, err := c.resolveThread(pos[0])
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(c.store.View(c.project, t))
	}
	data, err := t.Marshal()
	if err != nil {
		return err
	}
	os.Stdout.Write(data)
	return nil
}

// stateCommand builds the done, abandon, and reopen commands, which
// differ only in the target state.
func stateCommand(target thread.State) func([]string) error {
	name := map[thread.State]string{thread.Done: "done", thread.Abandoned: "abandon", thread.Open: "reopen"}[target]
	return func(args []string) error {
		fs := newFlagSet(name, "THREAD...")
		projectID := fs.String("project", "", "project id (default: derived from the working directory)")
		note := fs.String("note", "", "one-line note appended to the thread, saying why")
		pos, err := parse(fs, args)
		if err != nil {
			return err
		}
		if len(pos) < 1 {
			fs.Usage()
			return fmt.Errorf("at least one THREAD argument is required")
		}

		c, err := openWorkspace(*projectID, false)
		if err != nil {
			return err
		}
		for _, ref := range pos {
			t, err := c.resolveThread(ref)
			if err != nil {
				return err
			}
			if err := t.Resolve(target, *note, now()); err != nil {
				return err
			}
			if err := c.store.Save(c.project, t); err != nil {
				return err
			}
			fmt.Printf("%s: %s  %s\n", t.ID, t.State, t.Title())
		}
		return nil
	}
}

func runEdit(args []string) error {
	fs := newFlagSet("edit", "THREAD")
	projectID := fs.String("project", "", "project id (default: derived from the working directory)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fs.Usage()
		return fmt.Errorf("exactly one THREAD argument is required")
	}

	c, err := openWorkspace(*projectID, false)
	if err != nil {
		return err
	}
	t, err := c.resolveThread(pos[0])
	if err != nil {
		return err
	}
	return editFile(c.store.ThreadPath(c.project, t.ID))
}

func editFile(path string) error {
	cmd := editor.Command(path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
