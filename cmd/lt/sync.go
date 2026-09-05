package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/rjbs/loosethreads/internal/store"
	ltsync "github.com/rjbs/loosethreads/internal/sync"
)

func init() {
	register(&command{"sync", "sync collections with their git remotes (also: sync add NAME URL)", runSync})
}

// syncTimeout bounds one sync's network operations.
const syncTimeout = 60 * time.Second

// envNoPush disables background pushes after writes, for tests and for
// anyone who prefers to sync by hand.
const envNoPush = "LOOSETHREADS_NO_PUSH"

// pushInBackground starts a detached "lt sync" for the collection holding
// p, if it has a remote, so a write is durable on the remote within
// seconds without making the caller wait on the network.  Failures are
// silent here; the next foreground sync will report them.
func pushInBackground(s *store.Store, p *store.Project) {
	if p == nil || s.Config().Remote(p.Collection) == "" || os.Getenv(envNoPush) != "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "sync", "-quiet", "-collection", p.Collection)
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return
	}
	go cmd.Wait() // reap if we are still around; harmless if not
}

func runSync(args []string) error {
	fs := newFlagSet("sync", "[add NAME URL]")
	collection := fs.String("collection", "", "sync only this collection")
	quiet := fs.Bool("quiet", false, "print nothing unless something went wrong")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}

	s, err := store.Open("")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	defer cancel()

	switch {
	case len(pos) == 3 && pos[0] == "add":
		r, err := ltsync.Add(ctx, s, pos[1], pos[2])
		if err != nil && !errors.Is(err, ltsync.ErrConflict) {
			return err
		}
		fmt.Println(r)
		return nil
	case len(pos) != 0:
		fs.Usage()
		return fmt.Errorf("usage: lt sync [-collection NAME] | lt sync add NAME URL")
	}

	var results []ltsync.Result
	if *collection != "" {
		r, err := ltsync.Collection(ctx, s, *collection)
		if err != nil && !errors.Is(err, ltsync.ErrConflict) {
			return err
		}
		results = []ltsync.Result{r}
	} else {
		results, err = ltsync.All(ctx, s)
	}
	conflicted := false
	for _, r := range results {
		if len(r.Conflicts) > 0 {
			conflicted = true
		}
		if !*quiet || len(r.Conflicts) > 0 {
			fmt.Println(r)
		}
	}
	if err != nil {
		return err
	}
	if len(results) == 0 && !*quiet {
		fmt.Println("no collections have remotes; see lt sync add")
	}
	if conflicted {
		os.Exit(1)
	}
	return nil
}
