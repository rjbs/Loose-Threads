// Package tui is the terminal browser for Loose Threads: a list of the
// current project's threads with a detail pane, a project picker, and
// single-key actions for the common edits.
package tui

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rjbs/loosethreads/internal/editor"
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

	// sticky holds ids of threads shown as open since the last hard
	// refresh.  When one of them is closed by someone else it stays on
	// screen, styled as closed, so that a glance at the list shows the
	// transition; a hard refresh (or closing it here) clears it.
	sticky map[string]bool

	// fingerprint summarizes the store directories as last seen by the
	// poller, so that a change on disk triggers a reload.
	fingerprint string

	list  list.Model // threads of the current project
	plist list.Model // project picker
	input textinput.Model

	width, height int
	status        string
	err           error

	now func() time.Time
}

// New builds a Model over s, starting on the project with id start.  If
// that project has no directory in the store yet it is shown anyway, so
// that browsing from a fresh checkout works; its directory is created on
// the first add.  An empty start selects the first project.
func New(s *store.Store, start string) (*Model, error) {
	m := &Model{store: s, now: time.Now, sticky: map[string]bool{}}

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
	if keep != "" {
		found := false
		for _, p := range ps {
			if p.ID == keep {
				found = true
				break
			}
		}
		if !found {
			ps = append(ps, &store.Project{ID: keep, Dir: m.store.ProjectDir(keep)})
		}
	}
	m.projects = ps
	m.cur = 0
	for i, p := range ps {
		if p.ID == keep {
			m.cur = i
		}
	}
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

	ths, errs, err := m.store.Threads(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(errs) > 0 {
		m.status = fmt.Sprintf("%d unreadable thread file(s) skipped", len(errs))
	}
	m.threads = ths

	var items []list.Item
	idx := -1
	for _, t := range ths {
		if !t.IsOpen() && !m.showClosed && !m.sticky[t.ID] {
			continue
		}
		if t.IsOpen() {
			m.sticky[t.ID] = true
		}
		if t.ID == selected {
			idx = len(items)
		}
		items = append(items, threadItem{t})
	}
	m.list.SetItems(items)
	if idx >= 0 {
		m.list.Select(idx)
	} else if m.list.Index() >= len(items) && len(items) > 0 {
		m.list.Select(len(items) - 1)
	}
	m.fingerprint = m.currentFingerprint()
	return nil
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

// Polling.  A tick every pollInterval compares a fingerprint of the store
// root and the current project directory with the last one seen; any
// difference reloads.  Writes land by rename, which always updates the
// directory's mtime, so listing names, sizes, and mtimes catches every
// change without watching individual files.

const pollInterval = time.Second

type tickMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) currentFingerprint() string {
	var b strings.Builder
	dirs := []string{m.store.Root}
	if p := m.project(); p != nil {
		dirs = append(dirs, p.Dir)
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			fmt.Fprintf(&b, "%s: %v\n", dir, err)
			continue
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				continue
			}
			fmt.Fprintf(&b, "%s %d %d\n", e.Name(), info.Size(), info.ModTime().UnixNano())
		}
	}
	return b.String()
}

// poll reloads if the store has changed on disk since the last load.
func (m *Model) poll() {
	if m.currentFingerprint() == m.fingerprint {
		return
	}
	keep := ""
	if p := m.project(); p != nil {
		keep = p.ID
	}
	if err := m.loadProjects(keep); err != nil {
		m.err = err
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
func (m *Model) Init() tea.Cmd { return tick() }

type editorDoneMsg struct{ err error }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case editorDoneMsg:
		if msg.err != nil {
			m.err = fmt.Errorf("editor: %w", msg.err)
		}
		m.err = m.loadThreads()
		return m, nil
	case tickMsg:
		m.poll()
		return m, tick()
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
	if m.list.SettingFilter() {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
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
		m.openProjects()
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
	case "D", "X":
		if m.selected() == nil {
			return m, nil
		}
		m.noteTarget = thread.Done
		if msg.String() == "X" {
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

// noteVerb describes what pressing D or X on t will do, for the prompt.
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
	if err := t.Resolve(next, note, m.now()); err != nil {
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
	return nil
}

func (m *Model) edit(t *thread.Thread) tea.Cmd {
	path := m.store.ThreadPath(m.project(), t.ID)
	return tea.ExecProcess(editor.Command(path), func(err error) tea.Msg {
		return editorDoneMsg{err}
	})
}

func (m *Model) openProjects() {
	var items []list.Item
	for _, p := range m.projects {
		items = append(items, projectItem{p, m.openCount(p)})
	}
	m.plist.SetItems(items)
	m.plist.Select(m.cur)
	m.mode = modeProjects
}

func (m *Model) openCount(p *store.Project) int {
	if p == m.project() {
		return m.openCurrent()
	}
	ths, _, err := m.store.Threads(p)
	if err != nil {
		return 0
	}
	n := 0
	for _, t := range ths {
		if t.IsOpen() {
			n++
		}
	}
	return n
}

func (m *Model) updateProjects(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.plist.SettingFilter() {
		var cmd tea.Cmd
		m.plist, cmd = m.plist.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "q", "esc", "p":
		m.mode = modeThreads
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		if it, ok := m.plist.SelectedItem().(projectItem); ok {
			for i, p := range m.projects {
				if p == it.p {
					m.err = m.switchProject(i)
				}
			}
		}
		m.mode = modeThreads
		return m, nil
	}
	var cmd tea.Cmd
	m.plist, cmd = m.plist.Update(msg)
	return m, cmd
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

		p, err := m.store.Project(m.project().ID) // creates the directory if needed
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
		for i, it := range m.list.Items() {
			if it.(threadItem).t.ID == t.ID {
				m.list.Select(i)
			}
		}
		if m.addAndEdit {
			return m, m.edit(t)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// Layout: a one-line header, the body, and a one-line footer.  When wide
// enough the body splits into the list and a detail pane.

const minDetailWidth = 100

func (m *Model) layout() {
	bodyH := max(m.height-2, 1)
	listW := m.width
	if m.width >= minDetailWidth {
		listW = m.width / 2
	}
	m.list.SetSize(listW, bodyH)
	m.plist.SetSize(m.width, bodyH)
	m.input.Width = max(m.width-len(m.input.Prompt)-2, 10)
}

var (
	styleHeader   = lipgloss.NewStyle().Bold(true)
	styleDim      = lipgloss.NewStyle().Faint(true)
	styleSelected = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	styleClosed   = lipgloss.NewStyle().Faint(true).Strikethrough(true)
	styleError    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleDetailHd = lipgloss.NewStyle().Faint(true)
)

// View implements tea.Model.
func (m *Model) View() string {
	if m.width == 0 {
		return ""
	}
	switch m.mode {
	case modeHelp:
		return m.viewHelp()
	case modeProjects:
		return m.frame("projects", m.plist.View(), "enter: select  /: filter  esc: back")
	}

	header := "no projects"
	if p := m.project(); p != nil {
		header = p.DisplayName()
		if p.Name != "" {
			header += styleDim.Render("  " + p.ID)
		}
		header += styleDim.Render(fmt.Sprintf("  %d open", m.openCurrent()))
	}
	if m.showClosed {
		header += styleDim.Render("  (showing closed)")
	}

	body := m.list.View()
	if m.width >= minDetailWidth {
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(m.width/2).Render(body),
			m.viewDetail(m.width-m.width/2-1))
	}

	footer := "a: add  A: add+edit  e: edit  d/D: done  x/X: abandon  c: closed  p/[/]: project  /: filter  ?: help  q: quit"
	switch m.mode {
	case modeAdd:
		footer = m.input.View() + styleDim.Render("   (enter: save, esc: cancel)")
	case modeNote:
		footer = m.input.View() + styleDim.Render("   (enter: apply, esc: cancel)")
	}
	return m.frame(header, body, footer)
}

func (m *Model) frame(header, body, footer string) string {
	if m.err != nil {
		footer = styleError.Render(m.err.Error())
	} else if m.status != "" && m.mode != modeAdd && m.mode != modeNote {
		footer = m.status + styleDim.Render("   "+footer)
	}
	bodyH := max(m.height-2, 1)
	body = lipgloss.NewStyle().Height(bodyH).MaxHeight(bodyH).Render(body)
	return styleHeader.Render(truncate(header, m.width)) + "\n" + body + "\n" + truncate(footer, m.width)
}

func (m *Model) viewDetail(width int) string {
	t := m.selected()
	if t == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", styleDetailHd.Render("id:     "), t.ID)
	fmt.Fprintf(&b, "%s %s\n", styleDetailHd.Render("state:  "), t.State)
	fmt.Fprintf(&b, "%s %s\n", styleDetailHd.Render("created:"), t.Created.Format("2006-01-02 15:04"))
	if t.Closed != nil {
		fmt.Fprintf(&b, "%s %s\n", styleDetailHd.Render("closed: "), t.Closed.Format("2006-01-02 15:04"))
	}
	if t.Origin != "" {
		fmt.Fprintf(&b, "%s %s\n", styleDetailHd.Render("origin: "), t.Origin)
	}
	if t.Session != "" {
		fmt.Fprintf(&b, "%s %s\n", styleDetailHd.Render("session:"), t.Session)
	}
	b.WriteString("\n")
	b.WriteString(t.Body)
	return lipgloss.NewStyle().Width(width).PaddingLeft(1).Render(b.String())
}

func (m *Model) viewHelp() string {
	help := `Loose Threads

  j/k, up/down   move
  g/G            first/last
  /              filter by title
  enter, e       open thread in $EDITOR
  a              add a thread (title only)
  A              add a thread and open it in $EDITOR
  d              mark done (again: reopen)
  x              mark abandoned (again: reopen)
  D, X           the same, with a one-line note appended saying why
  c              show/hide done and abandoned threads
  p              pick a project
  [ / ]          previous / next project
  r, ctrl+r      hard refresh: reload and hide closed threads

The list refreshes itself as the store changes.  A thread closed by
someone else stays on screen, struck through, until a hard refresh.
  ?              this help
  q              quit

Press any key to return.`
	return lipgloss.NewStyle().Height(m.height).MaxHeight(m.height).Render(help)
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
	t := it.t
	glyph := " "
	switch t.State {
	case thread.Done:
		glyph = "✓"
	case thread.Abandoned:
		glyph = "✗"
	}
	cursor := "  "
	if index == l.Index() {
		cursor = "> "
	}
	title := t.Title()
	line := fmt.Sprintf("%s%s %s", cursor, glyph, title)
	if !t.IsOpen() {
		line = styleClosed.Render(line)
	} else if index == l.Index() {
		line = styleSelected.Render(line)
	}
	suffix := styleDim.Render("  " + t.ID[len(t.ID)-4:])
	fmt.Fprint(w, truncate(line+suffix, l.Width()))
}

type projectItem struct {
	p    *store.Project
	open int
}

func (i projectItem) FilterValue() string { return i.p.DisplayName() }

type projectDelegate struct{ m *Model }

func (projectDelegate) Height() int                         { return 1 }
func (projectDelegate) Spacing() int                        { return 0 }
func (projectDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (projectDelegate) Render(w io.Writer, l list.Model, index int, item list.Item) {
	it, ok := item.(projectItem)
	if !ok {
		return
	}
	cursor := "  "
	if index == l.Index() {
		cursor = "> "
	}
	line := fmt.Sprintf("%s%-4d %s", cursor, it.open, it.p.DisplayName())
	if it.p.Name != "" {
		line += styleDim.Render("  " + it.p.ID)
	}
	if index == l.Index() {
		line = styleSelected.Render(line)
	}
	fmt.Fprint(w, truncate(line, l.Width()))
}

// Run starts the program on the terminal.
func Run(m *Model) error {
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
