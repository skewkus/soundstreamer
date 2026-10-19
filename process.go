package main

import (
	"context"
	"os/exec"
)

func backgroundCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	hideChildWindow(cmd)
	return cmd
}
