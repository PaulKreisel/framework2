// Package slug derives, stores, and reads the kebab-case slug associated with
// a bead. The slug shadows the opaque bead id in branch names, worktree paths,
// and other human-facing surfaces.
//
// Storage: the slug lives in the bead's metadata under the keys "slug" and
// "slug_is_used". Once "slug_is_used" is true (set the first time a consumer
// derives a stable name from it, e.g. a git branch), the slug is frozen: [Set]
// returns an error and asynchronous refinement is dropped silently.
package slug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"framework2/internal/bdcli"
)

// MaxLen caps the derived slug at 30 chars. This is a fallback upper bound
// for the initial slug stored on a fresh bead, when the project config and
// the bead id needed to compute a precise per-branch budget aren't all
// available yet. Once those are known, callers should use
// projectconfig.Config.BranchSlugBudget to derive a tighter limit so the
// rendered branch stays within [MaxBranchLen].
const MaxLen = 30

// MaxBranchLen is the maximum length of a rendered branch name. GitHub
// branch-protection rules in our repos enforce `^.{0,70}$`, so anything
// longer is rejected at push time.
const MaxBranchLen = 70

// ErrSlugUsed is returned by [Set] when the slug is frozen (slug_is_used=true).
var ErrSlugUsed = errors.New("slug is already used; cannot be changed")

// Derive turns a free-form title into a deterministic kebab-case slug. Ported
// from the old framework's bash logic: lowercase, collapse non-[a-z0-9] runs
// to a single '-', strip leading/trailing '-', cap to maxLen, then re-trim a
// trailing '-' left behind by the cap. If maxLen <= 0, MaxLen is used.
func Derive(title string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = MaxLen
	}
	var b strings.Builder
	b.Grow(len(title))
	prevDash := true // suppresses leading '-'
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > maxLen {
		out = strings.TrimRight(out[:maxLen], "-")
	}
	return out
}

// Info is the slug-related fragment of a bead's metadata.
type Info struct {
	Slug string
	Used bool
}

// Get returns the slug info for ticketID. Missing keys are surfaced as zero
// values (no error), so legacy beads predating this feature still work.
func Get(db, beadsDir, ticketID string) (Info, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "show", ticketID).Output()
	if err != nil {
		return Info{}, fmt.Errorf("bd show %s: %w", ticketID, withStderr(err))
	}
	var rows []struct {
		Metadata struct {
			Slug       string `json:"slug"`
			SlugIsUsed bool   `json:"slug_is_used"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return Info{}, fmt.Errorf("parse bd show %s: %w", ticketID, err)
	}
	if len(rows) == 0 {
		return Info{}, fmt.Errorf("ticket not found: %s", ticketID)
	}
	return Info{Slug: rows[0].Metadata.Slug, Used: rows[0].Metadata.SlugIsUsed}, nil
}

// Set persists slug on ticketID. Returns [ErrSlugUsed] if slug_is_used is
// already true on the bead.
func Set(db, beadsDir, ticketID, slug string) error {
	info, err := Get(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	if info.Used {
		return ErrSlugUsed
	}
	payload, _ := json.Marshal(map[string]string{"slug": slug})
	out, err := bdcli.Cmd(beadsDir, db, "update", ticketID, "--metadata", string(payload)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("update slug on %s: %w: %s", ticketID, err, out)
	}
	return nil
}

// SetInitial stores the slug on a freshly created bead without first reading
// slug_is_used. Used by the create path where we know the bead is brand new.
func SetInitial(db, beadsDir, ticketID, slug string) error {
	payload, _ := json.Marshal(map[string]string{"slug": slug})
	out, err := bdcli.Cmd(beadsDir, db, "update", ticketID, "--metadata", string(payload)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("set initial slug on %s: %w: %s", ticketID, err, out)
	}
	return nil
}

// MarkUsed freezes the slug on ticketID by setting slug_is_used=true. Safe to
// call repeatedly.
func MarkUsed(db, beadsDir, ticketID string) error {
	out, err := bdcli.Cmd(beadsDir, db, "update", ticketID, "--set-metadata", "slug_is_used=true").CombinedOutput()
	if err != nil {
		return fmt.Errorf("mark slug used on %s: %w: %s", ticketID, err, out)
	}
	return nil
}

// BranchPrefix maps a bead's issue type to one of "enh", "fix", or "todo".
// Unknown types fall through to "todo".
func BranchPrefix(beadType string) string {
	switch strings.ToLower(strings.TrimSpace(beadType)) {
	case "feature", "feat", "enhancement", "enh":
		return "enh"
	case "bug", "fix":
		return "fix"
	default:
		return "todo"
	}
}

// Branch composes a branch name `<prefix>/<id>-<slug>`. If slug is empty, the
// trailing "-<slug>" is omitted so legacy beads still get a valid branch.
func Branch(prefix, id, slug string) string {
	if slug == "" {
		return prefix + "/" + id
	}
	return prefix + "/" + id + "-" + slug
}

// Refine calls `claude -p --model haiku --effort low` to produce a polished
// slug for title and returns it (already passed through [Derive] to enforce
// the kebab-case shape and the maxLen cap). Returns "" on any error, on empty
// output, or when the resulting slug equals fallback. maxLen <= 0 defaults to
// [MaxLen]. Honors ctx for cancellation.
func Refine(ctx context.Context, title, fallback string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = MaxLen
	}
	prompt := fmt.Sprintf(
		"Output ONLY a kebab-case slug of about 5 lowercase words separated by hyphens, "+
			"no longer than %d characters total. No explanation, no quotes, no backticks. "+
			"Just the slug for this issue title: %s", maxLen, title)
	cmd := exec.CommandContext(ctx, "claude", "-p", "--model", "haiku", "--effort", "low", prompt)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return ""
	}
	refined := Derive(raw, maxLen)
	if refined == "" || refined == fallback {
		return ""
	}
	return refined
}

// RefineTimeout is the upper bound applied to [Refine] when callers don't
// supply their own deadline.
const RefineTimeout = 60 * time.Second

func withStderr(err error) error {
	if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, ee.Stderr)
	}
	return err
}
