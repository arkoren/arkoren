package publication

import (
	"context"
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
	// Tag and Name must both be the exact module-prefixed version.
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
func (item Release) Check(entry release.Entry) error {
	if item.Tag != TagName(entry) || item.Name != TagName(entry) || item.Body != entry.Notes || item.Draft || !item.Prerelease || len(item.Assets) != 0 {
		return fmt.Errorf("existing release conflicts with immutable approved plan; never overwrite")
	}

	return nil
}

// releases reads all release pages before mutations and rejects duplicate tags.
func releases(ctx context.Context, client github.Client) (map[string]Release, error) {
	items, err := github.Pages[Release](ctx, client, github.API+"/releases")
	if err != nil {
		return nil, err
	}
	result := map[string]Release{}
	for _, item := range items {
		if _, ok := result[item.Tag]; ok {
			return nil, fmt.Errorf("duplicate GitHub release tag")
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
			} else if err := item.Check(entry); err != nil {
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
func PublishReleases(ctx context.Context, client github.Client, batch Batch) error {
	existing, err := releases(ctx, client)
	if err != nil {
		return err
	}
	for _, entry := range batch.Proposals() {
		name := TagName(entry)
		if item, ok := existing[name]; ok {
			if err := item.Check(entry); err != nil {
				return err
			}
			continue
		}
		if _, err := client.Run(ctx, entry.Notes, "release", "create", name, "--repo", github.Repository,
			"--verify-tag", "--prerelease", "--latest=false", "--title", name, "--notes-file", "-"); err != nil {
			return err
		}
	}
	confirmed, err := releases(ctx, client)
	if err != nil {
		return err
	}
	for _, entry := range batch.Proposals() {
		if err := confirmed[TagName(entry)].Check(entry); err != nil {
			return err
		}
	}

	return nil
}
