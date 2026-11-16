//go:build linux && desktop

package desktop

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
)

// On Linux the launcher is a .desktop file in the user's applications, with the app's icon in the
// user's hicolor theme, which every desktop's menu reads (the freedesktop.org specifications).

func dataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil || filepath.Clean(home) == "/" {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}

func launcherPath() string {
	d := dataHome()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "applications", brand.AppID+".desktop")
}

func iconPath() string {
	return filepath.Join(dataHome(), "icons", "hicolor", "256x256", "apps", brand.AppID+".png")
}

// ownsLauncher says the .desktop file at where is Murmur's: it names the app's icon.
func ownsLauncher(where string) bool {
	b, err := os.ReadFile(where)
	return err == nil && strings.Contains(string(b), "\nIcon="+brand.AppID+"\n")
}

// removeLauncherFiles takes out the icon the .desktop file used.
func removeLauncherFiles(string) { os.Remove(iconPath()) }

func installLauncher(exe, dir, where string) error {
	icon := iconPath()
	if err := os.MkdirAll(filepath.Dir(icon), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(icon, iconPNG, 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(where), 0o755); err != nil {
		return err
	}
	return os.WriteFile(where, []byte(desktopEntry(exe)), 0o644)
}

// FromBundle says whether this process is the app started from its macOS bundle: never here.
func FromBundle() bool { return false }

func setProcessAppID() {}

func allowForeground(int) {}
