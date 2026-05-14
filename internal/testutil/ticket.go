package testutil

import (
	"bytes"
	"testing"

	"framework2/internal/thiscli"
	"framework2/internal/ticketcli"
)

// RunTicket invokes the `ticket` command in-process with the given args and
// returns captured stdout, stderr, and the exit code that the binary would
// have returned.
//
// Use this in tests instead of shelling out to a built `ticket` binary — it
// is faster and avoids any "did you build a stale binary?" failure mode.
func RunTicket(t testing.TB, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	var sout, serr bytes.Buffer
	code := ticketcli.Run(args, &sout, &serr)
	return sout.String(), serr.String(), code
}

// RunThisFrom invokes the `this` command in-process as if cwd were the given
// directory. Returns captured stdout, stderr, and exit code.
func RunThisFrom(t testing.TB, cwd string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	var sout, serr bytes.Buffer
	code := thiscli.RunFrom(cwd, args, &sout, &serr)
	return sout.String(), serr.String(), code
}
