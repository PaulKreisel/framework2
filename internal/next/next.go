// Package next implements the `ticket next` subcommand.
package next

import (
	"io"

	"framework2/internal/bdcli"
)

// Run shells out to `bd ready`, optionally scoped to a parent ticket.
func Run(db, beadsDir, ticketID string, stdout, stderr io.Writer) error {
	args := []string{"ready"}
	if ticketID != "" {
		args = append(args, "--parent", ticketID)
	}
	cmd := bdcli.Cmd(beadsDir, db, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
