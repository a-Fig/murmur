//go:build !windows

package hook

// fillUserDirs is a no-op outside Windows: the user's folders there come from HOME and XDG's
// variables, which Muse passes on.
func fillUserDirs() {}
