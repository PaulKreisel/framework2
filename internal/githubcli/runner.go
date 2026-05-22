package githubcli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// GHRunner shells out to the `gh` CLI (or a stand-in in tests). It returns
// the command's stdout. The command's stderr is forwarded verbatim by the
// default runner so authentication / API errors surface unchanged.
type GHRunner func(ctx context.Context, args []string) ([]byte, error)

// newDefaultRunner builds a [GHRunner] that exec's the real `gh` binary and
// pipes its stderr to ghStderr. Used by [Run].
func newDefaultRunner(ghStderr io.Writer) GHRunner {
	return func(ctx context.Context, args []string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "gh", args...)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = ghStderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
		}
		return stdout.Bytes(), nil
	}
}
