package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "wert",
	Short: "wert — terminal team collaboration for developers",
	Long: `
 ██╗    ██╗███████╗██████╗ ████████╗
 ██║    ██║██╔════╝██╔══██╗╚══██╔══╝
 ██║ █╗ ██║█████╗  ██████╔╝   ██║
 ██║███╗██║██╔══╝  ██╔══██╗   ██║
 ╚███╔███╔╝███████╗██║  ██║   ██║
  ╚══╝╚══╝ ╚══════╝╚═╝  ╚═╝   ╚═╝

Real-time team collaboration in your terminal.
Chat · Task assignment · Status tracking · MCP integration

Commands:
  wert serve  — start a server (admin mode)
  wert join   — join a running server (member mode)
  wert mcp    — start MCP stdio server for Claude integration
`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(joinCmd)
	rootCmd.AddCommand(mcpCmd)
}
