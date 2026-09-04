package tui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme collects everything about the browser's appearance that is a
// matter of taste: colors, the selection treatment, and whether the panes
// are framed.  Layout logic reads only the few structural fields (Panes,
// FullRow); everything else is a style applied at render time.
type Theme struct {
	Name string

	// Header line: project name and its metadata.
	Header     lipgloss.Style
	HeaderMeta lipgloss.Style

	// Thread rows.
	Cursor        string // drawn before the selected row; a same-width blank precedes others
	CursorStyle   lipgloss.Style
	Selected      lipgloss.Style // the selected row's title
	SelectedMeta  lipgloss.Style // the selected row's id
	Normal        lipgloss.Style
	Meta          lipgloss.Style // ids, dates, and other secondary text
	Closed        lipgloss.Style // title of a done or abandoned thread
	DoneGlyph     string
	AbandonGlyph  string
	DoneStyle     lipgloss.Style
	AbandonStyle  lipgloss.Style
	FullRow       bool           // paint the selected row's background across the pane
	RowBackground lipgloss.Style // used when FullRow is set

	// Detail pane.
	DetailLabel lipgloss.Style
	DetailTitle lipgloss.Style
	DetailBody  lipgloss.Style

	// Footer: key hints, status, errors.
	Footer    lipgloss.Style
	FooterKey lipgloss.Style
	Status    lipgloss.Style
	Error     lipgloss.Style
	Prompt    lipgloss.Style

	// Warning box.
	WarningBox lipgloss.Style

	// Panes: frame the list and detail with borders and titles.
	Panes       bool
	PaneBorder  lipgloss.Border
	PaneStyle   lipgloss.Style // border of an unfocused pane
	FocusStyle  lipgloss.Style // border of the focused (list) pane
	PaneTitle   lipgloss.Style
	PanePadding int
}

var themes = map[string]Theme{}

func registerTheme(t Theme) { themes[t.Name] = t }

// DefaultTheme is used when none is named.
const DefaultTheme = "stark"

// LookupTheme returns the named theme, or false.
func LookupTheme(name string) (Theme, bool) {
	t, ok := themes[name]
	return t, ok
}

// ThemeNames lists the themes, sorted.
func ThemeNames() []string {
	var names []string
	for n := range themes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ThemeList is ThemeNames joined for help text.
func ThemeList() string { return strings.Join(ThemeNames(), ", ") }

func c(s string) lipgloss.Color { return lipgloss.Color(s) }

func init() {
	plain := lipgloss.NewStyle()
	faint := lipgloss.NewStyle().Faint(true)

	// stark: the original.  Bold and faint only, one accent for the
	// selection, no frames.
	registerTheme(Theme{
		Name:         "stark",
		Header:       lipgloss.NewStyle().Bold(true),
		HeaderMeta:   faint,
		Cursor:       "> ",
		CursorStyle:  lipgloss.NewStyle().Bold(true).Foreground(c("6")),
		Selected:     lipgloss.NewStyle().Bold(true).Foreground(c("6")),
		SelectedMeta: faint,
		Normal:       plain,
		Meta:         faint,
		Closed:       lipgloss.NewStyle().Faint(true).Strikethrough(true),
		DoneGlyph:    "✓",
		AbandonGlyph: "✗",
		DoneStyle:    faint,
		AbandonStyle: faint,
		DetailLabel:  faint,
		DetailTitle:  plain,
		DetailBody:   plain,
		Footer:       plain,
		FooterKey:    plain,
		Status:       plain,
		Error:        lipgloss.NewStyle().Foreground(c("1")),
		Prompt:       plain,
		WarningBox: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(c("9")).Foreground(c("15")).Bold(true).PaddingLeft(1),
	})

	// soft: the same open layout, but the selected row is a full-width
	// tinted bar, metadata is grey rather than merely faint, states get a
	// color each, and the footer's keys are picked out.  One accent hue.
	accent := c("39") // a mid blue
	grey := c("245")
	registerTheme(Theme{
		Name:          "soft",
		Header:        lipgloss.NewStyle().Bold(true).Foreground(c("252")),
		HeaderMeta:    lipgloss.NewStyle().Foreground(grey),
		Cursor:        "  ",
		CursorStyle:   plain,
		Selected:      lipgloss.NewStyle().Bold(true).Foreground(c("255")),
		SelectedMeta:  lipgloss.NewStyle().Foreground(c("250")),
		Normal:        lipgloss.NewStyle().Foreground(c("252")),
		Meta:          lipgloss.NewStyle().Foreground(grey),
		Closed:        lipgloss.NewStyle().Foreground(c("243")).Strikethrough(true),
		DoneGlyph:     "✓",
		AbandonGlyph:  "✗",
		DoneStyle:     lipgloss.NewStyle().Foreground(c("71")),  // green
		AbandonStyle:  lipgloss.NewStyle().Foreground(c("167")), // muted red
		FullRow:       true,
		RowBackground: lipgloss.NewStyle().Background(c("237")),
		DetailLabel:   lipgloss.NewStyle().Foreground(grey),
		DetailTitle:   lipgloss.NewStyle().Bold(true).Foreground(c("255")),
		DetailBody:    lipgloss.NewStyle().Foreground(c("252")),
		Footer:        lipgloss.NewStyle().Foreground(grey),
		FooterKey:     lipgloss.NewStyle().Foreground(accent),
		Status:        lipgloss.NewStyle().Foreground(accent),
		Error:         lipgloss.NewStyle().Foreground(c("167")),
		Prompt:        lipgloss.NewStyle().Foreground(accent),
		WarningBox: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(c("167")).Foreground(c("255")).Bold(true).PaddingLeft(1),
	})

	// charm: how Charm's own apps look.  Rounded frames around both
	// panes, a pink gutter bar marking the selection, purple titles,
	// generous padding.
	pink := c("212")
	purple := c("99")
	registerTheme(Theme{
		Name:         "charm",
		Header:       lipgloss.NewStyle().Bold(true).Foreground(purple),
		HeaderMeta:   lipgloss.NewStyle().Foreground(c("241")),
		Cursor:       "┃ ",
		CursorStyle:  lipgloss.NewStyle().Foreground(pink),
		Selected:     lipgloss.NewStyle().Foreground(pink),
		SelectedMeta: lipgloss.NewStyle().Foreground(c("176")),
		Normal:       lipgloss.NewStyle().Foreground(c("252")),
		Meta:         lipgloss.NewStyle().Foreground(c("241")),
		Closed:       lipgloss.NewStyle().Foreground(c("241")).Strikethrough(true),
		DoneGlyph:    "✓",
		AbandonGlyph: "✗",
		DoneStyle:    lipgloss.NewStyle().Foreground(c("78")),
		AbandonStyle: lipgloss.NewStyle().Foreground(c("203")),
		DetailLabel:  lipgloss.NewStyle().Foreground(c("241")),
		DetailTitle:  lipgloss.NewStyle().Bold(true).Foreground(purple),
		DetailBody:   lipgloss.NewStyle().Foreground(c("252")),
		Footer:       lipgloss.NewStyle().Foreground(c("241")),
		FooterKey:    lipgloss.NewStyle().Foreground(pink),
		Status:       lipgloss.NewStyle().Foreground(purple),
		Error:        lipgloss.NewStyle().Foreground(c("203")),
		Prompt:       lipgloss.NewStyle().Foreground(pink),
		WarningBox: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(c("203")).Foreground(c("255")).Bold(true).PaddingLeft(1),
		Panes:       true,
		PaneBorder:  lipgloss.RoundedBorder(),
		PaneStyle:   lipgloss.NewStyle().Foreground(c("240")),
		FocusStyle:  lipgloss.NewStyle().Foreground(purple),
		PaneTitle:   lipgloss.NewStyle().Bold(true).Foreground(purple),
		PanePadding: 1,
	})

	// lazy: after lazygit.  Thin square frames with titles in the border,
	// a green border on the focused pane, a blue tinted bar for the
	// selection, and nearly everything else in the terminal's default
	// foreground.
	green := c("2")
	registerTheme(Theme{
		Name:          "lazy",
		Header:        lipgloss.NewStyle().Bold(true),
		HeaderMeta:    faint,
		Cursor:        "",
		CursorStyle:   plain,
		Selected:      lipgloss.NewStyle().Bold(true),
		SelectedMeta:  lipgloss.NewStyle().Faint(true),
		Normal:        plain,
		Meta:          faint,
		Closed:        lipgloss.NewStyle().Faint(true).Strikethrough(true),
		DoneGlyph:     "✓",
		AbandonGlyph:  "✗",
		DoneStyle:     lipgloss.NewStyle().Foreground(green),
		AbandonStyle:  lipgloss.NewStyle().Foreground(c("1")),
		FullRow:       true,
		RowBackground: lipgloss.NewStyle().Background(c("17")),
		DetailLabel:   faint,
		DetailTitle:   lipgloss.NewStyle().Bold(true),
		DetailBody:    plain,
		Footer:        faint,
		FooterKey:     lipgloss.NewStyle().Foreground(green),
		Status:        lipgloss.NewStyle().Foreground(green),
		Error:         lipgloss.NewStyle().Foreground(c("1")),
		Prompt:        lipgloss.NewStyle().Foreground(green),
		WarningBox: lipgloss.NewStyle().Border(lipgloss.NormalBorder()).
			BorderForeground(c("1")).Foreground(c("15")).Bold(true).PaddingLeft(1),
		Panes:      true,
		PaneBorder: lipgloss.NormalBorder(),
		PaneStyle:  faint,
		FocusStyle: lipgloss.NewStyle().Foreground(green),
		PaneTitle:  lipgloss.NewStyle().Bold(true),
	})
}

// titledBox frames content in a border of the given outer width and
// height, with title embedded in the top edge, lazygit style.
func titledBox(title, content string, width, height int, border lipgloss.Border, edge, titleStyle lipgloss.Style, padding int) string {
	innerW := max(width-2, 1)
	innerH := max(height-2, 1)

	var top string
	if title != "" {
		t := " " + title + " "
		if lipgloss.Width(t) > innerW-2 {
			t = truncate(t, innerW-2)
		}
		top = edge.Render(border.TopLeft+border.Top) + titleStyle.Render(t) +
			edge.Render(strings.Repeat(border.Top, max(innerW-1-lipgloss.Width(t), 0))+border.TopRight)
	} else {
		top = edge.Render(border.TopLeft + strings.Repeat(border.Top, innerW) + border.TopRight)
	}
	bottom := edge.Render(border.BottomLeft + strings.Repeat(border.Bottom, innerW) + border.BottomRight)

	body := lipgloss.NewStyle().Width(innerW).Height(innerH).MaxHeight(innerH).PaddingLeft(padding).Render(content)
	var b strings.Builder
	b.WriteString(top)
	for _, line := range strings.Split(body, "\n") {
		b.WriteString("\n" + edge.Render(border.Left) + lipgloss.NewStyle().Width(innerW).MaxWidth(innerW).Render(line) + edge.Render(border.Right))
	}
	b.WriteString("\n" + bottom)
	return b.String()
}
