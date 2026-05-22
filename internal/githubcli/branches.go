package githubcli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// batchSize is how many repos go into one GraphQL request. Mirrors the prior
// art (find-ticket-branches) — large enough to keep the round-trip count low,
// small enough that GitHub doesn't reject the query for size.
const batchSize = 25

// refsPerRepo is the per-repo cap on refs returned by the GraphQL `refs(first:)`
// argument. Repos with >100 branches risk hidden matches; the prior bash
// script accepted the same trade-off.
const refsPerRepo = 100

// repoActivityWindow is how far back a repo's last push may be for us to
// search its branches. Hardcoded; see the ticket spec.
const repoActivityWindow = 365 * 24 * time.Hour

// Branch is one matching (owner, repo, branch-name) triple.
type Branch struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
}

// repoInfo mirrors the subset of `gh repo list --json name,pushedAt` we care
// about.
type repoInfo struct {
	Name     string    `json:"name"`
	PushedAt time.Time `json:"pushedAt"`
}

// listRepos returns the names of non-archived repos in org whose last push
// happened after cutoff. The order matches `gh repo list` output (and is then
// normalised by sorting before batching, so batch composition is
// deterministic).
func listRepos(ctx context.Context, runner GHRunner, org string, cutoff time.Time) ([]string, error) {
	out, err := runner(ctx, []string{"repo", "list", org, "--no-archived", "-L", "1000", "--json", "name,pushedAt"})
	if err != nil {
		return nil, fmt.Errorf("list repos for %s: %w", org, err)
	}
	var rs []repoInfo
	if err := json.Unmarshal(out, &rs); err != nil {
		return nil, fmt.Errorf("parse repos for %s: %w", org, err)
	}
	names := make([]string, 0, len(rs))
	for _, r := range rs {
		if r.PushedAt.After(cutoff) {
			names = append(names, r.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// buildBranchQuery builds a single GraphQL document that fetches refs/heads/*
// (up to [refsPerRepo] per repo) for each repo in batch. Each repo is given a
// stable rN alias so the response can be decoded as a map.
func buildBranchQuery(owner string, repos []string) string {
	var b strings.Builder
	b.WriteString("{")
	for i, name := range repos {
		fmt.Fprintf(&b, " r%d: repository(owner: %q, name: %q) { name refs(refPrefix: \"refs/heads/\", first: %d) { nodes { name } } }", i, owner, name, refsPerRepo)
	}
	b.WriteString(" }")
	return b.String()
}

// graphqlBranchResponse is the shape of `gh api graphql` output for the query
// produced by [buildBranchQuery]. Each map value is one repository node; a
// nil entry means the alias resolved to null (e.g. permission denied for a
// single repo) and is silently skipped by the parser.
type graphqlBranchResponse struct {
	Data map[string]*struct {
		Name string `json:"name"`
		Refs struct {
			Nodes []struct {
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"refs"`
	} `json:"data"`
}

// parseBranchResponse decodes one GraphQL batch response and returns every
// (owner, repo, branch) triple whose branch name contains searchTerm
// (case-insensitive substring). Output is unsorted; the caller merges and
// sorts globally.
func parseBranchResponse(raw []byte, owner, searchTerm string) ([]Branch, error) {
	var resp graphqlBranchResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse GraphQL response: %w", err)
	}
	needle := strings.ToLower(searchTerm)
	var matches []Branch
	for _, repo := range resp.Data {
		if repo == nil {
			continue
		}
		for _, ref := range repo.Refs.Nodes {
			if strings.Contains(strings.ToLower(ref.Name), needle) {
				matches = append(matches, Branch{
					Owner:  owner,
					Repo:   repo.Name,
					Branch: ref.Name,
				})
			}
		}
	}
	return matches, nil
}

// runBranchBatch runs one (owner, repos-batch) GraphQL call and parses the
// response into matching branches.
func runBranchBatch(ctx context.Context, runner GHRunner, owner string, repos []string, searchTerm string) ([]Branch, error) {
	query := buildBranchQuery(owner, repos)
	out, err := runner(ctx, []string{"api", "graphql", "-f", "query=" + query})
	if err != nil {
		return nil, fmt.Errorf("graphql for %s: %w", owner, err)
	}
	return parseBranchResponse(out, owner, searchTerm)
}

// ListBranches enumerates the orgs the user belongs to, lists each org's
// recently-active repos, and runs the GraphQL refs query in parallel batches.
// Returns all matches sorted by `<owner>/<repo>` then branch name.
func ListBranches(ctx context.Context, runner GHRunner, searchTerm string) ([]Branch, error) {
	orgs, err := listOrgs(ctx, runner)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-repoActivityWindow)

	type batch struct {
		owner string
		repos []string
	}
	var batches []batch
	for _, org := range orgs {
		repos, err := listRepos(ctx, runner, org, cutoff)
		if err != nil {
			return nil, err
		}
		for i := 0; i < len(repos); i += batchSize {
			end := min(i+batchSize, len(repos))
			batches = append(batches, batch{owner: org, repos: repos[i:end]})
		}
	}

	var (
		mu       sync.Mutex
		all      []Branch
		firstErr error
		wg       sync.WaitGroup
	)
	for _, b := range batches {
		wg.Add(1)
		go func(b batch) {
			defer wg.Done()
			res, err := runBranchBatch(ctx, runner, b.owner, b.repos, searchTerm)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			all = append(all, res...)
		}(b)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	sort.Slice(all, func(i, j int) bool {
		ki := all[i].Owner + "/" + all[i].Repo
		kj := all[j].Owner + "/" + all[j].Repo
		if ki != kj {
			return ki < kj
		}
		return all[i].Branch < all[j].Branch
	})
	return all, nil
}
