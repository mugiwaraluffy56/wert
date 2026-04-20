package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type WertMCP struct {
	serverURL string
	agentName string
	http      *http.Client
}

func New(serverURL, agentName string) *WertMCP {
	return &WertMCP{
		serverURL: strings.TrimRight(serverURL, "/"),
		agentName: agentName,
		http:      &http.Client{Timeout: 10 * time.Second},
	}
}

// internal types for JSON parsing
type task struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Assignee     string   `json:"assignee"`
	Status       string   `json:"status"`
	Priority     string   `json:"priority"`
	DueDate      string   `json:"due_date"`
	ClaimedBy    string   `json:"claimed_by,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	UpdatedBy    string   `json:"updated_by"`
}

type member struct {
	Username string    `json:"username"`
	Role     string    `json:"role"`
	Online   bool      `json:"online"`
	JoinedAt time.Time `json:"joined_at"`
}

type agentInfo struct {
	Name         string    `json:"name"`
	Capabilities []string  `json:"capabilities"`
	RegisteredAt time.Time `json:"registered_at"`
	Online       bool      `json:"online"`
}

func (t task) shortID() string {
	if len(t.ID) >= 8 {
		return t.ID[:8]
	}
	return t.ID
}

func (w *WertMCP) agentID() string {
	if w.agentName != "" {
		return w.agentName
	}
	return "claude"
}

func (w *WertMCP) Serve() error {
	// Auto-register agent capabilities if name is set.
	if w.agentName != "" {
		_ = w.registerSelf([]string{"task_management", "messaging", "code_review"})
	}

	s := server.NewMCPServer(
		"wert",
		"2.0.0",
		server.WithToolCapabilities(false),
	)

	// ── team_context ──────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("team_context",
			mcp.WithDescription(`Get full team context in one call: online members, task summary by person, and recent activity.
Use this first before doing any task management to understand the current state.
Returns structured markdown suitable for reasoning about what to do next.`),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			tasksBody, err := w.get("/api/tasks")
			if err != nil {
				return nil, fmt.Errorf("fetching tasks: %w", err)
			}
			membersBody, err := w.get("/api/members")
			if err != nil {
				return nil, fmt.Errorf("fetching members: %w", err)
			}

			var tasks []task
			var members []member
			if err := json.Unmarshal(tasksBody, &tasks); err != nil {
				return nil, fmt.Errorf("parsing tasks: %w", err)
			}
			if err := json.Unmarshal(membersBody, &members); err != nil {
				return nil, fmt.Errorf("parsing members: %w", err)
			}

			var sb strings.Builder
			sb.WriteString("# Team Context\n\n")

			sb.WriteString("## Team Members\n\n")
			for _, m := range members {
				status := "offline"
				if m.Online {
					status = "**online**"
				}
				sb.WriteString(fmt.Sprintf("- **%s** (%s) — %s\n", m.Username, m.Role, status))
			}
			sb.WriteString("\n")

			counts := map[string]int{"todo": 0, "in_progress": 0, "done": 0, "blocked": 0}
			for _, t := range tasks {
				counts[t.Status]++
			}
			sb.WriteString(fmt.Sprintf("## Task Summary (%d total)\n\n", len(tasks)))
			sb.WriteString(fmt.Sprintf("- In Progress: %d\n- Todo: %d\n- Blocked: %d\n- Done: %d\n\n",
				counts["in_progress"], counts["todo"], counts["blocked"], counts["done"]))

			byPerson := map[string][]task{}
			for _, t := range tasks {
				byPerson[t.Assignee] = append(byPerson[t.Assignee], t)
			}
			names := make([]string, 0, len(byPerson))
			for name := range byPerson {
				names = append(names, name)
			}
			sort.Strings(names)

			sb.WriteString("## Per-person breakdown\n\n")
			for _, name := range names {
				ts := byPerson[name]
				open := 0
				for _, t := range ts {
					if t.Status != "done" {
						open++
					}
				}
				sb.WriteString(fmt.Sprintf("### %s (%d open tasks)\n\n", name, open))
				for _, t := range ts {
					due := ""
					if t.DueDate != "" {
						due = fmt.Sprintf(" — due %s", t.DueDate)
					}
					claimed := ""
					if t.ClaimedBy != "" {
						claimed = fmt.Sprintf(" [claimed by %s]", t.ClaimedBy)
					}
					sb.WriteString(fmt.Sprintf("- [%s] `%s` %s (%s)%s%s\n",
						strings.ToUpper(t.Status), t.shortID(), t.Title, t.Priority, due, claimed))
				}
				sb.WriteString("\n")
			}

			return mcp.NewToolResultText(sb.String()), nil
		},
	)

	// ── list_tasks ────────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("list_tasks",
			mcp.WithDescription(`List tasks with optional filtering. Returns task IDs, titles, status, assignee, priority, due dates, and claimed_by.
Filter by assignee to see one person's workload. Filter by status to find blocked or in-progress tasks.
Task IDs are UUIDs — use the first 8 characters as a short ID for other tools.`),
			mcp.WithString("assignee", mcp.Description("Filter by exact username")),
			mcp.WithString("status", mcp.Description("Filter by status: todo | in_progress | done | blocked")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			q := "?"
			if a, _ := req.Params.Arguments["assignee"].(string); a != "" {
				q += "assignee=" + a + "&"
			}
			if s, _ := req.Params.Arguments["status"].(string); s != "" {
				q += "status=" + s + "&"
			}
			body, err := w.get("/api/tasks" + q)
			if err != nil {
				return nil, err
			}
			var tasks []task
			if err := json.Unmarshal(body, &tasks); err != nil {
				return nil, err
			}
			if len(tasks) == 0 {
				return mcp.NewToolResultText("No tasks found matching the filter."), nil
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("%d tasks:\n\n", len(tasks)))
			for _, t := range tasks {
				due := ""
				if t.DueDate != "" {
					due = fmt.Sprintf("  due:%s", t.DueDate)
				}
				claimed := ""
				if t.ClaimedBy != "" {
					claimed = fmt.Sprintf("  [claimed:%s]", t.ClaimedBy)
				}
				desc := ""
				if t.Description != "" {
					desc = fmt.Sprintf("\n    %s", t.Description)
				}
				sb.WriteString(fmt.Sprintf("- `%s`  [%s]  %s  →%s  @%s  (%s)%s%s%s\n",
					t.shortID(), strings.ToUpper(t.Status), t.Title, t.Priority, t.Assignee, t.ID, due, claimed, desc))
			}
			return mcp.NewToolResultText(sb.String()), nil
		},
	)

	// ── get_task ──────────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_task",
			mcp.WithDescription(`Get full details of a single task by its ID prefix.
Returns title, description, status, priority, assignee, due date, claimed_by, and update history.`),
			mcp.WithString("task_id", mcp.Required(), mcp.Description("Task ID or short 8-char prefix")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			taskID, _ := req.Params.Arguments["task_id"].(string)
			if taskID == "" {
				return nil, fmt.Errorf("task_id is required")
			}
			body, err := w.get("/api/tasks?")
			if err != nil {
				return nil, err
			}
			var tasks []task
			if err := json.Unmarshal(body, &tasks); err != nil {
				return nil, err
			}
			for _, t := range tasks {
				if strings.HasPrefix(t.ID, taskID) {
					var sb strings.Builder
					sb.WriteString(fmt.Sprintf("Task: %s\n\n", t.Title))
					sb.WriteString(fmt.Sprintf("ID:          %s  (short: %s)\n", t.ID, t.shortID()))
					sb.WriteString(fmt.Sprintf("Status:      %s\n", strings.ToUpper(t.Status)))
					sb.WriteString(fmt.Sprintf("Priority:    %s\n", t.Priority))
					sb.WriteString(fmt.Sprintf("Assignee:    @%s\n", t.Assignee))
					if t.ClaimedBy != "" {
						sb.WriteString(fmt.Sprintf("Claimed by:  %s\n", t.ClaimedBy))
					}
					if t.DueDate != "" {
						sb.WriteString(fmt.Sprintf("Due:         %s\n", t.DueDate))
					}
					if t.Description != "" {
						sb.WriteString(fmt.Sprintf("Description: %s\n", t.Description))
					}
					sb.WriteString(fmt.Sprintf("Updated:     %s by %s\n", t.UpdatedAt.Format("2006-01-02 15:04"), t.UpdatedBy))
					sb.WriteString(fmt.Sprintf("Created:     %s\n", t.CreatedAt.Format("2006-01-02 15:04")))
					return mcp.NewToolResultText(sb.String()), nil
				}
			}
			return nil, fmt.Errorf("task not found: %s", taskID)
		},
	)

	// ── search_tasks ──────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("search_tasks",
			mcp.WithDescription(`Search tasks by keyword across title and description.
Useful when you know part of a task name but not the ID. Returns matching task IDs and titles.`),
			mcp.WithString("query", mcp.Required(), mcp.Description("Search keyword (case-insensitive, matches title and description)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query, _ := req.Params.Arguments["query"].(string)
			if query == "" {
				return nil, fmt.Errorf("query is required")
			}
			body, err := w.get("/api/tasks?")
			if err != nil {
				return nil, err
			}
			var tasks []task
			if err := json.Unmarshal(body, &tasks); err != nil {
				return nil, err
			}
			lower := strings.ToLower(query)
			var matches []task
			for _, t := range tasks {
				if strings.Contains(strings.ToLower(t.Title), lower) ||
					strings.Contains(strings.ToLower(t.Description), lower) {
					matches = append(matches, t)
				}
			}
			if len(matches) == 0 {
				return mcp.NewToolResultText(fmt.Sprintf("No tasks match %q", query)), nil
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("%d match(es) for %q:\n\n", len(matches), query))
			for _, t := range matches {
				sb.WriteString(fmt.Sprintf("- `%s`  [%s]  %s  @%s\n",
					t.shortID(), strings.ToUpper(t.Status), t.Title, t.Assignee))
			}
			return mcp.NewToolResultText(sb.String()), nil
		},
	)

	// ── create_task ───────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("create_task",
			mcp.WithDescription(`Create a new task and assign it to a team member.
The task immediately appears on the assignee's terminal with a notification.
Use priority "high" for urgent issues, "low" for backlog items.
Include due_date (YYYY-MM-DD) for deadline-sensitive work.`),
			mcp.WithString("title", mcp.Required(), mcp.Description("Short, clear task title")),
			mcp.WithString("assignee", mcp.Required(), mcp.Description("Exact username of the person to assign to")),
			mcp.WithString("description", mcp.Description("Detailed description of what needs to be done")),
			mcp.WithString("priority", mcp.Description("low | medium | high  (default: medium)")),
			mcp.WithString("due_date", mcp.Description("Due date in YYYY-MM-DD format")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			title, _ := req.Params.Arguments["title"].(string)
			assignee, _ := req.Params.Arguments["assignee"].(string)
			description, _ := req.Params.Arguments["description"].(string)
			priority, _ := req.Params.Arguments["priority"].(string)
			dueDate, _ := req.Params.Arguments["due_date"].(string)
			if title == "" || assignee == "" {
				return nil, fmt.Errorf("title and assignee are required")
			}
			if priority == "" {
				priority = "medium"
			}
			payload := map[string]string{
				"title":       title,
				"assignee":    assignee,
				"description": description,
				"priority":    priority,
				"due_date":    dueDate,
				"created_by":  w.agentID(),
			}
			body, err := w.post("/api/tasks", payload)
			if err != nil {
				return nil, fmt.Errorf("creating task: %w", err)
			}
			var t task
			if err := json.Unmarshal(body, &t); err != nil {
				return mcp.NewToolResultText("Task created."), nil
			}
			msg := fmt.Sprintf("Task created — ID: `%s`\nTitle: %s\nAssigned to: @%s\nPriority: %s\n",
				t.shortID(), t.Title, t.Assignee, t.Priority)
			if t.DueDate != "" {
				msg += fmt.Sprintf("Due: %s\n", t.DueDate)
			}
			return mcp.NewToolResultText(msg), nil
		},
	)

	// ── update_task ───────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("update_task",
			mcp.WithDescription(`Update the status of a task.
Valid transitions: todo → in_progress → done (or → blocked at any point).
Use "done" when a task is complete. Use "blocked" if there's a dependency or blocker.
The change is broadcast live to all connected terminals.`),
			mcp.WithString("task_id", mcp.Required(), mcp.Description("Short 8-char task ID prefix (from list_tasks or team_context)")),
			mcp.WithString("status", mcp.Required(), mcp.Description("New status: todo | in_progress | done | blocked")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			taskID, _ := req.Params.Arguments["task_id"].(string)
			status, _ := req.Params.Arguments["status"].(string)
			if taskID == "" || status == "" {
				return nil, fmt.Errorf("task_id and status are required")
			}
			validStatuses := map[string]bool{"todo": true, "in_progress": true, "done": true, "blocked": true}
			if !validStatuses[status] {
				return nil, fmt.Errorf("invalid status %q — use: todo | in_progress | done | blocked", status)
			}
			payload := map[string]string{"status": status, "updated_by": w.agentID()}
			body, err := w.put("/api/tasks/"+taskID, payload)
			if err != nil {
				return nil, fmt.Errorf("updating task: %w", err)
			}
			var t task
			if err := json.Unmarshal(body, &t); err != nil {
				return mcp.NewToolResultText(fmt.Sprintf("Task %s updated to %s.", taskID, status)), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("Task `%s` updated: %s → **%s**\nTitle: %s\nAssignee: @%s\n",
				t.shortID(), taskID, strings.ToUpper(t.Status), t.Title, t.Assignee)), nil
		},
	)

	// ── delete_task ───────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("delete_task",
			mcp.WithDescription(`Permanently delete a task. This cannot be undone.
The deletion is broadcast live to all connected terminals.
Use update_task status=done instead if you just want to mark it complete.`),
			mcp.WithString("task_id", mcp.Required(), mcp.Description("Short 8-char task ID prefix")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			taskID, _ := req.Params.Arguments["task_id"].(string)
			if taskID == "" {
				return nil, fmt.Errorf("task_id is required")
			}
			if err := w.delete("/api/tasks/" + taskID); err != nil {
				return nil, fmt.Errorf("deleting task: %w", err)
			}
			return mcp.NewToolResultText(fmt.Sprintf("Task `%s` deleted.", taskID)), nil
		},
	)

	// ── list_members ──────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("list_members",
			mcp.WithDescription(`List all team members with their role and online status.
Use this to find valid assignee usernames before creating tasks.
Members must have connected at least once to appear here.`),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			body, err := w.get("/api/members")
			if err != nil {
				return nil, err
			}
			var members []member
			if err := json.Unmarshal(body, &members); err != nil {
				return nil, err
			}
			if len(members) == 0 {
				return mcp.NewToolResultText("No members have connected yet."), nil
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("%d team member(s):\n\n", len(members)))
			for _, m := range members {
				online := "offline"
				if m.Online {
					online = "ONLINE"
				}
				sb.WriteString(fmt.Sprintf("- **%s** (%s) — %s\n", m.Username, m.Role, online))
			}
			return mcp.NewToolResultText(sb.String()), nil
		},
	)

	// ── send_message ──────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("send_message",
			mcp.WithDescription(`Send a chat message to the team. Appears live in everyone's terminal.
Use this to notify the team about task completions, blockers, or code review requests.
The message supports @username mentions for notifications.`),
			mcp.WithString("content", mcp.Required(), mcp.Description("Message text. Supports @username mentions.")),
			mcp.WithString("from", mcp.Description("Sender display name (default: agent name or claude)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			content, _ := req.Params.Arguments["content"].(string)
			from, _ := req.Params.Arguments["from"].(string)
			if content == "" {
				return nil, fmt.Errorf("content is required")
			}
			if from == "" {
				from = w.agentID()
			}
			payload := map[string]string{"content": content, "from": from}
			_, err := w.post("/api/messages", payload)
			if err != nil {
				return nil, fmt.Errorf("sending message: %w", err)
			}
			return mcp.NewToolResultText(fmt.Sprintf("Message sent as %s: %q", from, content)), nil
		},
	)

	// ── get_dashboard ─────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_dashboard",
			mcp.WithDescription(`Get a complete team dashboard formatted as markdown.
Returns: online status, task counts by status, each person's tasks with IDs and priorities.
Use this for a quick overall status check or to generate a standup summary.`),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			tasksBody, err := w.get("/api/tasks")
			if err != nil {
				return nil, fmt.Errorf("fetching tasks: %w", err)
			}
			membersBody, err := w.get("/api/members")
			if err != nil {
				return nil, fmt.Errorf("fetching members: %w", err)
			}

			var tasks []task
			var members []member
			_ = json.Unmarshal(tasksBody, &tasks)
			_ = json.Unmarshal(membersBody, &members)

			var sb strings.Builder
			sb.WriteString("# wert Dashboard\n\n")
			sb.WriteString(fmt.Sprintf("_Generated %s_\n\n", time.Now().Format("2006-01-02 15:04")))

			sb.WriteString("## Team\n\n")
			for _, m := range members {
				dot := "○"
				if m.Online {
					dot = "●"
				}
				sb.WriteString(fmt.Sprintf("%s **%s** (%s)\n", dot, m.Username, m.Role))
			}
			sb.WriteString("\n")

			counts := map[string]int{}
			for _, t := range tasks {
				counts[t.Status]++
			}
			sb.WriteString(fmt.Sprintf("## Summary: %d tasks total\n\n", len(tasks)))
			sb.WriteString(fmt.Sprintf("| Status | Count |\n|--------|-------|\n"))
			sb.WriteString(fmt.Sprintf("| In Progress | %d |\n", counts["in_progress"]))
			sb.WriteString(fmt.Sprintf("| Todo | %d |\n", counts["todo"]))
			sb.WriteString(fmt.Sprintf("| Blocked | %d |\n", counts["blocked"]))
			sb.WriteString(fmt.Sprintf("| Done | %d |\n\n", counts["done"]))

			byMember := map[string][]task{}
			for _, t := range tasks {
				byMember[t.Assignee] = append(byMember[t.Assignee], t)
			}
			names := make([]string, 0, len(byMember))
			for name := range byMember {
				names = append(names, name)
			}
			sort.Strings(names)

			sb.WriteString("## Tasks by person\n\n")
			for _, name := range names {
				ts := byMember[name]
				sb.WriteString(fmt.Sprintf("### @%s\n\n", name))
				sb.WriteString("| ID | Status | Title | Priority | Due | Claimed |\n")
				sb.WriteString("|-----|--------|-------|----------|-----|--------|\n")
				for _, t := range ts {
					sb.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s | %s | %s |\n",
						t.shortID(), t.Status, t.Title, t.Priority, t.DueDate, t.ClaimedBy))
				}
				sb.WriteString("\n")
			}
			return mcp.NewToolResultText(sb.String()), nil
		},
	)

	// ── claim_task ────────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("claim_task",
			mcp.WithDescription(`Claim a task so other agents know you're working on it.
Prevents two agents from working on the same task simultaneously.
Returns an error if the task is already claimed by a different agent.
Always unclaim the task when done or if you're abandoning it.`),
			mcp.WithString("task_id", mcp.Required(), mcp.Description("Short 8-char task ID prefix")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			taskID, _ := req.Params.Arguments["task_id"].(string)
			if taskID == "" {
				return nil, fmt.Errorf("task_id is required")
			}
			payload := map[string]string{"agent": w.agentID()}
			body, err := w.post("/api/tasks/"+taskID+"/claim", payload)
			if err != nil {
				return nil, fmt.Errorf("claiming task: %w", err)
			}
			var t task
			if err := json.Unmarshal(body, &t); err != nil {
				return mcp.NewToolResultText(fmt.Sprintf("Task `%s` claimed.", taskID)), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("Task `%s` claimed by %s: %s", t.shortID(), t.ClaimedBy, t.Title)), nil
		},
	)

	// ── unclaim_task ──────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("unclaim_task",
			mcp.WithDescription(`Release a task claim so other agents can pick it up.
Call this when you finish a task, abandon it, or hand off to another agent.
You can only unclaim tasks that you previously claimed.`),
			mcp.WithString("task_id", mcp.Required(), mcp.Description("Short 8-char task ID prefix")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			taskID, _ := req.Params.Arguments["task_id"].(string)
			if taskID == "" {
				return nil, fmt.Errorf("task_id is required")
			}
			payload := map[string]string{"agent": w.agentID()}
			body, err := w.post("/api/tasks/"+taskID+"/unclaim", payload)
			if err != nil {
				return nil, fmt.Errorf("unclaiming task: %w", err)
			}
			var t task
			if err := json.Unmarshal(body, &t); err != nil {
				return mcp.NewToolResultText(fmt.Sprintf("Task `%s` unclaimed.", taskID)), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("Task `%s` unclaimed: %s", t.shortID(), t.Title)), nil
		},
	)

	// ── wait_for_change ───────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("wait_for_change",
			mcp.WithDescription(`Block until a team event occurs, then return it.
Use this to efficiently react to changes without polling: wait for a task update, new message, member joining, etc.
Useful for agent-to-agent coordination: one agent posts a result, another is woken up to consume it.
Times out after the specified seconds (default 30). Returns the event envelope on success.`),
			mcp.WithString("filter", mcp.Description("Comma-separated event types to wait for, e.g. 'task_update,agent_result'. Empty = any event.")),
			mcp.WithNumber("timeout", mcp.Description("Seconds to wait before timing out (default 30, max 120)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			filter, _ := req.Params.Arguments["filter"].(string)
			timeoutSec := 30.0
			if t, ok := req.Params.Arguments["timeout"].(float64); ok && t > 0 {
				timeoutSec = t
				if timeoutSec > 120 {
					timeoutSec = 120
				}
			}

			url := w.serverURL + "/api/watch"
			if filter != "" {
				url += "?filter=" + filter
			}

			// Use a client with a longer timeout for streaming.
			streamClient := &http.Client{Timeout: time.Duration(timeoutSec+5) * time.Second}
			reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
			defer cancel()

			httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
			if err != nil {
				return nil, fmt.Errorf("building request: %w", err)
			}
			httpReq.Header.Set("Accept", "text/event-stream")

			resp, err := streamClient.Do(httpReq)
			if err != nil {
				if reqCtx.Err() != nil {
					return mcp.NewToolResultText("timeout: no matching event within the wait period"), nil
				}
				return nil, fmt.Errorf("connecting to watch stream: %w", err)
			}
			defer resp.Body.Close()

			scanner := bufio.NewScanner(resp.Body)
			for scanner.Scan() {
				line := scanner.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				data := strings.TrimPrefix(line, "data: ")
				// Pretty-print the event.
				var pretty map[string]interface{}
				if err := json.Unmarshal([]byte(data), &pretty); err != nil {
					return mcp.NewToolResultText("event received: " + data), nil
				}
				out, _ := json.MarshalIndent(pretty, "", "  ")
				return mcp.NewToolResultText("event received:\n\n```json\n" + string(out) + "\n```"), nil
			}
			return mcp.NewToolResultText("timeout: no matching event within the wait period"), nil
		},
	)

	// ── send_direct_message ───────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("send_direct_message",
			mcp.WithDescription(`Send a private message to a specific agent or team member.
The message is delivered only to the recipient — not broadcast to the whole team.
Use this for agent-to-agent task handoffs, private instructions, or sensitive feedback.`),
			mcp.WithString("to", mcp.Required(), mcp.Description("Username or agent name of the recipient")),
			mcp.WithString("content", mcp.Required(), mcp.Description("Message content")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			to, _ := req.Params.Arguments["to"].(string)
			content, _ := req.Params.Arguments["content"].(string)
			if to == "" || content == "" {
				return nil, fmt.Errorf("to and content are required")
			}
			payload := map[string]string{
				"from":    w.agentID(),
				"to":      to,
				"content": content,
			}
			_, err := w.post("/api/direct", payload)
			if err != nil {
				return nil, fmt.Errorf("sending direct message: %w", err)
			}
			return mcp.NewToolResultText(fmt.Sprintf("Direct message sent to %s.", to)), nil
		},
	)

	// ── post_result ───────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("post_result",
			mcp.WithDescription(`Post a structured AI result to the team chat.
Results appear in a distinct format that separates agent output from regular chat.
Use this to publish analysis, summaries, code reviews, or any structured output.
Optionally associate the result with a specific task ID.
If this result is a step in an autonomous pipeline run, pass the pipeline_run_id — the server
will automatically forward the result to the next agent in the pipeline.`),
			mcp.WithString("content", mcp.Required(), mcp.Description("The result content (markdown supported)")),
			mcp.WithString("title", mcp.Description("Short title for the result (default: 'Result')")),
			mcp.WithString("task_id", mcp.Description("Associate this result with a task short ID")),
			mcp.WithString("pipeline_run_id", mcp.Description("Pipeline run ID received in the step DM — triggers automatic advance to the next agent")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			content, _ := req.Params.Arguments["content"].(string)
			title, _ := req.Params.Arguments["title"].(string)
			taskID, _ := req.Params.Arguments["task_id"].(string)
			runID, _ := req.Params.Arguments["pipeline_run_id"].(string)
			if content == "" {
				return nil, fmt.Errorf("content is required")
			}
			if title == "" {
				title = "Result"
			}
			payload := map[string]string{
				"agent":           w.agentID(),
				"task_id":         taskID,
				"title":           title,
				"content":         content,
				"pipeline_run_id": runID,
			}
			_, err := w.post("/api/results", payload)
			if err != nil {
				return nil, fmt.Errorf("posting result: %w", err)
			}
			msg := fmt.Sprintf("Result posted: %q (%d chars)", title, len(content))
			if runID != "" {
				msg += fmt.Sprintf("\nPipeline run %s advanced to next step.", runID[:8])
			}
			return mcp.NewToolResultText(msg), nil
		},
	)

	// ── register_capabilities ─────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("register_capabilities",
			mcp.WithDescription(`Register or update this agent's capabilities in the team registry.
Other agents can discover you via list_agents and route work based on capabilities.
Common capabilities: code_review, testing, documentation, deployment, analysis.
Registration is lost when the MCP server process exits unless auto-registered on startup.`),
			mcp.WithString("capabilities", mcp.Required(), mcp.Description("Comma-separated capability tags, e.g. 'code_review,testing,analysis'")),
			mcp.WithString("name", mcp.Description("Override agent name (default: agent name from --agent-name flag)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			capsStr, _ := req.Params.Arguments["capabilities"].(string)
			if capsStr == "" {
				return nil, fmt.Errorf("capabilities is required")
			}
			name, _ := req.Params.Arguments["name"].(string)
			if name == "" {
				name = w.agentID()
			}
			var caps []string
			for _, c := range strings.Split(capsStr, ",") {
				c = strings.TrimSpace(c)
				if c != "" {
					caps = append(caps, c)
				}
			}
			payload := map[string]interface{}{
				"name":         name,
				"capabilities": caps,
			}
			body, err := w.post("/api/agents", payload)
			if err != nil {
				return nil, fmt.Errorf("registering capabilities: %w", err)
			}
			var info agentInfo
			if err := json.Unmarshal(body, &info); err != nil {
				return mcp.NewToolResultText(fmt.Sprintf("Agent %q registered.", name)), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("Agent %q registered with capabilities: %s",
				info.Name, strings.Join(info.Capabilities, ", "))), nil
		},
	)

	// ── list_agents ───────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("list_agents",
			mcp.WithDescription(`List all registered AI agents and their capabilities.
Use this to discover what other agents are available and what they can do.
Useful before sending a direct message or delegating a subtask.
Agents are registered at startup and unregistered when they disconnect.`),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			body, err := w.get("/api/agents")
			if err != nil {
				return nil, fmt.Errorf("fetching agents: %w", err)
			}
			var agents []agentInfo
			if err := json.Unmarshal(body, &agents); err != nil {
				return nil, fmt.Errorf("parsing agents: %w", err)
			}
			if len(agents) == 0 {
				return mcp.NewToolResultText("No agents currently registered."), nil
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("%d registered agent(s):\n\n", len(agents)))
			for _, a := range agents {
				caps := "none"
				if len(a.Capabilities) > 0 {
					caps = strings.Join(a.Capabilities, ", ")
				}
				sb.WriteString(fmt.Sprintf("- **%s** — capabilities: %s  (registered %s)\n",
					a.Name, caps, a.RegisteredAt.Format("15:04")))
			}
			return mcp.NewToolResultText(sb.String()), nil
		},
	)

	// ── add_task_comment ──────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("add_task_comment",
			mcp.WithDescription(`Add a comment to a task. Comments appear on the task and are visible to all team members.
Use this to leave analysis notes, blockers, decisions, or progress updates on a specific task.
Comments are persistent and attached to the task, unlike chat messages.`),
			mcp.WithString("task_id", mcp.Required(), mcp.Description("Short 8-char task ID prefix")),
			mcp.WithString("content", mcp.Required(), mcp.Description("Comment text")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			taskID, _ := req.Params.Arguments["task_id"].(string)
			content, _ := req.Params.Arguments["content"].(string)
			if taskID == "" || content == "" {
				return nil, fmt.Errorf("task_id and content are required")
			}
			payload := map[string]interface{}{
				"author":   w.agentID(),
				"content":  content,
				"is_agent": true,
			}
			body, err := w.post("/api/tasks/"+taskID+"/comments", payload)
			if err != nil {
				return nil, fmt.Errorf("adding comment: %w", err)
			}
			var comment struct {
				ID        string `json:"id"`
				Author    string `json:"author"`
				Content   string `json:"content"`
				Timestamp string `json:"timestamp"`
			}
			if err := json.Unmarshal(body, &comment); err != nil {
				return mcp.NewToolResultText("Comment added."), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("Comment added to task `%s` by %s.", taskID, comment.Author)), nil
		},
	)

	// ── get_task_comments ─────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_task_comments",
			mcp.WithDescription(`Get all comments on a task, ordered chronologically.
Use this to read analysis notes, blockers, and discussion threads attached to a specific task.`),
			mcp.WithString("task_id", mcp.Required(), mcp.Description("Short 8-char task ID prefix")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			taskID, _ := req.Params.Arguments["task_id"].(string)
			if taskID == "" {
				return nil, fmt.Errorf("task_id is required")
			}
			body, err := w.get("/api/tasks/" + taskID + "/comments")
			if err != nil {
				return nil, fmt.Errorf("fetching comments: %w", err)
			}
			var comments []struct {
				ID        string    `json:"id"`
				Author    string    `json:"author"`
				Content   string    `json:"content"`
				Timestamp time.Time `json:"timestamp"`
				IsAgent   bool      `json:"is_agent"`
			}
			if err := json.Unmarshal(body, &comments); err != nil {
				return nil, fmt.Errorf("parsing comments: %w", err)
			}
			if len(comments) == 0 {
				return mcp.NewToolResultText(fmt.Sprintf("No comments on task `%s`.", taskID)), nil
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("%d comment(s) on task `%s`:\n\n", len(comments), taskID))
			for _, c := range comments {
				agent := ""
				if c.IsAgent {
					agent = " [agent]"
				}
				sb.WriteString(fmt.Sprintf("**%s**%s — %s\n%s\n\n",
					c.Author, agent, c.Timestamp.Format("15:04 Jan 2"), c.Content))
			}
			return mcp.NewToolResultText(sb.String()), nil
		},
	)

	// ── add_dependency ────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("add_dependency",
			mcp.WithDescription(`Mark a task as depending on another task.
This means the first task should not be started until the dependency is done.
Dependencies are visible in the task list and help the team understand blockers.`),
			mcp.WithString("task_id", mcp.Required(), mcp.Description("Short 8-char ID of the task that has a dependency")),
			mcp.WithString("depends_on", mcp.Required(), mcp.Description("Short 8-char ID of the task it depends on")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			taskID, _ := req.Params.Arguments["task_id"].(string)
			dependsOn, _ := req.Params.Arguments["depends_on"].(string)
			if taskID == "" || dependsOn == "" {
				return nil, fmt.Errorf("task_id and depends_on are required")
			}
			payload := map[string]string{"depends_on": dependsOn}
			body, err := w.post("/api/tasks/"+taskID+"/dependencies", payload)
			if err != nil {
				return nil, fmt.Errorf("adding dependency: %w", err)
			}
			var t task
			if err := json.Unmarshal(body, &t); err != nil {
				return mcp.NewToolResultText(fmt.Sprintf("Dependency added: `%s` depends on `%s`.", taskID, dependsOn)), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("Task `%s` (%s) now depends on `%s`. Total deps: %d.",
				t.shortID(), t.Title, dependsOn, len(t.Dependencies))), nil
		},
	)

	// ── remove_dependency ─────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("remove_dependency",
			mcp.WithDescription(`Remove a dependency relationship between two tasks.`),
			mcp.WithString("task_id", mcp.Required(), mcp.Description("Short 8-char ID of the task")),
			mcp.WithString("depends_on", mcp.Required(), mcp.Description("Short 8-char ID of the dependency to remove")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			taskID, _ := req.Params.Arguments["task_id"].(string)
			dependsOn, _ := req.Params.Arguments["depends_on"].(string)
			if taskID == "" || dependsOn == "" {
				return nil, fmt.Errorf("task_id and depends_on are required")
			}
			payload := map[string]string{"depends_on": dependsOn}
			body, err := w.deleteWithBody("/api/tasks/"+taskID+"/dependencies", payload)
			if err != nil {
				return nil, fmt.Errorf("removing dependency: %w", err)
			}
			var t task
			if err := json.Unmarshal(body, &t); err != nil {
				return mcp.NewToolResultText(fmt.Sprintf("Dependency removed from `%s`.", taskID)), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("Dependency removed. Task `%s` now has %d dep(s).", t.shortID(), len(t.Dependencies))), nil
		},
	)

	// ── set_context ───────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("set_context",
			mcp.WithDescription(`Write a key-value entry to the shared agent scratchpad.
The scratchpad is a persistent key-value store visible to all agents.
Use it to share state between agents without polluting team chat.
Example keys: "current_sprint_goal", "deploy_target", "review_queue".`),
			mcp.WithString("key", mcp.Required(), mcp.Description("The key to set")),
			mcp.WithString("value", mcp.Required(), mcp.Description("The value to store")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			key, _ := req.Params.Arguments["key"].(string)
			value, _ := req.Params.Arguments["value"].(string)
			if key == "" {
				return nil, fmt.Errorf("key is required")
			}
			payload := map[string]string{"key": key, "value": value}
			_, err := w.post("/api/context", payload)
			if err != nil {
				return nil, fmt.Errorf("setting context: %w", err)
			}
			return mcp.NewToolResultText(fmt.Sprintf("Context set: `%s` = %q", key, value)), nil
		},
	)

	// ── get_context ───────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_context",
			mcp.WithDescription(`Read from the shared agent scratchpad.
Pass a specific key to get one value, or omit key to list all entries.
The scratchpad is shared across all agents and persists between sessions.`),
			mcp.WithString("key", mcp.Description("Specific key to retrieve (omit to list all)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			key, _ := req.Params.Arguments["key"].(string)
			if key != "" {
				body, err := w.get("/api/context?key=" + key)
				if err != nil {
					return nil, fmt.Errorf("getting context: %w", err)
				}
				var entry struct {
					Key   string `json:"key"`
					Value string `json:"value"`
				}
				if err := json.Unmarshal(body, &entry); err != nil {
					return mcp.NewToolResultText(string(body)), nil
				}
				return mcp.NewToolResultText(fmt.Sprintf("`%s` = %q", entry.Key, entry.Value)), nil
			}
			body, err := w.get("/api/context")
			if err != nil {
				return nil, fmt.Errorf("getting context: %w", err)
			}
			var all map[string]string
			if err := json.Unmarshal(body, &all); err != nil {
				return nil, fmt.Errorf("parsing context: %w", err)
			}
			if len(all) == 0 {
				return mcp.NewToolResultText("Scratchpad is empty."), nil
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("%d scratchpad entry(ies):\n\n", len(all)))
			for k, v := range all {
				sb.WriteString(fmt.Sprintf("- `%s` = %q\n", k, v))
			}
			return mcp.NewToolResultText(sb.String()), nil
		},
	)

	// ── hand_off_task ─────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("hand_off_task",
			mcp.WithDescription(`Hand off a task to another agent, passing context about what was done and what remains.
The receiving agent gets a direct message with the handoff details.
A handoff event is broadcast to the whole team.
Use this instead of send_direct_message when ownership of a task is transferring.`),
			mcp.WithString("task_id", mcp.Required(), mcp.Description("Short 8-char task ID to hand off")),
			mcp.WithString("to", mcp.Required(), mcp.Description("Agent or member name to hand off to")),
			mcp.WithString("context", mcp.Description("What was done, what remains, and any relevant state")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			taskID, _ := req.Params.Arguments["task_id"].(string)
			to, _ := req.Params.Arguments["to"].(string)
			context, _ := req.Params.Arguments["context"].(string)
			if taskID == "" || to == "" {
				return nil, fmt.Errorf("task_id and to are required")
			}
			payload := map[string]string{
				"from":    w.agentID(),
				"to":      to,
				"context": context,
			}
			_, err := w.post("/api/tasks/"+taskID+"/handoff", payload)
			if err != nil {
				return nil, fmt.Errorf("handing off task: %w", err)
			}
			return mcp.NewToolResultText(fmt.Sprintf("Task `%s` handed off to %s.", taskID, to)), nil
		},
	)

	// ── react_to_result ───────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("react_to_result",
			mcp.WithDescription(`React to an agent result message with approve, ack, or reject.
Reactions create a simple review loop: one agent posts a result, another approves or requests changes.
The message_id comes from the post_result response or from wait_for_change events.
Each agent/member can have one reaction per message; reacting again updates it.`),
			mcp.WithString("message_id", mcp.Required(), mcp.Description("Full message ID from the agent result")),
			mcp.WithString("reaction", mcp.Required(), mcp.Description("approve | ack | reject")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			msgID, _ := req.Params.Arguments["message_id"].(string)
			reaction, _ := req.Params.Arguments["reaction"].(string)
			if msgID == "" || reaction == "" {
				return nil, fmt.Errorf("message_id and reaction are required")
			}
			validReactions := map[string]bool{"approve": true, "ack": true, "reject": true}
			if !validReactions[reaction] {
				return nil, fmt.Errorf("invalid reaction %q — use: approve | ack | reject", reaction)
			}
			payload := map[string]string{
				"reactor":  w.agentID(),
				"reaction": reaction,
			}
			_, err := w.post("/api/results/"+msgID+"/react", payload)
			if err != nil {
				return nil, fmt.Errorf("reacting: %w", err)
			}
			return mcp.NewToolResultText(fmt.Sprintf("Reacted %q to message %s.", reaction, msgID[:8])), nil
		},
	)

	// ── register_pipeline ─────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("register_pipeline",
			mcp.WithDescription(`Register a named agent pipeline — an ordered list of agents that process a task in sequence.
When triggered, the first agent in the pipeline receives a direct message with the task and context.
Pipelines are in-memory and must be re-registered on restart.
Example: "review-deploy" with steps ["reviewer", "deployer"].`),
			mcp.WithString("name", mcp.Required(), mcp.Description("Pipeline name (e.g. review-deploy)")),
			mcp.WithString("steps", mcp.Required(), mcp.Description("Comma-separated agent names in order (e.g. reviewer,deployer)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			name, _ := req.Params.Arguments["name"].(string)
			stepsStr, _ := req.Params.Arguments["steps"].(string)
			if name == "" || stepsStr == "" {
				return nil, fmt.Errorf("name and steps are required")
			}
			var steps []string
			for _, s := range strings.Split(stepsStr, ",") {
				s = strings.TrimSpace(s)
				if s != "" {
					steps = append(steps, s)
				}
			}
			payload := map[string]interface{}{"name": name, "steps": steps}
			body, err := w.post("/api/pipelines", payload)
			if err != nil {
				return nil, fmt.Errorf("registering pipeline: %w", err)
			}
			var info struct {
				Name  string   `json:"name"`
				Steps []string `json:"steps"`
			}
			if err := json.Unmarshal(body, &info); err != nil {
				return mcp.NewToolResultText(fmt.Sprintf("Pipeline %q registered.", name)), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("Pipeline %q registered with %d step(s): %s",
				info.Name, len(info.Steps), strings.Join(info.Steps, " → "))), nil
		},
	)

	// ── trigger_pipeline ──────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("trigger_pipeline",
			mcp.WithDescription(`Trigger a registered pipeline for a task.
Creates an autonomous pipeline run: each agent receives a DM with the run_id and previous
result, then automatically advances to the next agent when it calls post_result with that run_id.
No manual handoff needed — the server orchestrates the full chain.
Returns the run_id which agents use in post_result to advance the run.`),
			mcp.WithString("name", mcp.Required(), mcp.Description("Pipeline name to trigger")),
			mcp.WithString("task_id", mcp.Description("Task ID associated with this pipeline run")),
			mcp.WithString("context", mcp.Description("Context to pass to the first agent")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			name, _ := req.Params.Arguments["name"].(string)
			taskID, _ := req.Params.Arguments["task_id"].(string)
			context, _ := req.Params.Arguments["context"].(string)
			if name == "" {
				return nil, fmt.Errorf("name is required")
			}
			payload := map[string]string{
				"task_id":      taskID,
				"context":      context,
				"triggered_by": w.agentID(),
			}
			body, err := w.post("/api/pipelines/"+name+"/trigger", payload)
			if err != nil {
				return nil, fmt.Errorf("triggering pipeline: %w", err)
			}
			var result struct {
				Pipeline   string `json:"pipeline"`
				RunID      string `json:"run_id"`
				Steps      int    `json:"steps"`
				FirstAgent string `json:"first_agent"`
			}
			if err := json.Unmarshal(body, &result); err != nil {
				return mcp.NewToolResultText(fmt.Sprintf("Pipeline %q triggered.", name)), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf(
				"Pipeline %q triggered — run_id: %s\n%d steps, starting with %s.\nAgents must call post_result with pipeline_run_id=%s to auto-advance.",
				result.Pipeline, result.RunID, result.Steps, result.FirstAgent, result.RunID,
			)), nil
		},
	)

	// ── get_pipeline_run ──────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_pipeline_run",
			mcp.WithDescription(`Get the current state of a pipeline run: which step it's on, status, and step results so far.`),
			mcp.WithString("run_id", mcp.Required(), mcp.Description("Pipeline run ID returned by trigger_pipeline")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			runID, _ := req.Params.Arguments["run_id"].(string)
			if runID == "" {
				return nil, fmt.Errorf("run_id is required")
			}
			body, err := w.get("/api/pipeline-runs/" + runID)
			if err != nil {
				return nil, fmt.Errorf("fetching run: %w", err)
			}
			var run struct {
				ID          string   `json:"id"`
				Pipeline    string   `json:"pipeline"`
				Steps       []string `json:"steps"`
				CurrentStep int      `json:"current_step"`
				Status      string   `json:"status"`
				TaskID      string   `json:"task_id"`
				StepResults []string `json:"step_results"`
				StartedBy   string   `json:"started_by"`
			}
			if err := json.Unmarshal(body, &run); err != nil {
				return nil, fmt.Errorf("parsing run: %w", err)
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Pipeline run: %s\n\n", run.ID))
			sb.WriteString(fmt.Sprintf("Pipeline: %s\n", run.Pipeline))
			sb.WriteString(fmt.Sprintf("Status:   %s\n", strings.ToUpper(run.Status)))
			sb.WriteString(fmt.Sprintf("Progress: step %d/%d\n", run.CurrentStep, len(run.Steps)))
			if run.TaskID != "" {
				sb.WriteString(fmt.Sprintf("Task:     %s\n", run.TaskID))
			}
			sb.WriteString(fmt.Sprintf("Steps:    %s\n\n", strings.Join(run.Steps, " → ")))
			if len(run.StepResults) > 0 {
				sb.WriteString("Completed steps:\n")
				for i, res := range run.StepResults {
					preview := res
					if len(preview) > 100 {
						preview = preview[:100] + "..."
					}
					sb.WriteString(fmt.Sprintf("  [%d] %s: %s\n", i+1, run.Steps[i], preview))
				}
			}
			return mcp.NewToolResultText(sb.String()), nil
		},
	)

	// ── cancel_pipeline_run ───────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("cancel_pipeline_run",
			mcp.WithDescription(`Cancel an active pipeline run. No further agents will be invoked after cancellation.`),
			mcp.WithString("run_id", mcp.Required(), mcp.Description("Pipeline run ID to cancel")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			runID, _ := req.Params.Arguments["run_id"].(string)
			if runID == "" {
				return nil, fmt.Errorf("run_id is required")
			}
			_, err := w.post("/api/pipeline-runs/"+runID+"/cancel", map[string]string{})
			if err != nil {
				return nil, fmt.Errorf("cancelling run: %w", err)
			}
			return mcp.NewToolResultText(fmt.Sprintf("Pipeline run %s cancelled.", runID[:8])), nil
		},
	)

	// ── list_pipelines ────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("list_pipelines",
			mcp.WithDescription(`List all registered pipelines and their steps.
Use this to see what automated workflows are available before triggering one.`),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			body, err := w.get("/api/pipelines")
			if err != nil {
				return nil, fmt.Errorf("fetching pipelines: %w", err)
			}
			var pipelines []struct {
				Name  string   `json:"name"`
				Steps []string `json:"steps"`
			}
			if err := json.Unmarshal(body, &pipelines); err != nil {
				return nil, fmt.Errorf("parsing pipelines: %w", err)
			}
			if len(pipelines) == 0 {
				return mcp.NewToolResultText("No pipelines registered. Use register_pipeline to create one."), nil
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("%d pipeline(s):\n\n", len(pipelines)))
			for _, p := range pipelines {
				sb.WriteString(fmt.Sprintf("- **%s** → %s\n", p.Name, strings.Join(p.Steps, " → ")))
			}
			return mcp.NewToolResultText(sb.String()), nil
		},
	)

	// ── reply_message ─────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("reply_message",
			mcp.WithDescription(`Send a chat message as a reply to an existing message.
The reply appears in chat with a reference to the original message and sender.
Use this to create threaded discussions rather than sending standalone messages.`),
			mcp.WithString("reply_to_id", mcp.Required(), mcp.Description("Full message ID to reply to (from wait_for_change or get_task_comments)")),
			mcp.WithString("reply_from", mcp.Required(), mcp.Description("Display name of the original sender (for context)")),
			mcp.WithString("content", mcp.Required(), mcp.Description("Reply message text")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			replyTo, _ := req.Params.Arguments["reply_to_id"].(string)
			replyFrom, _ := req.Params.Arguments["reply_from"].(string)
			content, _ := req.Params.Arguments["content"].(string)
			if replyTo == "" || content == "" {
				return nil, fmt.Errorf("reply_to_id and content are required")
			}
			payload := map[string]string{
				"from":       w.agentID(),
				"content":    content,
				"reply_to":   replyTo,
				"reply_from": replyFrom,
			}
			_, err := w.post("/api/messages", payload)
			if err != nil {
				return nil, fmt.Errorf("sending reply: %w", err)
			}
			return mcp.NewToolResultText(fmt.Sprintf("Reply sent to message %s.", replyTo[:8])), nil
		},
	)

	return server.ServeStdio(s)
}

// registerSelf posts this agent to the capability registry on startup.
func (w *WertMCP) registerSelf(caps []string) error {
	payload := map[string]interface{}{
		"name":         w.agentName,
		"capabilities": caps,
	}
	_, err := w.post("/api/agents", payload)
	return err
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

func (w *WertMCP) get(path string) ([]byte, error) {
	resp, err := w.http.Get(w.serverURL + path)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("server %d: %s", resp.StatusCode, body)
	}
	return body, nil
}

func (w *WertMCP) post(path string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := w.http.Post(w.serverURL+path, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("server %d: %s", resp.StatusCode, body)
	}
	return body, nil
}

func (w *WertMCP) put(path string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPut, w.serverURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("PUT %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("server %d: %s", resp.StatusCode, body)
	}
	return body, nil
}

func (w *WertMCP) delete(path string) error {
	req, err := http.NewRequest(http.MethodDelete, w.serverURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return fmt.Errorf("DELETE %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server %d: %s", resp.StatusCode, body)
	}
	return nil
}

func (w *WertMCP) deleteWithBody(path string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodDelete, w.serverURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("DELETE %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("server %d: %s", resp.StatusCode, body)
	}
	return body, nil
}
