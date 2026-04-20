package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	wertmcp "wert/internal/mcp"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Start a MCP stdio server for Claude integration",
	Long: `Starts a Model Context Protocol (MCP) stdio server that connects to a
running wert server, exposing tools for Claude to manage tasks and team.

Add to your Claude MCP config (~/.claude/settings.json):

  {
    "mcpServers": {
      "wert": {
        "command": "/path/to/wert",
        "args": ["mcp", "--server", "http://localhost:8080", "--agent-name", "claude"]
      }
    }
  }

Available tools:
  team_context         — full team overview (use this first)
  list_tasks           — list all tasks (filterable)
  get_task             — get single task details
  search_tasks         — search tasks by keyword
  list_members         — list team members + online status
  create_task          — create and assign a task
  update_task          — update task status
  delete_task          — delete a task
  send_message         — broadcast a chat message
  get_dashboard        — full team overview as markdown table
  claim_task           — claim a task so other agents know you're working on it
  unclaim_task         — release a task claim
  wait_for_change      — block until a team event occurs (SSE push)
  send_direct_message  — private message to specific agent or member
  post_result          — publish structured AI output to the team
  register_capabilities — register agent capabilities in the registry
  list_agents          — discover other registered agents`,
	RunE: runMCP,
}

var mcpServer string
var mcpAgentName string

func init() {
	mcpCmd.Flags().StringVar(&mcpServer, "server", "http://localhost:8080", "URL of the running wert server")
	mcpCmd.Flags().StringVar(&mcpAgentName, "agent-name", "", "Name for this agent in the capability registry (e.g. claude, reviewer, deployer)")
}

func runMCP(cmd *cobra.Command, args []string) error {
	s := wertmcp.New(mcpServer, mcpAgentName)
	if err := s.Serve(); err != nil {
		return fmt.Errorf("mcp server error: %w", err)
	}
	return nil
}
