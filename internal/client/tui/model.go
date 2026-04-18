package tui

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"wert/internal/client"
	gh "wert/internal/github"
	"wert/internal/protocol"
)

// ── Screens ───────────────────────────────────────────────────────────────────

type screenType int

const (
	scrHome        screenType = 0
	scrChat        screenType = 1
	scrTasks       screenType = 2
	scrGitHub      screenType = 3
	scrWorkstation screenType = 4
	scrMembers     screenType = 5
)

var screenNames = []string{"Home", "Chat", "Tasks", "GitHub", "Workstation", "Members"}

// ── Pane layout ───────────────────────────────────────────────────────────────

type paneNode struct {
	split  byte      // 0=leaf, 'v'=top/bottom, 'h'=left/right
	screen screenType
	a, b   *paneNode // a=top/left  b=bottom/right
}

// ── Colour palette ────────────────────────────────────────────────────────────

var (
	cRed      = lipgloss.Color("#E53935")
	cGreen    = lipgloss.Color("#43A047")
	cRedDark  = lipgloss.Color("#B71C1C")
	cRedDim   = lipgloss.Color("#EF9A9A")
	cGreenDim = lipgloss.Color("#A5D6A7")
	cMuted    = lipgloss.Color("#757575")
	cFg       = lipgloss.Color("#F5F5F5")
	cBorder   = lipgloss.Color("#424242")
	cActive   = lipgloss.Color("#616161")
	cYellow   = lipgloss.Color("#FDD835")
)

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	headerSt = lipgloss.NewStyle().
			Background(cRedDark).Foreground(cFg).Bold(true).Padding(0, 2)

	activeTabSt = lipgloss.NewStyle().
			Background(cRed).Foreground(cFg).Bold(true).Padding(0, 1)

	inactiveTabSt = lipgloss.NewStyle().
			Foreground(cMuted).Padding(0, 1)

	unreadBadgeSt = lipgloss.NewStyle().
			Background(cRed).Foreground(cFg).Bold(true).Padding(0, 1)

	screenBoxSt = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).BorderForeground(cBorder)

	inputBoxSt = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).BorderForeground(cActive).Padding(0, 1)

	sectionTitleSt = lipgloss.NewStyle().
			Foreground(cRed).Bold(true)

	subTabActiveSt = lipgloss.NewStyle().
			Foreground(cGreen).Bold(true).Underline(true).Padding(0, 1)

	subTabInactiveSt = lipgloss.NewStyle().
				Foreground(cMuted).Padding(0, 1)

	statusBarSt = lipgloss.NewStyle().Foreground(cMuted).Italic(true)

	todoSt    = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	wipSt     = lipgloss.NewStyle().Foreground(cGreen).Bold(true)
	doneSt    = lipgloss.NewStyle().Foreground(cGreenDim).Bold(true)
	blockedSt = lipgloss.NewStyle().Foreground(cRedDim).Bold(true)

	adminNameSt  = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	selfNameSt   = lipgloss.NewStyle().Foreground(cGreen).Bold(true)
	memberNameSt = lipgloss.NewStyle().Foreground(cRedDim).Bold(true)
	timeSt       = lipgloss.NewStyle().Foreground(cMuted)
	msgTextSt    = lipgloss.NewStyle().Foreground(cFg)
	mentionSt    = lipgloss.NewStyle().Foreground(cYellow).Bold(true)
	mutedSt      = lipgloss.NewStyle().Foreground(cMuted)
	boldFgSt     = lipgloss.NewStyle().Foreground(cFg).Bold(true)

	highPriSt = lipgloss.NewStyle().Foreground(cRed)
	medPriSt  = lipgloss.NewStyle().Foreground(cGreen)
	lowPriSt  = lipgloss.NewStyle().Foreground(cMuted)

	labelSt = lipgloss.NewStyle().
		Background(lipgloss.Color("#333333")).
		Foreground(cMuted).
		Padding(0, 1)
)

// ── Tea message types ─────────────────────────────────────────────────────────

type ServerMsg struct{ Env protocol.Envelope }
type DisconnectedMsg struct{ Err error }
type reconnectMsg struct {
	cl  *client.Client
	err error
}
type githubDataMsg struct {
	data *gh.OrgData
	err  error
}

// ── Task filter tabs ──────────────────────────────────────────────────────────

var taskFilters = []string{"all", "todo", "in_progress", "done", "blocked"}
var taskFilterLabels = []string{"All", "Todo", "In Progress", "Done", "Blocked"}

// ── GitHub sub-tabs ───────────────────────────────────────────────────────────

var ghTabs = []string{"overview", "prs", "issues"}
var ghTabLabels = []string{"Overview", "Pull Requests", "Issues"}

// ── Model ─────────────────────────────────────────────────────────────────────

type Model struct {
	cl         *client.Client
	username   string
	role       string
	serverAddr string
	adminToken string

	width  int
	height int

	// navigation
	screen     screenType
	prevScreen screenType

	// data
	tasks    []*protocol.Task
	messages []*protocol.ChatMessage
	members  []*protocol.Member

	// chat state
	unreadChat int // increments when not on scrChat

	// tasks state
	taskFilter int // index into taskFilters

	// github state
	ghClient  *gh.Client
	ghData    *gh.OrgData
	ghLoading bool
	ghErr     string
	ghTab     int // index into ghTabs

	// viewports (one per screen)
	homeVP    viewport.Model
	chatVP    viewport.Model
	tasksVP   viewport.Model
	githubVP  viewport.Model
	membersVP viewport.Model

	input textinput.Model

	statusMsg   string
	connected   bool
	initialized bool

	// reconnection
	reconnecting      bool
	reconnectAttempts int

	// cmdline (activated by shift+; on any screen)
	cmdlineActive bool
	cmdlineValue  string // typed text inside the cmdline box (independent of m.input)

	// workstation screen
	wsPane  *paneNode       // split layout within workstation (nil = default tasks|chat)
	wsInput textinput.Model // message input inside workstation
}

func New(
	cl *client.Client,
	username, role, serverAddr, adminToken string,
	ghClient *gh.Client,
) *Model {
	ti := textinput.New()
	ti.Focus()

	wsIn := textinput.New()
	wsIn.Focus()

	return &Model{
		cl:         cl,
		username:   username,
		role:       role,
		serverAddr: serverAddr,
		adminToken: adminToken,
		screen:     scrHome,
		input:      ti,
		wsInput:    wsIn,
		tasks:      []*protocol.Task{},
		messages:   []*protocol.ChatMessage{},
		members:    []*protocol.Member{},
		connected:  true,
		ghClient:   ghClient,
	}
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink, waitForMsg(m.cl.Recv)}
	if m.ghClient != nil && m.ghClient.IsConfigured() {
		cmds = append(cmds, fetchGitHub(m.ghClient))
	}
	return tea.Batch(cmds...)
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.rebuildViewports()
		m.refreshContent()
		return m, nil

	case ServerMsg:
		m = m.applyEnvelope(msg.Env)
		m.refreshContent()
		return m, waitForMsg(m.cl.Recv)

	case DisconnectedMsg:
		m.connected = false
		m.reconnecting = true
		m.reconnectAttempts = 0
		m.statusMsg = "connection lost, reconnecting..."
		return m, tryReconnect(m.cl.Host(), m.username, m.adminToken)

	case reconnectMsg:
		if msg.err == nil {
			m.cl = msg.cl
			m.connected = true
			m.reconnecting = false
			m.reconnectAttempts = 0
			m.statusMsg = "reconnected"
			return m, waitForMsg(m.cl.Recv)
		}
		m.reconnectAttempts++
		if m.reconnectAttempts >= 15 {
			m.reconnecting = false
			m.statusMsg = "could not reconnect after 15 attempts"
			return m, nil
		}
		m.statusMsg = fmt.Sprintf("reconnecting... (%d/15)", m.reconnectAttempts)
		return m, tryReconnect(m.cl.Host(), m.username, m.adminToken)

	case githubDataMsg:
		m.ghLoading = false
		if msg.err != nil {
			m.ghErr = msg.err.Error()
		} else {
			m.ghData = msg.data
			m.ghErr = ""
		}
		m.refreshContent()
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+q":
			return m, tea.Quit

		case "esc":
			if m.cmdlineActive {
				m.cmdlineActive = false
				m.cmdlineValue = ""
				return m, nil
			}
			// go back to home from any other screen
			if m.screen != scrHome {
				m.prevScreen = m.screen
				m.screen = scrHome
				m.input.SetValue("")
				m.refreshContent()
			}
			return m, nil

		case ":":
			if m.cmdlineActive {
				m.cmdlineActive = false
				m.cmdlineValue = ""
			} else {
				m.cmdlineActive = true
				m.cmdlineValue = ""
			}
			return m, nil

		// sub-tab navigation — only on task/github screens
		case "[":
			if m.screen == scrTasks {
				if m.taskFilter > 0 {
					m.taskFilter--
				} else {
					m.taskFilter = len(taskFilters) - 1
				}
				m.refreshContent()
				return m, nil
			}
			if m.screen == scrGitHub {
				if m.ghTab > 0 {
					m.ghTab--
				} else {
					m.ghTab = len(ghTabs) - 1
				}
				m.refreshContent()
				return m, nil
			}

		case "]":
			if m.screen == scrTasks {
				m.taskFilter = (m.taskFilter + 1) % len(taskFilters)
				m.refreshContent()
				return m, nil
			}
			if m.screen == scrGitHub {
				m.ghTab = (m.ghTab + 1) % len(ghTabs)
				m.refreshContent()
				return m, nil
			}

		case "up", "down":
			var cmd tea.Cmd
			switch m.screen {
			case scrChat:
				m.chatVP, cmd = m.chatVP.Update(msg)
			case scrTasks:
				m.tasksVP, cmd = m.tasksVP.Update(msg)
			case scrGitHub:
				m.githubVP, cmd = m.githubVP.Update(msg)
			case scrMembers:
				m.membersVP, cmd = m.membersVP.Update(msg)
			}
			return m, cmd

		case "pgup", "pgdown":
			var cmd tea.Cmd
			switch m.screen {
			case scrChat:
				m.chatVP, cmd = m.chatVP.Update(msg)
			case scrTasks:
				m.tasksVP, cmd = m.tasksVP.Update(msg)
			case scrGitHub:
				m.githubVP, cmd = m.githubVP.Update(msg)
			case scrMembers:
				m.membersVP, cmd = m.membersVP.Update(msg)
			}
			return m, cmd

		case "enter":
			if m.cmdlineActive {
				text := strings.TrimSpace(m.cmdlineValue)
				m.cmdlineActive = false
				m.cmdlineValue = ""
				if text != "" {
					return m, m.handleHomeCmd(text)
				}
				return m, nil
			}
			if m.screen == scrWorkstation {
				text := strings.TrimSpace(m.wsInput.Value())
				m.wsInput.SetValue("")
				if text != "" {
					m.cl.SendChat(text)
				}
				return m, nil
			}
			text := strings.TrimSpace(m.input.Value())
			m.input.SetValue("")
			if text == "" {
				return m, nil
			}
			return m, m.handleText(text)

		default:
			// q on non-home screens goes back home (not when cmdline active)
			if m.screen != scrHome && msg.String() == "q" && m.input.Value() == "" && !m.cmdlineActive {
				m.screen = scrHome
				m.input.SetValue("")
				m.refreshContent()
				return m, nil
			}
			// cmdline is active: route keys into cmdlineValue, never touch m.input
			if m.cmdlineActive {
				switch msg.String() {
				case "backspace", "ctrl+h":
					runes := []rune(m.cmdlineValue)
					if len(runes) > 0 {
						m.cmdlineValue = string(runes[:len(runes)-1])
					}
				default:
					if s := msg.String(); len([]rune(s)) == 1 {
						m.cmdlineValue += s
					}
				}
				return m, nil
			}
			// on home screen without cmdline, ignore all keys
			if m.screen == scrHome {
				return m, nil
			}
			if m.screen == scrWorkstation {
				var inputCmd tea.Cmd
				m.wsInput, inputCmd = m.wsInput.Update(msg)
				return m, inputCmd
			}
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			return m, inputCmd
		}
	}

	return m, nil
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m Model) View() string {
	if m.width == 0 {
		return "loading..."
	}
	base := m.viewBase()
	if m.cmdlineActive {
		return m.overlayCmd(base)
	}
	return base
}

// viewBase renders the full screen without any cmdline overlay.
func (m Model) viewBase() string {
	header := m.viewHeader()
	status := m.viewStatus()
	if m.screen == scrWorkstation {
		return lipgloss.JoinVertical(lipgloss.Left, header, m.viewWorkstation(), status)
	}
	screenContent := m.viewScreen()
	if m.screen == scrHome {
		return lipgloss.JoinVertical(lipgloss.Left, header, screenContent, status)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, screenContent, status, m.viewInput())
}

// buildCmdlineBox returns the rendered box string and its visual width.
func (m Model) buildCmdlineBox() (string, int) {
	const (
		padW   = 2  // padding chars each side inside border
		inputW = 48 // visible columns for typed text
		// inner width between border chars: padW + "❯" + " " + inputW + padW
		innerW = padW + 1 + 1 + inputW + padW // = 54
		boxW   = innerW + 2                    // = 56 total visual width
	)

	borderSt := lipgloss.NewStyle().Foreground(cRed)
	promptSt  := lipgloss.NewStyle().Foreground(cRed).Bold(true)
	titleSt   := lipgloss.NewStyle().Foreground(cRedDim)
	cursorSt  := lipgloss.NewStyle().Reverse(true)

	// Top border with "Cmdline" centred
	title     := " Cmdline "
	dashTotal := innerW - len(title) // all ASCII
	leftDash  := dashTotal / 2
	rightDash := dashTotal - leftDash
	topLine := borderSt.Render("╭"+strings.Repeat("─", leftDash)) +
		titleSt.Render(title) +
		borderSt.Render(strings.Repeat("─", rightDash)+"╮")

	// Content: use cmdlineValue + block cursor — avoids ANSI cursor-positioning escape bug
	valRunes := []rune(m.cmdlineValue)
	if len(valRunes) > inputW-1 {
		valRunes = valRunes[len(valRunes)-(inputW-1):]
	}
	cursor  := cursorSt.Render(" ")
	trailing := strings.Repeat(" ", inputW-len(valRunes)-1)

	contentLine := borderSt.Render("│") +
		strings.Repeat(" ", padW) +
		promptSt.Render("❯") + " " +
		string(valRunes) + cursor + trailing +
		strings.Repeat(" ", padW) +
		borderSt.Render("│")

	bottomLine := borderSt.Render("╰" + strings.Repeat("─", innerW) + "╯")

	return topLine + "\n" + contentLine + "\n" + bottomLine, boxW
}

// stripANSI removes ANSI escape sequences, returning plain visual text.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEsc = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// overlayCmd paints the cmdline box centred over the base view,
// preserving the original content visible on either side of the box.
func (m Model) overlayCmd(base string) string {
	box, boxW := m.buildCmdlineBox()
	boxLines := strings.Split(box, "\n")
	boxH := len(boxLines)

	baseLines := strings.Split(base, "\n")
	totalRows := len(baseLines)

	startRow := (totalRows - boxH) / 2
	startCol := (m.width - boxW) / 2
	if startCol < 0 {
		startCol = 0
	}

	result := make([]string, totalRows)
	copy(result, baseLines)

	for i, boxLine := range boxLines {
		row := startRow + i
		if row < 0 || row >= totalRows {
			continue
		}
		// Strip ANSI from the base row to get plain visual runes for left/right context
		visual := []rune(stripANSI(result[row]))
		for len(visual) < m.width {
			visual = append(visual, ' ')
		}
		leftEnd := startCol
		if leftEnd > len(visual) {
			leftEnd = len(visual)
		}
		left := string(visual[:leftEnd])
		rightStart := startCol + boxW
		right := ""
		if rightStart < len(visual) {
			right = string(visual[rightStart:])
		}
		result[row] = left + boxLine + right
	}

	return strings.Join(result, "\n")
}

func (m Model) viewHeader() string {
	role := "member"
	if m.role == "admin" {
		role = "admin"
	}
	online := 0
	for _, mem := range m.members {
		if mem.Online {
			online++
		}
	}
	var left string
	if m.screen == scrHome {
		left = lipgloss.NewStyle().Foreground(cFg).Bold(true).Render("wert")
	} else {
		screenName := screenNames[int(m.screen)]
		left = lipgloss.NewStyle().Foreground(cFg).Bold(true).Render("wert") +
			mutedSt.Render("  /  ") +
			lipgloss.NewStyle().Foreground(cFg).Render(screenName)
	}
	if m.unreadChat > 0 && m.screen != scrChat {
		left += "  " + unreadBadgeSt.Render(fmt.Sprintf("%d", m.unreadChat))
	}
	right := fmt.Sprintf("%s  %s  %d online", m.username, role, online)
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 4
	if gap < 1 {
		gap = 1
	}
	return headerSt.Width(m.width).Render(left + strings.Repeat(" ", gap) + right)
}

func (m Model) viewNav() string {
	tabs := make([]string, len(screenNames))
	for i, name := range screenNames {
		label := fmt.Sprintf(" %d:%s ", i+1, name)
		if i == int(scrChat) && m.unreadChat > 0 {
			badge := unreadBadgeSt.Render(fmt.Sprintf("%d", m.unreadChat))
			if screenType(i) == m.screen {
				tabs[i] = activeTabSt.Render(fmt.Sprintf(" %d:%s ", i+1, name)) + badge
			} else {
				tabs[i] = inactiveTabSt.Render(fmt.Sprintf(" %d:%s ", i+1, name)) + badge
			}
			continue
		}
		if screenType(i) == m.screen {
			tabs[i] = activeTabSt.Render(label)
		} else {
			tabs[i] = inactiveTabSt.Render(label)
		}
	}
	return "  " + strings.Join(tabs, " ")
}

func (m Model) viewScreen() string {
	switch m.screen {
	case scrHome:
		return m.viewHome()
	case scrChat:
		return m.viewChat()
	case scrTasks:
		return m.viewTasks()
	case scrGitHub:
		return m.viewGitHub()
	case scrMembers:
		return m.viewMembersScreen()
	}
	return ""
}

// ── Home screen ───────────────────────────────────────────────────────────────

func (m Model) viewHome() string {
	return m.homeVP.View()
}


func (m Model) renderHome() string {
	w := m.width
	if w < 20 {
		w = 20
	}

	center := func(rendered string, visualW int) string {
		pad := (w - visualW) / 2
		if pad < 0 {
			pad = 0
		}
		return strings.Repeat(" ", pad) + rendered
	}

	logoLines := []string{
		`██╗    ██╗███████╗██████╗ ████████╗`,
		`██║    ██║██╔════╝██╔══██╗╚══██╔══╝`,
		`██║ █╗ ██║█████╗  ██████╔╝   ██║   `,
		`██║███╗██║██╔══╝  ██╔══██╗   ██║   `,
		`╚███╔███╔╝███████╗██║  ██║   ██║   `,
		` ╚══╝╚══╝ ╚══════╝╚═╝  ╚═╝   ╚═╝  `,
	}
	logoSt := lipgloss.NewStyle().Foreground(cRed).Bold(true)

	var open, done int
	for _, t := range m.tasks {
		if t.Status == protocol.StatusDone {
			done++
		} else {
			open++
		}
	}
	online := 0
	for _, mem := range m.members {
		if mem.Online {
			online++
		}
	}

	type menuItem struct{ label, key string }
	menu := []menuItem{
		{"Chat", "2"},
		{"Tasks", "3"},
		{"GitHub", "4"},
		{"Members", "5"},
		{"Help", "/help"},
		{"Quit", "ctrl+c"},
	}
	menuW := 32

	// logo(6) + blank(1) + stats(1) + blank(2) + menu(6) + blank(1) = 17
	contentLines := 6 + 1 + 1 + 2 + 6 + 1
	vpH := m.height - 2 // header(1) + status(1)
	topPad := (vpH - contentLines) / 2
	if topPad < 1 {
		topPad = 1
	}

	var sb strings.Builder
	for i := 0; i < topPad; i++ {
		sb.WriteString("\n")
	}

	for _, line := range logoLines {
		vw := lipgloss.Width(line)
		sb.WriteString(center(logoSt.Render(line), vw) + "\n")
	}
	sb.WriteString("\n")

	stats := fmt.Sprintf("%d open  %d done  %d online", open, done, online)
	sb.WriteString(center(mutedSt.Render(stats), len(stats)) + "\n")
	sb.WriteString("\n\n")

	for i, item := range menu {
		if i == 4 {
			sb.WriteString("\n")
		}
		gap := menuW - len(item.label) - len(item.key)
		if gap < 1 {
			gap = 1
		}
		row := lipgloss.NewStyle().Foreground(cFg).Render(item.label) +
			strings.Repeat(" ", gap) +
			mutedSt.Render(item.key)
		sb.WriteString(center(row, menuW) + "\n")
	}

	return sb.String()
}

// ── Chat screen ───────────────────────────────────────────────────────────────

func (m Model) viewChat() string {
	inner := m.chatVP.View()
	return screenBoxSt.Width(m.width - 2).Height(m.screenHeight()).Render(inner)
}

func (m Model) renderChat() string {
	var sb strings.Builder
	if len(m.messages) == 0 {
		sb.WriteString(mutedSt.Render("\n  no messages yet\n"))
		return sb.String()
	}
	for _, msg := range m.messages {
		if msg.Content == "" {
			sb.WriteString("\n")
			continue
		}
		ts := timeSt.Render(msg.Timestamp.Format("15:04"))
		var nameSt lipgloss.Style
		switch {
		case msg.From == m.username:
			nameSt = selfNameSt
		case msg.From == "wert":
			nameSt = mutedSt
		case msg.From == "mcp":
			nameSt = lipgloss.NewStyle().Foreground(cGreen).Bold(true)
		default:
			isAdmin := false
			for _, mem := range m.members {
				if mem.Username == msg.From && mem.Role == "admin" {
					isAdmin = true
					break
				}
			}
			if isAdmin {
				nameSt = adminNameSt
			} else {
				nameSt = memberNameSt
			}
		}
		content := renderMentions(msg.Content, m.username)
		line := fmt.Sprintf("  %s  %s  %s", ts, nameSt.Render(msg.From+":"), content)
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

// renderMentions highlights @username tokens in a message.
func renderMentions(content, self string) string {
	words := strings.Fields(content)
	out := make([]string, len(words))
	for i, w := range words {
		if strings.HasPrefix(w, "@") {
			name := strings.TrimPrefix(w, "@")
			name = strings.TrimRight(name, ".,!?")
			if strings.ToLower(name) == strings.ToLower(self) {
				out[i] = mentionSt.Render(w)
			} else {
				out[i] = lipgloss.NewStyle().Foreground(cRedDim).Render(w)
			}
		} else {
			out[i] = msgTextSt.Render(w)
		}
	}
	return strings.Join(out, " ")
}

// ── Tasks screen ──────────────────────────────────────────────────────────────

func (m Model) viewTasks() string {
	// filter sub-tabs
	tabs := make([]string, len(taskFilterLabels))
	for i, label := range taskFilterLabels {
		if i == m.taskFilter {
			tabs[i] = subTabActiveSt.Render(label)
		} else {
			tabs[i] = subTabInactiveSt.Render(label)
		}
	}
	tabBar := "  " + strings.Join(tabs, "  ") + mutedSt.Render("   [ ] to switch")
	content := m.tasksVP.View()
	inner := lipgloss.JoinVertical(lipgloss.Left, tabBar, content)
	return screenBoxSt.Width(m.width - 2).Height(m.screenHeight()).Render(inner)
}

func (m Model) renderTasks() string {
	var sb strings.Builder
	filter := taskFilters[m.taskFilter]
	tasks := m.filteredTasks(filter)
	if len(tasks) == 0 {
		label := taskFilterLabels[m.taskFilter]
		emptyMsg := "  no tasks yet"
		if filter != "all" {
			emptyMsg = "  no " + strings.ToLower(label) + " tasks"
		}
		sb.WriteString(mutedSt.Render("\n" + emptyMsg + "\n"))
		return sb.String()
	}

	maxW := m.width - 6
	if maxW < 20 {
		maxW = 20
	}

	for _, t := range tasks {
		badge, badgeSt := statusBadge(t.Status)
		id := shortID(t.ID)
		title := truncate(t.Title, maxW-40)

		due := ""
		if t.DueDate != "" {
			due = mutedSt.Render("  due:" + t.DueDate)
		}

		line1 := fmt.Sprintf("  %s  %s  %s%s",
			badgeSt.Render(badge),
			mutedSt.Render("#"+id),
			boldFgSt.Render(title),
			due,
		)
		sb.WriteString(line1 + "\n")

		if t.Description != "" {
			sb.WriteString("          " + mutedSt.Render(truncate(t.Description, maxW-10)) + "\n")
		}

		meta := fmt.Sprintf("          %s  assignee: %s  by: %s",
			priLabel(t.Priority),
			lipgloss.NewStyle().Foreground(cGreen).Render(t.Assignee),
			mutedSt.Render(t.UpdatedBy),
		)
		sb.WriteString(meta + "\n\n")
	}
	return sb.String()
}

func (m Model) filteredTasks(filter string) []*protocol.Task {
	var src []*protocol.Task
	if m.role == "admin" {
		src = m.tasks
	} else {
		for _, t := range m.tasks {
			if t.Assignee == m.username {
				src = append(src, t)
			}
		}
	}
	if filter == "all" {
		return src
	}
	var out []*protocol.Task
	for _, t := range src {
		if string(t.Status) == filter {
			out = append(out, t)
		}
	}
	return out
}

// ── GitHub screen ─────────────────────────────────────────────────────────────

func (m Model) viewGitHub() string {
	var header string
	if m.ghClient != nil && m.ghClient.IsConfigured() {
		refresh := "never"
		if m.ghData != nil {
			refresh = gh.TimeAgo(m.ghData.FetchedAt)
		}
		loading := ""
		if m.ghLoading {
			loading = mutedSt.Render("  refreshing...")
		}
		header = fmt.Sprintf("  org: %s   last fetch: %s%s   [r] refresh",
			boldFgSt.Render(m.ghClient.Org()), mutedSt.Render(refresh), loading)
	} else {
		header = sectionTitleSt.Render("  GitHub not configured")
	}

	// sub-tabs
	tabs := make([]string, len(ghTabLabels))
	for i, label := range ghTabLabels {
		if m.ghData != nil {
			switch i {
			case 1:
				label = fmt.Sprintf("%s (%d)", label, len(m.ghData.PRs))
			case 2:
				label = fmt.Sprintf("%s (%d)", label, len(m.ghData.Issues))
			}
		}
		if i == m.ghTab {
			tabs[i] = subTabActiveSt.Render(label)
		} else {
			tabs[i] = subTabInactiveSt.Render(label)
		}
	}
	tabBar := "  " + strings.Join(tabs, "  ") + mutedSt.Render("   [ ] to switch")

	content := m.githubVP.View()
	inner := lipgloss.JoinVertical(lipgloss.Left, header, tabBar, content)
	return screenBoxSt.Width(m.width - 2).Height(m.screenHeight()).Render(inner)
}

func (m Model) renderGitHub() string {
	var sb strings.Builder

	if m.ghClient == nil || !m.ghClient.IsConfigured() {
		sb.WriteString("\n")
		sb.WriteString(sectionTitleSt.Render("  Setup GitHub integration") + "\n\n")
		sb.WriteString(mutedSt.Render("  run this command in the input:\n\n"))
		sb.WriteString(msgTextSt.Render(`  /github setup --token ghp_yourtoken --org yourorgname`) + "\n\n")
		sb.WriteString(mutedSt.Render("  your token needs read:org and repo scopes\n"))
		return sb.String()
	}

	if m.ghLoading && m.ghData == nil {
		sb.WriteString(mutedSt.Render("\n  fetching from github...\n"))
		return sb.String()
	}

	if m.ghErr != "" && m.ghData == nil {
		sb.WriteString("\n" + lipgloss.NewStyle().Foreground(cRed).Render("  error: "+m.ghErr) + "\n")
		sb.WriteString(mutedSt.Render("  type /github refresh to try again\n"))
		return sb.String()
	}

	if m.ghData == nil {
		sb.WriteString(mutedSt.Render("\n  no data yet  -  type /github refresh\n"))
		return sb.String()
	}

	switch ghTabs[m.ghTab] {
	case "overview":
		sb.WriteString("\n")
		sb.WriteString("  " + sectionTitleSt.Render("repositories") + "\n\n")
		for _, r := range m.ghData.Repos {
			priv := ""
			if r.Private {
				priv = mutedSt.Render(" [private]")
			}
			desc := ""
			if r.Description != "" {
				desc = "  " + mutedSt.Render(truncate(r.Description, m.width-40))
			}
			sb.WriteString(fmt.Sprintf("  %s%s%s\n",
				boldFgSt.Render(r.Name), priv, desc))
			sb.WriteString(fmt.Sprintf("     %s stars  %s issues  %s\n",
				mutedSt.Render(fmt.Sprintf("%d", r.Stars)),
				mutedSt.Render(fmt.Sprintf("%d", r.OpenIssues)),
				mutedSt.Render("pushed "+gh.TimeAgo(r.PushedAt)),
			))
			sb.WriteString("\n")
		}
		if len(m.ghData.Members) > 0 {
			sb.WriteString("  " + sectionTitleSt.Render("org members") + "\n\n")
			names := make([]string, len(m.ghData.Members))
			for i, mem := range m.ghData.Members {
				names[i] = lipgloss.NewStyle().Foreground(cRedDim).Render(mem.Login)
			}
			sb.WriteString("  " + strings.Join(names, "  ") + "\n")
		}

	case "prs":
		sb.WriteString("\n")
		if len(m.ghData.PRs) == 0 {
			sb.WriteString(mutedSt.Render("  no open pull requests\n"))
			break
		}
		for _, pr := range m.ghData.PRs {
			draft := ""
			if pr.Draft {
				draft = mutedSt.Render(" [draft]")
			}
			lbs := renderLabels(pr.Labels)
			sb.WriteString(fmt.Sprintf("  %s  %s  %s%s%s\n",
				mutedSt.Render(fmt.Sprintf("#%-4d", pr.Number)),
				lipgloss.NewStyle().Foreground(cRedDim).Render(pr.RepoName),
				boldFgSt.Render(truncate(pr.Title, m.width-45)),
				draft,
				lbs,
			))
			sb.WriteString(fmt.Sprintf("       by %s   %s\n",
				lipgloss.NewStyle().Foreground(cGreen).Render(pr.Login),
				mutedSt.Render(gh.TimeAgo(pr.UpdatedAt)),
			))
			sb.WriteString("\n")
		}

	case "issues":
		sb.WriteString("\n")
		if len(m.ghData.Issues) == 0 {
			sb.WriteString(mutedSt.Render("  no open issues\n"))
			break
		}
		for _, issue := range m.ghData.Issues {
			lbs := renderLabels(issue.Labels)
			sb.WriteString(fmt.Sprintf("  %s  %s  %s%s\n",
				mutedSt.Render(fmt.Sprintf("#%-4d", issue.Number)),
				lipgloss.NewStyle().Foreground(cRedDim).Render(issue.RepoName),
				boldFgSt.Render(truncate(issue.Title, m.width-45)),
				lbs,
			))
			sb.WriteString(fmt.Sprintf("       by %s   %s\n",
				lipgloss.NewStyle().Foreground(cGreen).Render(issue.Login),
				mutedSt.Render(gh.TimeAgo(issue.UpdatedAt)),
			))
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

func renderLabels(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	out := make([]string, len(labels))
	for i, l := range labels {
		out[i] = labelSt.Render(l)
	}
	return "  " + strings.Join(out, " ")
}

// ── Workstation screen ────────────────────────────────────────────────────────

func (m Model) viewWorkstation() string {
	inputH := 3 // border(2) + 1 content line
	paneH := m.height - 2 - inputH
	if paneH < 4 {
		paneH = 4
	}
	var paneArea string
	if m.wsPane != nil {
		paneArea = m.renderPane(m.wsPane, m.width, paneH)
	} else {
		leftW := m.width / 2
		rightW := m.width - leftW
		left := m.renderScreenPane(scrTasks, leftW, paneH)
		right := m.renderScreenPane(scrChat, rightW, paneH)
		paneArea = lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	}
	return lipgloss.JoinVertical(lipgloss.Left, paneArea, m.viewWsInput())
}

func (m Model) viewWsInput() string {
	m.wsInput.Width = m.width - 8
	return inputBoxSt.Render(m.wsInput.View())
}

func (m Model) viewMembersScreen() string {
	inner := m.membersVP.View()
	return screenBoxSt.Width(m.width - 2).Height(m.screenHeight()).Render(inner)
}

// renderMembers returns member list content (used by /members command and pane rendering).
func (m Model) renderMembers() string {
	var sb strings.Builder
	sb.WriteString("\n")

	sorted := make([]*protocol.Member, len(m.members))
	copy(sorted, m.members)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Online != sorted[j].Online {
			return sorted[i].Online
		}
		return sorted[i].Username < sorted[j].Username
	})

	for _, mem := range sorted {
		dot := mutedSt.Render("  o  ")
		status := mutedSt.Render("offline")
		if mem.Online {
			dot = lipgloss.NewStyle().Foreground(cGreen).Render("  *  ")
			status = lipgloss.NewStyle().Foreground(cGreen).Render("online ")
		}

		var open, done int
		for _, t := range m.tasks {
			if t.Assignee == mem.Username {
				if t.Status == protocol.StatusDone {
					done++
				} else {
					open++
				}
			}
		}

		role := mutedSt.Render(mem.Role)
		if mem.Role == "admin" {
			role = lipgloss.NewStyle().Foreground(cRed).Render("admin")
		}

		sb.WriteString(fmt.Sprintf("%s %s  %s  %s  tasks open: %s  done: %s\n",
			dot,
			boldFgSt.Render(mem.Username),
			role,
			status,
			lipgloss.NewStyle().Foreground(cRed).Render(fmt.Sprintf("%d", open)),
			lipgloss.NewStyle().Foreground(cGreen).Render(fmt.Sprintf("%d", done)),
		))
		sb.WriteString("\n")
	}
	return sb.String()
}

// ── Status bar + input ────────────────────────────────────────────────────────

func (m Model) viewStatus() string {
	conn := lipgloss.NewStyle().Foreground(cGreen).Render("* ") +
		mutedSt.Render(m.serverAddr+"  ")
	if !m.connected {
		conn = lipgloss.NewStyle().Foreground(cRed).Bold(true).Render("* OFFLINE  ")
	}
	return conn + statusBarSt.Render(m.statusMsg)
}

func (m Model) viewInput() string {
	m.input.Placeholder = ""
	// width: terminal minus border(2) minus padding(2) each side = minus 6
	// do NOT set .Width() on the box — lipgloss miscounts ANSI cursor width and wraps
	m.input.Width = m.width - 8
	return inputBoxSt.Render(m.input.View())
}

// ── layout helpers ────────────────────────────────────────────────────────────

func (m Model) screenHeight() int {
	// header(1) + status(1) = 2 fixed on all screens
	// home: no input, no border → content fills m.height - 2
	// others: input(3) + border(2) → m.height - 2 - 3 - 2 = m.height - 7
	if m.screen == scrHome {
		// header(1) + homeVP + status(1) = m.height
		h := m.height - 2
		if h < 4 {
			h = 4
		}
		return h
	}
	h := m.height - 7
	if h < 4 {
		h = 4
	}
	return h
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (m *Model) rebuildViewports() {
	// home is borderless and full-width; others have a box border (w = m.width-4)
	hw := m.width
	if hw < 10 {
		hw = 10
	}
	w := m.width - 4
	if w < 10 {
		w = 10
	}
	// home: header(1) + status(1) = 2 overhead, no border
	homeH := clamp(m.height-2, 2, m.height)
	// others: header(1) + status(1) + input(3) + border(2) = 7 overhead
	ph := clamp(m.height-7, 2, m.height)
	m.homeVP = viewport.New(hw, homeH)
	m.chatVP = viewport.New(w, ph)
	m.tasksVP = viewport.New(w, clamp(ph-1, 2, ph))  // 1 line tab bar
	m.githubVP = viewport.New(w, clamp(ph-2, 2, ph)) // header + tab bar
	m.membersVP = viewport.New(w, ph)
}

func (m *Model) refreshContent() {
	if m.width == 0 {
		return
	}
	if !m.initialized {
		m.rebuildViewports()
		m.initialized = true
	}
	m.homeVP.SetContent(m.renderHome())
	m.chatVP.SetContent(m.renderChat())
	m.chatVP.GotoBottom()
	m.tasksVP.SetContent(m.renderTasks())
	m.githubVP.SetContent(m.renderGitHub())
	m.membersVP.SetContent(m.renderMembers())
}

// ── command handler ───────────────────────────────────────────────────────────

func (m *Model) handleText(text string) tea.Cmd {
	// home cmdline: q/2/3/4/5 navigation
	if m.screen == scrHome {
		return m.handleHomeCmd(text)
	}
	if strings.HasPrefix(text, "/") {
		return m.handleCommand(text)
	}
	m.cl.SendChat(text)
	m.screen = scrChat
	return nil
}

func (m *Model) handleHomeCmd(text string) tea.Cmd {
	cmd := strings.TrimSpace(text)

	// Split pane commands — navigate to workstation and set layout
	if strings.HasPrefix(cmd, "sp") {
		m.parseSplitCmd(cmd)
		m.screen = scrWorkstation
		return nil
	}
	if cmd == "cl" || cmd == "close" {
		m.wsPane = nil
		m.screen = scrWorkstation
		return nil
	}

	switch cmd {
	case "q", "q!", "quit":
		return tea.Quit
	case "docs":
		openBrowser("https://wert-docs.vercel.app")
		return nil
	case "1":
		m.wsPane = nil
		m.screen = scrHome
		m.refreshContent()
	case "2":
		m.wsPane = nil
		m.screen = scrChat
		m.unreadChat = 0
		m.refreshContent()
		m.chatVP.GotoBottom()
	case "3":
		m.wsPane = nil
		m.screen = scrTasks
		m.refreshContent()
	case "4":
		m.wsPane = nil
		m.screen = scrGitHub
		if m.ghClient != nil && m.ghClient.IsConfigured() && m.ghData == nil && !m.ghLoading {
			m.ghLoading = true
			m.refreshContent()
			return fetchGitHub(m.ghClient)
		}
		m.refreshContent()
	case "5":
		m.wsPane = nil
		m.screen = scrWorkstation
		m.refreshContent()
	case "6":
		m.screen = scrMembers
		m.refreshContent()
	default:
		m.statusMsg = "unknown: try  q  1-5  sp v<n> [h<n>]  cl"
	}
	return nil
}

func (m *Model) handleCommand(text string) tea.Cmd {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}
	cmd := fields[0]

	switch cmd {
	case "/exit", "/quit":
		return tea.Quit

	case "/help":
		lines := []string{
			"",
			"  navigation:  1-5 switch screens   tab next screen   [ ] filter sub-tabs   esc go back",
			"",
			"  /done <id>           mark task done",
			"  /wip <id>            mark in progress",
			"  /blocked <id>        mark blocked",
			"  /todo <id>           reset to todo",
			"  /members             show team",
		}
		if m.role == "admin" {
			lines = append(lines,
				`  /assign @user "title" ["desc"] [priority] [due:YYYY-MM-DD]   create task`,
				"  /delete <id>         remove task",
			)
		}
		lines = append(lines,
			"  /github setup --token <token> --org <org>   configure github",
			"  /github refresh      reload github data",
			"  /exit                quit wert",
			"",
		)
		m.prevScreen = m.screen
		m.injectLocalMessages(lines)
		m.screen = scrChat

	case "/done":
		m.updateStatus(fields, protocol.StatusDone)
	case "/wip":
		m.updateStatus(fields, protocol.StatusInProgress)
	case "/blocked":
		m.updateStatus(fields, protocol.StatusBlocked)
	case "/todo":
		m.updateStatus(fields, protocol.StatusTodo)

	case "/members":
		lines := []string{"", "  team members:"}
		for _, mem := range m.members {
			dot := "o"
			if mem.Online {
				dot = "*"
			}
			lines = append(lines, fmt.Sprintf("  %s  %s  (%s)", dot, mem.Username, mem.Role))
		}
		lines = append(lines, "")
		m.prevScreen = m.screen
		m.injectLocalMessages(lines)
		m.screen = scrChat

	case "/assign":
		if m.role != "admin" {
			m.statusMsg = "only admins can assign tasks"
			return nil
		}
		m.handleAssign(text)

	case "/delete":
		if m.role != "admin" {
			m.statusMsg = "only admins can delete tasks"
			return nil
		}
		if len(fields) < 2 {
			m.statusMsg = "usage: /delete <id>"
			return nil
		}
		m.cl.SendTaskDelete(fields[1])
		m.statusMsg = "delete sent for " + fields[1]

	case "/github":
		return m.handleGitHub(fields)

	default:
		m.statusMsg = "unknown command: " + cmd + "  (type /help)"
	}
	return nil
}

func (m *Model) updateStatus(fields []string, status protocol.TaskStatus) {
	if len(fields) < 2 {
		m.statusMsg = "usage: " + fields[0] + " <task-id>"
		return
	}
	m.cl.SendTaskUpdate(fields[1], status)
	m.statusMsg = "update sent"
}

func (m *Model) handleAssign(text string) {
	rest := strings.TrimSpace(strings.TrimPrefix(text, "/assign "))
	parts := strings.SplitN(rest, " ", 2)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "@") {
		m.statusMsg = `usage: /assign @user "title" ["desc"] [priority] [due:YYYY-MM-DD]`
		return
	}
	assignee := strings.TrimPrefix(parts[0], "@")
	strs := parseQuoted(strings.TrimSpace(parts[1]))
	if len(strs) == 0 {
		m.statusMsg = `usage: /assign @user "title" ["desc"] [priority]`
		return
	}
	title := strs[0]
	description, priority, due := "", "medium", ""
	for _, s := range strs[1:] {
		switch {
		case s == "high" || s == "medium" || s == "low":
			priority = s
		case strings.HasPrefix(s, "due:"):
			due = strings.TrimPrefix(s, "due:")
		default:
			if description == "" {
				description = s
			}
		}
	}
	data, err := protocol.NewEnvelope(protocol.MsgTaskCreate, protocol.TaskCreatePayload{
		Task: protocol.Task{
			Title:       title,
			Description: description,
			Assignee:    assignee,
			Priority:    priority,
			DueDate:     due,
		},
	})
	if err == nil {
		m.cl.Send <- data
	}
	m.statusMsg = fmt.Sprintf("task assigned to @%s", assignee)
}

func (m *Model) handleGitHub(fields []string) tea.Cmd {
	if len(fields) < 2 {
		m.statusMsg = "usage: /github setup --token ... --org ...   or   /github refresh"
		return nil
	}
	switch fields[1] {
	case "refresh":
		if m.ghClient == nil || !m.ghClient.IsConfigured() {
			m.statusMsg = "github not configured. use /github setup first"
			return nil
		}
		m.ghLoading = true
		m.statusMsg = "fetching github data..."
		m.screen = scrGitHub
		m.refreshContent()
		return fetchGitHub(m.ghClient)

	case "setup":
		token, org := "", ""
		for i, f := range fields {
			if f == "--token" && i+1 < len(fields) {
				token = fields[i+1]
			}
			if f == "--org" && i+1 < len(fields) {
				org = fields[i+1]
			}
		}
		if token == "" || org == "" {
			m.statusMsg = "usage: /github setup --token <token> --org <org>"
			return nil
		}
		m.ghClient = gh.New(token, org)
		m.ghLoading = true
		m.statusMsg = fmt.Sprintf("connecting to github org: %s", org)
		m.screen = scrGitHub
		m.refreshContent()
		return fetchGitHub(m.ghClient)
	}
	return nil
}

// ── envelope handler ──────────────────────────────────────────────────────────

func (m Model) applyEnvelope(env protocol.Envelope) Model {
	switch env.Type {
	case protocol.MsgSync:
		var p protocol.SyncPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		m.role = p.Role
		m.tasks = make([]*protocol.Task, len(p.Tasks))
		for i := range p.Tasks {
			cp := p.Tasks[i]
			m.tasks[i] = &cp
		}
		m.messages = make([]*protocol.ChatMessage, len(p.Messages))
		for i := range p.Messages {
			cp := p.Messages[i]
			m.messages[i] = &cp
		}
		m.members = make([]*protocol.Member, len(p.Members))
		for i := range p.Members {
			cp := p.Members[i]
			m.members[i] = &cp
		}

	case protocol.MsgChat:
		var p protocol.ChatPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		cp := p.Message
		m.messages = append(m.messages, &cp)
		if m.screen != scrChat {
			m.unreadChat++
			// @mention notification
			if strings.Contains(strings.ToLower(cp.Content), "@"+strings.ToLower(m.username)) {
				m.statusMsg = fmt.Sprintf("* @mention from %s", cp.From)
			}
		}

	case protocol.MsgTaskCreate:
		var p protocol.TaskCreatePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		cp := p.Task
		m.tasks = append(m.tasks, &cp)
		if cp.Assignee == m.username {
			m.statusMsg = fmt.Sprintf("* new task: %s", cp.Title)
		}

	case protocol.MsgTaskUpdate:
		var p protocol.TaskUpdatePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		for _, t := range m.tasks {
			if t.ID == p.TaskID {
				t.Status = p.Status
				t.UpdatedBy = p.UpdatedBy
				t.UpdatedAt = time.Now()
				break
			}
		}

	case protocol.MsgTaskDelete:
		var p protocol.TaskDeletePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		filtered := m.tasks[:0]
		for _, t := range m.tasks {
			if t.ID != p.TaskID {
				filtered = append(filtered, t)
			}
		}
		m.tasks = filtered

	case protocol.MsgMemberJoin:
		var p protocol.MemberEventPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		found := false
		for _, mem := range m.members {
			if mem.Username == p.Member.Username {
				mem.Online = true
				found = true
				break
			}
		}
		if !found {
			cp := p.Member
			m.members = append(m.members, &cp)
		}
		if p.Member.Username != m.username {
			m.statusMsg = p.Member.Username + " joined"
		}

	case protocol.MsgMemberLeave:
		var p protocol.MemberEventPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		for _, mem := range m.members {
			if mem.Username == p.Member.Username {
				mem.Online = false
				break
			}
		}
		m.statusMsg = p.Member.Username + " left"

	case protocol.MsgError:
		var p protocol.ErrorPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		m.statusMsg = "  " + p.Message
	}
	return m
}

// ── tea.Cmd helpers ───────────────────────────────────────────────────────────

func waitForMsg(ch <-chan protocol.Envelope) tea.Cmd {
	return func() tea.Msg {
		env, ok := <-ch
		if !ok {
			return DisconnectedMsg{}
		}
		return ServerMsg{Env: env}
	}
}

func tryReconnect(host, username, token string) tea.Cmd {
	return tea.Tick(3*time.Second, func(_ time.Time) tea.Msg {
		cl, err := client.Connect(host, username, token)
		return reconnectMsg{cl: cl, err: err}
	})
}

func fetchGitHub(cl *gh.Client) tea.Cmd {
	return func() tea.Msg {
		data, err := cl.FetchAll()
		return githubDataMsg{data: data, err: err}
	}
}

// ── misc helpers ──────────────────────────────────────────────────────────────

func (m *Model) injectLocalMessages(lines []string) {
	now := time.Now()
	for i, l := range lines {
		m.messages = append(m.messages, &protocol.ChatMessage{
			ID:        fmt.Sprintf("local-%d-%d", now.UnixNano(), i),
			From:      "wert",
			Content:   l,
			Timestamp: now,
		})
	}
	m.refreshContent()
}

func statusBadge(s protocol.TaskStatus) (string, lipgloss.Style) {
	switch s {
	case protocol.StatusTodo:
		return "TODO    ", todoSt
	case protocol.StatusInProgress:
		return "IN PROG ", wipSt
	case protocol.StatusDone:
		return "DONE    ", doneSt
	case protocol.StatusBlocked:
		return "BLOCKED ", blockedSt
	default:
		return string(s), lipgloss.NewStyle()
	}
}

func priLabel(p string) string {
	switch strings.ToLower(p) {
	case "high":
		return highPriSt.Render("^ high")
	case "low":
		return lowPriSt.Render("v low")
	default:
		return medPriSt.Render("- med")
	}
}

func shortID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

func truncate(s string, n int) string {
	if n <= 0 {
		return s
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n-1]) + "..."
}

// ── Split pane methods ────────────────────────────────────────────────────────

func (m *Model) parseSplitCmd(text string) {
	parts := strings.Fields(text) // ["sp", "v2", "h5"]
	if len(parts) < 2 {
		m.statusMsg = "usage: sp <v|h><n> [<v|h><n>]  e.g. sp v2 h5"
		return
	}
	current := m.screen
	if m.wsPane != nil {
		node := m.wsPane
		for node.split != 0 {
			node = node.a
		}
		current = node.screen
		m.wsPane = nil
	}
	arg0 := parts[1]
	if len(arg0) < 2 {
		m.statusMsg = "invalid: " + arg0
		return
	}
	dir0 := arg0[0]
	n0 := int(arg0[1] - '0')
	if (dir0 != 'v' && dir0 != 'h') || n0 < 1 || n0 > 6 {
		m.statusMsg = "use v or h and screen 1-5"
		return
	}
	secondLeaf := &paneNode{screen: screenType(n0 - 1)}
	root := &paneNode{split: dir0, a: &paneNode{screen: current}, b: secondLeaf}
	if len(parts) >= 3 {
		arg1 := parts[2]
		if len(arg1) >= 2 {
			dir1 := arg1[0]
			n1 := int(arg1[1] - '0')
			if (dir1 == 'v' || dir1 == 'h') && n1 >= 1 && n1 <= 6 {
				root.b = &paneNode{
					split:  dir1,
					a:      secondLeaf,
					b:      &paneNode{screen: screenType(n1 - 1)},
				}
			}
		}
	}
	m.wsPane = root
}

func (m Model) renderPane(node *paneNode, w, h int) string {
	if node == nil || w < 2 || h < 2 {
		return ""
	}
	if node.split == 0 {
		return m.renderScreenPane(node.screen, w, h)
	}
	if node.split == 'v' {
		topH := h / 2
		return lipgloss.JoinVertical(lipgloss.Left,
			m.renderPane(node.a, w, topH),
			m.renderPane(node.b, w, h-topH),
		)
	}
	// horizontal split
	leftW := w / 2
	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.renderPane(node.a, leftW, h),
		m.renderPane(node.b, w-leftW, h),
	)
}

func (m Model) renderScreenPane(screen screenType, w, h int) string {
	if w < 4 || h < 4 {
		return screenBoxSt.Width(w - 2).Height(h - 2).Render("")
	}
	vpW := w - 2
	vpH := h - 4 // border(2) + label line(1) + padding(1)
	if vpH < 1 {
		vpH = 1
	}
	vp := viewport.New(vpW, vpH)
	switch screen {
	case scrHome:
		vp.SetContent(m.renderHome())
	case scrChat:
		vp.SetContent(m.renderChat())
		vp.GotoBottom()
	case scrTasks:
		vp.SetContent(m.renderTasks())
	case scrGitHub:
		vp.SetContent(m.renderGitHub())
	case scrWorkstation:
		vp.SetContent(m.renderTasks()) // default pane content for workstation slot
	case scrMembers:
		vp.SetContent(m.renderMembers())
	}
	label := sectionTitleSt.Render("  " + screenNames[int(screen)])
	inner := lipgloss.JoinVertical(lipgloss.Left, label, vp.View())
	return screenBoxSt.Width(w - 2).Height(h - 2).Render(inner)
}

// ─────────────────────────────────────────────────────────────────────────────

func parseQuoted(s string) []string {
	var tokens []string
	var cur strings.Builder
	inQ := false
	for _, ch := range s {
		switch ch {
		case '"':
			if inQ {
				tokens = append(tokens, cur.String())
				cur.Reset()
				inQ = false
			} else {
				if cur.Len() > 0 {
					tokens = append(tokens, cur.String())
					cur.Reset()
				}
				inQ = true
			}
		case ' ':
			if inQ {
				cur.WriteRune(ch)
			} else if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(ch)
		}
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

