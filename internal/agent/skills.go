package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"framework2/skills"
)

// SkillsDir is the basename of the per-env directory under .claude/ that
// holds the framework-shipped skills materialised on each Regenerate.
const SkillsDir = "skills"

// writeSkills materialises the skills relevant to mode into
// <envPath>/.claude/skills/<name>/, mirroring the embedded skills.FS tree
// with the bucket prefix flattened away. The skills directory is wiped
// first so a renamed/removed skill in the source tree disappears from the
// env on the next call.
func writeSkills(envPath string, mode Mode) error {
	return WriteSkillsFromFS(skills.FS, envPath, mode)
}

// WriteSkillsFromFS is the FS-parameterised seam under writeSkills,
// exported so tests can drive it with a fixture FS. srcFS must look like
// the framework2-shipped skills tree, with agent/, spec/, all/ at its
// root. Each immediate subdirectory of a bucket is treated as a skill.
func WriteSkillsFromFS(srcFS fs.FS, envPath string, mode Mode) error {
	destRoot := filepath.Join(envPath, ".claude", SkillsDir)
	if err := os.RemoveAll(destRoot); err != nil {
		return fmt.Errorf("clear skills dir: %w", err)
	}

	buckets := []string{"all"}
	switch mode {
	case ModeAgent:
		buckets = append(buckets, "agent")
	case ModeSpec:
		buckets = append(buckets, "spec")
	}

	seen := make(map[string]string)
	for _, bucket := range buckets {
		if err := materialiseBucket(srcFS, bucket, destRoot, seen); err != nil {
			return err
		}
	}
	return nil
}

// materialiseBucket copies each skill subdirectory of bucket into destRoot,
// stripping the bucket prefix. Non-directory entries directly under bucket
// (e.g. the .keep placeholders that keep otherwise-empty buckets embeddable)
// are ignored.
func materialiseBucket(srcFS fs.FS, bucket, destRoot string, seen map[string]string) error {
	entries, err := fs.ReadDir(srcFS, bucket)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read skills bucket %s: %w", bucket, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if prev, dup := seen[name]; dup {
			return fmt.Errorf("skill %q present in both %q and %q buckets", name, prev, bucket)
		}
		seen[name] = bucket
		if err := copySkillDir(srcFS, path.Join(bucket, name), filepath.Join(destRoot, name)); err != nil {
			return fmt.Errorf("copy skill %s/%s: %w", bucket, name, err)
		}
	}
	return nil
}

func copySkillDir(srcFS fs.FS, srcDir, destDir string) error {
	return fs.WalkDir(srcFS, srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, srcDir), "/")
		target := destDir
		if rel != "" {
			target = filepath.Join(destDir, filepath.FromSlash(rel))
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := fs.ReadFile(srcFS, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o644)
	})
}
