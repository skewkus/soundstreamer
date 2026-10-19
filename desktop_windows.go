//go:build windows

package main

import (
	"crypto/sha256"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

var trayUser32 = syscall.NewLazyDLL("user32.dll")
var trayShell32 = syscall.NewLazyDLL("shell32.dll")
var trayKernel32 = syscall.NewLazyDLL("kernel32.dll")
var trayPostMessage = trayUser32.NewProc("PostMessageW")
var trayNotify = trayShell32.NewProc("Shell_NotifyIconW")

func wide(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }
func hideChildWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
func acquireDesktopInstance(root string) (func(), error) {
	absolute, _ := filepath.Abs(root)
	hash := sha256.Sum256([]byte(strings.ToLower(absolute)))
	handle, _, err := trayKernel32.NewProc("CreateMutexW").Call(0, 0, uintptr(unsafe.Pointer(wide(fmt.Sprintf("Local\\SoundStreamer-%x", hash[:16])))))
	if handle == 0 {
		return nil, fmt.Errorf("create app instance lock: %v", err)
	}
	release := func() { trayKernel32.NewProc("CloseHandle").Call(handle) }
	if err == syscall.Errno(183) {
		release()
		return nil, fmt.Errorf("Sound Streamer is already running. Use its system tray icon to open the controls or exit")
	}
	return release, nil
}
func desktopMessage(owner uintptr, title, message string, flags uintptr) {
	trayUser32.NewProc("MessageBoxW").Call(owner, uintptr(unsafe.Pointer(wide(message))), uintptr(unsafe.Pointer(wide(title))), flags|0x10000)
}
func showDesktopError(message string) { desktopMessage(0, "Sound Streamer — Error", message, 0x10) }

type trayPoint struct{ X, Y int32 }
type trayMessage struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Point          trayPoint
	Private        uint32
}
type trayWindowClass struct {
	Size, Style                        uint32
	Procedure                          uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	SmallIcon                          uintptr
}
type trayIconData struct {
	Size                uint32
	Window              uintptr
	ID, Flags, Callback uint32
	Icon                uintptr
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Version             uint32
	InfoTitle           [64]uint16
	InfoFlags           uint32
	GUID                [16]byte
	BalloonIcon         uintptr
}

const trayCallback = 0x8001
const trayShutdown = 0x8002

type desktopTray struct {
	app            *application
	baseURL        string
	stop           func()
	window         uintptr
	icon           trayIconData
	taskbarCreated uint32
	stopping       bool
}

func (t *desktopTray) portInfo() string {
	t.app.mu.Lock()
	port, saved := t.app.activePort, t.app.config.ListenPort
	activeHTTPS, savedHTTPS := t.app.activeHTTPS, t.app.config.HTTPSEnabled
	t.app.mu.Unlock()
	protocol := "HTTP"
	if activeHTTPS {
		protocol = "HTTPS"
	}
	text := fmt.Sprintf("Currently listening with %s on port %d.\n\nChange the port in Admin → Settings → Server. Changes apply after exiting and restarting Sound Streamer.", protocol, port)
	if port != saved || activeHTTPS != savedHTTPS {
		savedProtocol := "HTTP"
		if savedHTTPS {
			savedProtocol = "HTTPS"
		}
		text += fmt.Sprintf("\n\nSaved for the next start: %s on port %d (port can be overridden with -addr).", savedProtocol, saved)
	}
	return text
}
func (t *desktopTray) open(target string) {
	result, _, _ := trayShell32.NewProc("ShellExecuteW").Call(t.window, uintptr(unsafe.Pointer(wide("open"))), uintptr(unsafe.Pointer(wide(target))), 0, 0, 1)
	if result <= 32 {
		desktopMessage(t.window, "Sound Streamer", fmt.Sprintf("Could not open %s (Windows error %d).", target, result), 0x10)
	}
}
func (t *desktopTray) menu() {
	if t.stopping {
		return
	}
	menu, _, _ := trayUser32.NewProc("CreatePopupMenu").Call()
	if menu == 0 {
		return
	}
	defer trayUser32.NewProc("DestroyMenu").Call(menu)
	appendItem := func(id uintptr, label string) {
		trayUser32.NewProc("AppendMenuW").Call(menu, 0, id, uintptr(unsafe.Pointer(wide(label))))
	}
	separator := func() { trayUser32.NewProc("AppendMenuW").Call(menu, 0x800, 0, 0) }
	appendItem(1, "Open Admin")
	appendItem(2, "Open Control")
	appendItem(3, "Open Stream")
	t.app.mu.Lock()
	port := t.app.activePort
	t.app.mu.Unlock()
	separator()
	appendItem(8, fmt.Sprintf("Port: %d", port))
	appendItem(4, "Stop playback")
	separator()
	appendItem(5, "Open log file")
	appendItem(6, "About Sound Streamer")
	separator()
	appendItem(7, "Exit Sound Streamer")
	var point trayPoint
	trayUser32.NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&point)))
	trayUser32.NewProc("SetForegroundWindow").Call(t.window)
	selected, _, _ := trayUser32.NewProc("TrackPopupMenu").Call(menu, 0x102, uintptr(point.X), uintptr(point.Y), 0, t.window, 0)
	trayPostMessage.Call(t.window, 0, 0, 0)
	switch selected {
	case 1:
		t.open(t.baseURL + "/admin")
	case 2:
		t.open(t.baseURL + "/control")
	case 3:
		t.open(t.baseURL + "/stream")
	case 4:
		go func() {
			if err := t.app.action(actionRequest{Action: "stop"}); err != nil {
				showDesktopError(err.Error())
			}
		}()
	case 5:
		t.open(filepath.Join(t.app.root, "soundstreamer.log"))
	case 6:
		desktopMessage(t.window, "About Sound Streamer", fmt.Sprintf("Sound Streamer\nVersion %s\n\nFree and open-source software (MIT License).\n\nSynchronized audio and video for your rooms.\n\n%s\n\n%s\n\nConfiguration and logs:\n%s", appVersion, t.baseURL, t.portInfo(), t.app.root), 0x40)
	case 7:
		t.stopping = true
		t.stop()
	case 8:
		desktopMessage(t.window, "Sound Streamer — Port", t.portInfo(), 0x40)
	}
}
func (t *desktopTray) procedure(window uintptr, message uint32, wParam, lParam uintptr) uintptr {
	if message == t.taskbarCreated && message != 0 {
		trayNotify.Call(0, uintptr(unsafe.Pointer(&t.icon)))
		return 0
	}
	switch message {
	case trayCallback:
		switch uint32(lParam) {
		case 0x205, 0x7b:
			t.menu()
		case 0x203:
			t.open(t.baseURL + "/admin")
		}
		return 0
	case 0x11:
		return 1
	case 0x16:
		if wParam != 0 {
			t.stop()
		}
		return 0
	case 0x10:
		t.stopping = true
		t.stop()
		return 0
	case trayShutdown:
		trayNotify.Call(2, uintptr(unsafe.Pointer(&t.icon)))
		trayUser32.NewProc("DestroyWindow").Call(window)
		return 0
	case 2:
		trayUser32.NewProc("PostQuitMessage").Call(0)
		return 0
	}
	result, _, _ := trayUser32.NewProc("DefWindowProcW").Call(window, uintptr(message), wParam, lParam)
	return result
}
func startDesktop(app *application, baseURL string, stop func()) (func(), error) {
	t := &desktopTray{app: app, baseURL: baseURL, stop: stop}
	ready := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(done)
		instance, _, _ := trayKernel32.NewProc("GetModuleHandleW").Call(0)
		// The resource is embedded once and shared by Explorer, the EXE and the tray.
		icon, _, iconErr := trayUser32.NewProc("LoadImageW").Call(instance, uintptr(unsafe.Pointer(wide("APP"))), 1, 32, 32, 0x8000)
		if icon == 0 {
			ready <- fmt.Errorf("load tray icon: %v", iconErr)
			return
		}
		className := wide("SoundStreamer.Tray." + randomID())
		class := trayWindowClass{Procedure: syscall.NewCallback(t.procedure), Instance: instance, ClassName: className, Icon: icon, SmallIcon: icon}
		class.Size = uint32(unsafe.Sizeof(class))
		atom, _, err := trayUser32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&class)))
		if atom == 0 {
			ready <- fmt.Errorf("register tray window: %v", err)
			return
		}
		defer trayUser32.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(className)), instance)
		window, _, err := trayUser32.NewProc("CreateWindowExW").Call(0x80, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(wide("Sound Streamer"))), 0x80000000, 0, 0, 0, 0, 0, 0, instance, 0)
		if window == 0 {
			ready <- fmt.Errorf("create tray window: %v", err)
			return
		}
		t.window = window
		taskbarCreated, _, _ := trayUser32.NewProc("RegisterWindowMessageW").Call(uintptr(unsafe.Pointer(wide("TaskbarCreated"))))
		t.taskbarCreated = uint32(taskbarCreated)
		t.icon = trayIconData{Window: window, ID: 1, Flags: 7, Callback: trayCallback, Icon: icon}
		t.icon.Size = uint32(unsafe.Sizeof(t.icon))
		copy(t.icon.Tip[:], syscall.StringToUTF16("Sound Streamer "+appVersion))
		ok, _, err := trayNotify.Call(0, uintptr(unsafe.Pointer(&t.icon)))
		if ok == 0 {
			trayUser32.NewProc("DestroyWindow").Call(window)
			ready <- fmt.Errorf("create tray icon: %v", err)
			return
		}
		ready <- nil
		var message trayMessage
		for {
			result, _, _ := trayUser32.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
			if int32(result) <= 0 {
				break
			}
			trayUser32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&message)))
			trayUser32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&message)))
		}
		stop()
	}()
	if err := <-ready; err != nil {
		<-done
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { trayPostMessage.Call(t.window, trayShutdown, 0, 0); <-done }) }, nil
}
