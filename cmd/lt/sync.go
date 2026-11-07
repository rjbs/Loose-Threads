package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
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

// writeSyncProblems adds to the session-start context whatever is wrong
// with syncing, as recorded by earlier syncs, since the one the hook
// starts runs in the background where nobody sees its errors.
func writeSyncProblems(b *strings.Builder, s *store.Store) {
	sts, err := s.SyncStatuses()
	if err != nil {
		fmt.Fprintf(b, "Sync status could not be read: %v.  Mention this to the user.\n\n", err)
		return
	}
	var problems []string
	for _, st := range sts {
		problems = append(problems, st.Problems(time.Now())...)
	}
	if len(problems) == 0 {
		return
	}
	b.WriteString("Sync is not working.  Threads are safe in the local cache but are not\n")
	b.WriteString("reaching the server.  Mention this to the user:\n\n")
	for _, p := range problems {
		fmt.Fprintf(b, "  - %s\n", p)
	}
	b.WriteString("\n")
}

// printSyncStatus reports on each collection that syncs, for "lt sync
// -status".  It exits 1 if anything is wrong.
func printSyncStatus(s *store.Store) error {
	sts, err := s.SyncStatuses()
	if err != nil {
		return err
	}
	if len(sts) == 0 {
		fmt.Println("no collections sync; give one an account in config.yaml")
		return nil
	}
	failed := false
	for _, st := range sts {
		last := "never"
		if !st.Attempted.IsZero() {
			last = st.Attempted.Local().Format("2006-01-02 15:04")
		}
		fmt.Printf("%s: last sync %s, %d waiting, %d refused\n", st.Collection, last, st.Pending, len(st.Rejected))
		for _, p := range st.Problems(time.Now()) {
			fmt.Printf("  %s\n", p)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
	return nil
}

func runSync(args []string) error {
	fs := newFlagSet("sync", "")
	collection := fs.String("collection", "", "sync only this collection")
	quiet := fs.Bool("quiet", false, "print nothing unless something went wrong")
	status := fs.Bool("status", false, "report how syncing has gone, without syncing")
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

	if *status {
		return printSyncStatus(s)
	}

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
