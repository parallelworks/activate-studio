package main

// Wails v2 on Windows runs the web view's MoveFocus on every WM_SETFOCUS the
// main window receives, and ends the process when MoveFocus fails (go-webview2
// errorCallback calls os.Exit(1)). While the folder dialog is open the main
// window is disabled, MoveFocus fails with "The parameter is incorrect", and
// any program that activates the window (SetForegroundWindow) ends the shell.
//
// The guard puts a window procedure in front of Wails' for the time a dialog
// is open. It drops WM_SETFOCUS, remembers that one came, and when the dialog
// has closed sends one WM_SETFOCUS itself, so the web view takes the keyboard
// focus back as it would have. Every other message goes to Wails' procedure.

import (
	"os"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmSetFocus   = 0x0007
	gwlpWndProc  = ^uintptr(3) // -4
	guardWinName = "ACTIVATE Studio"
)

var (
	user32                = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows       = user32.NewProc("EnumWindows")
	procGetWindowThreadID = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible   = user32.NewProc("IsWindowVisible")
	procGetWindow         = user32.NewProc("GetWindow")
	procGetWindowTextW    = user32.NewProc("GetWindowTextW")
	procSetWindowLongPtr  = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProc    = user32.NewProc("CallWindowProcW")
	procSendMessageW      = user32.NewProc("SendMessageW")

	guardOnce    sync.Once
	guardHwnd    uintptr
	guardOrig    uintptr
	guardHolding atomic.Int32
	guardPending atomic.Bool
	guardProcPtr = windows.NewCallback(guardProc)
)

func guardProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if msg == wmSetFocus && guardHolding.Load() > 0 {
		guardPending.Store(true)
		return 0
	}
	r, _, _ := procCallWindowProc.Call(guardOrig, hwnd, msg, wparam, lparam)
	return r
}

// findMainWindow: the visible top-level window of this process with the
// application's title.
func findMainWindow() uintptr {
	var found uintptr
	pid := uint32(os.Getpid())
	cb := windows.NewCallback(func(h, _ uintptr) uintptr {
		var wp uint32
		procGetWindowThreadID.Call(h, uintptr(unsafe.Pointer(&wp)))
		if wp != pid {
			return 1
		}
		if v, _, _ := procIsWindowVisible.Call(h); v == 0 {
			return 1
		}
		if owner, _, _ := procGetWindow.Call(h, 4); owner != 0 { // GW_OWNER
			return 1
		}
		buf := make([]uint16, 256)
		n, _, _ := procGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if windows.UTF16ToString(buf[:n]) == guardWinName {
			found = h
			return 0
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	return found
}

// holdFocus is called before a dialog opens; the function it returns is
// called after the dialog closed. It does nothing when the guard is off or
// the window cannot be found.
func holdFocus(enabled bool) (release func()) {
	if !enabled {
		return func() {}
	}
	guardOnce.Do(func() {
		guardHwnd = findMainWindow()
		if guardHwnd == 0 {
			return
		}
		old, _, _ := procSetWindowLongPtr.Call(guardHwnd, gwlpWndProc, guardProcPtr)
		guardOrig = old
	})
	if guardHwnd == 0 || guardOrig == 0 {
		return func() {}
	}
	guardHolding.Add(1)
	return func() {
		if guardHolding.Add(-1) == 0 && guardPending.Swap(false) {
			procSendMessageW.Call(guardHwnd, wmSetFocus, 0, 0)
		}
	}
}
