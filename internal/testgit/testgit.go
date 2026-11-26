// Package testgit sets up git for tests: it keeps them from reading the developer's own git config,
// and makes a bare remote refuse pushes the way GitHub does now and then.
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
	// Nor a git identity from the environment, which would win over a test's own config (#1244).
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		os.Unsetenv(k)
	}
}
