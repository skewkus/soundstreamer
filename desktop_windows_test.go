//go:build windows

package main

import (
	"context"
	"os/exec"
	"testing"
	"unsafe"
)

func TestWindowsDesktopContracts(t *testing.T) {
	if unsafe.Sizeof(trayIconData{}) != 976 || unsafe.Sizeof(trayWindowClass{}) != 80 || unsafe.Sizeof(trayMessage{}) != 48 {
		t.Fatal("Win64 GUI structures have invalid layout")
	}
	root := t.TempDir()
	release, err := acquireDesktopInstance(root)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := acquireDesktopInstance(root); err == nil {
		second()
		release()
		t.Fatal("second app instance accepted")
	}
	release()
	again, err := acquireDesktopInstance(root)
	if err != nil {
		t.Fatal("app instance lock was not released")
	}
	again()
	cmd := backgroundCommand(context.Background(), "unused-command")
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&0x08000000 == 0 {
		t.Fatal("child command could show a console")
	}
	// Validate the child flags using an actual native CUI process with captured output.
	command := exec.Command("cmd.exe", "/c", "echo", "hidden-child-ok")
	hideChildWindow(command)
	output, err := command.Output()
	if err != nil || len(output) == 0 {
		t.Fatal("hidden command failed")
	}
	instance, _, _ := trayKernel32.NewProc("GetModuleHandleW").Call(0)
	icon, _, err := trayUser32.NewProc("LoadImageW").Call(instance, uintptr(unsafe.Pointer(wide("APP"))), 1, 32, 32, 0x8000)
	if icon == 0 {
		t.Fatalf("embedded tray icon cannot load: %v", err)
	}
}
