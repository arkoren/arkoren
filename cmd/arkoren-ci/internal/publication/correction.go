package publication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"arkoren.dev/cmd/arkoren-ci/internal/github"
)

// CorrectionsFile is the reviewed allowance for edited bodies of releases that
// already exist, relative to the checkout root. It is read from the executing
// reviewed main checkout, never from a candidate tree or a release tag.
const CorrectionsFile = ".github/release-corrections.json"

// digest matches a lowercase hexadecimal SHA-256.
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Correction pins one reviewed edit of an existing GitHub release body.
//
// A GitHub release body can be edited by anyone with write access, while the
// signed tag annotation keeps the exact original notes. The publisher normally
// requires the two to be equal (see [Release.Check]). A correction accepts one
// exact published body for one tag, identified by its SHA-256, so any further
// edit of that release is detected again. It never changes what the publisher
// writes: new releases always receive the signed notes.
type Correction struct {
	// Tag is the module-prefixed version tag, such as "core/v0.22.1".
	Tag string `json:"tag"`
	// BodySHA256 is the lowercase hex SHA-256 of the exact published body bytes
	// that the GitHub API returns, as printed by a body conflict error.
	BodySHA256 string `json:"body_sha256"`
	// Reason records why the edit was accepted, for review and later audits.
	Reason string `json:"reason"`
}

// corrections is the schema-1 file shape of [CorrectionsFile].
type corrections struct {
	// Schema identifies the allowance format; only 1 exists.
	Schema int `json:"schema"`
	// Corrections lists at most one pinned body per tag.
	Corrections []Correction `json:"corrections"`
}

// BodyDigest returns the lowercase hex SHA-256 of a release body, the identity
// that [Correction.BodySHA256] pins and that conflict errors print.
//
// Example:
//
//	pinned := BodyDigest(published.Body) // compare with Correction.BodySHA256
func BodyDigest(body string) string {
	sum := sha256.Sum256([]byte(body))

	return hex.EncodeToString(sum[:])
}

// Corrections reads [CorrectionsFile] under root and returns the pinned body
// digest of each corrected tag. A missing file means no corrections and returns
// an empty map. Unknown members, another schema, a malformed tag or digest, an
// empty reason and a repeated tag are errors, so an allowance cannot be widened
// by accident. Whether each tag names a published signed release is checked by
// [Drift], which has the release and tag listings.
//
// Example:
//
//	corrected, err := Corrections(root)
//	if err != nil {
//		return err
//	}
//	err = item.Check(entry, schema, corrected[TagName(entry)])
func Corrections(root string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(root, CorrectionsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	file := corrections{}
	if err := json.Unmarshal(data, &file, json.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("decode %s: %w", CorrectionsFile, err)
	}
	if file.Schema != 1 {
		return nil, fmt.Errorf("%s: unsupported schema %d", CorrectionsFile, file.Schema)
	}
	result := map[string]string{}
	for _, item := range file.Corrections {
		if !version.MatchString(filepath.Base(item.Tag)) || !digest.MatchString(item.BodySHA256) || item.Reason == "" {
			return nil, fmt.Errorf("%s: correction %q needs a version tag, a lowercase hex body_sha256 and a reason", CorrectionsFile, item.Tag)
		}
		if _, ok := result[item.Tag]; ok {
			return nil, fmt.Errorf("%s: duplicate correction for %s", CorrectionsFile, item.Tag)
		}
		result[item.Tag] = item.BodySHA256
	}

	return result, nil
}

// Drift compares every existing release of every recorded signed batch with its
// signed plan and returns all conflicts at once, joined and sorted, instead of
// stopping at the first. Releases not created yet are skipped: resuming them is
// the publisher's job. A correction that names no published signed release is
// also an error, so stale or speculative allowances cannot accumulate.
//
// The publisher's prepare phase and the read-only `release notes audit` command
// share this check, so the notes workflow reports an edited release body on its
// `release.edited` event, long before it would block the next publication.
func Drift(history []Batch, known map[string]Release, corrected map[string]string) error {
	problems := []string{}
	signed := map[string]bool{}
	for _, batch := range history {
		for _, entry := range batch.Proposals() {
			name := TagName(entry)
			item, ok := known[name]
			if !ok {
				continue
			}
			signed[name] = true
			if err := item.Check(entry, batch.Plan.SchemaVersion, corrected[name]); err != nil {
				problems = append(problems, err.Error())
			}
		}
	}
	for tag := range corrected {
		if !signed[tag] {
			problems = append(problems, fmt.Sprintf("%s: correction for %s names no published signed release; remove it", CorrectionsFile, tag))
		}
	}
	slices.Sort(problems)
	errs := []error{}
	for _, problem := range problems {
		errs = append(errs, errors.New(problem))
	}

	return errors.Join(errs...)
}

// Audit runs [Drift] over the signed tags in root and the full GitHub release
// listing, read-only. It needs complete tag history (signed annotations and
// their plan commits) and a read-only gh credential; it never requests App or
// signing credentials. It returns the number of listed releases on success.
func Audit(ctx context.Context, client github.Client, trust string) (int, error) {
	history, err := Recorded(ctx, client.Root, trust)
	if err != nil {
		return 0, err
	}
	known, err := releases(ctx, client)
	if err != nil {
		return 0, err
	}
	corrected, err := Corrections(client.Root)
	if err != nil {
		return 0, err
	}

	return len(known), Drift(history, known, corrected)
}
