//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"syscall"
)

func relaunch(execPath string) {
	if err := syscall.Exec(execPath, os.Args, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "relaunch failed:", err)
		os.Exit(1)
	}
}
