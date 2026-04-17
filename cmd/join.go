package cmd

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"wert/internal/client"
	"wert/internal/client/tui"
)

var joinCmd = &cobra.Command{
	Use:   "join",
	Short: "Join a wert server (member mode)",
	Long: `Connect to a running wert server on the local network.
Opens a full-screen TUI showing your assigned tasks and team chat.`,
	RunE: runJoin,
}

var (
	joinHost  string
	joinName  string
	joinToken string
)

func init() {
	joinCmd.Flags().StringVar(&joinHost, "host", "", "Server host:port (e.g. 192.168.1.5:8080)")
	joinCmd.Flags().StringVarP(&joinName, "name", "n", "", "Your display name")
	joinCmd.Flags().StringVar(&joinToken, "token", "", "Admin token (only needed to join as admin)")
	_ = joinCmd.MarkFlagRequired("host")
	_ = joinCmd.MarkFlagRequired("name")
}

func runJoin(cmd *cobra.Command, args []string) error {
	cl, err := client.Connect(joinHost, joinName, joinToken)
	if err != nil {
		return fmt.Errorf("cannot connect to %s: %w", joinHost, err)
	}

	m := tui.New(cl, joinName, "member", joinHost)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	return nil
}
