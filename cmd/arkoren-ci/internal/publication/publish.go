package publication

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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
	// Body must equal the exact original git-cliff notes signed in the tag, or
	// the exact body a reviewed [Correction] pins for this tag.
	Body string `json:"body"`
	// Draft must remain false and Prerelease true.
	Draft      bool `json:"draft"`
	Prerelease bool `json:"prerelease"`
	// Assets are not part of module publication.
	Assets []any `json:"assets"`
}

// overwrite is the remedy shared by every release conflict: the publisher reads
// existing releases and never edits, deletes or adopts them.
const overwrite = "published releases are never overwritten"

// Check compares an existing release with its signed plan entry and schema and
// returns an error naming the tag, the first differing field and the remedy.
// It never edits or adopts the release.
//
// corrected is the reviewed body digest pinned for this tag in
// [CorrectionsFile], or empty. When set, the body may equal either the signed
// notes or exactly the body with that SHA-256 (see [BodyDigest]); every other
// field must still match. Pass empty for a release this run just created, which
// must carry the signed notes.
//
// Example:
//
//	if err := item.Check(entry, batch.Plan.SchemaVersion, corrected[TagName(entry)]); err != nil {
//		return err // "release core/v0.1.1: body differs ..."
//	}
func (item Release) Check(entry release.Entry, schema int, corrected string) error {
	tag := TagName(entry)
	name := ReleaseName(entry, schema)
	switch {
	case item.Tag != tag:
		return fmt.Errorf("release %s: tag_name is %q; %s", tag, item.Tag, overwrite)
	case item.Name != name:
		return fmt.Errorf("release %s: name %q differs from signed plan title %q; %s: restore the title on GitHub", tag, item.Name, name, overwrite)
	case item.Draft || !item.Prerelease:
		return fmt.Errorf("release %s: draft=%t prerelease=%t, signed plan requires draft=false prerelease=true; %s: restore the flags on GitHub", tag, item.Draft, item.Prerelease, overwrite)
	case len(item.Assets) != 0:
		return fmt.Errorf("release %s: has %d assets, module releases have none; %s: remove the assets on GitHub", tag, len(item.Assets), overwrite)
	case item.Body == entry.Notes:
		return nil
	}
	published := BodyDigest(item.Body)
	if published == corrected {
		return nil
	}
	pinned := ""
	if corrected != "" {
		pinned = fmt.Sprintf(", pinned correction sha256 %s", corrected)
	}

	return fmt.Errorf("release %s: body differs from the notes in its signed tag (signed sha256 %s, published sha256 %s%s); %s: restore the signed notes on GitHub, or review the edit and pin the published sha256 for %s in %s",
		tag, BodyDigest(entry.Notes), published, pinned, overwrite, tag, CorrectionsFile)
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

// mainSHA reads the current remote branch immediately before validation/writes
// and makes that exact commit available in the local clone.
//
// The publisher job checks out main once at job start, while release-notes and
// Dependabot merges keep advancing the remote branch. Without the commit,
// [requireAncestor] could not tell "advanced main" from "untrusted history".
// The commit is fetched by the SHA the API reported, never by branch name, so a
// second advance between the read and the fetch cannot change what is trusted.
// The repository is public and the checkout keeps no credentials, so the fetch
// is anonymous. A commit already present locally skips the network entirely.
// A fetch failure returns a distinct error naming the SHA, not an ancestry error.
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

	main := ref.Object.SHA
	if _, err := git(ctx, client.Root, "cat-file", "-e", main+"^{commit}"); err == nil {
		return main, nil
	}
	if _, err := git(ctx, client.Root, "fetch", "--no-tags", "--quiet", "origin", main); err != nil {
		return "", fmt.Errorf("cannot fetch remote main %s into the publisher checkout: %w", main, err)
	}

	return main, nil
}

// requireAncestor checks that commit is an ancestor of main, the fetched
// API-read tip. Git exits 1 for a genuine non-ancestor, which returns
// untrusted; any other failure (missing object, unreadable repository) is a
// distinct operational error, so it is never reported as untrusted history.
func requireAncestor(ctx context.Context, root, commit, main, untrusted string) error {
	_, err := git(ctx, root, "merge-base", "--is-ancestor", commit, main)
	var exit *exec.ExitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return errors.New(untrusted)
	default:
		return fmt.Errorf("cannot check ancestry of %s against main %s: %w", commit, main, err)
	}
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
	if err := requireAncestor(ctx, root, head, main, "publisher code is not trusted main history"); err != nil {
		return err
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
	if err := requireAncestor(ctx, root, batch.Plan.SHA, main, "publication SHA is not trusted main history"); err != nil {
		return err
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
// Before selecting batches it requires every existing release of every signed batch
// to match its plan, apart from bodies pinned in [CorrectionsFile] (see [Drift]),
// and reports all such conflicts in one error.
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
	corrected, err := Corrections(client.Root)
	if err != nil {
		return Manifest{}, err
	}
	// Every existing release of every signed batch is checked, completed or not:
	// an unreviewed edit blocks publication until it is restored or pinned.
	if err := Drift(history, known, corrected); err != nil {
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
			if _, ok := known[TagName(entry)]; present == "" || !ok {
				missing = true
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
// the releases and resumes idempotently. An existing release may carry a body
// pinned in [CorrectionsFile] under client.Root; a created one never may.
func PublishReleases(ctx context.Context, client github.Client, batch Batch) error {
	existing, err := releases(ctx, client)
	if err != nil {
		return err
	}
	corrected, err := Corrections(client.Root)
	if err != nil {
		return err
	}
	var created []release.Entry
	for _, entry := range batch.Proposals() {
		name := TagName(entry)
		if item, ok := existing[name]; ok {
			if err := item.Check(entry, batch.Plan.SchemaVersion, corrected[name]); err != nil {
				return err
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
			} else if err := item.Check(entry, schema, ""); err != nil {
				last = fmt.Errorf("created %w", err)
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
