package dir

import (
	"os/exec"
	"strings"
)

func gitToplevel(path string) (string, bool) {
	return runGit(path, "rev-parse", "--show-toplevel")
}

func gitBranch(path string) (string, bool) {
	return runGit(path, "rev-parse", "--abbrev-ref", "HEAD")
}

func gitRemote(path string) (string, bool) {
	return runGit(path, "config", "remote.origin.url")
}

func runGit(path string, args ...string) (string, bool) {
	full := append([]string{"-C", path}, args...)
	out, err := exec.Command("git", full...).Output()
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "", false
	}
	return s, true
}
