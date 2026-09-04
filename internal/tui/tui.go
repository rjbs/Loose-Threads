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

	theme Theme
	now   func() time.Time
}

// SetTheme changes the theme.  Call before Run.
func (m *Model) SetTheme(t Theme) { m.theme = t }

// New builds a Model over s, starting on the project with id start.  If
// that project has no directory in the store yet it is shown anyway, so
// that browsing from a fresh checkout works; its directory is created on
// the first add.  An empty start selects the first project.
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
		// Match by directory rather than id: a directory whose project.yaml
		// carries the wrong id is still the directory lookups for keep
		// would use, and must not be listed twice.
		dir := m.store.ProjectDir(keep)
		found := false
		for i, p := range ps {
			if p.Dir == dir {
				m.cur, found = i, true
				break
			}
		}
		if !found {
			ps = append(ps, &store.Project{ID: keep, Dir: dir})
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
	m.layout()
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
			ths, _, _ = m.store.Threads(p)
		}
		n := 0
		for _, t := range ths {
			if t.IsOpen() {
				n++
			}
		}
		items = append(items, projectItem{p, ths, n})
	}
	m.plist.SetItems(items)
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
	if m.plist.SettingFilter() {
		var cmd tea.Cmd
		m.plist, cmd = m.plist.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc", "p":
		m.mode = modeThreads
		return m, nil
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
	if p.Mismatched() {
		ws = append(ws, "project.yaml id does not match directory")
	}
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

func (m *Model) bodyHeight() int { return max(m.height-2, 1) }

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
	left := listView(m.list)
	if th.Panes {
		left = titledBox("threads", left, listW, listH, th.PaneBorder, th.FocusStyle, th.PaneTitle, th.PanePadding)
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
			detail = titledBox("detail", detail, detailW, bodyH, th.PaneBorder, th.PaneStyle, th.PaneTitle, th.PanePadding)
		} else {
			detail = m.viewDetail(detailW)
		}
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(listW).Render(left), " ", detail)
	}

	footer := m.footerKeys("a: add", "A: add+edit", "e: edit", "d/D: done", "x/X: abandon",
		"c: closed", "p/[/]: project", "/: filter", "?: help", "q: quit")
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

// listView renders a list without the blank line the component leaves
// where its (hidden) title bar would be, unless a filter is being typed
// there.
func listView(l list.Model) string {
	v := l.View()
	if l.SettingFilter() || l.FilterState() == list.FilterApplied {
		return v
	}
	// The blank title bar is a line of spaces padded to the width, not an
	// empty line.
	if first, rest, ok := strings.Cut(v, "\n"); ok && strings.TrimSpace(first) == "" {
		return rest
	}
	return v
}

// viewPicker is the two-pane project picker: projects on the left, the
// highlighted project's open threads on the right.
func (m *Model) viewPicker() string {
	th := m.theme
	listW, bodyH := m.listWidth(), m.bodyHeight()

	left := listView(m.plist)
	if th.Panes {
		left = titledBox("projects", left, listW, bodyH, th.PaneBorder, th.FocusStyle, th.PaneTitle, th.PanePadding)
	}
	body := left
	if m.width >= minDetailWidth {
		detailW := m.width - listW - 1
		var right string
		if th.Panes {
			right = m.viewPickerPreview(detailW - 2 - th.PanePadding)
			right = titledBox("threads", right, detailW, bodyH, th.PaneBorder, th.PaneStyle, th.PaneTitle, th.PanePadding)
		} else {
			right = m.viewPickerPreview(detailW)
		}
		body = lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(listW).Render(left), " ", right)
	}

	header := th.Header.Render(headerGlyph+"projects") + th.HeaderMeta.Render(fmt.Sprintf("  %d", len(m.projects)))
	footer := m.footerKeys("enter: select", "/: filter", "esc: back", "q: quit")
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
		line := th.Normal.Render(t.Title()) + th.Meta.Render("  "+t.ID[len(t.ID)-4:])
		b.WriteString(truncate(line, width) + "\n")
	}
	pad := 1
	if th.Panes {
		pad = 0
	}
	return lipgloss.NewStyle().Width(width).PaddingLeft(pad).Render(b.String())
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
	}
	bodyH := m.bodyHeight()
	body = lipgloss.NewStyle().Height(bodyH).MaxHeight(bodyH).Render(body)
	return truncate(header, m.width) + "\n" + body + "\n" + truncate(footer, m.width)
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
	meta := "  " + t.ID[len(t.ID)-4:]
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
