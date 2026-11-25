package evidence

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"arkoren.dev/cmd/arkoren-ci/internal/process"
)

// stderrExcerptLimit bounds the gh diagnostic copied into an error.
const stderrExcerptLimit = 200

// GitHub reads metadata with gh's authenticated paginated GET implementation.
// A failed call includes a short single-line excerpt of gh's stderr (for example
// "HTTP 502: Bad Gateway"). gh prints API diagnostics there, never tokens or the
// environment; the excerpt makes failures diagnosable and lets retrying tell
// HTTP 4xx from 5xx.
func GitHub(root string) Reader {
	return func(ctx context.Context, path string) ([]byte, error) {
		data, err := process.Run(ctx, root, "gh", "", true, "api", "--method", "GET", "--paginate", "--slurp", path)
		if exit := (*exec.ExitError)(nil); errors.As(err, &exit) {
			excerpt := strings.Join(strings.Fields(string(exit.Stderr)), " ")
			excerpt = excerpt[:min(len(excerpt), stderrExcerptLimit)]

			return nil, fmt.Errorf("%w: gh: %s", err, excerpt)
		}

		return data, err
	}
}

// Delay waits for the next bounded poll without ignoring cancellation.
func Delay(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ValidateCheckout requires the event-selected commit and fetched branch ancestry
// before any credential-bearing metadata subprocess is invoked.
func ValidateCheckout(ctx context.Context, root string, target Target) error {
	head, err := process.Run(ctx, root, "git", "", false, "rev-parse", "HEAD")

	if err != nil {
		return err
	}
	if strings.TrimSpace(string(head)) != target.SHA {
		return fmt.Errorf("checkout does not match exact requested/upstream SHA")
	}
	_, err = process.Run(ctx, root, "git", "", false, "merge-base", "--is-ancestor", target.SHA, "refs/remotes/origin/"+target.Branch)

	if err != nil {
		return fmt.Errorf("requested SHA is not on the selected fetched branch: %w", err)
	}

	return nil
}
