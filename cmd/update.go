package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"wert/internal/updater"
	"wert/internal/version"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update wert to the latest release from GitHub",
	RunE:  runUpdate,
}

func init() {
	rootCmd.AddCommand(updateCmd)
}

func runUpdate(_ *cobra.Command, _ []string) error {
	fmt.Printf("  current version: %s\n", version.Version)

	rel, err := updater.LatestRelease()
	if err != nil {
		return fmt.Errorf("fetch release: %w", err)
	}
	if rel.TagName == version.Version {
		fmt.Printf("  already up to date (%s)\n", version.Version)
		return nil
	}

	_, tag, err := updater.Update(os.Stdout)
	if err != nil {
		return err
	}
	fmt.Printf("  updated to %s — run wert again to use the new version\n", tag)
	return nil
}

// SelfUpdateAndRelaunch runs the updater then re-execs with the original args.
// Called after the TUI exits when the user typed :update in the cmdline.
func SelfUpdateAndRelaunch() {
	execPath, tag, err := updater.Update(os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "  update failed:", err)
		os.Exit(1)
	}
	fmt.Printf("  updated to %s — relaunching...\n\n", tag)
	relaunch(execPath)
}

