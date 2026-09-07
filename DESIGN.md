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

### Closing with a note

A thread that was a question ("decide whether to register the MCP
server") is the only record of its own answer, so closing it bare turns
a record into a tombstone, and a later session that was tracking it
learns nothing.  Every way of changing state therefore takes an optional
one-line note, appended to the body as a final paragraph in the same
form as `lt append` (below), with the new state as its first word:

    **2026-09-04 12:40 (agent):**
    Done: registered it after all; the CLI alone was not used.

The label is `Done`, `Abandoned`, or `Reopened`.  The note lives in the
body rather than a frontmatter field so it needs no YAML quoting, reads
naturally in the editor, and accumulates as history if a thread is
reopened and closed again.  Nothing is structured about it beyond the
header and label; if resolutions ever need querying, the paragraphs are
easy to migrate.  In the browser, `d` and `x` close instantly and `D` and `X`
prompt for the note, so the quick case stays quick and the prompt is
there for the case where forgetting is the failure mode.

### Adding to a thread without an editor

`lt edit` is interactive, and an agent has no terminal to be interactive
in: the first agent to try it hung waiting on vim.  So `lt edit` refuses
to run without a terminal, and `lt append` (and the `append_thread` MCP
tool) is the non-interactive way to add to a thread.  It appends a
paragraph headed by a bold timestamp naming who wrote it:

    **2026-09-08 14:05 (agent):**
    The same bug affects the sync path; fix both together.

That is the most common revision an agent wants ("add context to this
thread"), and it keeps the thread's history append-only.  Retitling or
rewriting a body still means a person and an editor.

### Thread ids

The filename is the id: `YYYY-MM-DD-xxxxxx`, six random characters
from a lowercase alphabet with the confusable ones removed.  (Four,
originally; widened so that machines minting ids independently and
syncing later do not collide.)
Ids carry only the date, so listings sort by the `created` timestamp
and use the id as a tiebreaker.  `created` is recorded to the second,
so threads added within one second (a batch of `lt add` calls from one
shell command, say) still fall back to the random suffix; see *Open
questions*.  Uniqueness only matters within one project directory, and
a thread may be referred to by any unique suffix of its id, so a few
characters usually suffice.

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
4. the URL of the remote named `rjbs`
5. the absolute path of the git root
6. the absolute path of the working directory (not in a checkout)

Remote URLs are normalized to `host/path` with the scheme, user, port,
and trailing `.git` removed, so that `git@github.com:rjbs/foo.git` and
`https://github.com/rjbs/foo` are the same project.  Worktrees of the
same repository therefore share one project, which is what we want.

The path fallbacks are ugly but never merge unrelated projects, unlike
a basename fallback would.  They are also unstable: the moment a remote
is added the identity changes and threads recorded under the path are
orphaned.  So a path identity is flagged wherever it is seen: a callout
in the browser, a warning in the SessionStart context (with an
instruction to tell the user), and a line on stderr from `lt add`.

The common sequence is: threads recorded before the repository had a
remote, then a remote added, then a wish to fix the storage.  `lt
rehome` handles it: it computes the current (remote-based) identity,
looks for projects under the identities the checkout would have had
without a remote (its git root, and the directory itself), moves their
thread files into the remote-based project, and removes the emptied
directories.  It refuses to move anything if an id would collide, says
so and exits 0 if there is nothing to move, and errors if the checkout
still has no remote.

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
directory.  The directory name is what lookups use, so if the id inside
`project.yaml` disagrees with it (hand relocation carried the wrong
file along), the id is what is wrong; the tools flag the mismatch (a
callout in the browser, a warning on stderr from `lt list`) rather than
guessing.

## Components

Everything is one Go binary, `lt`, with subcommands.  One language means
one implementation of the file format and of state changes, shared by the
CLI, the TUI, and the MCP server.  The order below is also the build
order: each layer is useful on its own before the next exists.

### 1. Library: `internal/thread`, `store`, `project`, `editor`

Storage and parsing, no UI.

* `thread`: parse and serialize thread files, title, state changes, ids
* `store`: locate the store, project directories, list and write
  threads atomically, resolve ids by suffix, the shared JSON view
* `project`: derive project identity from a directory
* `editor`: build the `$EDITOR` command, with the Vim cursor positioning

The TUI and MCP server live in `internal/tui` and `internal/mcpserver`;
`cmd/lt` is the command line over all of them.

### 2. CLI: `lt`

Thin wrapper on the library.  Used by the human, by hooks, and by the
agent if no MCP server is running.  Rough shape:

    lt add "title" [-body TEXT | < body] [-project ID] [-session ID]
                                [-transcript PATH] [-origin agent|human]
    lt list [-scope session|project|all] [-all-states] [-json]
    lt show THREAD [-json]
    lt done THREAD... [-note WHY]
    lt abandon THREAD... [-note WHY]
    lt reopen THREAD... [-note WHY]
    lt append THREAD [TEXT | < text] [-origin agent|human]
    lt edit THREAD              # opens $EDITOR; needs a terminal
    lt project-id [DIR] [-v]    # print the derived identity
    lt rehome                   # move path-identified threads under the remote identity
    lt browse                   # the TUI; bare "lt" at a terminal does the same
    lt mcp                      # the MCP server, on stdio
    lt hook session-start       # see Claude Code integration
    lt hook pre-compact

Flags and positional arguments may be interleaved.  THREAD is a full id
or a unique suffix.  Inside a Claude Code session the environment
supplies defaults: `-session` from `CLAUDE_CODE_SESSION_ID`, and
`-origin agent` whenever `CLAUDECODE` or that variable is set, so the
agent records provenance without being told.  Machine-readable output
(`-json`) on `list` and `show` uses the same view as the MCP tools.

### 3. TUI: `lt browse`

A terminal browser over the store, built on Bubble Tea and the bubbles
components.  Bubble Tea was changing its API between v1 and v2 around
the time of this writing; pin one version at the start and read that
version's docs rather than trusting memory.  Lists threads for one project or
all projects, with hotkeys for the common edits:

* move between threads and projects; the project picker is itself
  two-pane, previewing the highlighted project's open threads
* outside any git repository, open on the picker rather than invent a
  path-identified project (threads for non-repositories are unsupported
  until someone needs them)
* toggle done, abandon, reopen
* show or hide closed threads
* add a new thread (title prompt, then optionally the editor)
* open the current thread in `$EDITOR`
* close with a one-line note (`D`, `X`)

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
* `append_thread(id, text, project?)`
* `set_thread_state(id, state, note?, project?)`

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
2. During work, something gets deferred.  Agent runs `lt add` (or calls
   `add_thread`) with the title, a short body explaining the context,
   and its session id.
3. Before compaction, the hook prompts the agent to record anything not
   yet written down.
4. Human runs `lt browse` at leisure, edits, closes, or reprioritizes.
5. Next session starts; step 1 shows what is still open.

## Sync between machines

Threads are found on disposable development VMs as often as on the
laptop, and must not die with the VM.  Primary storage stays local so
that everything works offline; sync moves files between stores.

### Collections

Git syncs whole trees, so grouping for sync has to be spatial.  The
store root holds *collections*, each a directory that may be a git
clone; a project lives in exactly one collection.  The `local`
collection has no remote and is never synced.

    $LOOSETHREADS_HOME/
      config.yaml                 collections and routing rules
      local/                      no remote, never synced
        <project-dir>/
      personal/                   clone of a private sync repository
        <project-dir>/
      work/                       clone of the work sync repository
        <project-dir>/

Different collections can point at repositories owned by different
parties, so the work git admin sees only the work collection, and a VM
configured with only `work` has nowhere for personal threads to land.
Project directories are unchanged: still flat within their collection,
still slug plus hash, still holding a `project.yaml`.  Everything above
the collection level is one path segment deeper than before.

Why git and not a hub with rsync: one file per thread with immutable
ids is the ideal git payload (adds never conflict; edits are rare), it
is offline-first, the hub is a repository you already have credentials
for on every machine, and history answers the purging question for
free.  Project ids are remote-based, so the same project lands in the
same directory on every machine; path-identified projects would not,
which is one more reason the path warning exists.

### Routing

When a project is first created, `config.yaml` decides its collection
by glob rules on the project id, first match wins, default `local`:

    collections:
      work:     { remote: git@gitbox.fastmail.com:rjbs/threads-work.git }
      personal: { remote: git@github.com:rjbs/threads-personal.git }
    routes:
      - { match: "github.com/fastmail/*",   collection: work }
      - { match: "gitbox.fastmail.com/*",   collection: work }
      - { match: "github.com/rjbs/*",       collection: personal }

The first `lt add` in a project reports where it landed, so a wrong
guess is visible at once.  `lt move-project COLLECTION` relocates a
project, the same file move `rehome` does.  A `collection:` field in
`project.yaml` to pin a project against the rules is possible but not
built until a case appears.

### Syncing

`lt sync` runs, for each collection with a remote: commit everything,
fetch, merge, push.  (Merge rather than rebase: a conflicted merge
leaves markers in the working tree and a state the next sync can finish
once they are gone, where a conflicted rebase leaves the repository
mid-operation.)  A VM disposed of without a manual sync is the
failure mode that matters, so writes push: `lt add`, `done`, `abandon`,
`reopen`, and the MCP equivalents commit and push in the background
when the collection has a remote.  The laptop pulls at SessionStart and
from the browser's poll loop.  `lt sync add NAME URL` clones a
collection into place and records it in `config.yaml`, for VM
provisioning.

Conflicts: adds never conflict.  The realistic conflict is the same
thread's state changed, or a note appended, on two machines.  First
version: `lt sync` reports the conflict and leaves git's markers.
Second version, only once it has actually happened: merge thread files
semantically (closed beats open; keep both notes).

Ids carry six random characters so that machines minting them
independently and syncing later do not collide.

Migration from the current layout: move existing project directories
under `local/`.

## Open questions

* **Same-second ordering.**  `created` has one-second precision, so a
  burst of adds ties and sorts by random suffix.  Sub-second timestamps
  in the file or a time component in the id would fix it; neither is
  worth the ugliness until it bites.
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
