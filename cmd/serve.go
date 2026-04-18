package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"wert/internal/client"
	"wert/internal/client/tui"
	gh "wert/internal/github"
	"wert/internal/server"
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
	serveGHToken  string
	serveGHOrg    string
)

func init() {
	serveCmd.Flags().StringVarP(&serveName, "name", "n", "", "Your admin display name (required)")
	serveCmd.Flags().StringVarP(&servePort, "port", "p", "8080", "Port to listen on")
	serveCmd.Flags().StringVar(&serveToken, "token", "", "Admin token (optional)")
	serveCmd.Flags().StringVar(&serveDataFile, "data", "wert-data.json", "Path to persistence file")
	serveCmd.Flags().StringVar(&serveGHToken, "github-token", "", "GitHub personal access token")
	serveCmd.Flags().StringVar(&serveGHOrg, "github-org", "", "GitHub organization name")
	_ = serveCmd.MarkFlagRequired("name")
}

func runServe(cmd *cobra.Command, args []string) error {
	// ensure admin always gets admin role even when no --token flag is passed
	if serveToken == "" {
		serveToken = uuid.New().String()
	}
	addr := "0.0.0.0:" + servePort

	localIPs := server.LocalIPs()
	fmt.Println()
	fmt.Println("  wert server starting")
	fmt.Printf("  port  :%s\n", servePort)
	if len(localIPs) > 0 {
		for _, ip := range localIPs {
			fmt.Printf("  ip    %s\n", ip)
		}
		fmt.Println()
		for _, ip := range localIPs {
			fmt.Printf("  wert join --host %s:%s --name <username>\n", ip, servePort)
		}
	}
	fmt.Println()

	srv := server.New(addr, serveDataFile, serveToken)
	go srv.Start()
	time.Sleep(150 * time.Millisecond)

	host := "localhost:" + servePort
	cl, err := client.Connect(host, serveName, serveToken)
	if err != nil {
		return fmt.Errorf("failed to connect to own server: %w", err)
	}

	joinStr := host
	if len(localIPs) > 0 {
		joinStr = strings.Join(localIPs, ", ") + ":" + servePort
	}

	ghClient := gh.New(serveGHToken, serveGHOrg)
	m := tui.New(cl, serveName, "admin", joinStr, serveToken, ghClient)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	return nil
}
