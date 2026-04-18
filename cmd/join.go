package cmd

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"wert/internal/client"
	"wert/internal/client/tui"
	gh "wert/internal/github"
)

var joinCmd = &cobra.Command{
	Use:   "join",
	Short: "Join a wert server (member mode)",
	Long: `Connect to a running wert server on the local network.
Opens a full-screen TUI with home, chat, tasks, github and members screens.`,
	RunE: runJoin,
}

var (
	joinHost     string
	joinName     string
	joinToken    string
	joinGHToken  string
	joinGHOrg    string
)

func init() {
	joinCmd.Flags().StringVar(&joinHost, "host", "", "Server host:port (e.g. 192.168.1.5:8080)")
	joinCmd.Flags().StringVarP(&joinName, "name", "n", "", "Your display name")
	joinCmd.Flags().StringVar(&joinToken, "token", "", "Admin token (only needed to join as admin)")
	joinCmd.Flags().StringVar(&joinGHToken, "github-token", "", "GitHub personal access token")
	joinCmd.Flags().StringVar(&joinGHOrg, "github-org", "", "GitHub organization name")
	_ = joinCmd.MarkFlagRequired("host")
	_ = joinCmd.MarkFlagRequired("name")
}

func runJoin(cmd *cobra.Command, args []string) error {
	cl, err := client.Connect(joinHost, joinName, joinToken)
	if err != nil {
		return fmt.Errorf("cannot connect to %s: %w", joinHost, err)
	}

	ghClient := gh.New(joinGHToken, joinGHOrg)
	m := tui.New(cl, joinName, "member", joinHost, joinToken, ghClient)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	return nil
}
