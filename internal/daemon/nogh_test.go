package daemon

import (
	"context"

	"github.com/a-Fig/murmur/internal/web"
)

// No test here asks GitHub anything (see newPage). This file is in the package, not in
// daemon_test, so that it also holds for the tests there.
func init() {
	newPage = func(src web.Source, token, _ string) *web.Server {
		return web.NewWithGitHub(src, token, func(context.Context) (map[string]web.PR, error) { return nil, nil }, nil)
	}
}
