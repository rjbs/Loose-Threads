# Loose Threads

A backlog of small deferred items that come up while working with Claude
Code: "we should fix that too, but not now."  Today those live in the
conversation and are recovered by asking "what did we defer?", which is
unreliable.  Loose Threads gives them a home that the agent can write to
and read from, and that the human can skim and edit outside of any
session.

## Vocabulary

* **thread**: one deferred item.  Stored as a JMAP Task.
* **project**: the unit under which threads are grouped.  Identified by
  the git remote where possible; see *Project identity*.  Stored as a
  JMAP TaskList.
* **collection**: a group of projects synced with one JMAP account, or,
  for `local`, never synced.  See *Storage and sync*.
* **session**: one Claude Code session.  Recorded on each thread as
  provenance, not used as a partition.
* **scope**: how much of the store a listing covers: this session, this
  project, or everything.

## Threads

A thread is shown and edited as Markdown with YAML frontmatter: what
`lt show` prints and what `lt edit` opens.  The first non-blank line of
the body is the title.  The rest of the body is free prose: what we
were doing, why we deferred it, anything that answers "why did I want
this?" months later.

    ---
    state: open
    created: 2026-09-04T12:40:00-04:00
    session: 2f25fed4-dac5-4879-a38f-f46d45bfd92d
    origin: agent
    ---
    Handle the empty-remote case in project_id

    Came up while writing the normalizer.  We return undef and the
    caller dies with a bad message.  Should fall back to the path.

The title goes in the body rather than the frontmatter so that titles
with colons, hashes, and brackets need no YAML quoting; the parser
defines the title as the first non-blank body line, truncated for
display.  Stored, the title and body become the Task's `title` and
`description`; see *Mapping*.

### Frontmatter fields

| field        | required | notes                                              |
|--------------|----------|----------------------------------------------------|
| `state`      | yes      | `open`, `done`, or `abandoned`                     |
| `created`    | yes      | ISO 8601 with offset                               |
| `session`    | no       | Claude Code session id, when created by the agent  |
| `closed`     | no       | when state left `open`                             |
| `origin`     | no       | `agent` or `human`; absent means unknown           |

Threads are never deleted by the tools.  `done` and `abandoned` items
stay in the store and are hidden by default in listings.  Purging is a
later conversation.

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
easy to migrate.  In the browser, `d` and `x` close instantly and `D` and `%`
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

An id is `YYYY-MM-DD-xxxxxx`, six random characters from a lowercase
alphabet with the confusable ones removed.  (Four, originally; widened
so that machines minting ids independently and syncing later do not
collide.)  The id is minted locally and becomes the Task's `uid`; see
*Thread identity*.  Ids carry only the date, so listings sort by the
`created` timestamp and use the id as a tiebreaker.  `created` is
recorded to the second, so threads added within one second (a batch of
`lt add` calls from one shell command, say) still fall back to the
random suffix; see *Open questions*.  Uniqueness only matters within one
project, and a thread may be referred to by any unique suffix of its
id, so a few characters usually suffice.

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
and trailing `.git` removed, and the host lowercased, so that
`git@github.com:rjbs/foo.git` and `https://github.com/rjbs/foo` are the
same project.  The path keeps its case, since some hosts are
case-sensitive.  Worktrees of the same repository therefore share one
project, which is what we want.

The path fallbacks are ugly but never merge unrelated projects, unlike
a basename fallback would.  They are also unstable: the moment a remote
is added the identity changes and threads recorded under the path are
orphaned.  Path identities also differ from machine to machine, so
they are best kept in the `local` collection.  So a path identity is
flagged wherever it is seen: a callout in the browser, a warning in the
SessionStart context (with an instruction to tell the user), and a line
on stderr from `lt add`.

The common sequence is: threads recorded before the repository had a
remote, then a remote added, then a wish to fix the storage.  `lt
rehome` handles it: it computes the current (remote-based) identity,
looks for projects under the identities the checkout would have had
without a remote (its git root, and the directory itself), moves their
threads into the remote-based project, and removes the emptied
projects.  It refuses to move anything if an id would collide, says
so and exits 0 if there is nothing to move, and errors if the checkout
still has no remote.

## Components

Everything is one Go binary, `lt`, with subcommands.  One language means
one implementation of the thread format, of state changes, and of sync,
shared by the CLI, the TUI, and the MCP server.

### 1. Library

No UI.

* `thread`: the thread type, its Markdown form, title, state changes, ids
* `store`: the SQLite cache, projects and threads in it, resolving ids
  by suffix, the shared JSON view, config, and sync
* `patch`: diff, apply, and rebase of JSON objects, the core of sync
* `jmap`: a minimal JMAP client
* `jmaptest`: an in-memory JMAP server for tests
* `project`: derive project identity from a directory
* `editor`: build the `$EDITOR` command, with the Vim cursor
  positioning, and edit a thread through a temporary file

The TUI and MCP server live in `internal/tui` and `internal/mcpserver`;
`cmd/lt` is the command line over all of them.

### 2. CLI: `lt`

Thin wrapper on the library.  Used by the human, by hooks, and by the
agent if no MCP server is running.  Rough shape:

    lt add "title" [-body TEXT | < body] [-project ID] [-session ID] [-origin agent|human]
    lt list [-scope session|project|all] [-all-states] [-json]
    lt show THREAD [-json]
    lt done THREAD... [-note WHY]
    lt abandon THREAD... [-note WHY]
    lt reopen THREAD... [-note WHY]
    lt append THREAD [TEXT | < text] [-origin agent|human]
    lt edit THREAD              # opens $EDITOR; needs a terminal
    lt project-id [DIR] [-v]    # print the derived identity
    lt rehome                   # move path-identified threads under the remote identity
    lt move-project COLLECTION  # move this project into another collection
    lt rename-project NAME      # set this project's display name
    lt project-defaults [-n]    # default names and colors for older lists
    lt sync [-collection NAME] [-status] [-quiet]
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
* close with a one-line note (`D`, `%`)
* move a thread to another project (`m`, then pick one)

When opening a thread in Vim the invocation can position the cursor on
the title line, so the frontmatter-first layout costs nothing.

The browser is meant to sit open in a tmux window beside a Claude Code
session, so it polls the store once a second, comparing a counter that
every write to the cache bumps, and reloads on any change.  It also
starts a background sync every minute, so changes from other machines
arrive on their own.  Under the default hide-closed view, a thread that
was on screen and is then closed by someone else stays on screen styled
as closed, so a glance shows the transition.  A hard refresh (`r`,
ctrl-R) returns to the plain open set.  Threads closed in the browser
itself hide at once, since the user already knows.  Threads deleted
elsewhere simply vanish.

### 4. MCP server: `lt mcp`

Exposes the library to Claude Code as typed tools over stdio, using the
official Go MCP SDK.  Small surface:

* `add_thread(title, body?, project?, session?)`
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
  backlog exists.  It starts a background sync, and if earlier syncs
  recorded problems, it lists them and asks the agent to tell the user.
  SessionStart fires again after compaction with `source: compact`, so
  the same hook re-injects identity and the open list after context is
  summarized; in that case it opens by asking the agent to record
  anything discussed before compaction that is missing from the list.
  Register it without a matcher so it runs on every source.
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

## Storage and sync

Threads are found on disposable development VMs as often as on the
laptop, and must not die with the VM, so they sync between machines.
They are JMAP Task objects, held in a local cache and synced with a
JMAP for Tasks server (TaskMaster, in practice).  A server gives other
clients, web and mobile included, something to talk to, and keeping
the data in the orthodox JMAP shape means general Tasks tools work on
it too.

Until November 2026 threads were Markdown files, one per thread, in a
directory per project, synced with git.  `lt migrate` imported them
into the cache, and was removed once every store had moved.

### Collections and routing

The cache is divided into collections.  A collection with an account
in `config.yaml` syncs with that JMAP account; `local`, and any other
without one, never syncs.  A project lives in exactly one collection.

    server: https://taskmaster.example.ts.net/jmap/session
    collections:
      work:     { account: work }
      personal: { account: personal }
    routes:
      - { match: "github.com/fastmail/*", collection: work }
      - { match: "github.com/rjbs/*",     collection: personal }

When a project is first created, the routes decide its collection by
glob rules on the project id, first match wins, default `local`.  The
first `lt add` in a project reports where it landed, so a wrong guess
is visible at once.  `lt move-project COLLECTION` moves a project, and
is how a `local` project is published.  A machine configured with
only `work`, like a work VM, has no account for personal threads to
reach.

### What agents see

The move to JMAP changed nothing for the agent.  A survey of every
Claude Code transcript on the laptop (2026-11-06, excluding this
project's own development) found about 1,290 `lt` invocations across 113 sessions,
no use of the MCP server, and no reads or edits of thread files at
all; the only direct contact with the store was one session debugging
the `lt edit` hang.  Agents almost always pass the full date-prefixed
id, copied from session-start context or `lt add` output, and never
use `-json`.  So the CLI's verbs, output, and id format are the whole
agent interface, and the storage under them can change freely.

### The local cache

Every `lt` command reads and writes a local cache and never waits on
the network.  This keeps the session-start hook instant, keeps `lt add`
working on a VM with no connection, and lets local behavior be tested
without a server.  Sync is a separate step, tested on its own.

The model is Jinx's (`~/code/hub/Jinx/doc/plan.md`), ported rather
than reused, and cut down to what we need: full mirroring of two
types, no windowed mode, no blobs.  Each object has:

* a **server version**: the last object fetched from the server,
  whole, including properties `lt` does not model, or null if the
  server has never seen it;
* a **local value**: what `lt` reads, equal to the server version
  unless a change is pending;
* at most one **pending entry**, which records that the object was
  created, changed, or destroyed, but stores no patch.

Reads see only local values, so a pending change reads exactly like a
synced one.  At sync-out, the patch is the diff from server version to
local value, so several edits collapse into one patch.  At sync-in, an
object with a pending change is **rebased**: the patch from its old
server version to its local value is applied to the newly fetched
object, a pending local property wins, and everything else takes the
server's value.

There is no long-running engine.  `lt` is a short-lived process, and
several may run at once (a few agent sessions and the browser), so
there is no set of live objects to keep in step.  The cache is one
SQLite database in WAL mode, which suits several short-lived writers,
through a pure-Go driver so that `lt` stays free of cgo.  Each write is
one short transaction.  Rather than Jinx's claims on pending entries,
only one sync of a collection runs at a time, under a lock file; a
second waits for the first and then runs, so a write made during a
sync is never left behind.  The pending mark is a sequence number, and
a sync clears it only if it is unchanged since the change was sent, so
a write made while a request is out stays pending.

A write starts a detached sync; the session-start hook shows the cache
and starts a detached sync rather than waiting for one, so its list may
be one sync behind.  The browser starts one every minute, and notices
any change to the cache by a counter every write bumps.

Because these syncs run where nobody sees them, each records its start
time and error, if any, in the cache.  The session-start hook, the
browser's footer, and `lt sync -status` report from that record: a
failed last sync, changes the server refused (the per-object error each
pending row keeps), and changes left waiting over an hour with no sync
at all, which means syncs are not running.  Merge conflicts, where the
local value of a property won, are reported only by a sync run by hand.

Each sync pulls, pushes, and pulls again.  The first pull is what makes
the description resolver work: rebase happens only on the way in, so a
pending description pushed without a pull first would replace notes
appended elsewhere since the last one.  Pulling first narrows that to
the time between two requests.

A sync is usually one or two requests, after fetching the Session.  The
pull is one request: for each type, `/changes` and then, by result
reference, `/get` of what was created and what was updated.  Setting
`maxChanges` to `maxObjectsInGet` keeps the gets within limits;
`hasMoreChanges` brings another pull.  The push is one more request,
holding the Task `/set`, any TaskList destroys, and a second pull,
which sees what the sets did because a request's calls run in order.
TaskList creates and updates, which are rare, go in a request of their
own first, so that Tasks can name new lists by their real ids.  If
there is nothing to push, there is no second request.  Calls are packed
into requests within `maxCallsInRequest`, and a full resync of Tasks
pages through `Task/query`.

### Appends and the description resolver

Rebase works per property, and the body with all of its appended notes
is one property, `description`.  Left alone, a note appended on one
machine would replace the description on the server, dropping a note
appended on another.  Appends are the most common write `lt` makes, so
`description` gets a resolver.  Given the base (the server version the
local edit started from), the new server value, and the local value:

* if the server value equals the base, the local value wins, as in any
  rebase;
* if the local value begins with the base, the result is the server
  value followed by what the local value added after the base;
* otherwise both sides really edited, the local value wins, and the
  conflict is recorded.

The prefix is taken against the base, never against the server value.
Two notes written the same day share their opening characters
(`\n\n**2026-11-06 14:0`), so a common prefix of server and local
values would run into the notes themselves and cut the local one in
half.

When both sides appended, the server's note lands first even if the
local one was written earlier.  That is acceptable.

Full edits through `lt edit` (write a temp file, edit, read it back,
update the Task) stay possible.  They are rare, and the resolver's
third case is what catches one that collides with an append.

### Mapping

| `lt` concept           | JMAP for Tasks                                              |
|------------------------|-------------------------------------------------------------|
| collection             | account: `server`, `token`, `account` in `config.yaml`      |
| project                | TaskList with `lt.semiotic.systems:project`; `name` is display only |
| thread id              | Task `uid`                                                  |
| title                  | `title`                                                     |
| body and notes         | `description`, `text/plain`                                 |
| state                  | `progress` (below)                                          |
| `created`              | `created`, falling back to `updated` when absent            |
| `closed`               | `progressUpdated`                                           |
| `session`              | `lt.semiotic.systems:session`, nullable                     |
| `origin`               | keyword `lt.semiotic.systems:origin:agent`; absent means unknown |

Vendor properties and keywords are prefixed `lt.semiotic.systems:`,
following RFC 8984's owning-domain convention.  Origin is a keyword of
the form `PREFIX:origin:VALUE` so that more keywords can join it later.

The file store also had `transcript`, a path to the session's
transcript.  It was dropped in the move: it was never populated in
practice (0 of 522 threads had it), held a path meaningful on one
machine only, and can be found from the session id anyway.

States map to `progress` as: `open` from `needs-action` or
`in-process`, `done` from `completed`, `abandoned` from `cancelled` or
`failed`.  `lt` writes only `needs-action`, `completed`, and
`cancelled`.

### Thread identity

The thread id becomes the Task's `uid`, which JSCalendar makes the
client's identity for the object.  The server's id stays inside the
cache.  Because `lt` mints the uid locally, a create whose response is
lost can be retried safely: the next sync pulls before it pushes, the
pull brings back the Task that was created, and it matches the pending
create by uid, which settles it.

The uid is the bare thread id, with no prefix marking it as ours.
Should one ever be needed, every uid can be rewritten in bulk.

### Projects as TaskLists

A project is a TaskList.  Its identity is the vendor property
`lt.semiotic.systems:project`, holding the project id, and not its
`name`, which any client may change; identity in the name would let a
rename in another client split the project in two.

The name and `color` are for people.  When `lt` creates a list, it
names it after the id without its host (`rjbs/Loose-Threads`), or for
a path, after the last element, and colors it from a fixed palette by a
hash of the id, so every machine agrees.  It never sets either again on
its own, so a change made in `lt rename-project` or in any other client
stays; `move-project` carries both to the new list.  `lt
project-defaults` fills in the defaults for lists that have no name of
their own (or only their id) or no color, as lists made before `lt`
picked them did not.

TaskLists have nothing like `uid`, so two machines that both start a
new project offline will each create a list for it.  After each pull,
duplicates are merged, keeping the list with the
lowest JMAP id (or the oldest, if none has one yet), moving the
others' Tasks into it, and destroying the empty ones.  The merge
runs right after the first pull, so a list made here for a project
another machine already created is absorbed before it is ever sent.
The same merge settles a list create whose response was lost: the
next pull brings back the list, of which the pending create is now a
duplicate.
The rule is deterministic, so two machines merging at once agree.  Until
then, reads treat every live list carrying a project id as that project.

Tasks name their lists in `taskListIds`, a map from list id to `true`,
as TaskMaster implements it.  `lt` keeps each Task in exactly one list;
one filed elsewhere in several belongs, for `lt`, to the first that is
a project.  A thread created offline in a project created offline
refers to a TaskList the server has never seen: its `taskListIds` names
the pending list by a creation reference made from its local key,
TaskLists always sync out before Tasks, and the reference is rewritten
when the real id arrives.  Jinx deferred cross-type references to
pending creates; with two types and a fixed order, we do not need to.

Moves follow:

* Moving a thread to another project (the browser's `m`) changes
  `taskListIds`.
* `lt rehome` moves every Task into the target project's list and
  destroys the old list.
* `lt move-project` (to another collection) is a move between
  accounts: create in the new account and destroy in the old.  `uid`
  keeps identity across it.  `Task/copy` would also do, but nothing
  needs it.

### Foreign lists and tasks

`lt` ignores TaskLists without the project property, so the default
list TaskMaster creates in each account, or any list made by another
client, is harmless.  A Task in one of our lists is a thread whether
or not `lt` created it.  Its session and origin are null, its id is
whatever uid its client chose (probably a UUID, ugly but working, since
ids are matched by suffix), and its `created` may need the `updated`
fallback.

`lt` writes descriptions as `text/plain`.  If one turns up with any
other content type, `lt append` refuses rather than append Markdown to
HTML.  This is not expected to happen.

### Servers and accounts

Collections are accounts: two, personal and work, on one
TaskMaster.  Keeping them separate is tidiness, not policy.  The same
backend is served two ways.  On the tailnet, it authenticates by
Tailscale login, and a machine there sees every account.  On the public
internet, it takes a bearer token, which `lt` sends when its config
gives one; a work VM's token sees only the work account, so its config
syncs only that collection.  A collection that names its own server
uses only its own token, so no token is sent anywhere it was not meant
for.

### Testing

Local reads and writes test against the cache alone.  Sync tests run
against `internal/jmaptest`, a small in-memory JMAP server serving Task
and TaskList with change tracking, uid queries, result references, and
patches.  It advertises small request limits, so a handful of threads
exercises chunking and paging, counts requests, can carry out a request
but lose its response, and can answer every `/changes` with
`cannotCalculateChanges`, which forces a full resync.  It is not
Jinx's scripted server: its tests are Go code, not data.

## Open questions

* **Same-second ordering.**  `created` has one-second precision, so a
  burst of adds ties and sorts by random suffix.  Sub-second timestamps
  in the Task or a time component in the id would fix it; neither is
  worth the ugliness until it bites.
* **Ordering and priority.**  Creation order is the only ordering
  defined so far.  Do we want a priority field, or is manual reordering
  in the TUI enough?  Leaning: no priority field until it hurts.
* **Session-less threads.**  Threads the human adds from the TUI have no
  session.  Fine, but the "this session" scope should not surprise
  anyone by omitting them.
* **Cross-project view.**  The TUI's "all projects" view is easy, but
  what does the agent do with `scope=all`?  Probably nothing by default;
  it exists for the human.

## Non-goals, for now

* Purging or archiving closed threads.
* Anything resembling a project management tool: no assignees, no due
  dates, no dependencies.  A thread that lingers long enough to want
  those things gets ported to Linear; Loose Threads does not grow them.
