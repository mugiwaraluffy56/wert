package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"wert/internal/client"
	"wert/internal/server"
	"wert/internal/client/tui"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start a wert server (admin mode)",
	Long: `Start a wert server on the local network.
You automatically connect as admin and see a full-screen TUI.
Other developers join with: wert join --host <your-ip>:<port> --name <username>`,
	RunE: runServe,
}

var (
	servePort     string
	serveName     string
	serveToken    string
	serveDataFile string
)

func init() {
	serveCmd.Flags().StringVarP(&serveName, "name", "n", "", "Your admin display name (required)")
	serveCmd.Flags().StringVarP(&servePort, "port", "p", "8080", "Port to listen on")
	serveCmd.Flags().StringVar(&serveToken, "token", "", "Admin token (optional; others need it to join as admin)")
	serveCmd.Flags().StringVar(&serveDataFile, "data", "wert-data.json", "Path to persistence file")
	_ = serveCmd.MarkFlagRequired("name")
}

func runServe(cmd *cobra.Command, args []string) error {
	addr := "0.0.0.0:" + servePort

	// Print banner before the TUI takes over.
	localIPs := server.LocalIPs()
	fmt.Println()
	fmt.Println("  ▶ wert server starting…")
	fmt.Printf("  Listening on  :%s\n", servePort)
	if len(localIPs) > 0 {
		for _, ip := range localIPs {
			fmt.Printf("  Local IP      %s\n", ip)
		}
		fmt.Printf("\n  Members join with:\n")
		for _, ip := range localIPs {
			fmt.Printf("    wert join --host %s:%s --name <username>\n", ip, servePort)
		}
	}
	if serveToken != "" {
		fmt.Printf("\n  Admin token:  %s\n", serveToken)
	}
	fmt.Println()

	// Start the HTTP + WebSocket server in background.
	srv := server.New(addr, serveDataFile, serveToken)
	go srv.Start()

	// Give the server a moment to bind.
	time.Sleep(150 * time.Millisecond)

	// Connect as admin.
	host := "localhost:" + servePort
	token := serveToken
	cl, err := client.Connect(host, serveName, token)
	if err != nil {
		return fmt.Errorf("failed to connect to own server: %w", err)
	}

	joinStr := host
	if len(localIPs) > 0 {
		joinStr = strings.Join(localIPs, ", ") + ":" + servePort
	}

	m := tui.New(cl, serveName, "admin", joinStr)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	return nil
}
