//go:build !windows

package main

import (
	"fmt"
	"os/exec"
)

func hideChildWindow(cmd *exec.Cmd)                      {}
func acquireDesktopInstance(root string) (func(), error) { return func() {}, nil }
func showDesktopError(message string)                    { fmt.Println(message) }
func startDesktop(app *application, baseURL string, stop func()) (func(), error) {
	return func() {}, nil
}
