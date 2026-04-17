package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"wert/internal/client"
	"wert/internal/protocol"
)

// ── Colour palette ────────────────────────────────────────────────────────────
var (
	cRed     = lipgloss.Color("#E53935") // primary accent
	cGreen   = lipgloss.Color("#43A047") // secondary accent
	cRedDark = lipgloss.Color("#B71C1C") // header background
	cRedDim  = lipgloss.Color("#EF9A9A") // soft red for member names
	cGreenDim = lipgloss.Color("#A5D6A7") // soft green for self / done
	cMuted   = lipgloss.Color("#757575")
	cFg      = lipgloss.Color("#F5F5F5")
	cBorder  = lipgloss.Color("#424242")
	cActive  = lipgloss.Color("#616161")
)

// ── Styles ────────────────────────────────────────────────────────────────────
var (
	headerStyle = lipgloss.NewStyle().
			Background(cRedDark).
			Foreground(cFg).
			Bold(true).
			Padding(0, 2)

	logoStyle = lipgloss.NewStyle().
			Foreground(cFg).
			Bold(true)

	paneTitleActive = lipgloss.NewStyle().
			Bold(true).
			Foreground(cGreen).
			BorderStyle(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderForeground(cGreen).
			Padding(0, 1)

	paneTitleInactive = lipgloss.NewStyle().
				Bold(true).
				Foreground(cMuted).
				BorderStyle(lipgloss.NormalBorder()).
				BorderBottom(true).
				BorderForeground(cBorder).
				Padding(0, 1)

	paneBox = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cBorder)

	paneBoxActive = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(cRed)

	inputBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(cActive).
			Padding(0, 1)

	statusBar = lipgloss.NewStyle().
			Foreground(cMuted).
			Italic(true)

	todoSt    = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	wipSt     = lipgloss.NewStyle().Foreground(cGreen).Bold(true)
	doneSt    = lipgloss.NewStyle().Foreground(cGreenDim).Bold(true)
	blockedSt = lipgloss.NewStyle().Foreground(cRedDim).Bold(true)

	adminNameSt  = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	selfNameSt   = lipgloss.NewStyle().Foreground(cGreen).Bold(true)
	memberNameSt = lipgloss.NewStyle().Foreground(cRedDim).Bold(true)
	timeSt       = lipgloss.NewStyle().Foreground(cMuted)
	msgTextSt    = lipgloss.NewStyle().Foreground(cFg)

	highPriSt  = lipgloss.NewStyle().Foreground(cRed)
	medPriSt   = lipgloss.NewStyle().Foreground(cGreen)
	lowPriSt   = lipgloss.NewStyle().Foreground(cMuted)
)

// ── Tea messages ──────────────────────────────────────────────────────────────

type ServerMsg struct{ Env protocol.Envelope }
type DisconnectedMsg struct{ Err error }

// ── Model ─────────────────────────────────────────────────────────────────────

type Model struct {
	cl         *client.Client
	username   string
	role       string
	serverAddr string

	width  int
	height int

	activePane string // "tasks" | "chat"

	tasks    []*protocol.Task
	messages []*protocol.ChatMessage
	members  []*protocol.Member

	tasksVP  viewport.Model
	chatVP   viewport.Model
	input    textinput.Model

	statusMsg   string
	connected   bool
	initialized bool
}

func New(cl *client.Client, username, role, serverAddr string) *Model {
	ti := textinput.New()
	ti.Placeholder = "  message or /help for commands…"
	ti.Focus()

	return &Model{
		cl:         cl,
		username:   username,
		role:       role,
		serverAddr: serverAddr,
		activePane: "chat",
		input:      ti,
		tasks:      []*protocol.Task{},
		messages:   []*protocol.ChatMessage{},
		members:    []*protocol.Member{},
		connected:  true,
	}
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, waitForMsg(m.cl.Recv))
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
		if msg.Err != nil {
			m.statusMsg = "disconnected: " + msg.Err.Error()
		} else {
			m.statusMsg = "disconnected from server"
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+q":
			return m, tea.Quit

		case "tab":
			if m.activePane == "chat" {
				m.activePane = "tasks"
			} else {
				m.activePane = "chat"
			}
			return m, nil

		case "up", "down", "pgup", "pgdown":
			// Route scroll to active pane.
			if m.activePane == "tasks" {
				var cmd tea.Cmd
				m.tasksVP, cmd = m.tasksVP.Update(msg)
				return m, cmd
			}
			var cmd tea.Cmd
			m.chatVP, cmd = m.chatVP.Update(msg)
			return m, cmd

		case "enter":
			text := strings.TrimSpace(m.input.Value())
			m.input.SetValue("")
			if text == "" {
				return m, nil
			}
			cmd := m.handleText(text)
			return m, cmd

		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
	}

	return m, nil
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m Model) View() string {
	if m.width == 0 {
		return "loading…"
	}

	header := m.viewHeader()
	panes := m.viewPanes()
	status := m.viewStatus()
	inputArea := m.viewInput()

	return lipgloss.JoinVertical(lipgloss.Left, header, panes, status, inputArea)
}

func (m Model) viewHeader() string {
	role := "member"
	if m.role == "admin" {
		role = "admin ★"
	}
	online := 0
	for _, mem := range m.members {
		if mem.Online {
			online++
		}
	}

	left := logoStyle.Render("▶ wert")
	right := fmt.Sprintf("%s  •  %s  •  %d online  •  [tab] switch  [ctrl+q] quit",
		m.username, role, online)

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 4
	if gap < 0 {
		gap = 0
	}
	row := headerStyle.Width(m.width).Render(
		left + strings.Repeat(" ", gap) + right,
	)
	return row
}

func (m Model) viewPanes() string {
	leftW, rightW := m.paneSizes()
	paneH := m.paneContentHeight()

	// Tasks pane
	var taskTitle string
	if m.activePane == "tasks" {
		taskTitle = paneTitleActive.Render("  Tasks")
	} else {
		taskTitle = paneTitleInactive.Render("  Tasks")
	}
	taskContent := m.tasksVP.View()
	tasksInner := lipgloss.JoinVertical(lipgloss.Left, taskTitle, taskContent)
	var tasksBox string
	if m.activePane == "tasks" {
		tasksBox = paneBoxActive.Width(leftW).Height(paneH + 3).Render(tasksInner)
	} else {
		tasksBox = paneBox.Width(leftW).Height(paneH + 3).Render(tasksInner)
	}

	// Chat pane
	onlineNames := []string{}
	for _, mem := range m.members {
		if mem.Online {
			onlineNames = append(onlineNames, mem.Username)
		}
	}
	chatLabel := fmt.Sprintf("  Chat  (%s)", strings.Join(onlineNames, ", "))
	var chatTitle string
	if m.activePane == "chat" {
		chatTitle = paneTitleActive.Render(chatLabel)
	} else {
		chatTitle = paneTitleInactive.Render(chatLabel)
	}
	chatContent := m.chatVP.View()
	chatInner := lipgloss.JoinVertical(lipgloss.Left, chatTitle, chatContent)
	var chatBox string
	if m.activePane == "chat" {
		chatBox = paneBoxActive.Width(rightW).Height(paneH + 3).Render(chatInner)
	} else {
		chatBox = paneBox.Width(rightW).Height(paneH + 3).Render(chatInner)
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, tasksBox, chatBox)
}

func (m Model) viewStatus() string {
	prefix := ""
	if !m.connected {
		prefix = lipgloss.NewStyle().Foreground(cRed).Bold(true).Render("● OFFLINE  ")
	} else {
		prefix = lipgloss.NewStyle().Foreground(cGreen).Render("● ") +
			lipgloss.NewStyle().Foreground(cMuted).Render(m.serverAddr+"  ")
	}
	msg := statusBar.Render(m.statusMsg)
	return prefix + msg
}

func (m Model) viewInput() string {
	_, leftW := m.paneSizes()
	_ = leftW
	m.input.Width = m.width - 6
	return inputBox.Width(m.width - 2).Render(m.input.View())
}

// ── layout helpers ────────────────────────────────────────────────────────────

func (m Model) paneSizes() (left, right int) {
	total := m.width - 2 // account for join gap
	left = total * 4 / 10
	right = total - left
	return
}

func (m Model) paneContentHeight() int {
	headerH := 1
	statusH := 1
	inputH := 3
	titlesH := 2
	borders := 2
	h := m.height - headerH - statusH - inputH - titlesH - borders
	if h < 4 {
		h = 4
	}
	return h
}

func (m *Model) rebuildViewports() {
	leftW, rightW := m.paneSizes()
	ph := m.paneContentHeight()
	m.tasksVP = viewport.New(leftW-2, ph)
	m.chatVP = viewport.New(rightW-2, ph)
}

// ── content renderers ─────────────────────────────────────────────────────────

func (m *Model) refreshContent() {
	if m.width == 0 {
		return
	}
	if !m.initialized {
		m.rebuildViewports()
		m.initialized = true
	}
	m.tasksVP.SetContent(m.renderTasks())
	m.chatVP.SetContent(m.renderChat())
	m.chatVP.GotoBottom()
}

func (m Model) renderTasks() string {
	var sb strings.Builder
	tasks := m.visibleTasks()
	if len(tasks) == 0 {
		sb.WriteString(lipgloss.NewStyle().Foreground(cMuted).Italic(true).Render(
			"\n  no tasks assigned\n"))
		return sb.String()
	}

	leftW, _ := m.paneSizes()
	maxW := leftW - 4
	if maxW < 20 {
		maxW = 20
	}

	for _, t := range tasks {
		badge, badgeSt := statusBadge(t.Status)
		priStr := priLabel(t.Priority)
		shortID := shortID(t.ID)

		title := truncate(t.Title, maxW-16)

		line1 := fmt.Sprintf("  %s %s  %s",
			badgeSt.Render(badge),
			lipgloss.NewStyle().Foreground(cMuted).Render("#"+shortID),
			msgTextSt.Render(title),
		)
		sb.WriteString(line1 + "\n")

		if t.Description != "" {
			desc := truncate(t.Description, maxW-6)
			sb.WriteString("     " + lipgloss.NewStyle().Foreground(cMuted).Render(desc) + "\n")
		}

		meta := fmt.Sprintf("     %s  assignee:%s  by:%s",
			priStr,
			lipgloss.NewStyle().Foreground(cGreen).Render(t.Assignee),
			lipgloss.NewStyle().Foreground(cMuted).Render(t.UpdatedBy),
		)
		sb.WriteString(meta + "\n")
		sb.WriteString("\n")
	}
	return sb.String()
}

func (m Model) renderChat() string {
	var sb strings.Builder
	if len(m.messages) == 0 {
		sb.WriteString(lipgloss.NewStyle().Foreground(cMuted).Italic(true).Render(
			"\n  no messages yet\n"))
		return sb.String()
	}
	for _, msg := range m.messages {
		ts := timeSt.Render(msg.Timestamp.Format("15:04"))
		var nameSt lipgloss.Style
		switch {
		case msg.From == m.username:
			nameSt = selfNameSt
		case msg.From == "mcp":
			nameSt = lipgloss.NewStyle().Foreground(cGreen).Bold(true)
		default:
			// check if admin
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
		line := fmt.Sprintf("  %s  %s  %s",
			ts,
			nameSt.Render(msg.From+":"),
			msgTextSt.Render(msg.Content),
		)
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

func (m Model) visibleTasks() []*protocol.Task {
	if m.role == "admin" {
		return m.tasks
	}
	var out []*protocol.Task
	for _, t := range m.tasks {
		if t.Assignee == m.username {
			out = append(out, t)
		}
	}
	return out
}

// ── input handler ─────────────────────────────────────────────────────────────

func (m *Model) handleText(text string) tea.Cmd {
	if strings.HasPrefix(text, "/") {
		return m.handleCommand(text)
	}
	// plain chat message
	m.cl.SendChat(text)
	return nil
}

func (m *Model) handleCommand(text string) tea.Cmd {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}
	cmd := fields[0]

	switch cmd {
	case "/help":
		lines := []string{
			"── commands ──────────────────────────────────────────",
			"/done <id>         mark task done",
			"/wip <id>          mark task in progress",
			"/blocked <id>      mark task blocked",
			"/todo <id>         reset task to todo",
			"/members           list team members",
		}
		if m.role == "admin" {
			lines = append(lines,
				"/assign @user title [description] [priority]  create task",
				"/delete <id>       remove task",
				"/desc <id> text    update task description",
			)
		}
		m.statusMsg = strings.Join(lines, "  |  ")

	case "/done":
		m.updateStatus(fields, protocol.StatusDone)
	case "/wip":
		m.updateStatus(fields, protocol.StatusInProgress)
	case "/blocked":
		m.updateStatus(fields, protocol.StatusBlocked)
	case "/todo":
		m.updateStatus(fields, protocol.StatusTodo)

	case "/members":
		var names []string
		for _, mem := range m.members {
			dot := "○"
			if mem.Online {
				dot = "●"
			}
			names = append(names, dot+" "+mem.Username+"("+mem.Role+")")
		}
		m.statusMsg = strings.Join(names, "  ")

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

	case "/desc":
		m.statusMsg = "descriptions can be set via /assign or updated from the MCP server"

	default:
		m.statusMsg = "unknown command: " + cmd + " — type /help"
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

// handleAssign parses: /assign @user title words [description in quotes] [priority]
// simplified syntax: /assign @john "Fix login bug" ["description here"] [high|medium|low]
func (m *Model) handleAssign(text string) {
	// strip "/assign "
	rest := strings.TrimPrefix(text, "/assign ")
	rest = strings.TrimSpace(rest)

	// get @user
	parts := strings.SplitN(rest, " ", 2)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "@") {
		m.statusMsg = `usage: /assign @user "title" ["description"] [priority]`
		return
	}
	assignee := strings.TrimPrefix(parts[0], "@")
	remaining := strings.TrimSpace(parts[1])

	// parse quoted strings
	strs := parseQuoted(remaining)
	if len(strs) == 0 {
		m.statusMsg = `usage: /assign @user "title" ["description"] [priority]`
		return
	}
	title := strs[0]
	description := ""
	priority := "medium"
	if len(strs) >= 2 {
		description = strs[1]
	}
	if len(strs) >= 3 {
		p := strings.ToLower(strs[2])
		if p == "high" || p == "medium" || p == "low" {
			priority = p
		}
	}

	m.cl.SendTaskCreate(title, description, assignee, priority)
	m.statusMsg = fmt.Sprintf("task assigned to @%s", assignee)
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

	case protocol.MsgTaskCreate:
		var p protocol.TaskCreatePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return m
		}
		cp := p.Task
		m.tasks = append(m.tasks, &cp)
		// Show notification if task is for this user.
		if cp.Assignee == m.username {
			m.statusMsg = fmt.Sprintf("★ new task assigned to you: %s", cp.Title)
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
		m.statusMsg = "⚠  " + p.Message
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

// ── display helpers ───────────────────────────────────────────────────────────

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
		return highPriSt.Render("▲ high")
	case "low":
		return lowPriSt.Render("▼ low")
	default:
		return medPriSt.Render("◆ med")
	}
}

func shortID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n-1]) + "…"
}

// parseQuoted splits a string by spaces, treating "quoted sections" as one token.
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
