// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package tray

import (
	"context"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This file is every Win32 call this package makes, and it is one file for that
// reason: the judgement lives in tray.go where a test can reach it, and what is
// here is the part that needs a desktop with a taskbar and therefore cannot be
// tested on a runner.

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassEx     = user32.NewProc("RegisterClassExW")
	procCreateWindowEx      = user32.NewProc("CreateWindowExW")
	procDefWindowProc       = user32.NewProc("DefWindowProcW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procGetMessage          = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessage     = user32.NewProc("DispatchMessageW")
	procPostMessage         = user32.NewProc("PostMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenu          = user32.NewProc("AppendMenuW")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procLoadIcon            = user32.NewProc("LoadIconW")
	procRegisterWindowMsg   = user32.NewProc("RegisterWindowMessageW")
	procShellNotifyIcon     = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandle     = kernel32.NewProc("GetModuleHandleW")
	procMessageBox          = user32.NewProc("MessageBoxW")
)

// The Win32 constants this file uses, named rather than spelled at the call
// site: a bare 0x0402 in the middle of a message switch is a number nobody can
// check against the documentation.
const (
	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmCommand       = 0x0111
	wmTrayCallback  = 0x0400 + 1 // WM_APP + 1, this window's own
	wmRButtonUp     = 0x0205
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	mfString = 0x00000000

	tpmLeftAlign   = 0x0000
	tpmRightAlign  = 0x0008
	tpmBottomAlign = 0x0020
	tpmRightButton = 0x0002

	idiApplication = 32512

	cwUseDefault = ^uintptr(0) - 0x7FFFFFFF // 0x80000000 as a signed default
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type msg struct {
	Owner   windows.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type point struct{ X, Y int32 }

// notifyIconData is the shell's own structure. Only the fields this program
// sets are named; the tail is padding the shell reads by size, which is why
// Size is filled from the struct rather than from a constant.
type notifyIconData struct {
	Size            uint32
	Wnd             windows.Handle
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            windows.Handle
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUIDItem        windows.GUID
	BalloonIcon     windows.Handle
}

// className is the window class this program registers.
//
// One name per program rather than per instance: registering the same class
// twice in one process fails, and that failure is what tells a second Run in
// one process that it is a second one.
const className = "BlankTrailWbmonTray"

// run shows the icon and pumps its messages.
func (i *Icon) run(ctx context.Context) error {
	// Windows delivers a window's messages to the thread that created it, and
	// Go moves goroutines between threads. Without this the loop below waits
	// for messages on whichever thread it happens to be on, and the icon never
	// answers a press.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	instance, _, _ := procGetModuleHandle.Call(0)

	class, err := windows.UTF16PtrFromString(className)
	if err != nil {
		return fmt.Errorf("tray: class name: %w", err)
	}

	// The shell announces its own restart with this message, and a program that
	// ignores it loses its icon whenever Explorer is restarted — which on a
	// machine somebody actually uses happens.
	restarted, err := windows.UTF16PtrFromString("TaskbarCreated")
	if err != nil {
		return fmt.Errorf("tray: taskbar message: %w", err)
	}
	taskbarCreated, _, _ := procRegisterWindowMsg.Call(uintptr(unsafe.Pointer(restarted)))

	var window windows.Handle
	proc := syscall.NewCallback(func(hwnd windows.Handle, message uint32, wParam, lParam uintptr) uintptr {
		switch message {
		case wmTrayCallback:
			// Both buttons open the menu, and so does a double click. A tray
			// icon whose left button does nothing is an icon people press twice
			// and then leave alone.
			switch lParam {
			case wmRButtonUp, wmLButtonUp, wmLButtonDblClk:
				i.popup(hwnd)
			}
			return 0
		case wmCommand:
			// The low word is the menu id. A press that chose nothing arrives as
			// zero, which is why no line carries that id.
			if id := uint32(wParam & 0xFFFF); id != 0 {
				i.Menu.Chose(i.Labels, id)
			}
			return 0
		case wmClose, wmDestroy:
			procPostQuitMessage.Call(0)
			return 0
		}
		if message == uint32(taskbarCreated) {
			// Put the icon back. The shell forgot it, not this program — and
			// if it refuses, the icon is gone with nobody watching, so it is
			// the one thing in here worth saying out loud.
			i.report(i.add(hwnd))
			return 0
		}
		ret, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
		return ret
	})

	wc := wndClassEx{
		WndProc:   proc,
		Instance:  windows.Handle(instance),
		ClassName: class,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if atom, _, err := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		return fmt.Errorf("tray: register window class: %w", err)
	}

	// A window nobody sees. It exists to receive messages: the icon is the
	// visible part, and a real window would put an empty frame on the taskbar
	// beside it.
	hwnd, _, err := procCreateWindowEx.Call(
		0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(class)),
		0, cwUseDefault, cwUseDefault, 0, 0, 0, 0, instance, 0)
	if hwnd == 0 {
		return fmt.Errorf("tray: create window: %w", err)
	}
	window = windows.Handle(hwnd)
	defer procDestroyWindow.Call(hwnd)

	if err := i.add(window); err != nil {
		return err
	}
	// Removed on every way out, including a panic on the way. An icon left
	// behind by a program that is gone stays in the notification area until the
	// shell is restarted, and pressing it does nothing.
	defer i.remove(window)

	// Stop, and the context, both end the loop through the same door: a posted
	// close, which the message loop is already waiting for. Anything that
	// touched the window from another goroutine would be touching it from the
	// wrong thread.
	i.stop = func() { procPostMessage.Call(uintptr(window), wmClose, 0, 0) }
	watching, cancelWatch := context.WithCancel(ctx)
	defer cancelWatch()
	go func() {
		<-watching.Done()
		procPostMessage.Call(uintptr(window), wmClose, 0, 0)
	}()

	var m msg
	for {
		got, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		// Zero is the quit this program posted; -1 is an error, and carrying on
		// after one is a loop that spins.
		if int32(got) <= 0 {
			return nil
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// add puts the icon in the notification area.
func (i *Icon) add(window windows.Handle) error {
	data, err := i.notifyData(window)
	if err != nil {
		return err
	}
	// Modify first is deliberate on a shell restart: the shell may or may not
	// still hold the icon, and an add over one it holds fails while a modify
	// over one it forgot fails too. Trying both makes either state end the same
	// way.
	if ok, _, err := procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(data))); ok == 0 {
		if ok, _, _ := procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(data))); ok == 0 {
			return fmt.Errorf("tray: add icon: %w", err)
		}
	}
	return nil
}

func (i *Icon) remove(window windows.Handle) {
	data, err := i.notifyData(window)
	if err != nil {
		return
	}
	procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(data)))
}

// notifyData fills the shell's structure.
//
// The icon is the stock application one. This program ships no .ico and no
// resource section: an icon compiled in needs a resource step in the build, and
// the release workflow cross-compiles six platforms from one Linux runner with
// nothing but the go tool. A recognisable icon is worth having and is worth
// having properly, with go-winres in the build — which is a change to the
// release pipeline, not to this file.
func (i *Icon) notifyData(window windows.Handle) (*notifyIconData, error) {
	icon, _, _ := procLoadIcon.Call(0, idiApplication)

	data := &notifyIconData{
		Wnd:             window,
		ID:              1,
		Flags:           nifMessage | nifIcon | nifTip,
		CallbackMessage: wmTrayCallback,
		Icon:            windows.Handle(icon),
	}
	data.Size = uint32(unsafe.Sizeof(*data))

	tip, err := windows.UTF16FromString(i.Tooltip)
	if err != nil {
		return nil, fmt.Errorf("tray: tooltip: %w", err)
	}
	// Truncated to what the structure holds, terminator included. The shell
	// reads a fixed array, and a tooltip longer than it would be read past its
	// end.
	if len(tip) > len(data.Tip) {
		tip = tip[:len(data.Tip)-1]
		tip = append(tip, 0)
	}
	copy(data.Tip[:], tip)
	return data, nil
}

// popup shows the menu at the pointer.
func (i *Icon) popup(window windows.Handle) {
	handle, _, _ := procCreatePopupMenu.Call()
	if handle == 0 {
		return
	}
	defer procDestroyMenu.Call(handle)

	// The lines come from tray.go, which is where what they say and what they do
	// is decided — including whether the middle one reads "Пауза" or
	// "Продолжить", which is read off the program at the moment the menu opens.
	for _, item := range i.Menu.Lines(i.Labels) {
		label, err := windows.UTF16PtrFromString(item.Label)
		if err != nil {
			continue
		}
		procAppendMenu.Call(handle, mfString, uintptr(item.ID), uintptr(unsafe.Pointer(label)))
	}

	var at point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&at)))
	// Without this the menu belongs to a window that is not in front, and
	// Windows dismisses it the moment the pointer moves — the documented dance
	// for a notification-area menu.
	procSetForegroundWindow.Call(uintptr(window))
	procTrackPopupMenu.Call(handle,
		tpmRightButton|tpmBottomAlign|tpmLeftAlign|tpmRightAlign,
		uintptr(at.X), uintptr(at.Y), 0, uintptr(window), 0)
}

// Alert says something to a person with no console.
//
// The one thing in this package that is not about the icon, and it is here
// because it is the same platform and the same reason: a program built with
// -H windowsgui has nowhere to print, so a failure that stops it before the
// log file exists has to be shown or it is not reported at all.
func Alert(title, text string) {
	const mbIconError = 0x00000010
	head, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	body, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	procMessageBox.Call(0, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(head)), mbIconError)
}
