package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/a-Fig/murmur/internal/brand"
)

// On Windows the launcher is a shortcut in the user's Start menu. It runs mur through
// `conhost.exe --headless`: mur is a console program, and a shortcut straight to it would open a
// console window for as long as `mur app` takes to start the window. The shortcut carries the
// app's AppUserModelID, which the window's process takes too (beforeWindow), so the window's
// taskbar button is the shortcut's: pinned, it starts the app.

// launcherPath is the shortcut's path. The Start menu is found through APPDATA, as Windows' own
// shell does for the user's, so a test that points APPDATA elsewhere keeps it out of the real one.
func launcherPath() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		return ""
	}
	return filepath.Join(base, "Microsoft", "Windows", "Start Menu", "Programs", brand.Product+".lnk")
}

func installLauncher(exe, dir, where string) error {
	ico := filepath.Join(dir, brand.Product+".ico")
	if err := os.WriteFile(ico, icoFrom(iconPNG, smallPNG), 0o644); err != nil {
		return err
	}
	conhost := filepath.Join(os.Getenv("SystemRoot"), "System32", "conhost.exe")
	if os.Getenv("SystemRoot") == "" || !exists(conhost) {
		return errors.New("no conhost.exe in " + filepath.Dir(conhost))
	}
	if err := os.MkdirAll(filepath.Dir(where), 0o755); err != nil {
		return err
	}
	return writeShortcut(where, shortcut{
		target: conhost,
		args:   "--headless " + windows.EscapeArg(exe) + " app --from-launcher",
		dir:    filepath.Dir(exe),
		icon:   ico,
		desc:   "Messages between the coding agents in your repos",
		appID:  brand.AppID,
	})
}

type shortcut struct {
	target, args, dir, icon, desc, appID string
}

var (
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procSetProcessAppID  = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
	procAllowForeground  = user32.NewProc("AllowSetForegroundWindow")
	clsidShellLink       = windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidShellLinkW        = windows.GUID{Data1: 0x000214F9, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidPersistFile       = windows.GUID{Data1: 0x0000010B, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidPropertyStore     = windows.GUID{Data1: 0x886D8EEB, Data2: 0x8CF2, Data3: 0x4446, Data4: [8]byte{0x8D, 0x02, 0xCD, 0xBA, 0x1D, 0xBD, 0xCF, 0x99}}
	pkeyAppUserModelID   = propertyKey{fmtid: windows.GUID{Data1: 0x9F4C2855, Data2: 0x9F79, Data3: 0x4B39, Data4: [8]byte{0xA8, 0xD0, 0xE1, 0xD4, 0x2D, 0xE1, 0xD5, 0xF3}}, pid: 5}
	errShortcutInterface = errors.New("the shell's shortcut object is missing an interface")
)

type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

// propVariant is PROPVARIANT holding a string (VT_LPWSTR): 24 bytes on 64-bit Windows.
type propVariant struct {
	vt       uint16
	reserved [3]uint16
	val      uintptr
	pad      uintptr
}

const (
	clsctxInproc            = 0x1
	coinitApartmentThreaded = 0x2
	vtLPWSTR                = 31
	swShowMinNoActive       = 7
)

// call calls method i of the COM object at obj, and turns a failed HRESULT into an error.
func call(obj unsafe.Pointer, i int, name string, args ...uintptr) error {
	vtbl := *(**[32]uintptr)(obj)
	hr, _, _ := syscall.SyscallN(vtbl[i], append([]uintptr{uintptr(obj)}, args...)...)
	if int32(hr) < 0 {
		return fmt.Errorf("%s: HRESULT 0x%08X", name, uint32(hr))
	}
	return nil
}

func release(obj unsafe.Pointer) {
	if obj != nil {
		call(obj, 2, "Release")
	}
}

func wide(s string) uintptr {
	p, _ := windows.UTF16PtrFromString(s)
	return uintptr(unsafe.Pointer(p))
}

// writeShortcut writes a .lnk through the shell's ShellLink object (IShellLinkW), with the
// AppUserModelID set through its property store, as Windows' own documentation does it.
func writeShortcut(path string, s shortcut) error {
	runtime.LockOSThread() // COM's apartment is the thread's
	defer runtime.UnlockOSThread()
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	if int32(hr) >= 0 {
		defer procCoUninitialize.Call()
	}
	var link unsafe.Pointer
	hr, _, _ = procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInproc, uintptr(unsafe.Pointer(&iidShellLinkW)), uintptr(unsafe.Pointer(&link)))
	if int32(hr) < 0 || link == nil {
		return fmt.Errorf("making a shortcut: HRESULT 0x%08X", uint32(hr))
	}
	defer release(link)
	// IShellLinkW: SetPath 20, SetArguments 11, SetWorkingDirectory 9, SetDescription 7,
	// SetIconLocation 17, SetShowCmd 15.
	steps := []struct {
		i    int
		name string
		args []uintptr
	}{
		{20, "SetPath", []uintptr{wide(s.target)}},
		{11, "SetArguments", []uintptr{wide(s.args)}},
		{9, "SetWorkingDirectory", []uintptr{wide(s.dir)}},
		{7, "SetDescription", []uintptr{wide(s.desc)}},
		{17, "SetIconLocation", []uintptr{wide(s.icon), 0}},
		// conhost --headless shows nothing; minimized, nothing shows should a Windows not know it.
		{15, "SetShowCmd", []uintptr{swShowMinNoActive}},
	}
	for _, st := range steps {
		if err := call(link, st.i, st.name, st.args...); err != nil {
			return err
		}
	}

	var store unsafe.Pointer
	if err := call(link, 0, "QueryInterface", uintptr(unsafe.Pointer(&iidPropertyStore)), uintptr(unsafe.Pointer(&store))); err != nil || store == nil {
		return errShortcutInterface
	}
	defer release(store)
	id, _ := windows.UTF16PtrFromString(s.appID)
	v := propVariant{vt: vtLPWSTR, val: uintptr(unsafe.Pointer(id))}
	if err := call(store, 6, "SetValue", uintptr(unsafe.Pointer(&pkeyAppUserModelID)), uintptr(unsafe.Pointer(&v))); err != nil {
		return err
	}
	if err := call(store, 7, "Commit"); err != nil {
		return err
	}
	runtime.KeepAlive(id)

	var file unsafe.Pointer
	if err := call(link, 0, "QueryInterface", uintptr(unsafe.Pointer(&iidPersistFile)), uintptr(unsafe.Pointer(&file))); err != nil || file == nil {
		return errShortcutInterface
	}
	defer release(file)
	return call(file, 6, "Save", wide(path), 1) // IPersistFile::Save
}

// setProcessAppID gives this process the app's AppUserModelID, the shortcut's, before it makes a
// window, so Windows counts the window as the shortcut's.
func setProcessAppID() {
	procSetProcessAppID.Call(wide(brand.AppID))
}

// allowForeground lets process pid take the foreground, which Windows gives only to a process the
// person has just used: the `mur app` they started, asking the open window to come forward.
func allowForeground(pid int) {
	procAllowForeground.Call(uintptr(pid))
}

// FromBundle says whether this process is the app started from its macOS bundle: never here.
func FromBundle() bool { return false }
