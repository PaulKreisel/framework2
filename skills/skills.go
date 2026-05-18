// Package skills exposes the Claude Code skills that framework2 ships
// automatically into every `ticket agent` and `ticket spec` session via the
// per-env .claude/skills/ directory.
//
// Layout convention:
//
//	skills/agent/<name>/SKILL.md   loaded only by `ticket agent` sessions
//	skills/spec/<name>/SKILL.md    loaded only by `ticket spec` sessions
//	skills/all/<name>/<files>      loaded by both
//
// Skill names must be globally unique across the three buckets — the
// materialiser refuses to write when the same <name> appears in more than
// one bucket that's in scope for the current mode.
package skills

import "embed"

// FS is the embedded skills tree rooted at the three bucket directories
// (agent/, spec/, all/). Each immediate child of a bucket is a skill
// directory containing at least a SKILL.md.
//
//go:embed all:agent all:spec all:all
var FS embed.FS
