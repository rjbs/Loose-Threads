package main

import (
	"github.com/rjbs/loosethreads/internal/store"
)

// syncInBackground will start a detached sync of p's collection after a
// write.  Sync with a JMAP server is not built yet.
func syncInBackground(s *store.Store, p *store.Project) {}

// syncAllInBackground will start a detached sync of every collection.
func syncAllInBackground(s *store.Store) {}
