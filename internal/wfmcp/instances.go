package wfmcp

import (
	"fmt"
	"path/filepath"
	"sort"
)

// Instances lists the `devserver*` presets of every pde*.yaml in envDir, the
// same set the proxy connects to. Each instance carries the wf-mcp port; the
// gateway is one below it, the editor frontend one above (ports.Block).
func Instances(envDir string) ([]Instance, error) {
	files, err := filepath.Glob(filepath.Join(envDir, "pde*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []Instance
	for _, f := range files {
		inst, err := parseInstances(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		out = append(out, inst...)
	}
	return out, nil
}
