// Command lt is the Loose Threads command line: a backlog of small
// deferred items, kept per project, shared between a human and the Claude
// Code sessions working in that project.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/rjbs/loosethreads/internal/project"
	"github.com/rjbs/loosethreads/internal/store"
	"github.com/rjbs/loosethreads/internal/thread"
)

// command is one subcommand.  run receives the arguments after the
// subcommand name and is responsible for flag parsing.
type command struct {
	name    string
	summary string
	run     func(args []string) error
}

var commands []*command

func register(c *command) { commands = append(commands, c) }

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	if os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		usage(os.Stdout)
		return
	}

	name := os.Args[1]
	for _, c := range commands {
		if c.name == name {
			if err := c.run(os.Args[2:]); err != nil {
				if errors.Is(err, flag.ErrHelp) {
					os.Exit(2)
				}
				fmt.Fprintf(os.Stderr, "lt %s: %v\n", name, err)
				os.Exit(1)
			}
			return
		}
	}
	fmt.Fprintf(os.Stderr, "lt: unknown command %q\n\n", name)
	usage(os.Stderr)
	os.Exit(2)
}

func usage(w *os.File) {
	fmt.Fprintln(w, "usage: lt <command> [flags] [args]")
	fmt.Fprintln(w)
	sort.Slice(commands, func(i, j int) bool { return commands[i].name < commands[j].name })
	width := 0
	for _, c := range commands {
		width = max(width, len(c.name))
	}
	for _, c := range commands {
		fmt.Fprintf(w, "  %-*s  %s\n", width, c.name, c.summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "The store lives in $%s (default ~/.local/share/loosethreads).\n", store.EnvRoot)
}

// newFlagSet returns a FlagSet whose usage output names the subcommand.
func newFlagSet(name, args string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: lt %s [flags] %s\n", name, args)
		fs.PrintDefaults()
	}
	return fs
}

// parse parses flags from args, allowing flags and positional arguments
// to be interleaved, and returns the positional arguments.  The standard
// FlagSet stops at the first positional, which makes "lt add TITLE -body
// ..." fail for no good reason.  A bare "--" ends flag parsing.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)

		// A non-boolean flag without "=" consumes the next argument.
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue // fs.Parse will report the unknown flag
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	if err := fs.Parse(flags); err != nil {
		return nil, err
	}
	return positional, nil
}

// env is the environment as seen by lt, so commands can pick up defaults
// from a Claude Code session without being told.
const (
	envSession = "CLAUDE_CODE_SESSION_ID"
	envInAgent = "CLAUDECODE"
)

func defaultSession() string { return os.Getenv(envSession) }

func defaultOrigin() thread.Origin {
	if os.Getenv(envInAgent) != "" || os.Getenv(envSession) != "" {
		return thread.OriginAgent
	}
	return thread.OriginHuman
}

// workspace is what most commands need: the store and the current project.
type workspace struct {
	store   *store.Store
	project *store.Project
}

// openWorkspace opens the store and resolves the project: projectID if
// given, else the identity of the working directory.  When create is
// false, a project that does not yet exist in the store is an error.
func openWorkspace(projectID string, create bool) (*workspace, error) {
	s, err := store.Open("")
	if err != nil {
		return nil, err
	}
	if projectID == "" {
		id, err := project.Identify(".")
		if err != nil {
			return nil, err
		}
		projectID = id.ID
	}

	var p *store.Project
	if create {
		p, err = s.Project(projectID)
	} else {
		p, err = s.LookupProject(projectID)
	}
	if err != nil {
		return nil, err
	}
	return &workspace{store: s, project: p}, nil
}

func (c *workspace) resolveThread(ref string) (*thread.Thread, error) {
	return c.store.Resolve(c.project, ref)
}

func now() time.Time { return time.Now().Truncate(time.Second) }
