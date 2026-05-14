// Package next implements the `ticket next` subcommand.
package next

import (
	"io"
	"os/exec"
)

// Run shells out to `bd ready`, optionally scoped to a parent ticket.
func Run(db, ticketID string, stdout, stderr io.Writer) error {
	args := []string{}
	if db != "" {
		args = append(args, "--db", db)
	}
	args = append(args, "ready")
	if ticketID != "" {
		args = append(args, "--parent", ticketID)
	}
	cmd := exec.Command("bd", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
