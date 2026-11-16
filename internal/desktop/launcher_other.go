//go:build !windows && !darwin && !(linux && desktop)

package desktop

// A build without the window has no launcher.

func launcherPath() string { return "" }

func installLauncher(exe, dir, where string) error { return nil }

func ownsLauncher(string) bool { return false }

func removeLauncherFiles(string) {}

// FromBundle says whether this process is the app started from its macOS bundle: never here.
func FromBundle() bool { return false }

func allowForeground(int) {}
