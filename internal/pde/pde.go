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
//
// With a `devserver:` block in project.yaml the launch preset is the browser
// editor: the headless gateway dev server plus the frontend dev server as a
// pde sidecar, on a port block reserved per ticket, bound to the ticket's
// workflow on the hub. Without the block the preset is the desktop AP.
package pde

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"framework2/internal/agentbrowser"
	"framework2/internal/devserver"
	"framework2/internal/dir"
	"framework2/internal/env"
	"framework2/internal/ports"
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
	// DryRun prints the config that would be written and touches nothing: no
	// worktree, no port block, no hub item, no file.
	DryRun bool
	// Force overwrites a pde.yaml whose generated header is missing.
	Force bool
	// HubAlias writes `pde.<alias>.yaml` bound to that hub instead of the
	// configured one. Same port block, own context yaml.
	HubAlias string
}

// configName returns pde.yaml, or pde.<alias>.yaml for a hub override.
func (r Request) configName() string {
	if r.HubAlias == "" {
		return ConfigName
	}
	return "pde." + r.HubAlias + ".yaml"
}

// Run performs the whole flow: locate the env folder, detect the ticket's
// changed plugins, prepare the browser-editor preset, build the jars, and
// write the pde.yaml.
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
	ds := cfg.Devserver
	if req.HubAlias != "" && !ds.Configured() {
		return fmt.Errorf("--hub-alias needs a `devserver:` block in %s (missing: %s)",
			projectconfig.Filename, strings.Join(ds.Missing(), ", "))
	}

	envPath, err := env.Create(req.DB, req.BeadsDir, req.TicketID)
	if err != nil {
		return err
	}

	entries, err := dir.Load(req.DB, req.BeadsDir, req.TicketID)
	if err != nil {
		return err
	}
	if len(entries) == 0 && !ds.Configured() {
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

	var frontend *Frontend
	if ds.Configured() {
		frontend, entries, err = ensureFrontend(req, ds, entries, stdout)
		if err != nil {
			return err
		}
		// The frontend worktree may carry changes of its own (a ticket that
		// touches knime-ui); they are workspace bundles like any other.
		plugins, err = Detect(entries, io.Discard)
		if err != nil {
			return err
		}
	}

	bundles, err := bundleList(plugins, cfg.PDE.ExtraBundles)
	if err != nil {
		return err
	}
	if frontend != nil {
		bundles = appendFrontendBundle(bundles, frontend)
	}
	programArgs, err := expandAll(cfg.PDE.ProgramArgs)
	if err != nil {
		return err
	}

	opts := Options{
		TicketID:    req.TicketID,
		Target:      target,
		BundlePool:  pool,
		Bundles:     bundles,
		DataDir:     filepath.Join(envPath, ".ap-data"),
		ProgramArgs: programArgs,
		VMArgs:      append(cfg.PDE.VMArgs, req.VMArgs...),
	}

	var devOpts *DevserverOptions
	switch {
	case ds.Configured() && req.DryRun:
		block, ok, err := ports.Lookup(req.BeadsDir, req.TicketID)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintf(stdout, "Ports: no block registered yet; would reserve one from %d (--dry-run)\n", ds.PortBase)
			block = ports.Block{Start: ds.PortBase}
		}
		alias := req.HubAlias
		if alias == "" {
			alias = ds.Hub
		}
		folder := ds.WorkflowFolderFor(alias)
		fmt.Fprintf(stdout, "Hub %s: would look up or create %s/<jira-id> and write the context yaml (--dry-run)\n", alias, folder)
		devOpts = &DevserverOptions{
			ThisExe:     thisExe(),
			Block:       block,
			ContextPath: "<context yaml, written without --dry-run>",
			DataDir:     filepath.Join(envPath, ".devserver-data"),
			Frontend:    frontend,
			HubAlias:    alias,
			ItemPath:    folder + "/<jira-id>",
		}
	case ds.Configured():
		block, err := ports.Allocate(req.BeadsDir, req.TicketID, ds.PortBase, stdout)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Ports: block %d (gateway %d, wf-mcp %d, editor %d, spare %d-%d for additional sidecars, e.g. a UI extension frontend, see the comments in %s)\n",
			block.Start, block.Gateway(), block.WfMCP(), block.Editor(), block.SpareFirst(), block.SpareLast(), req.configName())
		// Playwright sessions attach to the KNIME Agent Browser; start it here so the
		// first browser step of a ticket finds it (no daemon keeps it running). It
		// opens in the background with a blank tab: the backend is not up yet, and
		// the ticket's first Playwright session adopts that tab as its page.
		switch {
		case agentbrowser.Running():
			fmt.Fprintf(stdout, "Agent browser: running (DevTools %d)\n", agentbrowser.Port)
		default:
			if err := agentbrowser.Start(context.Background(), ""); err != nil {
				fmt.Fprintf(stdout, "Agent browser: not started (%v); Playwright needs it, `this view` starts it\n", err)
			} else {
				fmt.Fprintf(stdout, "Agent browser: started (DevTools %d)\n", agentbrowser.Port)
			}
		}
		itemName, err := dir.AncestorJiraID(req.DB, req.BeadsDir, req.TicketID)
		if err != nil {
			return err
		}
		if itemName == "" {
			itemName = req.TicketID
			fmt.Fprintf(stdout, "Hub: ticket has no Jira id, the workflow is named after the bead id (%s)\n", itemName)
		}
		res, err := devserver.Prepare(context.Background(), devserver.Request{
			Cfg: ds, Alias: req.HubAlias, TicketID: req.TicketID, ItemName: itemName,
		}, stdout)
		if err != nil {
			return err
		}
		state := "exists"
		if res.Created {
			state = "created"
		}
		fmt.Fprintf(stdout, "Hub %s: ticket workflow %s %s (id %s", res.Alias, res.ItemPath, state, res.ItemID)
		if res.LastUploadedOn != "" {
			fmt.Fprintf(stdout, ", last upload %s", res.LastUploadedOn)
		}
		fmt.Fprintln(stdout, ")")
		if res.Link != "" {
			fmt.Fprintf(stdout, "  %s\n", res.Link)
		}
		fmt.Fprintf(stdout, "Wrote %s\n", res.ContextPath)
		devOpts = &DevserverOptions{
			ThisExe:     thisExe(),
			Block:       block,
			ContextPath: res.ContextPath,
			DataDir:     filepath.Join(envPath, ".devserver-data"),
			Frontend:    frontend,
			HubAlias:    res.Alias,
			ItemPath:    res.ItemPath,
			Link:        res.Link,
		}
	default:
		fmt.Fprintf(stdout, "No browser-editor preset: %s has no complete `devserver:` block (missing: %s); writing the desktop AP preset instead.\n",
			projectconfig.Filename, strings.Join(ds.Missing(), ", "))
	}
	opts.Devserver = devOpts

	body := Render(opts)

	if req.DryRun {
		fmt.Fprintf(stdout, "\n# --dry-run: would write %s\n\n", filepath.Join(envPath, req.configName()))
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

	configPath := filepath.Join(envPath, req.configName())
	if err := Write(configPath, body, req.Force); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Wrote %s\n", configPath)

	if !req.SkipTargetInstall {
		if err := installTarget(envPath, stdout); err != nil {
			return err
		}
	}
	if devOpts == nil {
		fmt.Fprintf(stdout, "Next: cd %s && pde compile && pde launch\n", envPath)
		return nil
	}
	launch := "pde launch " + DevserverLaunchName
	if req.HubAlias != "" {
		launch = "pde launch --config " + req.configName() + " " + DevserverLaunchName
	}
	fmt.Fprintf(stdout, "Next: cd %s && pde compile && %s\n", envPath, launch)
	fmt.Fprintln(stdout, "The launch prints `[backend] ready:` once the backend accepts connections (20-60 s) and the editor URL on `[editor] ready:`; wf-mcp calls made earlier answer \"still starting\".")
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
//
// The same plugin changed in two worktrees of one bare repo (a ticket branch
// and its backport) is listed once, from the first worktree in path order:
// pde keeps only the last workspace bundle of a symbolic name, silently, so
// listing both would launch the backport. The dropped one is named in a
// comment.
func bundleList(plugins []Plugin, extras []string) ([]Bundle, error) {
	seen := map[string]bool{}
	byName := map[string]int{}
	var out []Bundle
	for _, p := range plugins {
		if seen[p.Path()] {
			continue
		}
		seen[p.Path()] = true
		key := p.Bare + "|" + p.Name
		if i, dup := byName[key]; dup && p.Bare != "" {
			out[i].Note += "; also changed in " + filepath.Base(p.Worktree) + " (left out: same bundle id, pde keeps one)"
			continue
		}
		byName[key] = len(out)
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

// appendFrontendBundle adds the frontend bundle unless detection already
// listed it (a ticket that changes the frontend), and drops the same bundle
// from other frontend worktrees, which pde would otherwise silently override.
func appendFrontendBundle(bundles []Bundle, fe *Frontend) []Bundle {
	name := filepath.Base(fe.Dir)
	var out []Bundle
	listed := false
	for _, b := range bundles {
		if b.Path == fe.Dir {
			listed = true
			out = append(out, Bundle{Path: b.Path, Note: b.Note + "; browser editor frontend"})
			continue
		}
		if filepath.Base(b.Path) == name && isUnder(b.Path, fe.Others) {
			continue
		}
		out = append(out, b)
	}
	if !listed {
		out = append(out, Bundle{Path: fe.Dir, Note: "browser editor frontend, always included (project.yaml devserver.frontend-dir)"})
	}
	return out
}

func isUnder(path string, worktrees []dir.Entry) bool {
	for _, w := range worktrees {
		if strings.HasPrefix(path, w.Path+string(filepath.Separator)) {
			return true
		}
	}
	return false
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

// thisExe is the `this` binary for the `backend` sidecar: the running one,
// falling back to a PATH lookup.
func thisExe() string {
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			return resolved
		}
		return exe
	}
	return "this"
}
