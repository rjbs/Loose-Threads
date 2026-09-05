# Loose Threads

A backlog of small deferred items that come up while working with Claude
Code: "we should fix that too, but not now."  Threads are Markdown files
with YAML frontmatter, kept per project in one central store, written by
the agent through a CLI or MCP server and browsed by you in a terminal
UI.  See [DESIGN.md](DESIGN.md) for the reasoning.

## Install

    go install ./cmd/lt

The store lives in `$LOOSETHREADS_HOME`, defaulting to
`~/.local/share/loosethreads`.

## Set up Claude Code

Register the hooks in `~/.claude/settings.json`.  The SessionStart hook
tells the model its session and project ids and lists the open threads;
it also runs after compaction, when that context would otherwise be lost.

    {
      "hooks": {
        "SessionStart": [
          { "hooks": [ { "type": "command", "command": "lt hook session-start" } ] }
        ],
        "PreCompact": [
          { "hooks": [ { "type": "command", "command": "lt hook pre-compact" } ] }
        ]
      }
    }

Optionally register the MCP server, which gives the model typed tools
instead of shelling out to `lt`:

    claude mcp add --scope user loosethreads -- lt mcp

Either way, a line in `~/.claude/CLAUDE.md` helps:

> When we defer work, record it immediately with Loose Threads (`lt add`
> or the `add_thread` tool).  When asked what was deferred, list threads
> rather than recalling from context.

## Sync between machines

Threads found on a disposable VM should not die with it.  The store is
divided into *collections*, each a directory under the store root; a
collection with a git remote is a clone and is synced, one without is
not.  Projects are routed into collections by rules in
`$LOOSETHREADS_HOME/config.yaml`:

    collections:
      work:
        remote: git@gitbox.example.com:rjbs/threads-work.git
      personal:
        remote: git@github.com:rjbs/threads-personal.git
    routes:
      - { match: "github.com/fastmail/*", collection: work }
      - { match: "github.com/rjbs/*",     collection: personal }

Sync commits are made as the collection's `author`, else the store's
top-level `author`, else whatever git itself is configured with, else
"Loose Threads <lt@hostname>" so a bare VM still works:

    author: { name: Ricardo Signes, email: rjbs@example.com }
    collections:
      work:
        remote: git@gitbox.example.com:rjbs/threads-work.git
        author: { name: Ricardo Signes, email: rjbs@work.example }

Anything unrouted lands in `local`, which is never synced.  Different
collections can point at repositories owned by different parties, so
the work admin never sees personal threads.

    lt sync add work git@gitbox.example.com:rjbs/threads-work.git
                                # clone into place (or attach) and record it
    lt sync                     # commit, pull, push every collection with a remote
    lt sync -collection work
    lt move-project work        # move this project into another collection

Writes push in the background automatically (`lt add`, `done`,
`abandon`, `reopen`, `edit`, the MCP tools, and the browser), so a
thread is on the remote within seconds of being recorded.  The
SessionStart hook pulls, and the browser syncs every minute.  Set
`LOOSETHREADS_NO_PUSH=1` to sync only by hand.

A conflict (the same thread changed on two machines) is left as git's
markers; `lt sync` reports it, the thread shows as unreadable until the
file is fixed, and the next sync commits the resolution.

## Use

    lt add "Handle the empty-remote case" -body "Came up while writing the normalizer."
    lt list                     # open threads for this project
    lt list -scope all          # every project
    lt list -all-states         # include done and abandoned
    lt show 7k2m                # by id or unique suffix
    lt done 7k2m
    lt done 7k2m -note "fixed in abc123"      # note is appended to the thread
    lt abandon 7k2m -note "superseded by the rewrite"
    lt reopen 7k2m
    lt edit 7k2m                # in $EDITOR, cursor on the title line
    lt project-id -v            # what project is this directory?
    lt rehome                   # after adding a remote: move path-identified threads under it
    lt browse                   # the TUI; bare "lt" at a terminal does the same

Flags may follow positional arguments.  Inside a Claude Code session,
`lt add` records the session id and marks the thread as agent-created
without being told.

In `lt browse`: `a` adds a thread, `A` adds and opens it in your editor,
`e` or enter edits, `d` marks done, `x` marks abandoned (either again
reopens), `D` and `X` do the same after asking for a one-line note
saying why, `c` shows closed threads, `p` opens the project picker
(projects on the left, the highlighted project's open threads on the
right), `[` and `]` step between projects, `/` filters, `?` lists
everything.  Run outside any git repository, the browser opens on the
picker, since there is no project to show.

The browser's default theme, `manxome`, follows the Vim colorscheme of
that name.  Others exist for comparison: `lt browse -theme charm`, or
set `LOOSETHREADS_THEME` to one of `manxome`, `stark`, `soft`, `charm`,
or `lazy`.

The browser refreshes itself as the store changes, so it can sit in a
tmux window beside a Claude Code session.  New threads appear as they
are added.  A thread that someone else closes stays on screen, struck
through, so you notice; `r` or ctrl-R is a hard refresh that hides
closed threads again.  Threads you close in the browser hide at once.
