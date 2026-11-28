//go:build !windows

package update

import "os"

// openIdentity opens path to read its identity. Elsewhere than Windows an open file never stops
// another from being renamed into its place.
func openIdentity(path string) (*os.File, error) { return os.Open(path) }
