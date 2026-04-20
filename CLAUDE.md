# wert - codebase notes for Claude

## What this is

wert is a LAN terminal team tool. Admin runs a server, team members connect to it. Everyone gets a full screen TUI with tasks and chat. There is also an MCP server so Claude can manage tasks, send messages, claim tasks, post results, and communicate privately with other agents.

## Project structure

```
wert/
  main.go                        entry point, just calls cmd.Execute()
  cmd/
    root.go                      cobra root command and banner
    serve.go                     admin mode, starts server then opens TUI
    join.go                      member mode, connects to server and opens TUI
    mcp.go                       starts MCP stdio server (--agent-name flag)
    update.go                    wert update command + SelfUpdateAndRelaunch()
    relaunch_unix.go             syscall.Exec relaunch (build tag !windows)
    relaunch_windows.go          exec.Command + os.Exit relaunch (build tag windows)
  internal/
    protocol/
      messages.go                all shared types: Task, Member, ChatMessage, AgentInfo, Envelope, payloads
    server/
      store.go                   in-memory store with JSON file persistence
      hub.go                     websocket hub, SSE watchers, connection management, message routing
      server.go                  http server, websocket upgrade, REST API (MCP + agent endpoints)
    client/
      client.go                  websocket client with Send/Recv channels
      tui/
        model.go                 full Bubble Tea TUI model, all rendering and input handling
    mcp/
      server.go                  MCP stdio server using mark3labs/mcp-go, calls REST API
    updater/
      updater.go                 self-update: fetches latest GitHub release, downloads, replaces binary
    version/
      version.go                 var Version = "dev"  (injected by ldflags at build time)
    github/
      client.go                  GitHub REST API client (repos, PRs, issues)
```

## How communication works

- Server runs on a port (default 8080)
- Clients connect via WebSocket at /ws
- All messages are JSON wrapped in an Envelope { type, payload }
- payload is json.RawMessage so each type is decoded separately
- The hub handles all message routing; unregistered clients are held in `pending` map
- On connect the server sends a full sync payload with all tasks, members, messages
- MCP server calls the REST API — no WebSocket needed
- SSE endpoint /api/watch streams all broadcast events to HTTP long-poll clients

## Auth and join approval

- `--token` on `wert serve` sets the public join password. Anyone who knows it can join as member.
- The admin's TUI connects with an internal `adminSecret` (UUID, never shown), not the join token.
- First-time members are held in a `pending` map until the admin approves or rejects them.
- On approval, `store.ApproveUser(username)` persists the approval so future reconnects skip the flow.
- MsgJoinPending → waiting screen (animated spinner) on member side
- MsgJoinRequest → Y/N popup overlay on admin side; queues multiple requests

## Key types

All in internal/protocol/messages.go

- Envelope — wraps every websocket message, has Type and raw Payload
- Task — ID (uuid), Title, Description, Assignee, Status, Priority, DueDate, ClaimedBy
- Member — Username, Role (admin or member), Online bool
- ChatMessage — ID, From, Content, Timestamp, IsAgent bool, Kind string (""|"dm"|"result"), Meta string
- AgentInfo — Name, Capabilities []string, RegisteredAt, Online
- SyncPayload — sent to new clients with full state
- TaskClaimPayload — TaskID, ClaimedBy
- AgentResultPayload — Agent, TaskID, Title, Content, Timestamp
- DirectMsgPayload — ID, From, To, Content, Timestamp, IsAgent
- AgentOnlinePayload — Agent AgentInfo, Online bool

## Agent communication features (v1.0.3)

1. **SSE push** — GET /api/watch?filter=type1,type2 streams all broadcast events; hub.AddSSEWatcher/RemoveSSEWatcher
2. **Agent identity** — --agent-name on wert mcp; auto-registers on startup; stamps updated_by
3. **Task claiming** — POST /api/tasks/:id/claim|unclaim {"agent":"name"}; store.ClaimTask/UnclaimTask; broadcast MsgTaskClaim/Unclaim
4. **Event filtering** — SSE filter param; wait_for_change MCP tool uses it
5. **Direct messages** — POST /api/direct; hub.sendDirect routes only to recipient WS client; TUI shows only if sender/recipient
6. **Structured results** — POST /api/results; store.AddAgentMessage(kind="result"); broadcast MsgAgentResult; TUI renders bordered block
7. **Capability registry** — GET/POST/DELETE /api/agents; store.RegisterAgent/UnregisterAgent/GetAgents; broadcast MsgAgentOnline; Members screen shows agents section

## TUI

Built with charmbracelet/bubbletea, bubbles, and lipgloss.

- Model in internal/client/tui/model.go (value receivers throughout)
- Six screens: Home(1), Chat(2), Tasks(3), GitHub(4), Workstation(5), Members(6)
- `Shift+;` opens a Vim-style floating cmdline overlay
- Admin sees all tasks; members see only their assigned tasks
- Commands start with /
- New task notification shows in status bar
- waitForMsg reads from client.Recv channel and feeds into bubbletea as ServerMsg
- Agents in chat: IsAgent=true → cyan style; Kind="result" → bordered block; Kind="dm" → shown only to sender/recipient
- ClaimedBy on task rows shows `claimed: agent-name` in cyan
- Members screen has an Agents section when agents are registered
- Version shown bottom-right of status bar (injected at build time)

## Task IDs

Tasks have full UUIDs. The TUI shows the first 8 chars as the short ID.
Commands and API calls use the short prefix. The server does prefix matching via GetTaskByPrefix / ClaimTask / UnclaimTask.

## Commands in TUI

Everyone: /done /wip /blocked /todo /members /help /github /exit /quit
Admin only: /assign @user "title" ["desc"] [priority] [due:YYYY-MM-DD], /delete, /accept, /reject

Cmdline (:): 1-6 screens, q/quit, docs, update, sp v/h<n>, cl/close

## MCP tools

team_context, list_tasks, get_task, search_tasks, list_members, create_task, update_task, delete_task, send_message, get_dashboard,
claim_task, unclaim_task, wait_for_change, send_direct_message, post_result, register_capabilities, list_agents

All tools go through the REST API. MCP server auto-registers capabilities on Serve() if --agent-name is set.

## Self-update

`wert update` or `:update` in TUI:
- Fetches latest release from GitHub API (mugiwaraluffy56/wert)
- Downloads asset matching GOOS-GOARCH (e.g. wert-darwin-arm64)
- Replaces binary in-place; Windows renames old to .old first
- Relaunches via syscall.Exec (Unix) or exec.Command+os.Exit (Windows)

## Persistence

Store writes to a JSON file (default wert-data.json) after every mutation.
Writes happen in a goroutine so they do not block.
On startup the store loads the file if it exists.
Agents are NOT persisted (in-memory only — register on reconnect).

## Flags

wert serve --name --port --token --data
wert join --host --name --token
wert mcp --server --agent-name
wert update (no flags)

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

Version is injected at build time:
```bash
go build -ldflags="-X wert/internal/version.Version=v1.0.3" -o wert .
```

## Things to know

- The admin token is optional. If set, it is the public join password; the serve process connects with an internal adminSecret (UUID) to get admin role.
- If no token is set, anyone can join as member; first person to serve is admin.
- Data file is only written on the server machine, not on clients.
- The TUI uses alt screen mode so it takes over the full terminal.
- Ctrl+Q or Ctrl+C to quit.
- SSE watchers receive a buffered 64-item channel; slow watchers are skipped (non-blocking select).
- Direct messages are delivered to WS clients only; the REST response goes back to the MCP caller.
