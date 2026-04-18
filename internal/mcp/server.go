package mcp

import (
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
	http      *http.Client
}

func New(serverURL string) *WertMCP {
	return &WertMCP{
		serverURL: strings.TrimRight(serverURL, "/"),
		http:      &http.Client{Timeout: 10 * time.Second},
	}
}

// internal types for JSON parsing
type task struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Assignee    string    `json:"assignee"`
	Status      string    `json:"status"`
	Priority    string    `json:"priority"`
	DueDate     string    `json:"due_date"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	UpdatedBy   string    `json:"updated_by"`
}

type member struct {
	Username string    `json:"username"`
	Role     string    `json:"role"`
	Online   bool      `json:"online"`
	JoinedAt time.Time `json:"joined_at"`
}

func (t task) shortID() string {
	if len(t.ID) >= 8 {
		return t.ID[:8]
	}
	return t.ID
}

func (w *WertMCP) Serve() error {
	s := server.NewMCPServer(
		"wert",
		"2.0.0",
		server.WithToolCapabilities(false),
	)

	// ── team_context ──────────────────────────────────────────────────────────
	// Primary tool for Claude Code: get everything needed to understand team state
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

			// Team members
			sb.WriteString("## Team Members\n\n")
			for _, m := range members {
				status := "offline"
				if m.Online {
					status = "**online**"
				}
				sb.WriteString(fmt.Sprintf("- **%s** (%s) — %s\n", m.Username, m.Role, status))
			}
			sb.WriteString("\n")

			// Task summary
			counts := map[string]int{"todo": 0, "in_progress": 0, "done": 0, "blocked": 0}
			for _, t := range tasks {
				counts[t.Status]++
			}
			sb.WriteString(fmt.Sprintf("## Task Summary (%d total)\n\n", len(tasks)))
			sb.WriteString(fmt.Sprintf("- In Progress: %d\n- Todo: %d\n- Blocked: %d\n- Done: %d\n\n",
				counts["in_progress"], counts["todo"], counts["blocked"], counts["done"]))

			// Per-person breakdown
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
					sb.WriteString(fmt.Sprintf("- [%s] `%s` %s (%s)%s\n",
						strings.ToUpper(t.Status), t.shortID(), t.Title, t.Priority, due))
				}
				sb.WriteString("\n")
			}

			return mcp.NewToolResultText(sb.String()), nil
		},
	)

	// ── list_tasks ────────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("list_tasks",
			mcp.WithDescription(`List tasks with optional filtering. Returns task IDs, titles, status, assignee, priority, and due dates.
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
				desc := ""
				if t.Description != "" {
					desc = fmt.Sprintf("\n    %s", t.Description)
				}
				sb.WriteString(fmt.Sprintf("- `%s`  [%s]  %s  →%s  @%s  (%s)%s%s\n",
					t.shortID(), strings.ToUpper(t.Status), t.Title, t.Priority, t.Assignee, t.ID, due, desc))
			}
			return mcp.NewToolResultText(sb.String()), nil
		},
	)

	// ── get_task ──────────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_task",
			mcp.WithDescription(`Get full details of a single task by its ID prefix.
Returns title, description, status, priority, assignee, due date, and update history.`),
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
			payload := map[string]string{"status": status, "updated_by": "claude"}
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
			mcp.WithString("from", mcp.Description("Sender display name (default: claude)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			content, _ := req.Params.Arguments["content"].(string)
			from, _ := req.Params.Arguments["from"].(string)
			if content == "" {
				return nil, fmt.Errorf("content is required")
			}
			if from == "" {
				from = "claude"
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

			// online status
			sb.WriteString("## Team\n\n")
			for _, m := range members {
				dot := "○"
				if m.Online {
					dot = "●"
				}
				sb.WriteString(fmt.Sprintf("%s **%s** (%s)\n", dot, m.Username, m.Role))
			}
			sb.WriteString("\n")

			// counts
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

			// per-person tasks
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
				sb.WriteString("| ID | Status | Title | Priority | Due |\n")
				sb.WriteString("|-----|--------|-------|----------|-----|\n")
				for _, t := range ts {
					sb.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s | %s |\n",
						t.shortID(), t.Status, t.Title, t.Priority, t.DueDate))
				}
				sb.WriteString("\n")
			}
			return mcp.NewToolResultText(sb.String()), nil
		},
	)

	return server.ServeStdio(s)
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
