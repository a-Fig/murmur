// Package testgit keeps a package's tests from reading the developer's own git config.
package testgit

import "os"

// Isolate points git at no global or system config, for this test binary and everything it starts.
// Call it in TestMain. Murmur reads murmur.update, murmur.channel and murmur.remote through plain git
// config, so a maintainer's own settings would otherwise change what the tests see: on a machine with
// murmur.channel=prerelease, internal/update's tests failed (#262). os.DevNull reads as empty, and
// nothing in Murmur or its tests writes to global config.
func Isolate() {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}
