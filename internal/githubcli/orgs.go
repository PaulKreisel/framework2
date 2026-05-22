package githubcli

import (
	"context"
	"fmt"
	"strings"
)

// ListOrgs returns the GitHub organizations the authenticated user belongs
// to. Shells out to `gh api user/orgs --jq '.[].login'` and returns one login
// per element.
//
// Exported so sibling subcommands (notably `github code`) can reuse the same
// org-discovery path. Each call re-queries GitHub — there is no cache.
func ListOrgs(ctx context.Context) ([]string, error) {
	return listOrgs(ctx, newDefaultRunner(nil))
}

func listOrgs(ctx context.Context, runner GHRunner) ([]string, error) {
	out, err := runner(ctx, []string{"api", "user/orgs", "--jq", ".[].login"})
	if err != nil {
		return nil, fmt.Errorf("list orgs: %w", err)
	}
	var orgs []string
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			orgs = append(orgs, line)
		}
	}
	return orgs, nil
}
