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
//
// BEADS_DIR resolution falls back to os.Getenv. Use [RunTicketIn] in parallel
// tests that need a per-test .beads/ directory.
func RunTicket(t testing.TB, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	return RunTicketIn(t, "", args...)
}

// RunTicketIn is like [RunTicket] but pins BEADS_DIR to beadsDir for the
// invocation, so production code paths see beadsDir even when the test runs
// in parallel and cannot use t.Setenv.
func RunTicketIn(t testing.TB, beadsDir string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	var sout, serr bytes.Buffer
	code := ticketcli.RunWithOptions(ticketcli.Options{BeadsDir: beadsDir}, args, &sout, &serr)
	return sout.String(), serr.String(), code
}

// RunThisFrom invokes the `this` command in-process as if cwd were the given
// directory. Returns captured stdout, stderr, and exit code.
//
// BEADS_DIR resolution falls back to os.Getenv. Use [RunThisFromIn] in
// parallel tests that need a per-test .beads/ directory.
func RunThisFrom(t testing.TB, cwd string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	return RunThisFromIn(t, "", cwd, args...)
}

// RunThisFromIn is like [RunThisFrom] but pins BEADS_DIR to beadsDir.
func RunThisFromIn(t testing.TB, beadsDir, cwd string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	var sout, serr bytes.Buffer
	code := thiscli.RunFromWithOptions(ticketcli.Options{BeadsDir: beadsDir}, cwd, args, &sout, &serr)
	return sout.String(), serr.String(), code
}
