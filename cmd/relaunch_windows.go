//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
)

func relaunch(execPath string) {
	c := exec.Command(execPath, os.Args[1:]...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "relaunch failed:", err)
		os.Exit(1)
	}
	os.Exit(0)
}
