# Loose Threads

A backlog of small deferred items that come up while working with Claude
Code: "we should fix that too, but not now."  Threads are JMAP Tasks,
kept per project in a local cache that syncs with a JMAP for Tasks
server, written by the agent through a CLI or MCP server and browsed by
you in a terminal UI.  See [DESIGN.md](DESIGN.md) for the reasoning.

## Install

    go install ./cmd/lt

The store (its `config.yaml` and the cache) lives in
`$LOOSETHREADS_HOME`, defaulting to `~/.local/share/loosethreads`.

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

Threads are stored as JMAP Tasks, and each project is a TaskList.
Everything `lt` does reads and writes a local cache
(`$LOOSETHREADS_HOME/cache.sqlite`) and never waits on the network;
sync with a JMAP for Tasks server, such as TaskMaster, is separate.

The cache is divided into *collections*, and a collection with an
account syncs with that JMAP account; one without does not.  Projects
are routed into collections by rules in `$LOOSETHREADS_HOME/config.yaml`.
`server` is the URL of the JMAP Session resource, for the whole store
or per collection:

    server: https://taskmaster.example.ts.net/jmap/session
    collections:
      work:     { account: work }
      personal: { account: personal }
      elsewhere: { account: rjbs, server: https://other.example/jmap/session }
    routes:
      - { match: "github.com/fastmail/*", collection: work }
      - { match: "github.com/rjbs/*",     collection: personal }

Anything unrouted lands in `local`, which is never synced.

A server reachable beyond the tailnet may want a bearer token, given
as `token` beside `server`, at the top or per collection.  A collection
with a server of its own never inherits the top-level token.  A work
VM that should see only work threads might be configured so:

    server: https://taskmaster.example.com/jmap/session
    token: SECRET
    collections:
      work: { account: work }
    routes:
      - { match: "*", collection: work }

The token is a secret, so keep `config.yaml` readable only by you.
`lt` reads it but never writes it.

    lt sync                     # pull, push, pull every collection with an account
    lt sync -collection work
    lt sync -status             # how syncing has gone, without syncing
    lt move-project work        # move this project into another collection

Writes start a sync in the background (`lt add`, `done`, `abandon`,
`reopen`, `append`, `edit`, the MCP tools, and the browser), so a
thread is on the server within seconds of being recorded.  The
SessionStart hook starts a sync without waiting for it, and the
browser syncs every minute.  Set `LOOSETHREADS_NO_SYNC=1` to sync only
by hand.

Each sync records how it went.  When the last sync of a collection
failed, the server refused a change, or changes have waited over an
hour with no sync, the SessionStart hook says so (and asks the agent to
tell you), the browser's footer names the collection, and
`lt sync -status` gives the details.

The same thread changed on two machines merges by property: the state
from one and a retitle from the other both survive, and notes appended
on both are both kept.  Only a real rewrite of the body on both sides
is a conflict; the local version wins and `lt sync` says so.

### From the Markdown-file store

Before JMAP storage, threads were Markdown files under the store root,
synced with git.  `lt migrate` imports them into the cache, keeping
their ids; `lt migrate -n` reports what it would do.  A project keeps
its old collection if that name is still in `config.yaml`, and is
routed otherwise.  Running it again, or on a second machine with the
same files, does not duplicate anything.  The files are left in place.

## Use

    lt add "Handle the empty-remote case" -body "Came up while writing the normalizer."
    lt list                     # open threads for this project
    lt list -scope all          # every project
    lt list -all-states         # include done and abandoned
    lt show 7k2m                # by id or unique suffix
    lt done 7k2m
    lt done 7k2m -note "fixed in abc123"      # note is appended, dated and attributed
    lt abandon 7k2m -note "superseded by the rewrite"
    lt reopen 7k2m
    lt append 7k2m "also affects sync"        # adds a dated, attributed paragraph
    lt edit 7k2m                # its Markdown form, in $EDITOR, cursor on the title line
    lt project-id -v            # what project is this directory?
    lt rehome                   # after adding a remote: move path-identified threads under it
    lt browse                   # the TUI; bare "lt" at a terminal does the same

Flags may follow positional arguments.  Inside a Claude Code session,
`lt add` records the session id and marks the thread as agent-created
without being told, and `lt append` signs its paragraph as the agent.
`lt edit` refuses to run without a terminal, since an editor started
without one hangs rather than failing; `lt append` is the way to add
to a thread from a script or an agent.

In `lt browse`: `a` adds a thread, `A` adds and opens it in your editor,
`e` or enter edits, `d` marks done, `x` marks abandoned (either again
reopens), `D` and `%` do the same after asking for a one-line note
saying why, `m` moves the thread to another project (pick one in the
picker and press enter; esc cancels), `c` shows closed threads, `p`
opens the project picker (projects on the left, the highlighted
project's open threads on the right), `[` and `]` step between projects, `/` filters by title (the
filter stays until ctrl-/ clears it or you change projects), `?` lists
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
