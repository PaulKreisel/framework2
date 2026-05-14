// Package agent implements the `ticket agent` subcommand: launching an AI
// agent (currently always `claude`) from a ticket's on-disk env folder.
package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// AgentCommand is the binary execed by Run. Exported as a var so future work
// can swap it (e.g. a per-ticket choice) without further plumbing.
var AgentCommand = "claude"

// Run resolves the env folder for ticketID (beadsDir/envs/<id>/) and execs
// AgentCommand with cwd set to that folder and stdio inherited.
//
// The env folder must already exist — Run does not create it. Use
// `ticket env <id>` first.
func Run(beadsDir, ticketID string) error {
	if ticketID == "" {
		return errors.New("ticket id required")
	}
	if beadsDir == "" {
		return errors.New("BEADS_DIR not set: cannot derive env root")
	}
	envPath := filepath.Join(beadsDir, "envs", ticketID)
	info, err := os.Stat(envPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("env folder does not exist for %s: %s (run `ticket env %s` first)", ticketID, envPath, ticketID)
		}
		return fmt.Errorf("stat env dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("env path is not a directory: %s", envPath)
	}
	cmd := exec.Command(AgentCommand)
	cmd.Dir = envPath
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
