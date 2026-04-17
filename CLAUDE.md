# wert - codebase notes for Claude

## What this is

wert is a LAN terminal team tool. Admin runs a server, team members connect to it. Everyone gets a full screen TUI with tasks and chat. There is also an MCP server so Claude can manage tasks and send messages.

## Project structure

```
wert/
  main.go                        entry point, just calls cmd.Execute()
  cmd/
    root.go                      cobra root command and banner
    serve.go                     admin mode, starts server then opens TUI
    join.go                      member mode, connects to server and opens TUI
    mcp.go                       starts MCP stdio server
  internal/
    protocol/
      messages.go                all shared types: Task, Member, ChatMessage, Envelope, payloads
    server/
      store.go                   in-memory store with JSON file persistence
      hub.go                     websocket hub, connection management, message routing
      server.go                  http server, websocket upgrade, REST API for MCP
    client/
      client.go                  websocket client with Send/Recv channels
      tui/
        model.go                 full Bubble Tea TUI model, all rendering and input handling
    mcp/
      server.go                  MCP stdio server using mark3labs/mcp-go, calls REST API
```

## How communication works

- Server runs on a port (default 8080)
- Clients connect via WebSocket at /ws
- All messages are JSON wrapped in an Envelope { type, payload }
- payload is json.RawMessage so each type is decoded separately
- The hub handles register, chat, task_create, task_update, task_delete
- On connect the server sends a full sync payload with all tasks, members, messages
- MCP server calls the REST API at /api/tasks, /api/members, /api/messages

## Key types

All in internal/protocol/messages.go

- Envelope - wraps every websocket message, has Type and raw Payload
- Task - has ID (uuid), Title, Description, Assignee, Status, Priority, timestamps
- Member - Username, Role (admin or member), Online bool
- ChatMessage - ID, From, Content, Timestamp
- SyncPayload - sent to new clients with full state

## TUI

Built with charmbracelet/bubbletea, bubbles, and lipgloss.

- Model in internal/client/tui/model.go
- Two panes: Tasks (left, 40%) and Chat (right, 60%)
- Tab switches active pane
- Admin sees all tasks, members only see their own assigned tasks
- Commands start with /
- New task notification shows in status bar when a task is assigned to you
- waitForMsg reads from client.Recv channel and feeds into bubbletea as ServerMsg

## Task IDs

Tasks have full UUIDs. The TUI shows the first 8 chars as the short ID.
Commands like /done use the short prefix. The server does prefix matching via GetTaskByPrefix.

## Commands in TUI

Everyone: /done /wip /blocked /todo /members /help
Admin only: /assign @user "title" ["desc"] [priority], /delete

## MCP tools

list_tasks, list_members, create_task, update_task, delete_task, send_message, get_dashboard
All go through the REST API so no WebSocket needed for MCP.

## Persistence

Store writes to a JSON file (default wert-data.json) after every mutation.
Writes happen in a goroutine so they do not block.
On startup the store loads the file if it exists.

## Flags

wert serve --name --port --token --data
wert join --host --name --token
wert mcp --server

## Dependencies

- github.com/charmbracelet/bubbletea - TUI framework
- github.com/charmbracelet/bubbles - viewport, textinput components
- github.com/charmbracelet/lipgloss - styling
- github.com/gorilla/websocket - websocket server and client
- github.com/google/uuid - task IDs
- github.com/spf13/cobra - CLI commands
- github.com/mark3labs/mcp-go - MCP server

## Build

```bash
go build -o wert .
make cross-build   # linux, darwin, windows amd64/arm64
```

## Things to know

- The admin token is optional. If set, only people who pass it with --token get admin role.
- If no token is set, the first person to run serve is just trusted as admin via WebSocket role.
- Data file is only written on the server machine, not on clients.
- The TUI uses alt screen mode so it takes over the full terminal.
- Ctrl+Q or Ctrl+C to quit.
