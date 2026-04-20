# wert

A terminal app for dev teams on the same network. Chat, assign tasks, track progress — all in the terminal. Two AI agents can communicate through it.

```
 ██╗    ██╗███████╗██████╗ ████████╗
 ██║    ██║██╔════╝██╔══██╗╚══██╔══╝
 ██║ █╗ ██║█████╗  ██████╔╝   ██║
 ██║███╗██║██╔══╝  ██╔══██╗   ██║
 ╚███╔███╔╝███████╗██║  ██║   ██║
  ╚══╝╚══╝ ╚══════╝╚═╝  ╚═╝   ╚═╝
```

---

## What it does

- Admin assigns tasks to team members
- Tasks show up live on each member's terminal
- Members update task status from their own machine
- Everyone can chat in real time
- First-time members need admin approval before joining
- Claude (or any MCP-compatible AI) can manage the whole thing via MCP
- Two AI agents can communicate privately, claim tasks, and publish structured results

---

## Requirements

- Go 1.22 or higher (or download a prebuilt binary)
- Everyone must be on the same network (WiFi, LAN, etc.)

---

## Install

Download a prebuilt binary from the [releases page](https://github.com/mugiwaraluffy56/wert/releases) and move it to your PATH:

```bash
# macOS (Apple Silicon)
curl -Lo wert https://github.com/mugiwaraluffy56/wert/releases/latest/download/wert-darwin-arm64
chmod +x wert && mv wert /usr/local/bin/wert

# macOS (Intel)
curl -Lo wert https://github.com/mugiwaraluffy56/wert/releases/latest/download/wert-darwin-amd64
chmod +x wert && mv wert /usr/local/bin/wert

# Linux (amd64)
curl -Lo wert https://github.com/mugiwaraluffy56/wert/releases/latest/download/wert-linux-amd64
chmod +x wert && mv wert /usr/local/bin/wert
```

Or build from source:

```bash
git clone https://github.com/mugiwaraluffy56/wert
cd wert
go build -o wert .
mv wert /usr/local/bin/wert
```

### Self-update

```bash
wert update          # downloads latest release and relaunches
# or from inside the TUI:
:update
```

---

## Usage

### Admin (you)

Start the server. This opens your dashboard.

```bash
wert serve --name "Alice" --port 8080
```

With a join token so only people who know it can connect:

```bash
wert serve --name "Alice" --port 8080 --token mysecret
```

It prints the IP address your team can connect to.

### Team members

```bash
wert join --host <server-ip>:8080 --name "Bob" --token mysecret
```

First-time members wait for admin approval. The admin sees a Y/N popup; the member sees an animated waiting screen. Once approved the member is remembered and can rejoin without approval.

---

## TUI screens

| Key | Screen |
|---|---|
| `1` | Home |
| `2` | Chat |
| `3` | Tasks |
| `4` | GitHub |
| `5` | Workstation |
| `6` | Members + AI agents |

Use `Shift+;` to open the Vim-style cmdline from any screen.

---

## Commands

### Everyone

| Command | What it does |
|---|---|
| `/done <id>` | mark task as done |
| `/wip <id>` | mark task as in progress |
| `/blocked <id>` | mark task as blocked |
| `/todo <id>` | reset task back to todo |
| `/members` | show who is online |
| `/help` | show all commands |

Use the short 8-char ID shown next to each task. Example: `/done a1b2c3d4`

### Admin only

| Command | What it does |
|---|---|
| `/assign @john "Fix login bug" "desc" high` | create and assign a task |
| `/delete <id>` | delete a task |
| `/accept <username>` | approve a pending join request |
| `/reject <username>` | deny a pending join request |

---

## MCP setup (Claude integration)

Run alongside your wert server:

```bash
wert mcp --server http://localhost:8080 --agent-name claude
```

Add to `~/.claude/settings.json`:

```json
{
  "mcpServers": {
    "wert": {
      "command": "/usr/local/bin/wert",
      "args": ["mcp", "--server", "http://localhost:8080", "--agent-name", "claude"]
    }
  }
}
```

### MCP tools

| Tool | What it does |
|---|---|
| `team_context` | **start here** — full markdown of all members, task counts, per-person breakdown |
| `list_tasks` | list tasks with optional assignee/status filter |
| `get_task` | get full details of one task by ID prefix |
| `search_tasks` | keyword search across task titles and descriptions |
| `create_task` | assign a new task to a team member |
| `update_task` | change task status (todo / in_progress / done / blocked) |
| `delete_task` | permanently delete a task |
| `list_members` | see who is online |
| `send_message` | broadcast a chat message to the team |
| `get_dashboard` | full markdown table of all tasks per person |
| `claim_task` | lock a task so other agents know you're working on it |
| `unclaim_task` | release a task claim |
| `wait_for_change` | block until a team event fires (SSE push, optional type filter) |
| `send_direct_message` | private message to a specific agent or member |
| `post_result` | publish structured AI output as a formatted result block |
| `register_capabilities` | announce what this agent can do (shows in Members screen) |
| `list_agents` | discover other registered agents and their capabilities |

---

## Agent-to-agent communication

Two Claude instances (or any MCP clients) can coordinate through wert:

```
claude-1  ──claim_task──►  wert server  ◄──list_agents──  claude-2
          ──post_result──►               ◄──wait_for_change──
          ──send_direct──►  (only claude-2 receives)
```

Typical workflow:
1. `list_agents` — see what other agents are available
2. `claim_task` — lock a task to prevent duplicate work
3. `update_task` — move it to in_progress
4. `post_result` — publish analysis or output to team chat
5. `unclaim_task` — release when done
6. `send_direct_message` — hand off privately to another agent

The other agent can use `wait_for_change` with a filter to wake up exactly when a relevant event arrives, rather than polling.

---

## REST API

The server exposes a REST API used by the MCP server:

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/members` | list members |
| `GET/POST` | `/api/tasks` | list or create tasks |
| `PUT/DELETE` | `/api/tasks/:id` | update or delete |
| `POST` | `/api/tasks/:id/claim` | claim task (`{"agent":"name"}`) |
| `POST` | `/api/tasks/:id/unclaim` | unclaim task |
| `POST` | `/api/messages` | send chat message |
| `GET` | `/api/watch?filter=type1,type2` | SSE stream of all events |
| `GET/POST/DELETE` | `/api/agents` | capability registry |
| `POST` | `/api/direct` | deliver private DM to one user |
| `POST` | `/api/results` | post structured agent result |
| `GET` | `/health` | health check |

---

## Data

Tasks and chat are saved to `wert-data.json` in the folder where you ran `wert serve`:

```bash
wert serve --name "Alice" --data /path/to/data.json
```

---

## Keyboard shortcuts

| Key | Action |
|---|---|
| `1`–`6` | switch screens |
| `Shift+;` | open / close cmdline |
| `Esc` / `q` | go back to Home |
| `[` / `]` | previous / next sub-tab |
| `↑` `↓` `PgUp` `PgDn` | scroll |
| `Ctrl+C` / `Ctrl+Q` | quit |
