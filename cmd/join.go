package cmd

import (
	"fmt"
	"net"
	"os"
	"strings"

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

func joinConnectError(host string, err error) error {
	msg := err.Error()
	var hint string
	switch {
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "timed out") ||
		strings.Contains(msg, "did not properly respond") || strings.Contains(msg, "connectex"):
		hint = "Connection timed out — the server machine's firewall is likely blocking port 8080.\n\n" +
			"  On macOS (server):  System Settings → Network → Firewall → allow wert, or run:\n" +
			"    sudo /usr/libexec/ApplicationFirewall/socketfilterfw --add $(which wert)\n\n" +
			"  On Linux (server):  sudo ufw allow 8080/tcp\n\n" +
			"  Also verify you can ping " + host + " from this machine."
	case strings.Contains(msg, "connection refused"):
		hint = "Connection refused — is the server running?\n" +
			"  Start it with:  wert serve --name <admin> --port 8080"
	case strings.Contains(msg, "no such host") || strings.Contains(msg, "no route"):
		h, _, _ := net.SplitHostPort(host)
		hint = "Cannot reach " + h + " — check that both machines are on the same network\n" +
			"  and that the IP shown by 'wert serve' matches what you typed."
	default:
		return fmt.Errorf("cannot connect to %s: %w", host, err)
	}
	return fmt.Errorf("cannot connect to %s\n\n  %s", host, hint)
}

func runJoin(cmd *cobra.Command, args []string) error {
	cl, err := client.Connect(joinHost, joinName, joinToken)
	if err != nil {
		return joinConnectError(joinHost, err)
	}

	ghClient := gh.New(joinGHToken, joinGHOrg)
	m := tui.New(cl, joinName, "member", joinHost, joinToken, ghClient)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if tui.UpdateRequested {
		SelfUpdateAndRelaunch()
	}
	return nil
}
