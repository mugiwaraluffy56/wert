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
	"wert/internal/version"
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

	cCyan      = lipgloss.Color("#00BCD4")
	cCyanDim   = lipgloss.Color("#80DEEA")
	agentNameSt = lipgloss.NewStyle().Foreground(cCyan).Bold(true)
	agentDmSt   = lipgloss.NewStyle().Foreground(cCyanDim).Italic(true)
	resultBoxSt = lipgloss.NewStyle().
			BorderLeft(true).
			BorderForeground(cCyan).
			Padding(0, 1).
			Foreground(lipgloss.Color("#E0F7FA"))

	labelSt = lipgloss.NewStyle().
		Background(lipgloss.Color("#333333")).
		Foreground(cMuted).
		Padding(0, 1)
)

// UpdateRequested is set to true when the user types :update in the cmdline.
// The outer cobra command reads this after the TUI exits and runs the updater.
var UpdateRequested bool

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
type approvalTickMsg struct{} // drives the waiting-for-approval animation

// ── Task filter tabs ──────────────────────────────────────────────────────────

var taskFilters = []string{"all", "todo", "in_progress", "done", "blocked"}
var taskFilterLabels = []string{"All", "Todo", "In Progress", "Done", "Blocked"}

// ── GitHub sub-tabs ───────────────────────────────────────────────────────────

var ghTabs = []string{"overview", "repos", "prs", "issues"}
var ghTabLabels = []string{"Overview", "Repos", "Pull Requests", "Issues"}

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
	agents   []*protocol.AgentInfo

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

	// join approval — admin side
	joinReqPopup   bool     // floating Y/N popup is visible
	joinReqCurrent string   // username shown in the popup
	joinReqQueue   []string // overflow queue when popup is already open

	// join approval — member side
	pendingJoins    []string // badge count of pending join requests (admin sees this)
	pendingApproval bool     // this client is waiting for admin to approve them
	approvalFrame   int      // animation frame for waiting screen
	joinRejected    bool     // rejected by admin
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
		agents:     []*protocol.AgentInfo{},
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

	case approvalTickMsg:
		m.approvalFrame = (m.approvalFrame + 1) % 12
		if m.pendingApproval {
			return m, approvalTick()
		}
		return m, nil

	case ServerMsg:
		wasApproval := m.pendingApproval
		m = m.applyEnvelope(msg.Env)
		m.refreshContent()
		cmds := []tea.Cmd{waitForMsg(m.cl.Recv)}
		if m.pendingApproval && !wasApproval {
			cmds = append(cmds, approvalTick())
		}
		return m, tea.Batch(cmds...)

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
		// Join request popup intercepts all keys when active.
		if m.joinReqPopup && m.role == "admin" {
			switch strings.ToLower(msg.String()) {
			case "y":
				m.cl.SendJoinApprove(m.joinReqCurrent)
				m.removePendingJoin(m.joinReqCurrent)
				m.statusMsg = "approved " + m.joinReqCurrent
				m.advanceJoinQueue()
			case "n", "esc":
				m.cl.SendJoinReject(m.joinReqCurrent)
				m.removePendingJoin(m.joinReqCurrent)
				m.statusMsg = "rejected " + m.joinReqCurrent
				m.advanceJoinQueue()
			}
			return m, nil
		}

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
			// number keys switch sub-tabs when input is empty
			if m.input.Value() == "" && !m.cmdlineActive {
				if m.screen == scrTasks {
					switch msg.String() {
					case "1":
						m.taskFilter = 0; m.refreshContent(); return m, nil
					case "2":
						m.taskFilter = 1; m.refreshContent(); return m, nil
					case "3":
						m.taskFilter = 2; m.refreshContent(); return m, nil
					case "4":
						m.taskFilter = 3; m.refreshContent(); return m, nil
					case "5":
						m.taskFilter = 4; m.refreshContent(); return m, nil
					}
				}
				if m.screen == scrGitHub {
					switch msg.String() {
					case "1":
						m.ghTab = 0; m.refreshContent(); return m, nil
					case "2":
						m.ghTab = 1; m.refreshContent(); return m, nil
					case "3":
						m.ghTab = 2; m.refreshContent(); return m, nil
					case "4":
						m.ghTab = 3; m.refreshContent(); return m, nil
					}
				}
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
	if m.pendingApproval {
		return m.viewWaitingApproval()
	}
	if m.joinRejected {
		return m.viewRejected()
	}
	base := m.viewBase()
	if m.joinReqPopup {
		return m.viewJoinPopup(base)
	}
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
	if len(m.pendingJoins) > 0 {
		left += "  " + lipgloss.NewStyle().Background(cYellow).Foreground(lipgloss.Color("#000000")).Bold(true).Padding(0, 1).Render(fmt.Sprintf("⏳ %d pending", len(m.pendingJoins)))
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

		// Structured agent result — render as a bordered block.
		if msg.IsAgent && msg.Kind == "result" {
			title := msg.Meta
			if title == "" {
				title = "Result"
			}
			header := agentNameSt.Render("["+msg.From+"]") + "  " + mutedSt.Render(title) + "  " + ts
			body := resultBoxSt.Render(msg.Content)
			sb.WriteString("  " + header + "\n  " + body + "\n")
			// Render reactions if any.
			if len(msg.Reactions) > 0 {
				reactionLine := "  " + mutedSt.Render("  reactions: ")
				for _, r := range msg.Reactions {
					var rst lipgloss.Style
					switch r.Reaction {
					case "approve":
						rst = lipgloss.NewStyle().Foreground(cGreen).Bold(true)
					case "reject":
						rst = lipgloss.NewStyle().Foreground(cRed).Bold(true)
					default:
						rst = lipgloss.NewStyle().Foreground(cMuted)
					}
					reactionLine += rst.Render(r.Reactor+":"+r.Reaction) + mutedSt.Render("  ")
				}
				sb.WriteString(reactionLine + "\n")
			}
			sb.WriteString("\n")
			continue
		}

		// Handoff event — render as dim system line.
		if msg.IsAgent && msg.Kind == "handoff" {
			label := agentDmSt.Render("[" + msg.From + " → " + msg.Meta + "]")
			sb.WriteString(fmt.Sprintf("  %s  %s  %s\n", ts, label, mutedSt.Render(msg.Content)))
			continue
		}

		// Direct message — render with private indicator.
		if msg.IsAgent && msg.Kind == "dm" {
			recipient := msg.Meta
			arrow := agentDmSt.Render("→ " + recipient)
			label := agentNameSt.Render(msg.From+":") + " " + arrow
			content := mutedSt.Render(msg.Content)
			sb.WriteString(fmt.Sprintf("  %s  %s  %s\n", ts, label, content))
			continue
		}

		var nameSt lipgloss.Style
		switch {
		case msg.IsAgent:
			nameSt = agentNameSt
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
		// Thread reply reference.
		if msg.ReplyTo != "" {
			replyRef := "↩ " + msg.ReplyFrom
			sb.WriteString(fmt.Sprintf("  %s\n", agentDmSt.Render("    "+replyRef)))
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
	all := m.filteredTasks("all")
	counts := map[string]int{"all": len(all)}
	for _, t := range all {
		counts[string(t.Status)]++
	}

	// stats bar
	statsBar := fmt.Sprintf("  %s  %s  %s  %s  %s",
		wipSt.Render(fmt.Sprintf("● %d in progress", counts["in_progress"])),
		todoSt.Render(fmt.Sprintf("◦ %d todo", counts["todo"])),
		blockedSt.Render(fmt.Sprintf("✗ %d blocked", counts["blocked"])),
		doneSt.Render(fmt.Sprintf("✓ %d done", counts["done"])),
		mutedSt.Render(fmt.Sprintf("%d total", counts["all"])),
	)

	// tab bar with counts and number hints
	labels := []string{
		fmt.Sprintf("1:All(%d)", counts["all"]),
		fmt.Sprintf("2:Todo(%d)", counts["todo"]),
		fmt.Sprintf("3:In Progress(%d)", counts["in_progress"]),
		fmt.Sprintf("4:Done(%d)", counts["done"]),
		fmt.Sprintf("5:Blocked(%d)", counts["blocked"]),
	}
	tabs := make([]string, len(labels))
	for i, label := range labels {
		if i == m.taskFilter {
			tabs[i] = subTabActiveSt.Render(label)
		} else {
			tabs[i] = subTabInactiveSt.Render(label)
		}
	}
	tabBar := "  " + strings.Join(tabs, "  ")
	sep := mutedSt.Render("  " + strings.Repeat("─", m.width-8))

	content := m.tasksVP.View()
	inner := lipgloss.JoinVertical(lipgloss.Left, statsBar, sep, tabBar, content)
	return screenBoxSt.Width(m.width - 2).Height(m.screenHeight()).Render(inner)
}

func (m Model) renderTasks() string {
	var sb strings.Builder
	filter := taskFilters[m.taskFilter]
	tasks := m.filteredTasks(filter)
	cw := m.width - 6
	if cw < 30 {
		cw = 30
	}

	if len(tasks) == 0 {
		sb.WriteString(mutedSt.Render("\n  no tasks\n"))
		return sb.String()
	}

	if filter == "all" {
		// render grouped: In Progress → Blocked → Todo → Done
		type group struct {
			status protocol.TaskStatus
			label  string
		}
		groups := []group{
			{protocol.StatusInProgress, "In Progress"},
			{protocol.StatusBlocked, "Blocked"},
			{protocol.StatusTodo, "Todo"},
			{protocol.StatusDone, "Done"},
		}
		for _, g := range groups {
			var items []*protocol.Task
			for _, t := range tasks {
				if t.Status == g.status {
					items = append(items, t)
				}
			}
			if len(items) == 0 {
				continue
			}
			header := fmt.Sprintf(" %s  %d ", g.label, len(items))
			dashes := cw - len(header) - 4
			if dashes < 4 {
				dashes = 4
			}
			sb.WriteString("\n  " + mutedSt.Render("──") + sectionTitleSt.Render(header) + mutedSt.Render(strings.Repeat("─", dashes)) + "\n\n")
			for _, t := range items {
				sb.WriteString(m.renderTaskRow(t, cw))
			}
		}
	} else {
		sb.WriteString("\n")
		for _, t := range tasks {
			sb.WriteString(m.renderTaskRow(t, cw))
		}
	}
	return sb.String()
}

func (m Model) renderTaskRow(t *protocol.Task, cw int) string {
	var sb strings.Builder

	// Fixed column widths (visual chars)
	const markerW = 13 // "  ● IN PROG  "
	const idW = 10     // "#a1b2c3d  "
	const priW = 8
	const assigneeW = 14
	const ageW = 9

	rightW := priW + assigneeW + ageW + 4 // +4 for spaces between
	titleW := cw - markerW - idW - 2 - rightW
	if titleW < 8 {
		titleW = 8
	}

	var markerStr string
	switch t.Status {
	case protocol.StatusTodo:
		markerStr = todoSt.Render("  ◦ TODO     ")
	case protocol.StatusInProgress:
		markerStr = wipSt.Render("  ● IN PROG  ")
	case protocol.StatusDone:
		markerStr = doneSt.Render("  ✓ DONE     ")
	case protocol.StatusBlocked:
		markerStr = blockedSt.Render("  ✗ BLOCKED  ")
	default:
		markerStr = mutedSt.Render("  ? ???      ")
	}

	rawTitle := truncate(t.Title, titleW)
	pad := titleW - utf8.RuneCountInString(rawTitle)
	if pad < 0 {
		pad = 0
	}
	title := boldFgSt.Render(rawTitle) + strings.Repeat(" ", pad)
	id := mutedSt.Render("#" + shortID(t.ID))
	assignee := lipgloss.NewStyle().Foreground(cGreen).Render("@" + truncate(t.Assignee, 11))
	age := mutedSt.Render(gh.TimeAgo(t.UpdatedAt))

	line1 := markerStr + id + "  " + title + "  " + priLabel(t.Priority) + "  " + assignee + "  " + age
	sb.WriteString(line1 + "\n")

	indent := strings.Repeat(" ", markerW+idW+2)
	if t.Description != "" {
		descW := cw - markerW - idW - 2
		if descW > 10 {
			sb.WriteString(indent + mutedSt.Render(truncate(t.Description, descW)) + "\n")
		}
	}
	var metaPlain []string
	if t.DueDate != "" {
		today := time.Now().Format("2006-01-02")
		dueStr := "due: " + t.DueDate
		if t.Status != protocol.StatusDone {
			if t.DueDate < today {
				dueStr = "OVERDUE: " + t.DueDate
			} else if t.DueDate == today {
				dueStr = "DUE TODAY: " + t.DueDate
			}
		}
		metaPlain = append(metaPlain, dueStr)
	}
	if t.UpdatedBy != "" && t.UpdatedBy != t.Assignee {
		metaPlain = append(metaPlain, "by: "+t.UpdatedBy)
	}
	if len(t.Comments) > 0 {
		metaPlain = append(metaPlain, fmt.Sprintf("%d comment(s)", len(t.Comments)))
	}
	if len(t.Dependencies) > 0 {
		metaPlain = append(metaPlain, fmt.Sprintf("deps: %d", len(t.Dependencies)))
	}
	hasMeta := len(metaPlain) > 0 || t.ClaimedBy != ""
	if hasMeta {
		line := indent
		if len(metaPlain) > 0 {
			// Color due-date entries red/yellow when overdue/due-today.
			rendered := make([]string, len(metaPlain))
			today := time.Now().Format("2006-01-02")
			for i, s := range metaPlain {
				switch {
				case strings.HasPrefix(s, "OVERDUE"):
					rendered[i] = lipgloss.NewStyle().Foreground(cRed).Bold(true).Render(s)
				case strings.HasPrefix(s, "DUE TODAY"):
					rendered[i] = lipgloss.NewStyle().Foreground(cYellow).Bold(true).Render(s)
				case strings.HasPrefix(s, "due:") && t.DueDate > today:
					rendered[i] = mutedSt.Render(s)
				default:
					rendered[i] = mutedSt.Render(s)
				}
			}
			line += strings.Join(rendered, mutedSt.Render("   "))
		}
		if t.ClaimedBy != "" {
			if len(metaPlain) > 0 {
				line += mutedSt.Render("   ")
			}
			line += agentNameSt.Render("claimed: " + t.ClaimedBy)
		}
		sb.WriteString(line + "\n")
	}

	sb.WriteString("\n")
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
	var orgBar string
	if m.ghClient != nil && m.ghClient.IsConfigured() {
		status := lipgloss.NewStyle().Foreground(cGreen).Render("●")
		if !m.connected {
			status = lipgloss.NewStyle().Foreground(cRed).Render("●")
		}
		age := "not fetched"
		if m.ghData != nil {
			age = gh.TimeAgo(m.ghData.FetchedAt)
		}
		loading := ""
		if m.ghLoading {
			loading = mutedSt.Render("  refreshing...")
		}
		orgBar = fmt.Sprintf("  %s %s   fetched: %s%s",
			status, boldFgSt.Render(m.ghClient.Org()), mutedSt.Render(age), loading)
	} else {
		orgBar = mutedSt.Render("  GitHub not configured — run /github setup")
	}

	// tab bar with counts and number hints
	labels := make([]string, len(ghTabLabels))
	for i, label := range ghTabLabels {
		prefix := fmt.Sprintf("%d:", i+1)
		if m.ghData != nil {
			switch ghTabs[i] {
			case "repos":
				label = fmt.Sprintf("%s(%d)", label, len(m.ghData.Repos))
			case "prs":
				label = fmt.Sprintf("%s(%d)", label, len(m.ghData.PRs))
			case "issues":
				label = fmt.Sprintf("%s(%d)", label, len(m.ghData.Issues))
			}
		}
		labels[i] = prefix + label
	}
	tabs := make([]string, len(labels))
	for i, label := range labels {
		if i == m.ghTab {
			tabs[i] = subTabActiveSt.Render(label)
		} else {
			tabs[i] = subTabInactiveSt.Render(label)
		}
	}
	tabBar := "  " + strings.Join(tabs, "  ")
	sep := mutedSt.Render("  " + strings.Repeat("─", m.width-8))

	content := m.githubVP.View()
	inner := lipgloss.JoinVertical(lipgloss.Left, orgBar, sep, tabBar, content)
	return screenBoxSt.Width(m.width - 2).Height(m.screenHeight()).Render(inner)
}

func (m Model) renderGitHub() string {
	var sb strings.Builder
	cw := m.width - 6
	if cw < 30 {
		cw = 30
	}

	if m.ghClient == nil || !m.ghClient.IsConfigured() {
		sb.WriteString("\n")
		sb.WriteString(sectionTitleSt.Render("  Setup GitHub integration") + "\n\n")
		sb.WriteString(mutedSt.Render("  /github setup --token ghp_yourtoken --org yourorg\n\n"))
		sb.WriteString(mutedSt.Render("  token needs: repo + read:org scopes (Classic PAT)\n"))
		return sb.String()
	}
	if m.ghLoading && m.ghData == nil {
		sb.WriteString(mutedSt.Render("\n  fetching from github...\n"))
		return sb.String()
	}
	if m.ghErr != "" && m.ghData == nil {
		sb.WriteString("\n" + lipgloss.NewStyle().Foreground(cRed).Render("  error: "+m.ghErr) + "\n")
		sb.WriteString(mutedSt.Render("  /github refresh to try again\n"))
		return sb.String()
	}
	if m.ghData == nil {
		sb.WriteString(mutedSt.Render("\n  /github refresh to load data\n"))
		return sb.String()
	}

	switch ghTabs[m.ghTab] {
	case "overview":
		sb.WriteString(m.renderGHOverview(cw))
	case "repos":
		sb.WriteString(m.renderGHRepos(cw))
	case "prs":
		sb.WriteString(m.renderGHPRs(cw))
	case "issues":
		sb.WriteString(m.renderGHIssues(cw))
	}
	return sb.String()
}

func (m Model) renderGHOverview(cw int) string {
	var sb strings.Builder
	d := m.ghData

	// stat boxes
	boxW := 18
	gap := "   "
	repoBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(cBorder).
		Padding(0, 2).Width(boxW).Align(lipgloss.Center).
		Render(boldFgSt.Render(fmt.Sprintf("%d", len(d.Repos))) + "\n" + mutedSt.Render("repos"))
	prBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(cBorder).
		Padding(0, 2).Width(boxW).Align(lipgloss.Center).
		Render(wipSt.Render(fmt.Sprintf("%d", len(d.PRs))) + "\n" + mutedSt.Render("open PRs"))
	issueBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(cBorder).
		Padding(0, 2).Width(boxW).Align(lipgloss.Center).
		Render(todoSt.Render(fmt.Sprintf("%d", len(d.Issues))) + "\n" + mutedSt.Render("open issues"))
	sb.WriteString("\n")
	sb.WriteString("  " + lipgloss.JoinHorizontal(lipgloss.Top, repoBox, gap, prBox, gap, issueBox) + "\n\n")

	// recently pushed repos
	if len(d.Repos) > 0 {
		n := len(d.Repos)
		if n > 5 {
			n = 5
		}
		sb.WriteString("  " + sectionTitleSt.Render("Recently pushed") + "\n")
		sb.WriteString("  " + mutedSt.Render(strings.Repeat("─", cw-4)) + "\n")
		for _, r := range d.Repos[:n] {
			priv := ""
			if r.Private {
				priv = mutedSt.Render("[prv] ")
			}
			desc := ""
			if r.Description != "" {
				descW := cw - 30
				if descW > 5 {
					desc = "  " + mutedSt.Render(truncate(r.Description, descW))
				}
			}
			sb.WriteString(fmt.Sprintf("  %s%s%s\n",
				priv+boldFgSt.Render(truncate(r.Name, 22)),
				desc,
				mutedSt.Render(fmt.Sprintf("  pushed %s  ★%d", gh.TimeAgo(r.PushedAt), r.Stars)),
			))
		}
		sb.WriteString("\n")
	}

	// recent PRs preview
	if len(d.PRs) > 0 {
		n := len(d.PRs)
		if n > 5 {
			n = 5
		}
		sb.WriteString("  " + sectionTitleSt.Render("Recent pull requests") + "\n")
		sb.WriteString("  " + mutedSt.Render(strings.Repeat("─", cw-4)) + "\n")
		for _, pr := range d.PRs[:n] {
			draft := ""
			if pr.Draft {
				draft = mutedSt.Render("[draft] ")
			}
			sb.WriteString(fmt.Sprintf("  %s  %s  %s%s  %s\n",
				mutedSt.Render(fmt.Sprintf("#%-4d", pr.Number)),
				lipgloss.NewStyle().Foreground(cRedDim).Render(truncate(pr.RepoName, 16)),
				draft,
				boldFgSt.Render(truncate(pr.Title, cw-50)),
				mutedSt.Render(gh.TimeAgo(pr.UpdatedAt)),
			))
		}
		sb.WriteString("\n")
	}

	// org members
	if len(d.Members) > 0 {
		sb.WriteString("  " + sectionTitleSt.Render(fmt.Sprintf("Org members (%d)", len(d.Members))) + "\n")
		sb.WriteString("  " + mutedSt.Render(strings.Repeat("─", cw-4)) + "\n  ")
		for i, mem := range d.Members {
			if i > 0 {
				sb.WriteString("  ")
			}
			sb.WriteString(lipgloss.NewStyle().Foreground(cRedDim).Render(mem.Login))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func (m Model) renderGHRepos(cw int) string {
	var sb strings.Builder
	d := m.ghData
	if len(d.Repos) == 0 {
		sb.WriteString(mutedSt.Render("\n  no repositories\n"))
		return sb.String()
	}

	// column widths
	nameW := 24
	starsW := 6
	issuesW := 8
	pushedW := 10
	descW := cw - nameW - starsW - issuesW - pushedW - 10
	if descW < 10 {
		descW = 10
	}

	// header
	sb.WriteString("\n")
	hdr := fmt.Sprintf("  %-*s  %-*s  %*s  %-*s  %-s",
		nameW, "Name",
		descW, "Description",
		starsW, "★",
		issuesW, "Issues",
		"Updated",
	)
	sb.WriteString(mutedSt.Render(hdr) + "\n")
	sb.WriteString(mutedSt.Render("  " + strings.Repeat("─", cw-4)) + "\n")

	for _, r := range d.Repos {
		priv := ""
		if r.Private {
			priv = mutedSt.Render("[prv] ")
		}
		name := priv + boldFgSt.Render(truncate(r.Name, nameW-6))
		desc := mutedSt.Render(truncate(r.Description, descW))
		stars := mutedSt.Render(fmt.Sprintf("%*d", starsW, r.Stars))
		issues := mutedSt.Render(fmt.Sprintf("%-*d", issuesW, r.OpenIssues))
		pushed := mutedSt.Render(gh.TimeAgo(r.PushedAt))
		sb.WriteString(fmt.Sprintf("  %-*s  %-*s  %s  %-*s  %s\n",
			nameW, name,
			descW, desc,
			stars,
			issuesW, issues,
			pushed,
		))
	}
	return sb.String()
}

func (m Model) renderGHPRs(cw int) string {
	var sb strings.Builder
	d := m.ghData
	if len(d.PRs) == 0 {
		sb.WriteString(mutedSt.Render("\n  no open pull requests\n"))
		return sb.String()
	}

	// group by repo
	type repoGroup struct {
		name string
		prs  []gh.PR
	}
	seen := map[string]int{}
	var groups []repoGroup
	for _, pr := range d.PRs {
		if idx, ok := seen[pr.RepoName]; ok {
			groups[idx].prs = append(groups[idx].prs, pr)
		} else {
			seen[pr.RepoName] = len(groups)
			groups = append(groups, repoGroup{name: pr.RepoName, prs: []gh.PR{pr}})
		}
	}

	titleW := cw - 8 - 16 - 16 - 10 // num + repo + author + age
	if titleW < 10 {
		titleW = 10
	}

	for _, g := range groups {
		header := fmt.Sprintf(" %s  %d PR ", g.name, len(g.prs))
		dashes := cw - len(header) - 4
		if dashes < 4 {
			dashes = 4
		}
		sb.WriteString("\n  " + mutedSt.Render("──") + sectionTitleSt.Render(header) + mutedSt.Render(strings.Repeat("─", dashes)) + "\n\n")
		for _, pr := range g.prs {
			marker := lipgloss.NewStyle().Foreground(cGreen).Render("●")
			if pr.Draft {
				marker = mutedSt.Render("○")
			}
			draft := ""
			if pr.Draft {
				draft = mutedSt.Render("[draft] ")
			}
			lbs := renderLabels(pr.Labels)
			line := fmt.Sprintf("    %s %s  %s%s%s",
				marker,
				mutedSt.Render(fmt.Sprintf("#%-4d", pr.Number)),
				draft+boldFgSt.Render(truncate(pr.Title, titleW)),
				lbs,
				mutedSt.Render(fmt.Sprintf("  @%s  %s", truncate(pr.Login, 12), gh.TimeAgo(pr.UpdatedAt))),
			)
			sb.WriteString(line + "\n")
		}
	}
	sb.WriteString("\n")
	return sb.String()
}

func (m Model) renderGHIssues(cw int) string {
	var sb strings.Builder
	d := m.ghData
	if len(d.Issues) == 0 {
		sb.WriteString(mutedSt.Render("\n  no open issues\n"))
		return sb.String()
	}

	// group by repo
	type repoGroup struct {
		name   string
		issues []gh.Issue
	}
	seen := map[string]int{}
	var groups []repoGroup
	for _, issue := range d.Issues {
		if idx, ok := seen[issue.RepoName]; ok {
			groups[idx].issues = append(groups[idx].issues, issue)
		} else {
			seen[issue.RepoName] = len(groups)
			groups = append(groups, repoGroup{name: issue.RepoName, issues: []gh.Issue{issue}})
		}
	}

	titleW := cw - 8 - 16 - 16 - 10
	if titleW < 10 {
		titleW = 10
	}

	for _, g := range groups {
		header := fmt.Sprintf(" %s  %d issue ", g.name, len(g.issues))
		dashes := cw - len(header) - 4
		if dashes < 4 {
			dashes = 4
		}
		sb.WriteString("\n  " + mutedSt.Render("──") + sectionTitleSt.Render(header) + mutedSt.Render(strings.Repeat("─", dashes)) + "\n\n")
		for _, issue := range g.issues {
			lbs := renderLabels(issue.Labels)
			line := fmt.Sprintf("    %s %s  %s%s%s",
				todoSt.Render("◦"),
				mutedSt.Render(fmt.Sprintf("#%-4d", issue.Number)),
				boldFgSt.Render(truncate(issue.Title, titleW))+lbs,
				"",
				mutedSt.Render(fmt.Sprintf("  @%s  %s", truncate(issue.Login, 12), gh.TimeAgo(issue.UpdatedAt))),
			)
			sb.WriteString(line + "\n")
		}
	}
	sb.WriteString("\n")
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

	// Agents section.
	if len(m.agents) > 0 {
		sep := mutedSt.Render("  " + strings.Repeat("─", 40))
		sb.WriteString(sep + "\n")
		sb.WriteString("  " + sectionTitleSt.Render("AI Agents") + "\n\n")
		for _, a := range m.agents {
			caps := mutedSt.Render("no capabilities listed")
			if len(a.Capabilities) > 0 {
				caps = mutedSt.Render(strings.Join(a.Capabilities, ", "))
			}
			sb.WriteString(fmt.Sprintf("  %s  %s  %s\n",
				agentNameSt.Render("["+a.Name+"]"),
				lipgloss.NewStyle().Foreground(cGreen).Render("registered"),
				caps,
			))
		}
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
	left := conn + statusBarSt.Render(m.statusMsg)

	// Due-date reminders: scan tasks relevant to this user.
	today := time.Now().Format("2006-01-02")
	var overdue, dueToday int
	for _, t := range m.tasks {
		if t.Status == protocol.StatusDone || t.DueDate == "" {
			continue
		}
		if m.role != "admin" && t.Assignee != m.username {
			continue
		}
		if t.DueDate < today {
			overdue++
		} else if t.DueDate == today {
			dueToday++
		}
	}
	if overdue > 0 {
		left += "  " + lipgloss.NewStyle().Foreground(cRed).Bold(true).Render(fmt.Sprintf("! %d overdue", overdue))
	}
	if dueToday > 0 {
		left += "  " + lipgloss.NewStyle().Foreground(cYellow).Bold(true).Render(fmt.Sprintf("~ %d due today", dueToday))
	}

	ver := mutedSt.Render(version.Version)
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(ver)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + ver
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
	m.tasksVP = viewport.New(w, clamp(ph-3, 2, ph))  // statsBar + sep + tabBar
	m.githubVP = viewport.New(w, clamp(ph-3, 2, ph)) // orgBar + sep + tabBar
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
	case "update":
		UpdateRequested = true
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
		m.statusMsg = "unknown: try  q  1-6  docs  update  sp v<n> [h<n>]  cl"
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

	case "/accept":
		if m.role != "admin" {
			m.statusMsg = "only admins can accept join requests"
			return nil
		}
		if len(fields) < 2 {
			m.statusMsg = "usage: /accept <username>"
			return nil
		}
		m.cl.SendJoinApprove(fields[1])
		m.statusMsg = "approved " + fields[1]
		return nil

	case "/reject":
		if m.role != "admin" {
			m.statusMsg = "only admins can reject join requests"
			return nil
		}
		if len(fields) < 2 {
			m.statusMsg = "usage: /reject <username>"
			return nil
		}
		m.cl.SendJoinReject(fields[1])
		m.statusMsg = "rejected " + fields[1]
		return nil

	case "/help":
		lines := []string{
			"",
			"  navigation:  1-6 switch screens   [ ] filter sub-tabs   esc go back",
			"",
			"  /done <id>              mark task done",
			"  /wip <id>               mark in progress",
			"  /blocked <id>           mark blocked",
			"  /todo <id>              reset to todo",
			"  /comment <id> <text>    add a comment to a task",
			"  /reply @user <text>     reply to last message from a user",
			"  /react <msg-id> approve|ack|reject   react to a result",
			"  /members                show team",
		}
		if m.role == "admin" {
			lines = append(lines,
				`  /assign @user "title" ["desc"] [priority] [due:YYYY-MM-DD]   create task`,
				"  /delete <id>            remove task",
				"  /accept <username>      approve a pending join request",
				"  /reject <username>      deny a pending join request",
			)
		}
		lines = append(lines,
			"  /github setup --token <token> --org <org>   configure github",
			"  /github refresh         reload github data",
			"  /exit                   quit wert",
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

	case "/comment":
		if len(fields) < 3 {
			m.statusMsg = "usage: /comment <task-id> <text>"
			return nil
		}
		task := m.findTaskByPrefix(fields[1])
		if task == nil {
			m.statusMsg = "task not found: " + fields[1]
			return nil
		}
		text := strings.Join(fields[2:], " ")
		p := protocol.TaskCommentPayload{
			Comment: protocol.TaskComment{
				TaskID:  task.ID,
				Content: text,
			},
		}
		if data, err := protocol.NewEnvelope(protocol.MsgTaskComment, p); err == nil {
			m.cl.Send <- data
		}
		m.statusMsg = "comment sent"

	case "/react":
		if len(fields) < 3 {
			m.statusMsg = "usage: /react <msg-id-prefix> approve|ack|reject"
			return nil
		}
		msgID := m.findMessageIDByPrefix(fields[1])
		if msgID == "" {
			m.statusMsg = "message not found: " + fields[1]
			return nil
		}
		reaction := fields[2]
		p := protocol.ResultReactionPayload{
			MessageID: msgID,
			Reaction:  reaction,
		}
		if data, err := protocol.NewEnvelope(protocol.MsgResultReaction, p); err == nil {
			m.cl.Send <- data
		}
		m.statusMsg = "reaction sent"

	case "/reply":
		// /reply @username <text>  — replies to the last message from that user
		if len(fields) < 3 {
			m.statusMsg = "usage: /reply @username <text>"
			return nil
		}
		target := strings.TrimPrefix(fields[1], "@")
		text := strings.Join(fields[2:], " ")
		// Find last message from target user.
		var replyToID, replyFrom string
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].From == target {
				replyToID = m.messages[i].ID
				replyFrom = m.messages[i].From
				break
			}
		}
		if replyToID == "" {
			m.statusMsg = "no messages found from @" + target
			return nil
		}
		p := protocol.ChatPayload{
			Message: protocol.ChatMessage{
				Content:   text,
				ReplyTo:   replyToID,
				ReplyFrom: replyFrom,
			},
		}
		if data, err := protocol.NewEnvelope(protocol.MsgChat, p); err == nil {
			m.cl.Send <- data
		}
		m.screen = scrChat

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
		m.pendingApproval = false
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
		// Upsert: update existing task if found (e.g. after comment/dep changes), otherwise append.
		found := false
		for i, t := range m.tasks {
			if t.ID == cp.ID {
				m.tasks[i] = &cp
				found = true
				break
			}
		}
		if !found {
			m.tasks = append(m.tasks, &cp)
			if cp.Assignee == m.username {
				m.statusMsg = fmt.Sprintf("* new task: %s", cp.Title)
			}
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
		if m.pendingApproval && strings.Contains(p.Message, "rejected") {
			m.pendingApproval = false
			m.joinRejected = true
			return m
		}
		m.statusMsg = "  " + p.Message

	case protocol.MsgJoinPending:
		m.pendingApproval = true

	case protocol.MsgJoinRequest:
		var p protocol.JoinRequestPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		m.pendingJoins = append(m.pendingJoins, p.Username)
		if !m.joinReqPopup {
			m.joinReqPopup = true
			m.joinReqCurrent = p.Username
		} else {
			m.joinReqQueue = append(m.joinReqQueue, p.Username)
		}

	case protocol.MsgJoinApprove:
		var p protocol.JoinApprovePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		m.removePendingJoin(p.Username)

	case protocol.MsgJoinReject:
		var p protocol.JoinRejectPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		m.removePendingJoin(p.Username)

	case protocol.MsgTaskClaim:
		var p protocol.TaskClaimPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		for _, t := range m.tasks {
			if t.ID == p.TaskID {
				t.ClaimedBy = p.ClaimedBy
				break
			}
		}

	case protocol.MsgTaskUnclaim:
		var p protocol.TaskClaimPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		for _, t := range m.tasks {
			if t.ID == p.TaskID {
				t.ClaimedBy = ""
				break
			}
		}

	case protocol.MsgAgentResult:
		var p protocol.AgentResultPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		cp := protocol.ChatMessage{
			ID:        p.Agent + "-result",
			From:      p.Agent,
			Content:   p.Content,
			Timestamp: p.Timestamp,
			IsAgent:   true,
			Kind:      "result",
			Meta:      p.Title,
		}
		m.messages = append(m.messages, &cp)
		if m.screen != scrChat {
			m.unreadChat++
			m.statusMsg = fmt.Sprintf("* result from %s: %s", p.Agent, p.Title)
		}

	case protocol.MsgDirectMsg:
		var p protocol.DirectMsgPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		// Show DM only if we are sender or recipient.
		if p.From != m.username && p.To != m.username {
			return m
		}
		cp := protocol.ChatMessage{
			ID:        p.ID,
			From:      p.From,
			Content:   p.Content,
			Timestamp: p.Timestamp,
			IsAgent:   true,
			Kind:      "dm",
			Meta:      p.To,
		}
		m.messages = append(m.messages, &cp)
		if m.screen != scrChat {
			m.unreadChat++
			m.statusMsg = fmt.Sprintf("* DM from %s", p.From)
		}

	case protocol.MsgTaskComment:
		var p protocol.TaskCommentPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		// The task is updated via MsgTaskCreate upsert; just show status notification.
		m.statusMsg = fmt.Sprintf("* comment on task by %s", p.Comment.Author)

	case protocol.MsgAgentHandoff:
		var p protocol.AgentHandoffPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		m.statusMsg = fmt.Sprintf("* %s handed off task to %s", p.From, p.To)
		// Inject as chat message so the handoff is visible in the chat log.
		shortTask := p.TaskID
		if len(shortTask) > 8 {
			shortTask = shortTask[:8]
		}
		content := fmt.Sprintf("[handoff] task %s → %s", shortTask, p.To)
		if p.Context != "" {
			content += ": " + p.Context
		}
		msg := &protocol.ChatMessage{
			ID:        p.TaskID + "-handoff-" + p.To,
			From:      p.From,
			Content:   content,
			Timestamp: p.Timestamp,
			IsAgent:   true,
			Kind:      "handoff",
			Meta:      p.To,
		}
		m.messages = append(m.messages, msg)
		if m.screen != scrChat {
			m.unreadChat++
		}

	case protocol.MsgResultReaction:
		var p protocol.ResultReactionPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		// Find the message and update its reactions.
		for _, msg := range m.messages {
			if msg.ID == p.MessageID {
				updated := false
				for i, r := range msg.Reactions {
					if r.Reactor == p.Reactor {
						msg.Reactions[i].Reaction = p.Reaction
						msg.Reactions[i].At = p.At
						updated = true
						break
					}
				}
				if !updated {
					msg.Reactions = append(msg.Reactions, protocol.ResultReaction{
						Reactor:  p.Reactor,
						Reaction: p.Reaction,
						At:       p.At,
					})
				}
				break
			}
		}
		m.statusMsg = fmt.Sprintf("* %s reacted %s", p.Reactor, p.Reaction)

	case protocol.MsgPipelineEvent:
		var p protocol.PipelineEventPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		m.statusMsg = fmt.Sprintf("* pipeline %q %s (step %d/%d → %s)", p.Name, p.Event, p.Step, p.Total, p.Agent)

	case protocol.MsgPipelineRun:
		var p protocol.PipelineRunPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		run := p.Run
		shortID := run.ID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		var label string
		switch p.Event {
		case "started":
			label = fmt.Sprintf("[pipeline:%s] %s started — %d steps: %s", shortID, run.Pipeline, len(run.Steps), strings.Join(run.Steps, " → "))
		case "advanced":
			label = fmt.Sprintf("[pipeline:%s] step %d/%d done → %s", shortID, run.CurrentStep, len(run.Steps), run.Steps[run.CurrentStep])
		case "done":
			label = fmt.Sprintf("[pipeline:%s] %s complete ✓ (%d steps)", shortID, run.Pipeline, len(run.Steps))
		case "cancelled":
			label = fmt.Sprintf("[pipeline:%s] %s cancelled at step %d/%d", shortID, run.Pipeline, run.CurrentStep, len(run.Steps))
		case "failed":
			label = fmt.Sprintf("[pipeline:%s] %s failed at step %d/%d", shortID, run.Pipeline, run.CurrentStep, len(run.Steps))
		default:
			label = fmt.Sprintf("[pipeline:%s] %s %s", shortID, run.Pipeline, p.Event)
		}
		msg := &protocol.ChatMessage{
			ID:        run.ID + "-run-" + p.Event,
			From:      "pipeline",
			Content:   label,
			Timestamp: run.UpdatedAt,
			IsAgent:   true,
			Kind:      "pipeline",
			Meta:      run.Pipeline,
		}
		m.messages = append(m.messages, msg)
		m.statusMsg = "* " + label
		if m.screen != scrChat {
			m.unreadChat++
		}

	case protocol.MsgAgentOnline:
		var p protocol.AgentOnlinePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		if p.Online {
			found := false
			for _, a := range m.agents {
				if a.Name == p.Agent.Name {
					*a = p.Agent
					found = true
					break
				}
			}
			if !found {
				cp := p.Agent
				m.agents = append(m.agents, &cp)
			}
			m.statusMsg = fmt.Sprintf("* agent %s connected", p.Agent.Name)
		} else {
			out := m.agents[:0]
			for _, a := range m.agents {
				if a.Name != p.Agent.Name {
					out = append(out, a)
				}
			}
			m.agents = out
			m.statusMsg = fmt.Sprintf("* agent %s disconnected", p.Agent.Name)
		}
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

// ── Join approval helpers ─────────────────────────────────────────────────────

func (m *Model) findTaskByPrefix(prefix string) *protocol.Task {
	for _, t := range m.tasks {
		if strings.HasPrefix(t.ID, prefix) {
			return t
		}
	}
	return nil
}

func (m *Model) findMessageIDByPrefix(prefix string) string {
	for i := len(m.messages) - 1; i >= 0; i-- {
		if strings.HasPrefix(m.messages[i].ID, prefix) {
			return m.messages[i].ID
		}
	}
	return ""
}

func (m *Model) removePendingJoin(username string) {
	out := m.pendingJoins[:0]
	for _, u := range m.pendingJoins {
		if u != username {
			out = append(out, u)
		}
	}
	m.pendingJoins = out
}

func (m *Model) advanceJoinQueue() {
	if len(m.joinReqQueue) == 0 {
		m.joinReqPopup = false
		m.joinReqCurrent = ""
		return
	}
	m.joinReqCurrent = m.joinReqQueue[0]
	m.joinReqQueue = m.joinReqQueue[1:]
}

// viewJoinPopup overlays a Y/N popup centred on base.
func (m Model) viewJoinPopup(base string) string {
	const boxW = 50
	const innerW = boxW - 2

	bSt := lipgloss.NewStyle().Foreground(cYellow)
	labelSt2 := lipgloss.NewStyle().Foreground(cFg).Bold(true)
	mutSt := lipgloss.NewStyle().Foreground(cMuted)

	title := " Join Request "
	dashes := innerW - len(title)
	lDash, rDash := dashes/2, dashes-dashes/2
	topLine := bSt.Render("╭"+strings.Repeat("─", lDash)) + labelSt2.Render(title) + bSt.Render(strings.Repeat("─", rDash)+"╮")

	empty := bSt.Render("│") + strings.Repeat(" ", innerW) + bSt.Render("│")

	user := m.joinReqCurrent
	msg := "  " + lipgloss.NewStyle().Foreground(cFg).Bold(true).Render(user) + mutSt.Render(" wants to join the workspace")
	msgPlain := "  " + user + " wants to join the workspace"
	msgPad := strings.Repeat(" ", innerW-len(msgPlain))
	userLine := bSt.Render("│") + msg + msgPad + bSt.Render("│")

	yKey := lipgloss.NewStyle().Background(cGreen).Foreground(lipgloss.Color("#000000")).Bold(true).Padding(0, 2).Render("Y  accept")
	nKey := lipgloss.NewStyle().Background(cRedDark).Foreground(cFg).Bold(true).Padding(0, 2).Render("N  reject")
	keys := "  " + yKey + "    " + nKey
	keysPlain := "  " + "Y  accept" + "    " + "N  reject"
	keysPad := strings.Repeat(" ", innerW-len(keysPlain)-4) // -4 for padding in badges
	keysLine := bSt.Render("│") + keys + keysPad + bSt.Render("│")

	var queueLine string
	if len(m.joinReqQueue) > 0 {
		ql := fmt.Sprintf("  %d more pending", len(m.joinReqQueue))
		queueLine = bSt.Render("│") + mutSt.Render(ql) + strings.Repeat(" ", innerW-len(ql)) + bSt.Render("│")
	} else {
		queueLine = empty
	}

	botLine := bSt.Render("╰" + strings.Repeat("─", innerW) + "╯")

	box := strings.Join([]string{topLine, empty, userLine, empty, keysLine, queueLine, botLine}, "\n")
	boxLines := strings.Split(box, "\n")
	boxH := len(boxLines)

	baseLines := strings.Split(base, "\n")
	total := len(baseLines)
	startRow := (total - boxH) / 2
	startCol := (m.width - boxW) / 2
	if startCol < 0 {
		startCol = 0
	}

	result := make([]string, total)
	copy(result, baseLines)
	for i, bl := range boxLines {
		row := startRow + i
		if row < 0 || row >= total {
			continue
		}
		visual := []rune(stripANSI(result[row]))
		for len(visual) < m.width {
			visual = append(visual, ' ')
		}
		leftEnd := startCol
		if leftEnd > len(visual) {
			leftEnd = len(visual)
		}
		right := ""
		if rs := startCol + boxW; rs < len(visual) {
			right = string(visual[rs:])
		}
		result[row] = string(visual[:leftEnd]) + bl + right
	}
	return strings.Join(result, "\n")
}

// viewWaitingApproval renders the animated waiting screen for pending members.
func (m Model) viewWaitingApproval() string {
	spinFrames := []string{"|", "/", "-", "\\", "|", "/", "-", "\\", "|", "/", "-", "\\"}
	spin := spinFrames[m.approvalFrame%len(spinFrames)]

	dotCount := (m.approvalFrame / 3) % 4
	dots := strings.Repeat(".", dotCount) + strings.Repeat(" ", 3-dotCount)

	bSt := lipgloss.NewStyle().Foreground(cYellow)
	mutSt := lipgloss.NewStyle().Foreground(cMuted)
	fgSt := lipgloss.NewStyle().Foreground(cFg).Bold(true)

	const boxW = 52
	const innerW = boxW - 2

	title := " wert "
	dashes := innerW - len(title)
	lD, rD := dashes/2, dashes-dashes/2
	top := bSt.Render("╭"+strings.Repeat("─", lD)) + fgSt.Render(title) + bSt.Render(strings.Repeat("─", rD)+"╮")
	bot := bSt.Render("╰" + strings.Repeat("─", innerW) + "╯")
	empty := bSt.Render("│") + strings.Repeat(" ", innerW) + bSt.Render("│")

	line1text := spin + "  waiting for admin approval" + dots
	line1pad := strings.Repeat(" ", innerW-2-len(line1text))
	line1 := bSt.Render("│") + "  " + bSt.Render(spin) + fgSt.Render("  waiting for admin approval") + mutSt.Render(dots) + line1pad + bSt.Render("│")

	line2text := "  admin must run:  /accept " + m.username
	line2pad := strings.Repeat(" ", innerW-len(line2text))
	line2 := bSt.Render("│") + mutSt.Render(line2text) + line2pad + bSt.Render("│")

	_ = line1text // suppress unused

	box := strings.Join([]string{top, empty, line1, line2, empty, bot}, "\n")

	pad := (m.height - 6) / 2
	if pad < 0 {
		pad = 0
	}
	centeredBox := lipgloss.NewStyle().Width(m.width).Align(lipgloss.Center).Render(box)
	return strings.Repeat("\n", pad) + centeredBox
}

// viewRejected renders the rejection screen.
func (m Model) viewRejected() string {
	bSt := lipgloss.NewStyle().Foreground(cRed)
	fgSt := lipgloss.NewStyle().Foreground(cFg).Bold(true)
	mutSt := lipgloss.NewStyle().Foreground(cMuted)

	const boxW = 46
	const innerW = boxW - 2

	title := " Access Denied "
	dashes := innerW - len(title)
	lD, rD := dashes/2, dashes-dashes/2
	top := bSt.Render("╭"+strings.Repeat("─", lD)) + fgSt.Render(title) + bSt.Render(strings.Repeat("─", rD)+"╮")
	bot := bSt.Render("╰" + strings.Repeat("─", innerW) + "╯")
	empty := bSt.Render("│") + strings.Repeat(" ", innerW) + bSt.Render("│")

	msg := "  your join request was rejected"
	line1 := bSt.Render("│") + bSt.Render(msg) + strings.Repeat(" ", innerW-len(msg)) + bSt.Render("│")
	hint := "  press Ctrl+C to exit"
	line2 := bSt.Render("│") + mutSt.Render(hint) + strings.Repeat(" ", innerW-len(hint)) + bSt.Render("│")

	box := strings.Join([]string{top, empty, line1, line2, empty, bot}, "\n")
	pad := (m.height - 6) / 2
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat("\n", pad) + lipgloss.NewStyle().Width(m.width).Align(lipgloss.Center).Render(box)
}

// approvalTick drives the member-side waiting animation.
func approvalTick() tea.Cmd {
	return tea.Tick(350*time.Millisecond, func(_ time.Time) tea.Msg {
		return approvalTickMsg{}
	})
}

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

