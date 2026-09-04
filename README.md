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

## Use

    lt add "Handle the empty-remote case" -body "Came up while writing the normalizer."
    lt list                     # open threads for this project
    lt list -scope all          # every project
    lt list -all-states         # include done and abandoned
    lt show 7k2m                # by id or unique suffix
    lt done 7k2m
    lt abandon 7k2m
    lt reopen 7k2m
    lt edit 7k2m                # in $EDITOR, cursor on the title line
    lt project-id -v            # what project is this directory?
    lt browse                   # the TUI

Flags may follow positional arguments.  Inside a Claude Code session,
`lt add` records the session id and marks the thread as agent-created
without being told.

In `lt browse`: `a` adds a thread, `A` adds and opens it in your editor,
`e` or enter edits, `d` marks done, `x` marks abandoned (either again
reopens), `c` shows closed threads, `p` picks a project, `[` and `]`
step between projects, `/` filters, `?` lists everything.

The browser refreshes itself as the store changes, so it can sit in a
tmux window beside a Claude Code session.  New threads appear as they
are added.  A thread that someone else closes stays on screen, struck
through, so you notice; `r` or ctrl-R is a hard refresh that hides
closed threads again.  Threads you close in the browser hide at once.
