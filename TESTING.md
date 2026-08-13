# Testing framework2

Tests are realistic: they exercise the real `bd` binary against a real bd database, and (for `dir` tests) real `git init`-ed repos in tmpdirs. No mocks.

## Tests are disabled on purpose

**Read this before "fixing" anything about the test setup.**

Every `*_test.go` file in this repo carries a `//go:build runtests` constraint, so the default build excludes the whole suite. `make test` refuses to run and exits 1, and `go test ./...` fails on a single deliberate guard test (`internal/testsdisabled`) whose only job is to say so out loud instead of quietly printing "no test files" for 20 packages.

Why: the suite is realistic, which makes it slow — real `bd`, real Dolt servers, real git repos in tmpdirs. Running it is a deliberate act, not something an agent should do reflexively at the end of every change.

**This is a switch, not a bug.** Do not drop the build tags, do not delete the guard, do not re-point `make test` at `go test ./...`. If you think the default should change, ask the repo owner first.

## Running tests

The escape hatch is the `runtests` tag:

```sh
make test-for-real                                   # all packages
go test -tags runtests ./...                         # same thing
go test -tags runtests ./internal/dir/...            # one package
go test -tags runtests -run TestLink ./internal/dir/ # one test
```

The first run per package creates the test DB and takes a few seconds longer than subsequent runs.

New test files must carry the tag too — copy the header block from any existing `*_test.go`.

## The shared bd database

Each test package owns a bd database at `<repo>/testdata/<pkg>/.beads/` (gitignored). The package's `TestMain` starts a long-lived `dolt sql-server` against that directory for the duration of the test process, so subprocess `bd` calls drop from ~0.9s (embedded boot per call) to ~0.2s (SQL handshake per call).

Per-package data dirs let `go test ./...` run packages in parallel without contending on a single Dolt directory.

**Wire your test package in** with one file:

```go
// testmain_test.go
package mypkg_test

import (
    "os"
    "testing"

    "framework2/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunTestMain(m)) }
```

`RunTestMain` is idempotent: on first run it `bd init`s the DB and flips it to server mode; on subsequent runs it reuses what's there and just starts the server. The server is torn down on process exit (including SIGINT/SIGTERM).

In test bodies, call `testutil.SharedBdDB(t)` to get the DB path. It also re-sets `BEADS_DIR` in the test's scope via `t.Setenv`, so `exec.Command("bd", …)` invocations hit this package's DB.

Test bead IDs use the `tst-` prefix.

**Hard rule:** every test must clean up every bead it creates (typically with `bd close`). The DB is shared across tests in the package; if you leak open beads, you'll pollute another test's view of `bd ready` etc. Register cleanup via `t.Cleanup(...)` immediately after creation.

`t.Setenv` forbids `t.Parallel()`, and the package DB is global state anyway — keep tests serial within a package.

**Orphan servers.** A test process killed with `SIGKILL` (or a runaway `go test` left over from a previous shell) can leak a `dolt sql-server`. Clean up with `BEADS_DIR=… bd dolt stop` per package, or `bd dolt killall` (from a non-test cwd) to nuke any running servers bd knows about.

## Invoking `ticket` subcommands

`testutil.RunTicket(t, args...)` returns `(stdout, stderr string, exitCode int)`. It calls the dispatcher in-process — no subprocess, no stale-binary failure mode.

```go
stdout, stderr, code := testutil.RunTicket(t, "next", parentID)
```

## Setting up test state

Use `exec.Command` directly rather than wrapper helpers — keep test setup transparent. The two patterns you'll need most:

**Create a bead and register its cleanup:**

```go
func createBead(t *testing.T, title string) string {
    t.Helper()
    out, err := exec.Command("bd", "create", title, "--silent").Output()
    if err != nil {
        t.Fatalf("bd create: %v", err)
    }
    id := strings.TrimSpace(string(out))
    t.Cleanup(func() {
        _ = exec.Command("bd", "close", id).Run()
    })
    return id
}
```

**Make a throwaway git repo:**

```go
func gitRepo(t *testing.T, withRemote bool) string {
    t.Helper()
    dir := t.TempDir()
    run := func(args ...string) {
        cmd := exec.Command(args[0], args[1:]...)
        cmd.Dir = dir
        if out, err := cmd.CombinedOutput(); err != nil {
            t.Fatalf("%v: %v\n%s", args, err, out)
        }
    }
    run("git", "init", "-q", "-b", "main")
    if withRemote {
        run("git", "remote", "add", "origin", "git@example.com:org/repo.git")
    }
    // ...commit something so HEAD resolves to a branch, etc.
    return dir
}
```

These are tiny — write them inline in the test file. Resist the urge to centralize.

## Worked example

```go
package mypkg_test

import (
    "os/exec"
    "strings"
    "testing"

    "framework2/internal/testutil"
)

func TestNextListsReadyChildren(t *testing.T) {
    testutil.SharedBdDB(t)   // sets BEADS_DIR

    parent := createBead(t, "parent")
    child := createBead(t, "child")
    if out, err := exec.Command("bd", "dep", "add", child, parent).CombinedOutput(); err != nil {
        t.Fatalf("dep add: %v\n%s", err, out)
    }

    stdout, stderr, code := testutil.RunTicket(t, "next", parent)
    if code != 0 {
        t.Fatalf("exit %d, stderr=%q", code, stderr)
    }
    if !strings.Contains(stdout, child) {
        t.Errorf("expected child %s in output, got %q", child, stdout)
    }
}

func createBead(t *testing.T, title string) string {
    t.Helper()
    out, err := exec.Command("bd", "create", title, "--silent").Output()
    if err != nil {
        t.Fatalf("bd create: %v", err)
    }
    id := strings.TrimSpace(string(out))
    t.Cleanup(func() { _ = exec.Command("bd", "close", id).Run() })
    return id
}
```

That's the whole pattern: `SharedBdDB(t)` for the env, `exec.Command("bd", …)` for setup, `RunTicket(t, …)` for the system under test, `t.Cleanup` for teardown.
