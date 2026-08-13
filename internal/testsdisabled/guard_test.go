// This guard is INTENTIONAL. It is the one test that still runs by default, and
// it exists to fail — so that `go test ./...` says out loud that the rest of the
// suite is switched off rather than quietly printing "no test files". Do not
// delete it, do not make it pass, and do not tag it out.
//
// Under `-tags runtests` the guard disappears and the real suite compiles in.

//go:build !runtests

package testsdisabled

import "testing"

// TestSuiteIsDisabledOnPurpose always fails. See the file comment above and
// TESTING.md — this is a deliberate switch, not a broken test.
func TestSuiteIsDisabledOnPurpose(t *testing.T) {
	t.Fatal(`tests in this repo are DISABLED ON PURPOSE — this failure is expected.

Every *_test.go file carries a "//go:build runtests" constraint, so the default
build excludes the suite. The suite is slow (real bd, real git repos in tmpdirs)
and running it is a deliberate act, not a reflex.

  * To run the suite anyway:   make test-for-real   (or: go test -tags runtests ./...)
  * To run one package:        go test -tags runtests ./internal/agent/

Do NOT "fix" this by deleting the guard, dropping the build tags, or making this
test pass. If you believe tests should run by default, ask the repo owner first.
See TESTING.md, section "Tests are disabled on purpose".`)
}
