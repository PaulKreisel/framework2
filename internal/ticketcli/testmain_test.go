// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package ticketcli_test

import (
	"os"
	"testing"

	"framework2/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunTestMain(m)) }
