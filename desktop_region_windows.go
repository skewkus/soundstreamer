package main

import (
	"fmt"
	"syscall"
)

func desktopRegionArgs() []string {
	user32 := syscall.NewLazyDLL("user32.dll")
	user32.NewProc("SetProcessDPIAware").Call()
	metrics := user32.NewProc("GetSystemMetrics")
	width, _, _ := metrics.Call(0)
	height, _, _ := metrics.Call(1)
	return []string{"-offset_x", "0", "-offset_y", "0", "-video_size", fmt.Sprintf("%dx%d", width, height)}
}
