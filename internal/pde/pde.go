// Package pde implements the `ticket pde` subcommand: writing a pde.yaml into a
// ticket's env folder so the headless `pde` CLI can compile, launch and test the
// bundles that ticket changed.
//
// It is the pde-CLI counterpart of ticket-eclipse, which sets up the same set of
// plugins for the Eclipse IDE instead. Both detect plugins the same way and both
// run each plugin's lib/fetch_jars Maven build; the difference is the output —
// an IDE workspace there, a pde.yaml here.
//
// The generated file goes in the env folder because `pde` discovers its config
// by walking up from the working directory. `pde compile` therefore works from
// the env folder and from anywhere below it, including the `ticket agent`
// sessions that already start there.
package pde

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"framework2/internal/dir"
	"framework2/internal/env"
	"framework2/internal/projectconfig"
)

// ConfigName is the basename pde looks for first when walking up from cwd.
const ConfigName = "pde.yaml"

// Request is one `ticket pde` invocation.
type Request struct {
	DB       string
	BeadsDir string
	TicketID string
	// VMArgs are appended to the generated launch preset's vmArgs.
	VMArgs []string
	// SkipFetchJars leaves each plugin's lib/fetch_jars unbuilt. Compilation
	// will fail on missing Bundle-ClassPath jars unless they are already there
	// from an earlier run.
	SkipFetchJars bool
	// SkipTargetInstall leaves the env folder without a p2 profile. Only useful
	// offline: `pde compile` cannot resolve anything until one exists.
	SkipTargetInstall bool
	// DryRun prints the config that would be written and touches nothing.
	DryRun bool
	// Force overwrites a pde.yaml whose generated header is missing.
	Force bool
}

// Run performs the whole flow: locate the env folder, detect the ticket's
// changed plugins, build their jars, and write the pde.yaml.
func Run(req Request, stdout io.Writer) error {
	cfg, err := projectconfig.Load(req.BeadsDir)
	if err != nil {
		return err
	}
	if !cfg.PDE.Configured() {
		return fmt.Errorf("%s has no `pde.target:` — set the .target definition to generate a pde.yaml", projectconfig.Filename)
	}
	target, err := projectconfig.ExpandHome(cfg.PDE.Target)
	if err != nil {
		return err
	}
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("pde.target does not exist: %s", target)
	}
	pool, err := projectconfig.ExpandHome(cfg.PDE.BundlePool)
	if err != nil {
		return err
	}

	envPath, err := env.Create(req.DB, req.BeadsDir, req.TicketID)
	if err != nil {
		return err
	}

	entries, err := dir.Load(req.DB, req.BeadsDir, req.TicketID)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("ticket %s has no linked dirs; run `ticket dir worktree` first", req.TicketID)
	}

	plugins, err := Detect(entries, stdout)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Detected %d changed plugin(s).\n", len(plugins))
	for _, p := range plugins {
		fmt.Fprintf(stdout, "  %s\n", p.Path())
	}

	bundles, err := bundleList(plugins, cfg.PDE.ExtraBundles)
	if err != nil {
		return err
	}
	programArgs, err := expandAll(cfg.PDE.ProgramArgs)
	if err != nil {
		return err
	}

	body := Render(Options{
		TicketID:    req.TicketID,
		Target:      target,
		BundlePool:  pool,
		Bundles:     bundles,
		DataDir:     filepath.Join(envPath, ".ap-data"),
		ProgramArgs: programArgs,
		VMArgs:      append(cfg.PDE.VMArgs, req.VMArgs...),
	})

	if req.DryRun {
		fmt.Fprintf(stdout, "\n# --dry-run: would write %s\n\n", filepath.Join(envPath, ConfigName))
		_, err := stdout.Write(body)
		return err
	}

	// fetch_jars runs before the write so a config only appears once the jars
	// its bundles reference are on disk.
	if !req.SkipFetchJars && len(plugins) > 0 {
		fmt.Fprintln(stdout, "Building fetch_jars...")
		if failed := FetchJars(plugins, stdout); len(failed) > 0 {
			fmt.Fprintf(stdout, "WARNING: fetch_jars failed for %s — `pde compile` will report unresolved types from lib/ jars.\n",
				strings.Join(failed, ", "))
		}
	}

	configPath := filepath.Join(envPath, ConfigName)
	if err := Write(configPath, body, req.Force); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Wrote %s\n", configPath)

	if !req.SkipTargetInstall {
		if err := installTarget(envPath, stdout); err != nil {
			return err
		}
	}
	fmt.Fprintf(stdout, "Next: cd %s && pde compile && pde launch\n", envPath)
	return nil
}

// installTarget provisions the env folder's own p2 profile, which pde needs
// before it can resolve anything. It is skipped once the profile exists: the
// per-ticket profile is cheap to create (seconds, since the artifacts come from
// the shared bundle pool) but there is no reason to redo it on every re-run.
//
// A missing `pde` on PATH is a warning, not an error — the config is already
// written and is the useful output; the user can install the profile later.
func installTarget(envPath string, stdout io.Writer) error {
	if _, err := os.Stat(filepath.Join(envPath, ".target", "p2")); err == nil {
		return nil
	}
	if _, err := exec.LookPath("pde"); err != nil {
		fmt.Fprintln(stdout, "WARNING: `pde` not on PATH; run `pde target install` in the env folder yourself.")
		return nil
	}
	fmt.Fprintln(stdout, "Installing target platform (first time for this ticket)...")
	cmd := exec.Command("pde", "target", "install")
	cmd.Dir = envPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pde target install failed (VPN required to resolve the .target): %w: %s", err, trimTail(out, 2000))
	}
	return nil
}

// bundleList turns detected plugins plus the configured extras into `bundles:`
// entries. Extras are appended last and de-duplicated against the detected set,
// so a ticket that happens to change an extra bundle lists it once.
func bundleList(plugins []Plugin, extras []string) ([]Bundle, error) {
	seen := map[string]bool{}
	var out []Bundle
	for _, p := range plugins {
		if seen[p.Path()] {
			continue
		}
		seen[p.Path()] = true
		out = append(out, Bundle{Path: p.Path(), Note: "changed in " + filepath.Base(p.Worktree)})
	}
	for _, raw := range extras {
		path, err := projectconfig.ExpandHome(raw)
		if err != nil {
			return nil, err
		}
		if seen[path] {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("pde.extra-bundles entry does not exist: %s", path)
		}
		seen[path] = true
		out = append(out, Bundle{Path: path, Note: "always included (project.yaml pde.extra-bundles)"})
	}
	return out, nil
}

// Write saves body to path. An existing file is overwritten only when it carries
// the generated header or force is set — hand-written configs are expected (the
// `pde` skill tells agents to edit them) and must not be silently destroyed by a
// regeneration.
func Write(path string, body []byte, force bool) error {
	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		if !force && !isGenerated(existing) {
			return fmt.Errorf("refusing to overwrite hand-written %s (no %q header); pass --force to replace it",
				path, GeneratedMarker)
		}
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("read %s: %w", path, err)
	}
	return os.WriteFile(path, body, 0o644)
}

// isGenerated reports whether the file's first line carries the marker.
func isGenerated(body []byte) bool {
	first, _, _ := strings.Cut(string(body), "\n")
	return strings.Contains(first, GeneratedMarker)
}

// expandAll applies ExpandHome to every entry. Only `~/`-prefixed entries
// change, so flags like `-preferences` pass through untouched.
func expandAll(args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for _, a := range args {
		expanded, err := projectconfig.ExpandHome(a)
		if err != nil {
			return nil, err
		}
		out = append(out, expanded)
	}
	return out, nil
}
