# wert

A terminal app for dev teams on the same network. Chat, assign tasks, track progress, all in the terminal.

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
- Claude can manage the whole thing via MCP

---

## Requirements

- Go 1.22 or higher
- Everyone must be on the same network (WiFi, LAN, etc.)

---

## Install

```bash
git clone <repo>
cd wert
go build -o wert .
```

Or build for all platforms:

```bash
make cross-build
# outputs to dist/
```

Move the binary somewhere in your PATH:

```bash
mv wert /usr/local/bin/wert
```

---

## Usage

### Admin (you)

Start the server. This opens your dashboard.

```bash
wert serve --name "Puneeth" --port 8080
```

With an admin token so others cant join as admin:

```bash
wert serve --name "Puneeth" --port 8080 --token mysecret
```

It will print the IP address your team can connect to.

### Team members

Each person runs this on their own machine:

```bash
wert join --host 192.168.1.5:8080 --name "John"
```

Replace `192.168.1.5` with the IP printed when you ran `serve`.

---

## TUI layout

```
 wert   Puneeth  *  admin  *  3 online  *  [tab] switch  [ctrl+q] quit
+-- Tasks -------------------++-- Chat (Puneeth, John, Sarah) ---------+
| TODO     #a1b2c3d4 Fix bug || 10:23  admin: fix that login bug       |
| IN PROG  #e5f6g7h8 API     || 10:24  john: on it                     |
| DONE     #i9j0k1l2 Tests   || 10:25  sarah: PR is up                 |
+----------------------------++------------------------------------------+
* 192.168.1.5:8080   new task assigned to you: Fix login bug
+----------------------------------------------------------------------+
|  type a message or /help for commands                                |
+----------------------------------------------------------------------+
```

Tab switches between the Tasks and Chat pane. Typing always goes to the input at the bottom.

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

Use the short 8 char ID shown next to each task. Example: `/done a1b2c3d4`

### Admin only

| Command | What it does |
|---|---|
| `/assign @john "Fix login bug" "description here" high` | create and assign a task |
| `/delete <id>` | delete a task |

Priority can be `low`, `medium`, or `high`. Description is optional.

---

## MCP setup (Claude integration)

Run the MCP server while wert is already running:

```bash
wert mcp --server http://localhost:8080
```

Add this to your Claude config at `~/.claude/settings.json`:

```json
{
  "mcpServers": {
    "wert": {
      "command": "/usr/local/bin/wert",
      "args": ["mcp", "--server", "http://localhost:8080"]
    }
  }
}
```

Claude then has access to these tools:

| Tool | What it does |
|---|---|
| `create_task` | assign a task to someone |
| `list_tasks` | list tasks, filter by person or status |
| `update_task` | change task status |
| `delete_task` | delete a task |
| `send_message` | send a message to the team chat |
| `list_members` | see who is online |
| `get_dashboard` | full overview of all tasks and members |

---

## Data

Tasks and chat history are saved to `wert-data.json` in the folder where you ran `wert serve`. You can change the path:

```bash
wert serve --name "Puneeth" --data /path/to/data.json
```

---

## Keyboard shortcuts

| Key | Action |
|---|---|
| Tab | switch between Tasks and Chat pane |
| Enter | send message or run command |
| Up / Down | scroll the active pane |
| Ctrl+Q | quit |
