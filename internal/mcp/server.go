package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type WertMCP struct {
	serverURL string // e.g. http://localhost:8080
	http      *http.Client
}

func New(serverURL string) *WertMCP {
	return &WertMCP{
		serverURL: strings.TrimRight(serverURL, "/"),
		http:      &http.Client{},
	}
}

func (w *WertMCP) Serve() error {
	s := server.NewMCPServer("wert", "1.0.0",
		server.WithToolCapabilities(false),
	)

	// ── list_tasks ────────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("list_tasks",
			mcp.WithDescription("List all team tasks. Optionally filter by assignee username or status (todo|in_progress|done|blocked)."),
			mcp.WithString("assignee", mcp.Description("Filter by assignee username")),
			mcp.WithString("status", mcp.Description("Filter by status: todo, in_progress, done, blocked")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query := "?"
			if a, ok := req.Params.Arguments["assignee"].(string); ok && a != "" {
				query += "assignee=" + a + "&"
			}
			if s, ok := req.Params.Arguments["status"].(string); ok && s != "" {
				query += "status=" + s + "&"
			}
			body, err := w.get("/api/tasks" + query)
			if err != nil {
				return nil, err
			}
			return mcp.NewToolResultText(prettyJSON(body)), nil
		},
	)

	// ── list_members ──────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("list_members",
			mcp.WithDescription("List all team members and their online status."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			body, err := w.get("/api/members")
			if err != nil {
				return nil, err
			}
			return mcp.NewToolResultText(prettyJSON(body)), nil
		},
	)

	// ── create_task ───────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("create_task",
			mcp.WithDescription("Create a new task and assign it to a team member. The task will immediately appear on their terminal."),
			mcp.WithString("title", mcp.Required(), mcp.Description("Short task title")),
			mcp.WithString("assignee", mcp.Required(), mcp.Description("Username to assign the task to")),
			mcp.WithString("description", mcp.Description("Detailed task description")),
			mcp.WithString("priority", mcp.Description("Priority level: low | medium | high (default: medium)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			title, _ := req.Params.Arguments["title"].(string)
			assignee, _ := req.Params.Arguments["assignee"].(string)
			description, _ := req.Params.Arguments["description"].(string)
			priority, _ := req.Params.Arguments["priority"].(string)
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
			}
			body, err := w.post("/api/tasks", payload)
			if err != nil {
				return nil, err
			}
			return mcp.NewToolResultText("Task created:\n" + prettyJSON(body)), nil
		},
	)

	// ── update_task ───────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("update_task",
			mcp.WithDescription("Update the status of a task. Use the short 8-char task ID prefix."),
			mcp.WithString("task_id", mcp.Required(), mcp.Description("Task ID or short prefix (first 8 chars)")),
			mcp.WithString("status", mcp.Required(), mcp.Description("New status: todo | in_progress | done | blocked")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			taskID, _ := req.Params.Arguments["task_id"].(string)
			status, _ := req.Params.Arguments["status"].(string)
			if taskID == "" || status == "" {
				return nil, fmt.Errorf("task_id and status are required")
			}
			payload := map[string]string{
				"status":     status,
				"updated_by": "mcp",
			}
			body, err := w.put("/api/tasks/"+taskID, payload)
			if err != nil {
				return nil, err
			}
			return mcp.NewToolResultText("Task updated:\n" + prettyJSON(body)), nil
		},
	)

	// ── delete_task ───────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("delete_task",
			mcp.WithDescription("Delete a task by its ID prefix."),
			mcp.WithString("task_id", mcp.Required(), mcp.Description("Task ID or short prefix")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			taskID, _ := req.Params.Arguments["task_id"].(string)
			if taskID == "" {
				return nil, fmt.Errorf("task_id is required")
			}
			if err := w.delete("/api/tasks/" + taskID); err != nil {
				return nil, err
			}
			return mcp.NewToolResultText("Task " + taskID + " deleted."), nil
		},
	)

	// ── send_message ──────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("send_message",
			mcp.WithDescription("Broadcast a chat message to the entire team."),
			mcp.WithString("content", mcp.Required(), mcp.Description("The message to send")),
			mcp.WithString("from", mcp.Description("Sender name (default: mcp)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			content, _ := req.Params.Arguments["content"].(string)
			from, _ := req.Params.Arguments["from"].(string)
			if content == "" {
				return nil, fmt.Errorf("content is required")
			}
			if from == "" {
				from = "mcp"
			}
			payload := map[string]string{"content": content, "from": from}
			_, err := w.post("/api/messages", payload)
			if err != nil {
				return nil, err
			}
			return mcp.NewToolResultText("Message sent."), nil
		},
	)

	// ── get_dashboard ─────────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_dashboard",
			mcp.WithDescription("Get a full team dashboard: all tasks grouped by status and member, plus online members."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			tasksBody, err := w.get("/api/tasks")
			if err != nil {
				return nil, err
			}
			membersBody, err := w.get("/api/members")
			if err != nil {
				return nil, err
			}

			type Task struct {
				ID       string `json:"id"`
				Title    string `json:"title"`
				Assignee string `json:"assignee"`
				Status   string `json:"status"`
				Priority string `json:"priority"`
			}
			type Member struct {
				Username string `json:"username"`
				Role     string `json:"role"`
				Online   bool   `json:"online"`
			}

			var tasks []Task
			var members []Member
			_ = json.Unmarshal(tasksBody, &tasks)
			_ = json.Unmarshal(membersBody, &members)

			var sb strings.Builder
			sb.WriteString("═══ WERT DASHBOARD ═══\n\n")

			sb.WriteString("── Team ──\n")
			for _, m := range members {
				dot := "○"
				if m.Online {
					dot = "●"
				}
				sb.WriteString(fmt.Sprintf("  %s %s (%s)\n", dot, m.Username, m.Role))
			}
			sb.WriteString("\n")

			// Group by member
			byMember := map[string][]Task{}
			for _, t := range tasks {
				byMember[t.Assignee] = append(byMember[t.Assignee], t)
			}
			for member, ts := range byMember {
				sb.WriteString(fmt.Sprintf("── %s's tasks ──\n", member))
				for _, t := range ts {
					sb.WriteString(fmt.Sprintf("  [%s] %s  (%s)  #%s\n",
						strings.ToUpper(t.Status), t.Title, t.Priority, t.ID[:8]))
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
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, body)
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
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, body)
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
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, body)
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
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, body)
	}
	return nil
}

func prettyJSON(data []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		return string(data)
	}
	return buf.String()
}
