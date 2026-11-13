package publication

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"arkoren.dev/cmd/arkoren-ci/internal/evidence"
	"arkoren.dev/cmd/arkoren-ci/internal/github"
	"arkoren.dev/cmd/arkoren-ci/internal/release"
)

// Manifest records validated pending batches, without credentials or executable data.
type Manifest struct {
	// Candidate is the exact trusted event SHA.
	Candidate string `json:"candidate"`
	// Batches are ordered by source ancestry, not lexical SHA.
	Batches []Batch `json:"batches"`
}

// Release holds the immutable release fields checked before and after creation.
type Release struct {
	// Tag is the module-prefixed version; Name follows the recorded plan schema.
	Tag  string `json:"tag_name"`
	Name string `json:"name"`
	// Body must equal the exact original git-cliff notes.
	Body string `json:"body"`
	// Draft must remain false and Prerelease true.
	Draft      bool `json:"draft"`
	Prerelease bool `json:"prerelease"`
	// Assets are not part of module publication.
	Assets []any `json:"assets"`
}

// Check refuses conflicting existing releases; it never edits or adopts them.
func (item Release) Check(entry release.Entry, schema int) error {
	if item.Tag != TagName(entry) || item.Name != ReleaseName(entry, schema) || item.Body != entry.Notes || item.Draft || !item.Prerelease || len(item.Assets) != 0 {
		return fmt.Errorf("existing release conflicts with immutable approved plan; never overwrite")
	}

	return nil
}

// errDuplicateRelease reports a listing that contains one tag twice, which
// GitHub can briefly produce while pagination shifts under a concurrent create.
var errDuplicateRelease = errors.New("duplicate GitHub release tag")

// confirmation spaces post-create listings; GitHub's release list can briefly
// omit or show stale just-created releases. Total wait 3m43s, far below the
// one-hour App token and the 120-minute job timeout.
var confirmation = []time.Duration{0, 3 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute}

// releases reads all release pages before mutations and rejects duplicate tags
// with [errDuplicateRelease].
func releases(ctx context.Context, client github.Client) (map[string]Release, error) {
	items, err := github.Pages[Release](ctx, client, github.API+"/releases")
	if err != nil {
		return nil, err
	}
	result := map[string]Release{}
	for _, item := range items {
		if _, ok := result[item.Tag]; ok {
			return nil, errDuplicateRelease
		}
		result[item.Tag] = item
	}

	return result, nil
}

// mainSHA reads the current remote branch immediately before validation/writes.
func mainSHA(ctx context.Context, client github.Client) (string, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := client.Call(ctx, "GET", github.API+"/git/ref/heads/main", nil, &ref); err != nil {
		return "", err
	}
	if len(ref.Object.SHA) != 40 {
		return "", fmt.Errorf("invalid remote main identity")
	}

	return ref.Object.SHA, nil
}

// requireEvidence reuses the exact repository/event/latest-attempt Go authority.
func requireEvidence(ctx context.Context, root, sha, mode string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	_, err := evidence.Collect(ctx, evidence.Target{SHA: sha, Branch: "main", Repository: github.Repository, Mode: mode}, evidence.GitHub(root), evidence.Delay)

	return err
}

// codeEvidence validates the trusted currently executing implementation too.
func codeEvidence(ctx context.Context, root, main string) error {
	head, err := git(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if _, err := git(ctx, root, "merge-base", "--is-ancestor", head, main); err != nil {
		return fmt.Errorf("publisher code is not trusted main history")
	}

	return requireEvidence(ctx, root, head, "publisher-code")
}

// fresh refuses stale or regressive new proposals and uses only current policy.
func fresh(ctx context.Context, root, sha, main string) (Batch, error) {
	if sha != main {
		return Batch{}, fmt.Errorf("stale candidate; rerun latest exact-green main")
	}
	plan, err := release.Prepare(ctx, root, sha, nil)
	if err != nil {
		return Batch{}, err
	}
	if err := approved(root, plan); err != nil {
		return Batch{}, err
	}
	raw, err := git(ctx, root, "tag", "--list")
	if err != nil {
		return Batch{}, err
	}
	for tag := range strings.SplitSeq(raw, "\n") {
		if tag == "" {
			continue
		}
		target, err := git(ctx, root, "rev-parse", "refs/tags/"+tag+"^{commit}")
		if err != nil {
			return Batch{}, err
		}
		if _, err := git(ctx, root, "merge-base", "--is-ancestor", target, sha); err != nil {
			return Batch{}, fmt.Errorf("nonancestor release tag; refuse regressive history")
		}
	}

	return NewBatch(plan)
}

// Validate recomputes the full plan and forbids stale incomplete tag batches.
func Validate(ctx context.Context, root, trust, main string, batch Batch) error {
	if _, err := git(ctx, root, "merge-base", "--is-ancestor", batch.Plan.SHA, main); err != nil {
		return fmt.Errorf("publication SHA is not trusted main history")
	}
	if err := Recompute(ctx, root, batch); err != nil {
		return err
	}
	complete := true
	for _, entry := range batch.Proposals() {
		name := TagName(entry)
		present, err := git(ctx, root, "tag", "--list", name)
		if err != nil {
			return err
		}
		if present == "" {
			complete = false
			continue
		}
		if _, err := VerifyTag(ctx, root, trust, name, &batch); err != nil {
			return err
		}
	}
	if !complete {
		_, err := fresh(ctx, root, batch.Plan.SHA, main)
		return err
	}

	return nil
}

// Prepare discovers pending signed batches and new genuine proposals read-only.
func Prepare(ctx context.Context, client github.Client, trust, sha string) (Manifest, error) {
	main, err := mainSHA(ctx, client)
	if err != nil {
		return Manifest{}, err
	}
	if err := codeEvidence(ctx, client.Root, main); err != nil {
		return Manifest{}, err
	}
	history, err := Recorded(ctx, client.Root, trust)
	if err != nil {
		return Manifest{}, err
	}
	matching := slices.ContainsFunc(history, func(batch Batch) bool { return batch.Plan.SHA == sha })
	if sha != main && !matching {
		return Manifest{}, fmt.Errorf("stale candidate has no signed batch to resume")
	}
	known, err := releases(ctx, client)
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{Candidate: sha, Batches: []Batch{}}
	for _, batch := range history {
		if sha != main && batch.Plan.SHA != sha {
			continue
		}
		missing := false
		for _, entry := range batch.Proposals() {
			present, err := git(ctx, client.Root, "tag", "--list", TagName(entry))
			if err != nil {
				return Manifest{}, err
			}
			if present == "" {
				missing = true
			}
			item, ok := known[TagName(entry)]
			if !ok {
				missing = true
			} else if err := item.Check(entry, batch.Plan.SchemaVersion); err != nil {
				return Manifest{}, err
			}
		}
		if missing || batch.Plan.SHA == sha {
			if err := Validate(ctx, client.Root, trust, main, batch); err != nil {
				return Manifest{}, err
			}
			if err := requireEvidence(ctx, client.Root, batch.Plan.SHA, "publisher-recovery"); err != nil {
				return Manifest{}, err
			}
			if missing {
				manifest.Batches = append(manifest.Batches, batch)
			} else {
				complete, err := Reconcile(ctx, client.Root, trust, batch)
				if err != nil {
					return Manifest{}, err
				}
				if !complete {
					return Manifest{}, fmt.Errorf("completed batch has missing remote refs")
				}
				if err := PublicVerify(ctx, client.Root, batch); err != nil {
					return Manifest{}, err
				}
			}
		}
	}
	if sha == main {
		if err := requireEvidence(ctx, client.Root, sha, "publisher"); err != nil {
			return Manifest{}, err
		}
		batch, err := fresh(ctx, client.Root, sha, main)
		if err != nil {
			return Manifest{}, err
		}
		if len(batch.Proposals()) > 0 {
			manifest.Batches = append(manifest.Batches, batch)
		}
	}
	counts := map[string]int{}
	for _, batch := range manifest.Batches {
		raw, err := git(ctx, client.Root, "rev-list", "--count", batch.Plan.SHA)
		if err != nil {
			return Manifest{}, err
		}
		count, err := strconv.Atoi(raw)
		if err != nil {
			return Manifest{}, err
		}
		counts[batch.Digest] = count
	}
	slices.SortFunc(manifest.Batches, func(left, right Batch) int { return counts[left.Digest] - counts[right.Digest] })

	return manifest, nil
}

// ValidateManifest runs before every phase; manifests never authorize mutations.
func ValidateManifest(ctx context.Context, client github.Client, trust string, manifest Manifest) error {
	main, err := mainSHA(ctx, client)
	if err != nil {
		return err
	}
	if err := codeEvidence(ctx, client.Root, main); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, batch := range manifest.Batches {
		if seen[batch.Digest] || len(batch.Proposals()) == 0 {
			return fmt.Errorf("duplicate or empty publication batch")
		}
		seen[batch.Digest] = true
		if err := Validate(ctx, client.Root, trust, main, batch); err != nil {
			return err
		}
		if err := requireEvidence(ctx, client.Root, batch.Plan.SHA, "publisher-before-write"); err != nil {
			return err
		}
	}

	return nil
}

// PublishReleases creates exact prereleases through native gh with a fresh App token.
// The caller must first finish all public builds in this batch in the same job.
//
// The pre-mutation listing is fail-fast: a listing error, or an existing release
// that conflicts with the approved plan, returns at once with no wait and no
// write. Missing releases are created with `gh release create`; a create error
// returns immediately. Because GitHub's release list can briefly omit or show
// stale just-created releases, only the releases created by this call are then
// re-read with bounded backoff (see [confirmation], about 3m43s) until each
// matches the plan. Confirmation is read-only: exhaustion fails without
// recreating, editing or deleting anything, and a rerun of the same SHA finds
// the releases and resumes idempotently.
func PublishReleases(ctx context.Context, client github.Client, batch Batch) error {
	existing, err := releases(ctx, client)
	if err != nil {
		return err
	}
	var created []release.Entry
	for _, entry := range batch.Proposals() {
		name := TagName(entry)
		if item, ok := existing[name]; ok {
			if err := item.Check(entry, batch.Plan.SchemaVersion); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			continue
		}
		if _, err := client.Run(ctx, entry.Notes, "release", "create", name, "--repo", github.Repository,
			"--verify-tag", "--prerelease", "--latest=false", "--title", ReleaseName(entry, batch.Plan.SchemaVersion), "--notes-file", "-"); err != nil {
			return err
		}
		created = append(created, entry)
	}

	return confirm(ctx, client, batch.Plan.SchemaVersion, created)
}

// confirm re-reads the full release listing on the [confirmation] schedule until
// every created entry is present and passes [Release.Check]. A release that is
// absent, stale, or listed in a duplicated page stays pending and is retried; a
// persistent mismatch therefore fails at the bound with its conflict reason.
// Other listing errors and context cancellation return immediately. It only
// reads; it never writes, edits or deletes a release. With nothing created it
// performs no listing.
func confirm(ctx context.Context, client github.Client, schema int, created []release.Entry) error {
	pending := created
	var last error
	for _, delay := range confirmation {
		if len(pending) == 0 {
			return nil
		}
		if err := evidence.Delay(ctx, delay); err != nil {
			return err
		}
		listed, err := releases(ctx, client)
		if errors.Is(err, errDuplicateRelease) {
			last = err
			continue
		}
		if err != nil {
			return err
		}
		var next []release.Entry
		for _, entry := range pending {
			name := TagName(entry)
			item, ok := listed[name]
			if !ok {
				last = fmt.Errorf("created release %s not yet visible", name)
				next = append(next, entry)
			} else if err := item.Check(entry, schema); err != nil {
				last = fmt.Errorf("created release %s: %w", name, err)
				next = append(next, entry)
			}
		}
		pending = next
	}
	if len(pending) == 0 {
		return nil
	}

	return fmt.Errorf("created releases unconfirmed after bounded listing; rerun the same SHA, never recreate: %w", last)
}
