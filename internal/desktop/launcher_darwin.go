package desktop

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
)

// On macOS the launcher is Murmur.app in ~/Applications, where Launchpad and Spotlight find it.
// Its executable is a symbolic link to the installed mur, so mur's self-update, which replaces
// the binary where it is, updates the app too; a copy would stay at the version it was copied at.
// Started from the bundle, mur runs the window itself (FromBundle), so the Dock's icon is the
// bundle's and a second click on it brings the window forward, as for any app.

func launcherPath() string {
	home, err := os.UserHomeDir()
	if err != nil || filepath.Clean(home) == "/" {
		return "" // a service account's: /Applications is everyone's
	}
	return filepath.Join(home, "Applications", brand.Product+".app")
}

func installLauncher(exe, dir, where string) error {
	// Made beside the old one and moved into its place, so Finder never sees half a bundle, in a
	// directory of its own, so nothing of the person's is in the way or removed (Sol review).
	if err := os.MkdirAll(filepath.Dir(where), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(where), "."+brand.Product+".app.*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp) // gone by then once it is in place
	os.Chmod(tmp, 0o755)
	tc := filepath.Join(tmp, "Contents")
	if err := os.MkdirAll(filepath.Join(tc, "MacOS"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(tc, "Resources"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tc, "Info.plist"), infoPlist(), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tc, "Resources", bundleExe+".icns"), icnsFrom(dockPNG), 0o644); err != nil {
		return err
	}
	if err := os.Symlink(exe, filepath.Join(tc, "MacOS", bundleExe)); err != nil {
		return err
	}
	os.RemoveAll(where)
	if err := os.Rename(tmp, where); err != nil {
		return err
	}
	return nil
}

// ownsLauncher says the bundle at where is Murmur's: its Info.plist names the app's identifier.
func ownsLauncher(where string) bool {
	b, err := os.ReadFile(filepath.Join(where, "Contents", "Info.plist"))
	return err == nil && strings.Contains(string(b), "<string>"+brand.AppID+"</string>")
}

func removeLauncherFiles(string) {} // the bundle holds its own icon

// FromBundle says whether this process was started from Murmur.app: its executable is the
// bundle's, which LaunchServices starts with no arguments (or a -psn_ one, before macOS 10.9).
func FromBundle() bool {
	if len(os.Args) > 2 || len(os.Args) == 2 && !strings.HasPrefix(os.Args[1], "-psn_") {
		return false
	}
	return strings.HasSuffix(filepath.Dir(os.Args[0]), ".app/Contents/MacOS") && filepath.Base(os.Args[0]) == bundleExe
}

func setProcessAppID() {} // the bundle is the app's identity

func allowForeground(int) {} // macOS lets an app that is asked to come forward do so
