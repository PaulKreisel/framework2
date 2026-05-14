# Testing framework2

Tests are realistic: they exercise the real `bd` binary against a real bd database, and (for `dir` tests) real `git init`-ed repos in tmpdirs. No mocks.

## Running tests

```sh
go test ./...        # all packages
go test ./internal/dir/...   # one package
go test -run TestLink ./internal/dir/   # one test
```

The first run creates the shared test DB and takes a few seconds longer than subsequent runs.

## The shared bd database

`testutil.SharedBdDB(t)` returns the path to a persistent bd DB at `<repo>/testdata/.beads`. The directory is gitignored.

- First call per process: runs `bd init --prefix tst --skip-agents --skip-hooks` with `BEADS_DIR` pointed at the test path.
- Subsequent calls: reuse it.
- Side effect: sets `BEADS_DIR` for the calling test via `t.Setenv`, so any `exec.Command("bd", …)` you spawn inside the test hits the test DB instead of the developer's real one.
- Test bead IDs use the `tst-` prefix.

**Hard rule:** every test must clean up every bead it creates (typically with `bd close`). The DB is shared across tests in the package; if you leak open beads, you'll pollute another test's view of `bd ready` etc. Register cleanup via `t.Cleanup(...)` immediately after creation.

`t.Setenv` forbids `t.Parallel()`, and the shared DB is global state anyway — keep tests serial.

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
