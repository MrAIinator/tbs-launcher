//go:build !windows

package main

import "os/exec"

func hideConsole(cmd *exec.Cmd) {}

func detach(cmd *exec.Cmd) {}

func alert(text string) { logLine(text) }

func openWindow(url string) error { return nil }

func openExternal(url string) {}

func execCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
