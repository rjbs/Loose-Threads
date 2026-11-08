// Package tui is the terminal browser for Loose Threads: a list of the
// current project's threads with a detail pane, a project picker, and
// single-key actions for the common edits.
package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rjbs/loosethreads/internal/editor"
	"github.com/rjbs/loosethreads/internal/project"
	"github.com/rjbs/loosethreads/internal/store"
	"github.com/rjbs/loosethreads/internal/thread"
)

type mode int

const (
	modeThreads mode = iota
	modeProjects
	modeAdd
	modeNote // closing with a note
	modeHelp
)

// Model is the Bubble Tea model for the browser.
type Model struct {
	store    *store.Store
	projects []*store.Project
	cur      int // index into projects
	threads  []*thread.Thread

	showClosed bool
	mode       mode
	addAndEdit bool
	noteTarget thread.State // the state a modeNote prompt will apply

	// moving is the thread the picker is choosing a destination for, or
	// nil when the picker is just switching projects.
	moving *thread.Thread

	// sticky holds ids of threads shown as open since the last hard
	// refresh.  When one of them is closed by someone else it stays on
	// screen, styled as closed, so that a glance at the list shows the
	// transition; a hard refresh (or closing it here) clears it.
	sticky map[string]bool

	// generation is the store's change counter as of the last load, so
	// that a change by any process triggers a reload.
	generation int64

	// syncTrouble names the collections whose syncing has problems, for
	// the footer, or is empty.
	syncTrouble string

	// sync, if set, starts a background sync of the named collection, or
	// of every collection when the name is empty.
	sync func(collection string)

	// editing is the edit in progress in the external editor, if any.
	editing *editor.Session

	list  list.Model // threads of the current project
	plist list.Model // project picker
	input textinput.Model

	width, height int
	status        string
	err           error

	theme Theme
	now   func() time.Time
}

// SetTheme changes the theme.  Call before Run.
func (m *Model) SetTheme(t Theme) { m.theme = t }

// SetSync gives the browser a way to start a background sync of one
// collection, or of all when the name is empty.  Call before Run.
func (m *Model) SetSync(f func(collection string)) { m.sync = f }

// New builds a Model over s, starting on the project with id start.  If
// that project is not in the store yet it is shown anyway, so that
// browsing from a fresh checkout works; it is created on the first add.  An empty start selects the first project.
func New(s *store.Store, start string) (*Model, error) {
	m := &Model{store: s, now: time.Now, sticky: map[string]bool{}}
	m.theme, _ = LookupTheme(DefaultTheme)

	m.list = list.New(nil, threadDelegate{m}, 0, 0)
	m.list.SetShowTitle(false)
	m.list.SetShowStatusBar(false)
	m.list.SetShowHelp(false)
	m.list.SetFilteringEnabled(true)
	m.list.DisableQuitKeybindings()

	m.plist = list.New(nil, projectDelegate{m}, 0, 0)
	m.plist.SetShowTitle(false)
	m.plist.SetShowStatusBar(false)
	m.plist.SetShowHelp(false)
	m.plist.DisableQuitKeybindings()

	m.input = textinput.New()
	m.input.Prompt = "title: "
	m.input.CharLimit = 200

	if err := m.loadProjects(start); err != nil {
		return nil, err
	}
	return m, nil
}

// loadProjects refreshes the project list, keeping (or selecting) the
// project with id keep.
func (m *Model) loadProjects(keep string) error {
	ps, err := m.store.Projects()
	if err != nil {
		return err
	}
	m.cur = 0
	if keep != "" {
		found := false
		for i, p := range ps {
			if p.ID == keep {
				m.cur, found = i, true
				break
			}
		}
		if !found {
			ps = append(ps, &store.Project{ID: keep, Collection: m.store.Config().Route(keep)})
			m.cur = len(ps) - 1
		}
	}
	m.projects = ps
	return m.loadThreads()
}

// refresh is the hard refresh: forget sticky threads and reload.
func (m *Model) refresh() error {
	m.sticky = map[string]bool{}
	keep := ""
	if p := m.project(); p != nil {
		keep = p.ID
	}
	return m.loadProjects(keep)
}

// switchProject moves to project index i and starts a fresh view of it.
func (m *Model) switchProject(i int) error {
	m.cur = i
	m.sticky = map[string]bool{}
	clearFilter(&m.list)
	m.list.ResetSelected()
	return m.loadThreads()
}

// loadThreads re-reads the current project's threads and rebuilds the
// list, preserving the selection where possible.
func (m *Model) loadThreads() error {
	m.threads = nil
	if len(m.projects) == 0 {
		m.list.SetItems(nil)
		return nil
	}
	p := m.projects[m.cur]

	var selected string
	if it, ok := m.list.SelectedItem().(threadItem); ok {
		selected = it.t.ID
	}

	ths, err := m.store.Threads(p)
	if err != nil {
		return err
	}
	m.threads = ths

	var items []list.Item
	for _, t := range ths {
		if !t.IsOpen() && !m.showClosed && !m.sticky[t.ID] {
			continue
		}
		if t.IsOpen() {
			m.sticky[t.ID] = true
		}
		items = append(items, threadItem{t})
	}
	setItems(&m.list, items)
	visible := m.list.VisibleItems()
	if idx := visibleIndex(m.list, selected); idx >= 0 {
		m.list.Select(idx)
	} else if m.list.Index() >= len(visible) && len(visible) > 0 {
		m.list.Select(len(visible) - 1)
	}
	m.generation, _ = m.store.Generation()
	m.syncTrouble = syncTrouble(m.store)
	m.layout()
	return nil
}

// Filtering.  The list component filters asynchronously: each change to
// the filter text returns a command whose result, a FilterMatchesMsg, holds
// the matching items themselves.  If a poll reloads the list between the
// keystroke and the result, the result carries the old thread values, and
// acting on one would save stale data over the reload.  The lists are
// short, so instead we filter synchronously whenever the text or the items
// change, and drop the component's own FilterMatchesMsg.
// -- claude, 2026-09-23

// setItems replaces l's items, recomputing the filter's matches at once
// if one is in place.
func setItems(l *list.Model, items []list.Item) {
	if cmd := l.SetItems(items); cmd != nil {
		if msg, ok := cmd().(list.FilterMatchesMsg); ok {
			*l, _ = l.Update(msg)
		}
	}
}

// updateFiltering passes a key to l while its filter is being typed,
// refiltering if the text changed.
func updateFiltering(l *list.Model, msg tea.KeyMsg) tea.Cmd {
	before := l.FilterValue()
	var cmd tea.Cmd
	*l, cmd = l.Update(msg)
	if l.FilterValue() != before && l.FilterState() != list.Unfiltered {
		setItems(l, l.Items())
	}
	return cmd
}

// clearFilter drops l's filter, whether being typed or applied.
func clearFilter(l *list.Model) {
	l.ResetFilter()
	l.FilterInput.Blur()
}

// clearFilterKey is what terminals send for ctrl-/, the same byte as
// ctrl-_.
const clearFilterKey = "ctrl+_"

// visibleIndex returns the position of the thread with id among l's
// visible items, or -1.
func visibleIndex(l list.Model, id string) int {
	if id == "" {
		return -1
	}
	for i, it := range l.VisibleItems() {
		if it.(threadItem).t.ID == id {
			return i
		}
	}
	return -1
}

// openCurrent returns how many of the current project's threads are open.
func (m *Model) openCurrent() int {
	n := 0
	for _, t := range m.threads {
		if t.IsOpen() {
			n++
		}
	}
	return n
}

// Polling.  A tick every pollInterval compares the store's generation, a
// counter every write to the cache bumps, with the one last loaded; any
// difference reloads.  Every syncInterval the browser starts a background
// sync, and the poll notices whatever it brings.

const (
	pollInterval = time.Second
	syncInterval = 60 * time.Second
)

type tickMsg struct{}
type syncTickMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

func syncTick() tea.Cmd {
	return tea.Tick(syncInterval, func(time.Time) tea.Msg { return syncTickMsg{} })
}

func (m *Model) syncAll() {
	if m.sync != nil {
		m.sync("")
	}
}

// syncTrouble summarizes, in a few words, which collections have sync
// problems.  Every sync records its outcome, which changes the store's
// generation, so this is recomputed whenever the browser reloads.
func syncTrouble(s *store.Store) string {
	sts, err := s.SyncStatuses()
	if err != nil {
		return "sync status unreadable: " + err.Error()
	}
	var names []string
	for _, st := range sts {
		if len(st.Problems(time.Now())) > 0 {
			names = append(names, st.Collection)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "sync problems: " + strings.Join(names, ", ") + " (lt sync -status)"
}

// poll reloads if the store has changed since the last load.
func (m *Model) poll() {
	if g, err := m.store.Generation(); err == nil && g == m.generation {
		return
	}
	keep := ""
	if p := m.project(); p != nil {
		keep = p.ID
	}
	if err := m.loadProjects(keep); err != nil {
		m.err = err
	}
	if m.mode == modeProjects {
		m.refreshPicker()
	}
}

func (m *Model) project() *store.Project {
	if len(m.projects) == 0 {
		return nil
	}
	return m.projects[m.cur]
}

func (m *Model) selected() *thread.Thread {
	if it, ok := m.list.SelectedItem().(threadItem); ok {
		return it.t
	}
	return nil
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	m.syncAll()
	return tea.Batch(tick(), syncTick())
}

type editorDoneMsg struct{ err error }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case editorDoneMsg:
		m.finishEdit(msg.err)
		return m, nil
	case tickMsg:
		m.poll()
		return m, tick()
	case list.FilterMatchesMsg:
		return m, nil // see "Filtering" above
	case syncTickMsg:
		m.syncAll()
		return m, syncTick()
	case tea.KeyMsg:
		m.status = ""
		m.err = nil
		switch m.mode {
		case modeThreads:
			return m.updateThreads(msg)
		case modeProjects:
			return m.updateProjects(msg)
		case modeAdd:
			return m.updateAdd(msg)
		case modeNote:
			return m.updateNote(msg)
		case modeHelp:
			m.mode = modeThreads
			return m, nil
		}
	}
	return m, nil
}

func (m *Model) updateThreads(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == clearFilterKey {
		clearFilter(&m.list)
		return m, nil
	}
	if m.list.SettingFilter() {
		return m, updateFiltering(&m.list, msg)
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.mode = modeHelp
		return m, nil
	case "r", "ctrl+r":
		m.err = m.refresh()
		return m, nil
	case "c":
		m.showClosed = !m.showClosed
		m.err = m.loadThreads()
		return m, nil
	case "p":
		clearFilter(&m.list)
		m.openProjects()
		return m, nil
	case "m":
		if t := m.selected(); t != nil {
			m.moving = t
			m.openProjects()
		}
		return m, nil
	case "[", "]":
		if n := len(m.projects); n > 1 {
			if msg.String() == "[" {
				m.err = m.switchProject((m.cur + n - 1) % n)
			} else {
				m.err = m.switchProject((m.cur + 1) % n)
			}
		}
		return m, nil
	case "a", "A":
		if m.project() == nil {
			m.err = fmt.Errorf("no project to add to")
			return m, nil
		}
		m.mode = modeAdd
		m.addAndEdit = msg.String() == "A"
		m.input.Prompt = "title: "
		m.input.Reset()
		return m, m.input.Focus()
	case "d":
		return m, m.toggleState(thread.Done, "")
	case "x":
		return m, m.toggleState(thread.Abandoned, "")
	case "D", "%":
		if m.selected() == nil {
			return m, nil
		}
		m.noteTarget = thread.Done
		if msg.String() == "%" {
			m.noteTarget = thread.Abandoned
		}
		m.mode = modeNote
		m.input.Prompt = fmt.Sprintf("%s, because: ", noteVerb(m.selected(), m.noteTarget))
		m.input.Reset()
		return m, m.input.Focus()
	case "e", "enter":
		if t := m.selected(); t != nil {
			return m, m.edit(t)
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// noteVerb describes what pressing D or % on t will do, for the prompt.
func noteVerb(t *thread.Thread, target thread.State) string {
	if t.State == target {
		return "reopen"
	}
	if target == thread.Abandoned {
		return "abandon"
	}
	return "done"
}

// toggleState moves the selected thread to target, or back to open if it
// is already in target, appends note if any, and saves it.
func (m *Model) toggleState(target thread.State, note string) tea.Cmd {
	t := m.selected()
	if t == nil {
		return nil
	}
	next := target
	if t.State == target {
		next = thread.Open
	}
	if err := t.Resolve(next, note, thread.OriginHuman, m.now()); err != nil {
		m.err = err
		return nil
	}
	if err := m.store.Save(m.project(), t); err != nil {
		m.err = err
		return nil
	}
	if !t.IsOpen() {
		delete(m.sticky, t.ID) // closed here, so it need not linger
	}
	m.status = fmt.Sprintf("%s: %s", t.ID, t.State)
	m.err = m.loadThreads()
	m.pushLater()
	return nil
}

// pushLater syncs the current project's collection in the background, so
// browser edits reach the server like CLI ones do.
func (m *Model) pushLater() {
	if p := m.project(); p != nil {
		m.pushCollection(p.Collection)
	}
}

func (m *Model) pushCollection(name string) {
	if m.sync != nil {
		m.sync(name)
	}
}

// edit opens t in the external editor, by way of a temporary file;
// finishEdit saves the result when the editor exits.
func (m *Model) edit(t *thread.Thread) tea.Cmd {
	sess, err := editor.Start(t)
	if err != nil {
		m.err = err
		return nil
	}
	m.editing = sess
	return tea.ExecProcess(editor.Command(sess.Path), func(err error) tea.Msg {
		return editorDoneMsg{err}
	})
}

func (m *Model) finishEdit(editErr error) {
	sess := m.editing
	m.editing = nil
	if sess == nil {
		return
	}
	if editErr != nil {
		os.Remove(sess.Path)
		m.err = fmt.Errorf("editor: %w", editErr)
		return
	}
	edited, err := sess.Finish()
	switch {
	case err != nil:
		m.err = err
	case edited != nil:
		if err := m.store.Save(m.project(), edited); err != nil {
			m.err = err
			return
		}
		m.status = edited.ID + ": saved"
		m.pushLater()
	}
	if err := m.loadThreads(); err != nil {
		m.err = err
	}
}

// StartInPicker opens the browser on the project picker instead of a
// project's threads, for directories that belong to no project.
func (m *Model) StartInPicker() {
	m.openProjects()
	m.plist.Select(0)
}

// openProjects switches to the picker, reading each project's threads
// once so the right-hand pane can preview them.
func (m *Model) openProjects() {
	var items []list.Item
	for _, p := range m.projects {
		ths := m.threads
		if p != m.project() {
			ths, _ = m.store.Threads(p)
		}
		n := 0
		for _, t := range ths {
			if t.IsOpen() {
				n++
			}
		}
		items = append(items, projectItem{p, ths, n})
	}
	setItems(&m.plist, items)
	m.plist.Select(m.cur)
	m.mode = modeProjects
}

// refreshPicker rebuilds the picker's items after the store changed,
// keeping the highlighted row.
func (m *Model) refreshPicker() {
	idx := m.plist.Index()
	m.openProjects()
	m.plist.Select(min(idx, max(len(m.plist.Items())-1, 0)))
}

func (m *Model) updateProjects(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == clearFilterKey {
		clearFilter(&m.plist)
		return m, nil
	}
	if m.plist.SettingFilter() {
		return m, updateFiltering(&m.plist, msg)
	}
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc", "p":
		clearFilter(&m.plist)
		m.moving = nil
		m.mode = modeThreads
		return m, nil
	case "enter":
		var cmd tea.Cmd
		if it, ok := m.plist.SelectedItem().(projectItem); ok {
			for i, p := range m.projects {
				if p != it.p {
					continue
				}
				if m.moving != nil {
					cmd = m.moveTo(i)
				} else {
					m.err = m.switchProject(i)
				}
			}
		}
		clearFilter(&m.plist)
		m.moving = nil
		m.mode = modeThreads
		return m, cmd
	}
	var cmd tea.Cmd
	m.plist, cmd = m.plist.Update(msg)
	return m, cmd
}

// moveTo moves the thread being moved into project index i, staying on
// the current project, and pushes both collections involved.
func (m *Model) moveTo(i int) tea.Cmd {
	t, from := m.moving, m.project()
	if i == m.cur {
		m.status = fmt.Sprintf("%s is already in %s", t.ID, from.DisplayName())
		return nil
	}
	to, err := m.store.Project(m.projects[i].ID) // creates the project if needed
	if err != nil {
		m.err = err
		return nil
	}
	m.projects[i] = to
	if err := m.store.Move(from, to, t.ID); err != nil {
		m.err = err
		return nil
	}
	delete(m.sticky, t.ID)
	m.status = fmt.Sprintf("%s moved to %s", t.ID, to.DisplayName())
	m.err = m.loadThreads()
	m.pushLater()
	if to.Collection != from.Collection {
		m.pushCollection(to.Collection)
	}
	return nil
}

func (m *Model) updateNote(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeThreads
		m.input.Blur()
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		m.mode = modeThreads
		m.input.Blur()
		return m, m.toggleState(m.noteTarget, m.input.Value())
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) updateAdd(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeThreads
		m.input.Blur()
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		title := strings.TrimSpace(m.input.Value())
		if title == "" {
			return m, nil
		}
		m.mode = modeThreads
		m.input.Blur()

		p, err := m.store.Project(m.project().ID) // creates the project if needed
		if err != nil {
			m.err = err
			return m, nil
		}
		m.projects[m.cur] = p
		t := &thread.Thread{State: thread.Open, Origin: thread.OriginHuman, Body: title + "\n"}
		if err := m.store.Create(p, t, m.now()); err != nil {
			m.err = err
			return m, nil
		}
		m.status = "added " + t.ID
		if err := m.loadThreads(); err != nil {
			m.err = err
			return m, nil
		}
		// A new thread the filter hides would seem not to have been
		// added, so drop the filter to show it.
		idx := visibleIndex(m.list, t.ID)
		if idx < 0 {
			clearFilter(&m.list)
			idx = visibleIndex(m.list, t.ID)
		}
		if idx >= 0 {
			m.list.Select(idx)
		}
		if m.addAndEdit {
			return m, m.edit(t)
		}
		m.pushLater()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// Layout: a one-line header, an optional one-line warning, the body, and
// a one-line footer.  When wide enough the body splits into the list and
// a detail pane.

const minDetailWidth = 100

// warnings returns short callouts for the current project.  They are
// shown in a bordered box at the top of the list pane; the long
// explanations live in DESIGN.md and the CLI's stderr messages.
func (m *Model) warnings() []string {
	p := m.project()
	if p == nil {
		return nil
	}
	var ws []string
	if project.IsPath(p.ID) {
		ws = append(ws, "project identified by path alone")
	}
	return ws
}

// warningGlyph marks each warning.  The obvious choice, U+26A0 WARNING
// SIGN plus the emoji variation selector, is a neutral-width character
// that terminals render at either one or two cells depending on how they
// treat the selector, which misaligns the box border.  U+2757 is wide in
// the Unicode tables themselves, so every layer agrees on two cells.
// -- claude, 2026-09-04
const warningGlyph = "\u2757"

// warningBox renders the warnings in a red-bordered box of the given
// width, or "" when there are none.
func (m *Model) warningBox(width int) string {
	ws := m.warnings()
	if len(ws) == 0 {
		return ""
	}
	for i, w := range ws {
		ws[i] = warningGlyph + " " + w
	}
	return m.theme.WarningBox.Width(max(width-2, 1)).Render(strings.Join(ws, "\n"))
}

// warningHeight is the number of rows the warning box takes.
func (m *Model) warningHeight() int {
	if n := len(m.warnings()); n > 0 {
		return n + 2 // border above and below
	}
	return 0
}

// bodyHeight leaves rows for the header, a blank spacer, and the footer.
func (m *Model) bodyHeight() int { return max(m.height-3, 1) }

func (m *Model) listWidth() int {
	if m.width >= minDetailWidth {
		return m.width / 2
	}
	return m.width
}

func (m *Model) layout() {
	w, h := m.listWidth(), m.bodyHeight()-m.warningHeight()
	if m.theme.Panes {
		w, h = w-2-m.theme.PanePadding, h-2
	}
	m.list.SetSize(max(w, 1), max(h, 1))
	pw, ph := m.listWidth(), m.bodyHeight()
	if m.theme.Panes {
		pw, ph = pw-2-m.theme.PanePadding, ph-2
	}
	m.plist.SetSize(max(pw, 1), max(ph, 1))
	m.input.Width = max(m.width-len(m.input.Prompt)-2, 10)
}

// View implements tea.Model.
func (m *Model) View() string {
	if m.width == 0 {
		return ""
	}
	th := m.theme
	switch m.mode {
	case modeHelp:
		return m.viewHelp()
	case modeProjects:
		return m.viewPicker()
	}

	header := th.Header.Render("no projects")
	if p := m.project(); p != nil {
		header = th.Header.Render(headerGlyph + p.DisplayName())
		if p.Name != "" {
			header += th.HeaderMeta.Render("  " + p.ID)
		}
		header += th.HeaderMeta.Render(fmt.Sprintf("  %d open", m.openCurrent()))
	}
	if m.showClosed {
		header += th.HeaderMeta.Render("  (showing closed)")
	}

	listW, bodyH := m.listWidth(), m.bodyHeight()
	listH := bodyH - m.warningHeight()
	left := m.listView(m.list)
	if th.Panes {
		left = m.box("threads", left, listW, listH, true)
	}
	if box := m.warningBox(listW); box != "" {
		left = lipgloss.JoinVertical(lipgloss.Left, box, left)
	}
	body := left
	if m.width >= minDetailWidth {
		detailW := m.width - listW - 1
		var detail string
		if th.Panes {
			// Wrap to the frame's interior, not the frame; wrapping at the
			// outer width and then squeezing produced ragged lines.
			detail = m.viewDetail(detailW - 2 - th.PanePadding)
			detail = m.box("detail", detail, detailW, bodyH, false)
		} else {
			detail = m.viewDetail(detailW)
		}
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(listW).Render(left), " ", detail)
	}

	footer := m.footerKeys("a: add", "A: add+edit", "e: edit", "d/D: done", "x/%: abandon",
		"m: move", "c: closed", "p/[/]: project", filterHint(m.list), "?: help", "q: quit")
	switch m.mode {
	case modeAdd:
		footer = th.Prompt.Render(m.input.View()) + th.Footer.Render("   (enter: save, esc: cancel)")
	case modeNote:
		footer = th.Prompt.Render(m.input.View()) + th.Footer.Render("   (enter: apply, esc: cancel)")
	}
	return m.frame(header, body, footer)
}

// headerGlyph precedes the project name.  U+1F9F5 SPOOL OF THREAD is in
// the emoji block proper, so its two-cell width is agreed on everywhere.
const headerGlyph = " \U0001F9F5 "

// box frames content as a titled pane, styled for focus or not.
func (m *Model) box(title, content string, width, height int, focused bool) string {
	th := m.theme
	edge := th.PaneStyle
	if focused {
		edge = th.FocusStyle
	}
	titleStyle := th.PaneTitle
	if th.TitleInBorderColor {
		titleStyle = edge
	}
	return titledBox(title, content, width, height, th.PaneBorder, edge, titleStyle, th.PanePadding)
}

// listView renders a list without the blank line the component leaves
// where its (hidden) title bar would be.  While a filter is typed, the
// component draws it there; once applied, the component would show
// nothing, so we draw it ourselves to keep it clear the list is filtered.
func (m *Model) listView(l list.Model) string {
	v := l.View()
	if l.SettingFilter() {
		return v
	}
	// The blank title bar is a line of spaces padded to the width, not an
	// empty line.
	first, rest, ok := strings.Cut(v, "\n")
	if !ok || strings.TrimSpace(first) != "" {
		return v
	}
	if l.FilterState() == list.FilterApplied {
		// The title bar's padding matches the component's own filter
		// line, so accepting the filter doesn't shift the list.
		line := m.theme.Prompt.Render(l.FilterInput.Prompt+l.FilterValue()) +
			m.theme.Meta.Render("  (ctrl-/ to clear)")
		return l.Styles.TitleBar.Render(truncate(line, l.Width())) + "\n" + rest
	}
	return rest
}

// viewPicker is the two-pane project picker: projects on the left, the
// highlighted project's open threads on the right.
func (m *Model) viewPicker() string {
	th := m.theme
	listW, bodyH := m.listWidth(), m.bodyHeight()

	left := m.listView(m.plist)
	if th.Panes {
		left = m.box("projects", left, listW, bodyH, true)
	}
	body := left
	if m.width >= minDetailWidth {
		detailW := m.width - listW - 1
		var right string
		if th.Panes {
			right = m.viewPickerPreview(detailW - 2 - th.PanePadding)
			right = m.box("threads", right, detailW, bodyH, false)
		} else {
			right = m.viewPickerPreview(detailW)
		}
		body = lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(listW).Render(left), " ", right)
	}

	header := th.Header.Render(headerGlyph+"Loose Threads") + th.HeaderMeta.Render(fmt.Sprintf("  %d projects", len(m.projects)))
	footer := m.footerKeys("enter: select", filterHint(m.plist), "esc: back", "q: quit")
	if t := m.moving; t != nil {
		header = th.Header.Render(headerGlyph+"move: "+t.Title()) + th.HeaderMeta.Render("  "+thread.Suffix(t.ID)+"  to which project?")
		footer = m.footerKeys("enter: move here", filterHint(m.plist), "esc: cancel", "q: quit")
	}
	return m.frame(header, body, footer)
}

// viewPickerPreview lists the highlighted project's open threads.
func (m *Model) viewPickerPreview(width int) string {
	it, ok := m.plist.SelectedItem().(projectItem)
	if !ok {
		return ""
	}
	th := m.theme
	var b strings.Builder
	b.WriteString(th.DetailTitle.Render(it.p.DisplayName()))
	if it.p.Name != "" {
		b.WriteString(th.Meta.Render("  " + it.p.ID))
	}
	b.WriteString(th.Meta.Render(fmt.Sprintf("  %d open", it.open)))
	b.WriteString("\n\n")
	if it.open == 0 {
		b.WriteString(th.Meta.Render("no open threads"))
	}
	for _, t := range it.threads {
		if !t.IsOpen() {
			continue
		}
		line := th.Normal.Render(t.Title()) + th.Meta.Render("  "+thread.Suffix(t.ID))
		b.WriteString(truncate(line, width) + "\n")
	}
	pad := 1
	if th.Panes {
		pad = 0
	}
	return lipgloss.NewStyle().Width(width).PaddingLeft(pad).Render(b.String())
}

// filterHint is the footer's filter key: how to start one, or how to
// clear the one in place.
func filterHint(l list.Model) string {
	if l.FilterState() == list.Unfiltered {
		return "/: filter"
	}
	return "ctrl-/: unfilter"
}

// footerKeys renders "key: what" hints with the key picked out.
func (m *Model) footerKeys(hints ...string) string {
	th := m.theme
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		k, v, ok := strings.Cut(h, ": ")
		if !ok {
			parts = append(parts, th.Footer.Render(h))
			continue
		}
		parts = append(parts, th.FooterKey.Render(k)+th.Footer.Render(": "+v))
	}
	return strings.Join(parts, th.Footer.Render("  "))
}

func (m *Model) frame(header, body, footer string) string {
	th := m.theme
	if m.err != nil {
		footer = th.Error.Render(m.err.Error())
	} else if m.status != "" && m.mode != modeAdd && m.mode != modeNote {
		footer = th.Status.Render(m.status) + "   " + footer
	} else if m.syncTrouble != "" && m.mode != modeAdd && m.mode != modeNote {
		footer = th.Error.Render(m.syncTrouble) + "   " + footer
	}
	bodyH := m.bodyHeight()
	body = lipgloss.NewStyle().Height(bodyH).MaxHeight(bodyH).Render(body)
	return truncate(header, m.width) + "\n\n" + body + "\n" + truncate(footer, m.width)
}

func (m *Model) viewDetail(width int) string {
	t := m.selected()
	if t == nil {
		return ""
	}
	th := m.theme
	var b strings.Builder
	row := func(label, value string) {
		fmt.Fprintf(&b, "%s %s\n", th.DetailLabel.Render(label), th.DetailBody.Render(value))
	}
	row("id:     ", t.ID)
	row("state:  ", string(t.State))
	row("created:", t.Created.Format("2006-01-02 15:04"))
	if t.Closed != nil {
		row("closed: ", t.Closed.Format("2006-01-02 15:04"))
	}
	if t.Origin != "" {
		row("origin: ", string(t.Origin))
	}
	if t.Session != "" {
		row("session:", t.Session)
	}
	b.WriteString("\n")
	title, rest, _ := strings.Cut(strings.TrimLeft(t.Body, "\n"), "\n")
	b.WriteString(th.DetailTitle.Render(title))
	b.WriteString("\n")
	b.WriteString(th.DetailBody.Render(rest))
	pad := 1
	if th.Panes {
		pad = 0
	}
	return lipgloss.NewStyle().Width(width).PaddingLeft(pad).Render(b.String())
}

// helpSections is the key reference shown by "?".  Each entry is a key
// (or keys) and what it does; blank keys are section headings.
var helpSections = [][2]string{
	{"", "Moving around"},
	{"j / k, ↓ / ↑", "move"},
	{"g / G", "first / last"},
	{"/", "filter by title"},
	{"ctrl-/", "clear the filter"},
	{"p", "project picker"},
	{"[ / ]", "previous / next project"},

	{"", "Threads"},
	{"a", "add a thread"},
	{"A", "add and open in $EDITOR"},
	{"e, enter", "open in $EDITOR"},
	{"d", "mark done (again to reopen)"},
	{"x", "mark abandoned (again to reopen)"},
	{"D / %", "the same, after asking why"},
	{"m", "move to another project"},

	{"", "The view"},
	{"c", "show / hide closed threads"},
	{"r, ctrl-r", "hard refresh (hides closed)"},
	{"?", "this help"},
	{"q", "quit"},
}

// helpNotes are paragraphs; the view wraps them to the pane.
const helpNotes = "The list refreshes itself as the store changes.  A thread someone else closes " +
	"stays on screen, struck through, until a hard refresh.  A thread you close here " +
	"disappears at once.\n\n" +
	"Threads live in a local cache under $LOOSETHREADS_HOME, synced with a JMAP " +
	"for Tasks server.  The same cache is what \"lt\" and the MCP server use."

func (m *Model) viewHelp() string {
	th := m.theme
	keyW := 0
	for _, h := range helpSections {
		keyW = max(keyW, lipgloss.Width(h[0]))
	}

	var b strings.Builder
	for i, h := range helpSections {
		if h[0] == "" {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(th.DetailTitle.Render(h[1]) + "\n")
			continue
		}
		pad := strings.Repeat(" ", keyW-lipgloss.Width(h[0]))
		b.WriteString("  " + th.FooterKey.Render(h[0]) + pad + "   " + th.DetailBody.Render(h[1]) + "\n")
	}
	keys := b.String()
	notes := th.Meta.Render(helpNotes)

	bodyH := m.bodyHeight()
	var body string
	if m.width >= minDetailWidth {
		// Keys on the left, notes on the right, like the other views.
		listW := m.listWidth()
		noteW := m.width - listW - 1
		left, right := keys, notes
		if th.Panes {
			left = m.box("keys", keys, listW, bodyH, true)
			right = m.box("about", lipgloss.NewStyle().Width(noteW-2-th.PanePadding).Render(notes), noteW, bodyH, false)
		} else {
			right = lipgloss.NewStyle().Width(noteW).PaddingLeft(1).Render(notes)
		}
		body = lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(listW).Render(left), " ", right)
	} else {
		body = keys + "\n" + lipgloss.NewStyle().Width(m.width).Render(notes)
		if th.Panes {
			body = m.box("help", body, m.width, bodyH, true)
		}
	}

	header := th.Header.Render(headerGlyph+"help") + th.HeaderMeta.Render("  lt browse")
	footer := th.Footer.Render("press any key to return")
	return m.frame(header, body, footer)
}

func truncate(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

// Items and delegates.

type threadItem struct{ t *thread.Thread }

func (i threadItem) FilterValue() string { return i.t.Title() }

type threadDelegate struct{ m *Model }

func (threadDelegate) Height() int                         { return 1 }
func (threadDelegate) Spacing() int                        { return 0 }
func (threadDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (d threadDelegate) Render(w io.Writer, l list.Model, index int, item list.Item) {
	it, ok := item.(threadItem)
	if !ok {
		return
	}
	th := d.m.theme
	t := it.t
	sel := index == l.Index()

	glyph := " "
	switch t.State {
	case thread.Done:
		glyph = th.DoneStyle.Render(th.DoneGlyph)
	case thread.Abandoned:
		glyph = th.AbandonStyle.Render(th.AbandonGlyph)
	}

	cursor := strings.Repeat(" ", lipgloss.Width(th.Cursor))
	if sel {
		cursor = th.CursorStyle.Render(th.Cursor)
	}

	title := t.Title()
	meta := "  " + thread.Suffix(t.ID)
	var line string
	switch {
	case !t.IsOpen():
		line = th.Closed.Render(title) + th.Meta.Render(meta)
	case sel:
		line = th.Selected.Render(title) + th.SelectedMeta.Render(meta)
	default:
		line = th.Normal.Render(title) + th.Meta.Render(meta)
	}
	line = cursor + glyph + " " + line

	width := l.Width()
	line = truncate(line, width)
	if sel && th.FullRow {
		line = th.RowBackground.Width(width).Render(line)
	}
	fmt.Fprint(w, line)
}

type projectItem struct {
	p       *store.Project
	threads []*thread.Thread
	open    int
}

func (i projectItem) FilterValue() string { return i.p.DisplayName() }

type projectDelegate struct{ m *Model }

func (projectDelegate) Height() int                         { return 1 }
func (projectDelegate) Spacing() int                        { return 0 }
func (projectDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (d projectDelegate) Render(w io.Writer, l list.Model, index int, item list.Item) {
	it, ok := item.(projectItem)
	if !ok {
		return
	}
	th := d.m.theme
	sel := index == l.Index()

	cursor := strings.Repeat(" ", lipgloss.Width(th.Cursor))
	if sel {
		cursor = th.CursorStyle.Render(th.Cursor)
	}
	count := fmt.Sprintf("%-4d ", it.open)
	name := it.p.DisplayName()
	var line string
	if sel {
		line = th.Selected.Render(count + name)
	} else {
		line = th.Normal.Render(count + name)
	}
	if it.p.Name != "" {
		line += th.Meta.Render("  " + it.p.ID)
	}
	if it.p.Collection != "" && it.p.Collection != store.LocalCollection {
		line += th.Meta.Render("  [" + it.p.Collection + "]")
	}
	line = cursor + line
	line = truncate(line, l.Width())
	if sel && th.FullRow {
		line = th.RowBackground.Width(l.Width()).Render(line)
	}
	fmt.Fprint(w, line)
}

// Run starts the program on the terminal.
func Run(m *Model) error {
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
