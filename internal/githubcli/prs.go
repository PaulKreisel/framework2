package githubcli

import (
	"context"
	"encoding/json"
	"fmt"
)

// OpenPRNumbers returns the numbers of open PRs on ownerRepo whose head is
// branch, via `gh pr list`. Used by `jira --discover-branches`, which knows
// the exact (owner/repo, branch) pairs from branch discovery and therefore
// needs no local-dir-based PR detection.
func OpenPRNumbers(ctx context.Context, runner GHRunner, ownerRepo, branch string) ([]int, error) {
	out, err := runner(ctx, []string{"pr", "list",
		"--repo", ownerRepo,
		"--head", branch,
		"--state", "open",
		"--json", "number"})
	if err != nil {
		return nil, fmt.Errorf("list open PRs for %s@%s: %w", ownerRepo, branch, err)
	}
	var rows []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse open PRs for %s@%s: %w", ownerRepo, branch, err)
	}
	nums := make([]int, len(rows))
	for i, r := range rows {
		nums[i] = r.Number
	}
	return nums, nil
}
