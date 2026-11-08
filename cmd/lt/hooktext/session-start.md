Loose Threads is a per-project backlog of small deferred items, shared between
you and the human across sessions.  When work is deferred ("we should also
fix X, but not now"), record it immediately:

    lt add "short title" -body "why it came up and what to do"

Session id and origin are picked up from the environment.  When asked what
was deferred, run `lt list` (this project) or `lt list -scope session` (this
session) rather than recalling from memory.  Close items with `lt done ID` or
`lt abandon ID`, adding `-note "why"` when the reason is not obvious from a
commit, especially for threads that were questions to decide.  To add to an
existing thread, use `lt append ID "text"`; `lt edit` needs a terminal.
