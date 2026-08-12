package pde

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// commandRunner is swapped out in tests so they never shell out to Maven.
var commandRunner = func(workDir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, trimTail(out, 2000))
	}
	return nil
}

// FetchJars runs `mvn clean package` in every detected plugin's
// `lib/fetch_jars` directory, which is how KNIME plugins materialize the
// third-party jars their Bundle-ClassPath references. Plugins without such a
// directory are skipped.
//
// A failure is reported but does not abort the run: as with ticket-eclipse, one
// plugin whose jar fetch is broken should not stop the workspace from being set
// up. The names of the failed plugins are returned so the caller can surface
// them, since compilation will fail later in a much less obvious way.
//
// This duplicates what `pde fetch-jars` does upstream, on purpose — that
// subcommand only exists on pde's HEAD, not in the released build on PATH.
func FetchJars(plugins []Plugin, progress io.Writer) (failed []string) {
	for _, p := range plugins {
		fetchDir := filepath.Join(p.Path(), "lib", "fetch_jars")
		if _, err := os.Stat(filepath.Join(fetchDir, "pom.xml")); err != nil {
			continue
		}
		if err := commandRunner(fetchDir, "mvn", "clean", "package", "-q"); err != nil {
			fmt.Fprintf(progress, "  FAIL %s: %v\n", p.Name, err)
			failed = append(failed, p.Name)
			continue
		}
		fmt.Fprintf(progress, "  OK   %s\n", p.Name)
	}
	return failed
}

func trimTail(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return "..." + string(b[len(b)-n:])
}
