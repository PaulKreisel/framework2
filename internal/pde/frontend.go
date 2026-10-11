package pde

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"framework2/internal/dir"
	"framework2/internal/projectconfig"
)

// Frontend is the browser-editor frontend checkout the devserver preset
// serves from.
type Frontend struct {
	// Dir is the bundle directory (the pnpm workspace root) inside the
	// primary worktree.
	Dir string
	// Primary is the worktree Dir lives in.
	Primary dir.Entry
	// Others are further worktrees of the frontend repo linked to the ticket.
	// They are not listed as bundles: pde keeps only the last workspace bundle
	// of a symbolic name, silently.
	Others []dir.Entry
	// Created is true when this call made the worktree.
	Created bool
	// InstallWarning is set when `pnpm install` was needed and failed.
	InstallWarning string
}

// ensureFrontend makes sure the ticket has a worktree of the frontend repo,
// picks the primary one, and installs its node modules when missing.
func ensureFrontend(req Request, cfg projectconfig.DevserverConfig, entries []dir.Entry, stdout io.Writer) (*Frontend, []dir.Entry, error) {
	bare, err := projectconfig.ExpandHome(cfg.FrontendRepo)
	if err != nil {
		return nil, entries, err
	}
	bare, err = filepath.Abs(bare)
	if err != nil {
		return nil, entries, err
	}
	fe := &Frontend{}
	matches := entriesForBare(entries, bare)
	if len(matches) == 0 {
		if req.DryRun {
			fmt.Fprintf(stdout, "Frontend: would create a %s worktree for %s (--dry-run)\n", filepath.Base(bare), req.TicketID)
			return nil, entries, nil
		}
		if err := dir.Worktree(req.DB, req.BeadsDir, req.TicketID, bare); err != nil {
			return nil, entries, fmt.Errorf("create frontend worktree: %w", err)
		}
		entries, err = dir.Load(req.DB, req.BeadsDir, req.TicketID)
		if err != nil {
			return nil, entries, err
		}
		matches = entriesForBare(entries, bare)
		if len(matches) == 0 {
			return nil, entries, fmt.Errorf("frontend worktree for %s was created but is not linked", bare)
		}
		fe.Created = true
	}
	fe.Primary, fe.Others = pickPrimary(matches, req.TicketID)
	fe.Dir = filepath.Join(fe.Primary.Path, cfg.FrontendDir)
	if _, err := os.Stat(fe.Dir); err != nil {
		return nil, entries, fmt.Errorf("frontend dir %s does not exist in %s", cfg.FrontendDir, fe.Primary.Path)
	}
	switch {
	case fe.Created:
		fmt.Fprintf(stdout, "Frontend: created worktree %s (branch %s)\n", fe.Primary.Path, fe.Primary.Branch)
	default:
		fmt.Fprintf(stdout, "Frontend: using worktree %s (branch %s)\n", fe.Primary.Path, fe.Primary.Branch)
	}
	for _, o := range fe.Others {
		fmt.Fprintf(stdout, "  not listed (same bundle id, pde keeps one): %s (branch %s)\n", o.Path, o.Branch)
	}
	if req.DryRun {
		return fe, entries, nil
	}
	if _, err := os.Stat(filepath.Join(fe.Dir, "node_modules")); err == nil {
		return fe, entries, nil
	}
	fmt.Fprintf(stdout, "Frontend: node_modules missing, running `pnpm install --frozen-lockfile` in %s...\n", fe.Dir)
	if err := commandRunner(fe.Dir, "pnpm", "install", "--frozen-lockfile"); err != nil {
		fe.InstallWarning = fmt.Sprintf("WARNING: pnpm install failed in %s: %v\nThe editor sidecar will not start until it succeeds; run it by hand.", fe.Dir, err)
		fmt.Fprintln(stdout, fe.InstallWarning)
	} else {
		fmt.Fprintln(stdout, "  OK   pnpm install")
	}
	return fe, entries, nil
}

func entriesForBare(entries []dir.Entry, bare string) []dir.Entry {
	var out []dir.Entry
	for _, e := range entries {
		if e.BareRepo == bare {
			out = append(out, e)
		}
	}
	return out
}

// pickPrimary prefers the worktree whose branch carries the ticket id (the
// ticket's own branch), else the first one.
func pickPrimary(matches []dir.Entry, ticketID string) (dir.Entry, []dir.Entry) {
	idx := 0
	for i, m := range matches {
		if strings.Contains(m.Branch, ticketID) {
			idx = i
			break
		}
	}
	var others []dir.Entry
	for i, m := range matches {
		if i != idx {
			others = append(others, m)
		}
	}
	return matches[idx], others
}
