// Package collaborators lists the people who can push to a GitHub repository.
package collaborators

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// List uses the local GitHub CLI's credentials. GitHub must allow that account to
// list collaborators. An incomplete or empty response is an error.
func List(ctx context.Context, addr string) ([]string, error) {
	if addr == "" {
		return nil, errors.New("the repo isn't on GitHub, so its collaborators can't be listed")
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, errors.New("listing the collaborators needs the GitHub CLI, gh, which isn't installed")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "api", "--paginate", "repos/"+strings.TrimPrefix(addr, "github.com/")+"/collaborators", "--jq", ".[] | select(.permissions.push) | .login")
	b, err := cmd.Output()
	if err != nil {
		return nil, errors.New("gh couldn't list the collaborators (is it signed in, with access to the repo?)")
	}
	var logins []string
	for _, l := range strings.Fields(string(b)) {
		logins = append(logins, strings.ToLower(l))
	}
	if len(logins) == 0 {
		return nil, errors.New("gh listed no collaborators who can push")
	}
	return logins, nil
}
