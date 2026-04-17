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
        "args": ["mcp", "--server", "http://localhost:8080"]
      }
    }
  }

Available tools:
  list_tasks       — list all tasks (filterable)
  list_members     — list team members + online status
  create_task      — create and assign a task
  update_task      — update task status
  delete_task      — delete a task
  send_message     — broadcast a chat message
  get_dashboard    — full team overview`,
	RunE: runMCP,
}

var mcpServer string

func init() {
	mcpCmd.Flags().StringVar(&mcpServer, "server", "http://localhost:8080", "URL of the running wert server")
}

func runMCP(cmd *cobra.Command, args []string) error {
	s := wertmcp.New(mcpServer)
	if err := s.Serve(); err != nil {
		return fmt.Errorf("mcp server error: %w", err)
	}
	return nil
}
