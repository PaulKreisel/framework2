package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"framework2/internal/agent"
	"framework2/internal/testutil"
)

// TestRegenerateMaterialisesShippedSkills exercises the real embedded
// skills.FS through the public Regenerate entrypoint. The `new-skill`
// skill lives in skills/all/ so it must land in the env regardless of
// mode.
func TestRegenerateMaterialisesShippedSkills(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-skills-bundled", "x", "")

	for _, mode := range []agent.Mode{agent.ModeAgent, agent.ModeSpec} {
		envPath := mustEnv(t, beadsDir, ticket)
		if _, _, err := agent.Regenerate("", beadsDir, envPath, ticket, mode); err != nil {
			t.Fatalf("Regenerate %s: %v", mode, err)
		}
		skillPath := filepath.Join(envPath, ".claude", "skills", "new-skill", "SKILL.md")
		body, err := os.ReadFile(skillPath)
		if err != nil {
			t.Fatalf("read %s: %v", skillPath, err)
		}
		if !strings.Contains(string(body), "name: new-skill") {
			t.Errorf("%s missing expected frontmatter\n--- body ---\n%s", skillPath, body)
		}
		_ = os.RemoveAll(filepath.Join(beadsDir, "envs", ticket))
	}
}

// TestWriteSkillsFromFSModeSeparation drives the materialiser with a
// fixture FS containing one skill per bucket and verifies the mode
// filter: `all/` is always copied, the matching mode bucket is copied,
// the other mode bucket is not.
func TestWriteSkillsFromFSModeSeparation(t *testing.T) {
	t.Parallel()
	src := fstest.MapFS{
		"all/shared/SKILL.md":      &fstest.MapFile{Data: []byte("shared body\n")},
		"agent/coder/SKILL.md":     &fstest.MapFile{Data: []byte("coder body\n")},
		"spec/specwriter/SKILL.md": &fstest.MapFile{Data: []byte("specwriter body\n")},
	}

	cases := []struct {
		mode    agent.Mode
		want    []string
		notWant []string
	}{
		{agent.ModeAgent, []string{"shared", "coder"}, []string{"specwriter"}},
		{agent.ModeSpec, []string{"shared", "specwriter"}, []string{"coder"}},
	}
	for _, tc := range cases {
		envPath := t.TempDir()
		if err := agent.WriteSkillsFromFS(src, envPath, tc.mode); err != nil {
			t.Fatalf("WriteSkillsFromFS %s: %v", tc.mode, err)
		}
		skillsDir := filepath.Join(envPath, ".claude", "skills")
		for _, name := range tc.want {
			if _, err := os.Stat(filepath.Join(skillsDir, name, "SKILL.md")); err != nil {
				t.Errorf("mode %s: expected skill %q present: %v", tc.mode, name, err)
			}
		}
		for _, name := range tc.notWant {
			if _, err := os.Stat(filepath.Join(skillsDir, name)); !os.IsNotExist(err) {
				t.Errorf("mode %s: skill %q leaked from the other bucket (err=%v)", tc.mode, name, err)
			}
		}
	}
}

// TestWriteSkillsFromFSWipesStale ensures a skill that was removed from
// the source tree disappears from the env on the next materialisation,
// rather than silently sticking around.
func TestWriteSkillsFromFSWipesStale(t *testing.T) {
	t.Parallel()
	envPath := t.TempDir()

	first := fstest.MapFS{
		"all/old/SKILL.md": &fstest.MapFile{Data: []byte("v1\n")},
	}
	if err := agent.WriteSkillsFromFS(first, envPath, agent.ModeAgent); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(envPath, ".claude", "skills", "old", "SKILL.md")); err != nil {
		t.Fatalf("setup: skill not written: %v", err)
	}

	second := fstest.MapFS{
		"all/new/SKILL.md": &fstest.MapFile{Data: []byte("v2\n")},
	}
	if err := agent.WriteSkillsFromFS(second, envPath, agent.ModeAgent); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(envPath, ".claude", "skills", "old")); !os.IsNotExist(err) {
		t.Errorf("stale skill survived re-materialisation (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(envPath, ".claude", "skills", "new", "SKILL.md")); err != nil {
		t.Errorf("new skill missing: %v", err)
	}
}

// TestWriteSkillsFromFSCollision rejects a duplicate skill name across
// buckets — we can't tell from the materialised env which source bucket
// won, so refuse rather than silently picking one.
func TestWriteSkillsFromFSCollision(t *testing.T) {
	t.Parallel()
	src := fstest.MapFS{
		"all/dup/SKILL.md":   &fstest.MapFile{Data: []byte("a\n")},
		"agent/dup/SKILL.md": &fstest.MapFile{Data: []byte("b\n")},
	}
	err := agent.WriteSkillsFromFS(src, t.TempDir(), agent.ModeAgent)
	if err == nil || !strings.Contains(err.Error(), "dup") {
		t.Fatalf("expected duplicate-name error, got %v", err)
	}
}

// TestWriteSkillsFromFSIgnoresKeepFiles verifies bucket-level placeholder
// files (the .keep markers the embedded FS uses to keep otherwise-empty
// buckets embeddable) are skipped rather than copied as bogus skills.
func TestWriteSkillsFromFSIgnoresKeepFiles(t *testing.T) {
	t.Parallel()
	src := fstest.MapFS{
		"agent/.keep":        &fstest.MapFile{Data: nil},
		"spec/.keep":         &fstest.MapFile{Data: nil},
		"all/real/SKILL.md":  &fstest.MapFile{Data: []byte("ok\n")},
	}
	envPath := t.TempDir()
	if err := agent.WriteSkillsFromFS(src, envPath, agent.ModeAgent); err != nil {
		t.Fatalf("WriteSkillsFromFS: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(envPath, ".claude", "skills"))
	if err != nil {
		t.Fatalf("read skills dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "real" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected only [real], got %v", names)
	}
}
