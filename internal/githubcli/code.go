package githubcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// defaultCodeThreshold is the default total-result threshold above which the
// human renderer collapses content-only matches to a per-repo count.
const defaultCodeThreshold = 100

// codeCommand parses `github code` arguments and dispatches the search.
//
// CLI:
//
//	code <search-term>... [--threshold <n>] [--json] [-- <gh flags>...]
//
// Everything after `--` is forwarded verbatim to `gh search code`.
func codeCommand(ctx context.Context, runner GHRunner, args []string, stdout io.Writer) error {
	var (
		terms     []string
		ghExtra   []string
		threshold = defaultCodeThreshold
		asJSON    bool
		sawDDash  bool
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if sawDDash {
			ghExtra = append(ghExtra, a)
			continue
		}
		switch {
		case a == "-h" || a == "--help":
			fmt.Fprintln(stdout, codeUsage)
			return nil
		case a == "--":
			sawDDash = true
		case a == "--json":
			asJSON = true
		case a == "--threshold":
			if i+1 >= len(args) {
				return fmt.Errorf("--threshold requires a value\n\n%s", codeUsage)
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return fmt.Errorf("--threshold %q is not an integer\n\n%s", args[i+1], codeUsage)
			}
			threshold = n
			i++
		case strings.HasPrefix(a, "--threshold="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--threshold="))
			if err != nil {
				return fmt.Errorf("--threshold value %q is not an integer\n\n%s", strings.TrimPrefix(a, "--threshold="), codeUsage)
			}
			threshold = n
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q (forward gh flags after `--`)\n\n%s", a, codeUsage)
		default:
			terms = append(terms, a)
		}
	}
	if len(terms) == 0 {
		return errors.New(codeUsage)
	}

	matches, err := SearchCode(ctx, runner, terms, ghExtra)
	if err != nil {
		return err
	}
	grouped := groupCodeMatches(matches, terms)
	if asJSON {
		return writeCodeJSON(stdout, grouped)
	}
	return writeCodeHuman(stdout, grouped, threshold)
}

// codeSearchLimit is the per-org cap on results returned by `gh search code`.
// 600 mirrors the prior bash script (6 pages * 100). gh enforces a hard cap of
// 1000.
const codeSearchLimit = 600

// CodeMatch is one path-level hit in a repo. Mirrors the subset of
// `gh search code --json repository,path` that we use.
type CodeMatch struct {
	Repo string `json:"repo"` // nameWithOwner
	Path string `json:"path"`
}

// RepoCodeMatches groups a single repository's matches and splits them
// according to whether the path itself contains a search term.
type RepoCodeMatches struct {
	Repo        string   `json:"repo"`
	Count       int      `json:"count"`
	NameMatches []string `json:"name_matches"`
	ContentOnly []string `json:"content_only"`
}

// ghSearchCodeItem is the JSON shape of one element of `gh search code
// --json repository,path` output.
type ghSearchCodeItem struct {
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Path string `json:"path"`
}

// runCodeSearch shells out for a single org and decodes the result list.
// Empty orgs (no matches) yield a nil slice without error.
func runCodeSearch(ctx context.Context, runner GHRunner, org string, terms, ghExtra []string) ([]CodeMatch, error) {
	args := []string{"search", "code"}
	args = append(args, terms...)
	args = append(args, "--owner", org, "--limit", fmt.Sprintf("%d", codeSearchLimit), "--json", "repository,path")
	args = append(args, ghExtra...)
	out, err := runner(ctx, args)
	if err != nil {
		return nil, fmt.Errorf("code search for %s: %w", org, err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	var items []ghSearchCodeItem
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("parse code search for %s: %w", org, err)
	}
	matches := make([]CodeMatch, 0, len(items))
	for _, it := range items {
		matches = append(matches, CodeMatch{Repo: it.Repository.NameWithOwner, Path: it.Path})
	}
	return matches, nil
}

// SearchCode enumerates the user's orgs, fans out one `gh search code` per
// org in parallel, and returns the merged list. Order is not stable; the
// caller groups and sorts.
func SearchCode(ctx context.Context, runner GHRunner, terms, ghExtra []string) ([]CodeMatch, error) {
	orgs, err := listOrgs(ctx, runner)
	if err != nil {
		return nil, err
	}
	var (
		mu       sync.Mutex
		all      []CodeMatch
		firstErr error
		wg       sync.WaitGroup
	)
	for _, org := range orgs {
		wg.Add(1)
		go func(org string) {
			defer wg.Done()
			res, err := runCodeSearch(ctx, runner, org, terms, ghExtra)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			all = append(all, res...)
		}(org)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return all, nil
}

// groupCodeMatches buckets matches by repo and splits each repo's paths into
// name-matches (path lowercased contains any lowercased term) and
// content-only (everything else). Both lists keep input order. Repos are
// returned sorted by descending count, then by repo name for determinism.
func groupCodeMatches(matches []CodeMatch, terms []string) []RepoCodeMatches {
	lowered := make([]string, len(terms))
	for i, t := range terms {
		lowered[i] = strings.ToLower(t)
	}
	pathHits := func(p string) bool {
		lp := strings.ToLower(p)
		for _, t := range lowered {
			if strings.Contains(lp, t) {
				return true
			}
		}
		return false
	}

	byRepo := map[string]*RepoCodeMatches{}
	order := []string{}
	for _, m := range matches {
		r, ok := byRepo[m.Repo]
		if !ok {
			r = &RepoCodeMatches{Repo: m.Repo, NameMatches: []string{}, ContentOnly: []string{}}
			byRepo[m.Repo] = r
			order = append(order, m.Repo)
		}
		r.Count++
		if pathHits(m.Path) {
			r.NameMatches = append(r.NameMatches, m.Path)
		} else {
			r.ContentOnly = append(r.ContentOnly, m.Path)
		}
	}
	out := make([]RepoCodeMatches, 0, len(order))
	for _, name := range order {
		out = append(out, *byRepo[name])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Repo < out[j].Repo
	})
	return out
}

// totalCount sums match counts across all repos.
func totalCount(repos []RepoCodeMatches) int {
	n := 0
	for _, r := range repos {
		n += r.Count
	}
	return n
}

// writeCodeHuman renders the grouped results as the human-readable format
// described in the ticket. When total > threshold, content-only paths are
// collapsed to a count (filenames are still listed via name_matches).
func writeCodeHuman(w io.Writer, repos []RepoCodeMatches, threshold int) error {
	total := totalCount(repos)
	if total == 0 {
		_, err := fmt.Fprintln(w, "No results found.")
		return err
	}
	abbreviated := total > threshold

	for _, r := range repos {
		if abbreviated {
			if len(r.NameMatches) == 0 {
				if _, err := fmt.Fprintf(w, "%s (%d content-only)\n", r.Repo, r.Count); err != nil {
					return err
				}
				continue
			}
			if _, err := fmt.Fprintf(w, "%s (%d):\n", r.Repo, r.Count); err != nil {
				return err
			}
			for _, p := range r.NameMatches {
				if _, err := fmt.Fprintf(w, "  %s\n", p); err != nil {
					return err
				}
			}
			if len(r.ContentOnly) > 0 {
				if _, err := fmt.Fprintf(w, "  ... +%d content-only\n", len(r.ContentOnly)); err != nil {
					return err
				}
			}
		} else {
			if _, err := fmt.Fprintf(w, "%s (%d):\n", r.Repo, r.Count); err != nil {
				return err
			}
			for _, p := range r.NameMatches {
				if _, err := fmt.Fprintf(w, "  %s\n", p); err != nil {
					return err
				}
			}
			for _, p := range r.ContentOnly {
				if _, err := fmt.Fprintf(w, "  %s\n", p); err != nil {
					return err
				}
			}
		}
	}
	if _, err := fmt.Fprintf(w, "\nTotal: %d results across %d repos\n", total, len(repos)); err != nil {
		return err
	}
	return nil
}

// writeCodeJSON renders the grouped results as the JSON shape described in
// the ticket. content_only is always populated regardless of threshold.
func writeCodeJSON(w io.Writer, repos []RepoCodeMatches) error {
	payload := struct {
		Total int               `json:"total"`
		Repos []RepoCodeMatches `json:"repos"`
	}{
		Total: totalCount(repos),
		Repos: repos,
	}
	if payload.Repos == nil {
		payload.Repos = []RepoCodeMatches{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}
