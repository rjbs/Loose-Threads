package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/rjbs/loosethreads/internal/store"
)

func init() {
	register(&command{"sync", "sync collections with their JMAP accounts", runSync})
}

// syncTimeout bounds one sync, including any wait for another sync of
// the same collection to finish.
const syncTimeout = 60 * time.Second

// envNoSync disables background syncs after writes, for tests and for
// anyone who prefers to sync by hand.
const envNoSync = "LOOSETHREADS_NO_SYNC"

// syncInBackground starts a detached "lt sync" for the collection holding
// p, if it syncs, so a write reaches the server within seconds without
// making the caller wait on the network.  Failures are silent here; the
// next foreground sync reports them.
func syncInBackground(s *store.Store, p *store.Project) {
	if p == nil {
		return
	}
	if _, ok := s.Config().Endpoint(p.Collection); ok {
		spawnSync("-collection", p.Collection)
	}
}

// syncAllInBackground starts a detached "lt sync" of every collection
// that syncs.
func syncAllInBackground(s *store.Store) {
	if len(s.Config().Synced()) > 0 {
		spawnSync()
	}
}

func spawnSync(args ...string) {
	if os.Getenv(envNoSync) != "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, append([]string{"sync", "-quiet"}, args...)...)
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return
	}
	go cmd.Wait() // reap if we are still around; harmless if not
}

func runSync(args []string) error {
	fs := newFlagSet("sync", "")
	collection := fs.String("collection", "", "sync only this collection")
	quiet := fs.Bool("quiet", false, "print nothing unless something went wrong")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		fs.Usage()
		return fmt.Errorf("sync takes no arguments")
	}

	s, err := store.Open("")
	if err != nil {
		return err
	}
	defer s.Close()

	names := s.Config().Synced()
	if *collection != "" {
		names = []string{*collection}
	}
	if len(names) == 0 {
		if !*quiet {
			fmt.Println("no collections sync; give one an account in config.yaml")
		}
		return nil
	}

	failed := false
	for _, name := range names {
		ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
		r, err := s.Sync(ctx, name)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "lt sync: %s: %v\n", name, err)
			failed = true
			continue
		}
		if !*quiet || len(r.Problems) > 0 {
			fmt.Println(r)
		}
		if len(r.Problems) > 0 {
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
	return nil
}
