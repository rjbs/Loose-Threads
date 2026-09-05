package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/rjbs/loosethreads/internal/store"
	ltsync "github.com/rjbs/loosethreads/internal/sync"
)

func init() {
	register(&command{"sync", "sync collections with their git remotes (also: sync add NAME URL)", runSync})
}

// syncTimeout bounds one sync's network operations.
const syncTimeout = 60 * time.Second

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
