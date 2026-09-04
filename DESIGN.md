# Loose Threads

A backlog of small deferred items that come up while working with Claude
Code: "we should fix that too, but not now."  Today those live in the
conversation and are recovered by asking "what did we defer?", which is
unreliable.  Loose Threads gives them a home that the agent can write to
and read from, and that the human can skim and edit outside of any
session.

## Vocabulary

* **thread**: one deferred item.  One file on disk.
* **project**: the unit under which threads are grouped.  Identified by
  the git remote where possible; see *Project identity*.
* **session**: one Claude Code session.  Recorded on each thread as
  provenance, not used as a partition.
* **scope**: how much of the store a listing covers: this session, this
  project, or everything.

## Storage

All threads for all projects live in one central directory, outside any
project checkout, so the human-facing tools can navigate across projects.

    $LOOSETHREADS_HOME            (default: ~/.local/share/loosethreads)
    └── <project-dir>/            (one directory per project; see below)
        ├── project.yaml          (the exact project id, optional display name)
        └── <thread-id>.md        (one file per thread)

A thread file is Markdown with YAML frontmatter.  The first non-blank
line of the body is the title.  The rest of the body is free prose:
what we were doing, why we deferred it, anything that answers "why did I
want this?" months later.

    ---
    state: open
    created: 2026-09-04T12:40:00-04:00
    session: 2f25fed4-dac5-4879-a38f-f46d45bfd92d
    transcript: /Users/rjbs/.claude/projects/-Users-rjbs-code-Foo/2f25fed4-....jsonl
    ---
    Handle the empty-remote case in project_id

    Came up while writing the normalizer.  We return undef and the
    caller dies with a bad message.  Should fall back to the path.

The title goes in the body rather than the frontmatter so that titles
with colons, hashes, and brackets need no YAML quoting.  Tools that
write files will always produce a title line; the parser defines the
title as the first non-blank body line, truncated for display.

### Frontmatter fields

| field        | required | notes                                              |
|--------------|----------|----------------------------------------------------|
| `state`      | yes      | `open`, `done`, or `abandoned`                     |
| `created`    | yes      | ISO 8601 with offset                               |
| `session`    | no       | Claude Code session id, when created by the agent  |
| `transcript` | no       | path to that session's transcript                  |
| `closed`     | no       | when state left `open`                             |
| `origin`     | no       | `agent` or `human`; absent means unknown           |

Threads are never deleted by the tools.  `done` and `abandoned` items
stay on disk and are hidden by default in listings.  Purging is a later
conversation.

### Thread ids

The filename is the id.  It should sort by creation time and be short
enough to type: `YYYY-MM-DD-xxxx` where `xxxx` is a few random base32
characters.  Uniqueness only matters within one project directory.

### Concurrency

Distinct sessions create distinct files, so there is no shared-write
problem in the common case.  The rare case is the TUI and an MCP call
editing the same file.  All writers write the whole file atomically
(write to a temp name, rename).  We do not attempt merging.

## Project identity

Derived from the git checkout containing the working directory.  The
first of these that exists wins:

1. the URL of the remote named `github`
2. the URL of the remote named `gitbox`
3. the URL of the remote named `origin`
4. the absolute path of the git root
5. the absolute path of the working directory (not in a checkout)

Remote URLs are normalized to `host/path` with the scheme, user, port,
and trailing `.git` removed, so that `git@github.com:rjbs/foo.git` and
`https://github.com/rjbs/foo` are the same project.  Worktrees of the
same repository therefore share one project, which is what we want.

The path fallbacks are ugly but never merge unrelated projects, unlike
a basename fallback would.

### Project directories

Every project is one directory directly under the store root, so all
projects are the same depth and no project id can be a prefix of
another.  The directory name is a readable slug plus a short hash:

    github.com/rjbs/Dist-Zilla      ->  github.com-rjbs-dist-zilla-8f3a1c/
    /Users/rjbs/code/LooseThreads   ->  users-rjbs-code-loosethreads-1b2c3d/

The slug is the id lowercased, with each run of characters outside
`[a-z0-9.]` collapsed to one hyphen and the ends trimmed.  The suffix
is the first six hex digits of the SHA-256 of the exact normalized id.
The slug is lossy on purpose and exists only for humans; the hash
carries uniqueness, so ids with identical slugs (`rjbs/foo-bar` and
`rjbs/foo/bar`) still get distinct directories.  Lowercasing the slug
also keeps case-insensitive filesystems from merging two ids.

Each project directory holds a `project.yaml` recording the exact id,
since the directory name cannot be reversed:

    id: github.com/rjbs/Dist-Zilla
    name: Dist::Zilla        # optional, for display; defaults to id

A directory under the store root is a project if and only if it has a
`project.yaml`.  Listing all projects means reading one file per
directory.

## Components

Everything is one Go binary, `lt`, with subcommands.  One language means
one implementation of the file format and of state changes, shared by the
CLI, the TUI, and the MCP server.  The order below is also the build
order: each layer is useful on its own before the next exists.

### 1. Library: the `thread` and `store` packages

Storage and parsing, no UI.

* locate the store, list projects, list threads with filtering
* parse and serialize thread files
* create, update state, rewrite body
* derive project identity from a directory

### 2. CLI: `lt`

Thin wrapper on the library.  Used by the human, by hooks, and by the
agent if no MCP server is running.  Rough shape:

    lt add [--project ID] [--session ID] "title" [< body]
    lt list [--scope session|project|all] [--all-states]
    lt show THREAD
    lt done THREAD
    lt abandon THREAD
    lt reopen THREAD
    lt edit THREAD              # opens $EDITOR
    lt project-id [DIR]         # print the derived identity
    lt browse                   # the TUI
    lt hook session-start       # see Claude Code integration
    lt hook pre-compact

Machine-readable output (`--json`) on `list` and `show` so hooks and the
MCP server can share the CLI or the library as convenient.

### 3. TUI: `lt browse`

A terminal browser over the store, built on Bubble Tea and the bubbles
components.  Bubble Tea was changing its API between v1 and v2 around
the time of this writing; pin one version at the start and read that
version's docs rather than trusting memory.  Lists threads for one project or
all projects, with hotkeys for the common edits:

* move between threads and projects
* toggle done, abandon, reopen
* show or hide closed threads
* add a new thread (title prompt, then optionally the editor)
* open the current thread in `$EDITOR`

When opening a thread in Vim the invocation can position the cursor on
the title line, so the frontmatter-first layout costs nothing.

The browser is meant to sit open in a tmux window beside a Claude Code
session, so it polls the store (once a second, comparing a listing of
the root and current project directory) and reloads on any change.
Under the default hide-closed view, a thread that was on screen and is
then closed by someone else stays on screen styled as closed, so a
glance shows the transition.  A hard refresh (`r`, ctrl-R) returns to
the plain open set.  Threads closed in the browser itself hide at once,
since the user already knows.  Deleted files simply vanish.

### 4. MCP server: `lt mcp`

Exposes the library to Claude Code as typed tools over stdio, using the
official Go MCP SDK.  Small surface:

* `add_thread(title, body?, project?, session?, transcript?)`
* `list_threads(scope?, project?, session?, include_closed?)`
* `get_thread(id, project?)`
* `set_thread_state(id, state, project?)`

The server does not know which session it is serving.  The agent passes
project and session ids explicitly, having learned them at session
start (below).  When `project` is omitted the server uses the identity
of the directory it was started in, which Claude Code sets to the
working directory of the session.  Registration:

    claude mcp add --scope user loosethreads -- lt mcp

If `lt` via the shell turns out to be enough for the agent, the MCP
server can simply go unregistered.

### 5. Claude Code integration

Hooks, all of which call `lt`:

* **SessionStart**: `lt hook session-start` reads the hook JSON from
  stdin (session id, transcript path, cwd, source), derives the project
  id, and emits `additionalContext` telling the agent its session id,
  project id, and the list of open threads for the project.  This is how
  the agent learns its identity.  It also serves as a reminder that the
  backlog exists.  SessionStart fires again after compaction with
  `source: compact`, so the same hook re-injects identity and the open
  list after context is summarized; in that case it opens by asking the
  agent to record anything discussed before compaction that is missing
  from the list.  Register it without a matcher so it runs on every
  source.
* **PreCompact**: `lt hook pre-compact` emits a nudge: "before context
  is summarized, record any deferred items with Loose Threads."  The
  docs describe `additionalContext` for PreCompact, but the model has no
  turn between the hook and the compaction in which to act, so this is
  belt-and-braces.  The post-compaction SessionStart is the reliable
  path.

Registration, in `~/.claude/settings.json`:

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

Plus a paragraph in `~/.claude/CLAUDE.md`: when we defer something,
record it as a thread; when asked what was deferred, list threads
rather than recalling from context.

The agent should distinguish scopes when reporting: "we logged this one
already this session" versus "you also have these from before."

## Agent workflow, end to end

1. Session starts.  Hook injects: session id, project id, open threads.
2. During work, something gets deferred.  Agent calls `add_thread` with
   the title, a short body explaining the context, and its session id.
3. Before compaction, the hook prompts the agent to record anything not
   yet written down.
4. Human runs `lt browse` at leisure, edits, closes, or reprioritizes.
5. Next session starts; step 1 shows what is still open.

## Open questions

* **Ordering and priority.**  Creation order is the only ordering
  defined so far.  Do we want a priority field, or is manual reordering
  in the TUI enough?  Leaning: no priority field until it hurts.
* **Session-less threads.**  Threads the human adds from the TUI have no
  session.  Fine, but the "this session" scope should not surprise
  anyone by omitting them.
* **Cross-project view.**  The TUI's "all projects" view is easy given
  the layout, but what does the agent do with `scope=all`?  Probably
  nothing by default; it exists for the human.
* **Store location on multiple machines.**  A single directory is easy
  to sync or keep in git.  Not designing for it yet.

## Non-goals, for now

* Purging or archiving closed threads.
* Sync between machines.
* Anything resembling a project management tool: no assignees, no due
  dates, no dependencies.  A thread that lingers long enough to want
  those things gets ported to Linear; Loose Threads does not grow them.
