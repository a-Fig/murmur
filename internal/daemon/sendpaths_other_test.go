//go:build !windows

package daemon_test

// shortName is path's short name, which only Windows has.
func shortName(string) string { return "" }
