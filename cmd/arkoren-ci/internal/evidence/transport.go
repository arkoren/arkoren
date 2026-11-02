package evidence

import (
	"context"
	"fmt"
	"strings"
	"time"

	"arkoren.dev/cmd/arkoren-ci/internal/process"
)

// GitHub reads metadata with gh's authenticated paginated GET implementation.
func GitHub(root string) Reader {
	return func(ctx context.Context, path string) ([]byte, error) {
		return process.Run(ctx, root, "gh", "", true, "api", "--method", "GET", "--paginate", "--slurp", path)
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
