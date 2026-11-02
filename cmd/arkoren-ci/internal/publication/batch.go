package publication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"arkoren.dev/cmd/arkoren-ci/internal/process"
	"arkoren.dev/cmd/arkoren-ci/internal/release"
	"arkoren.dev/cmd/arkoren-ci/internal/repository"
)

// marker identifies the historical signed annotation protocol.
const marker = "Arkoren module publication v1\n"

// principal binds signatures to the dedicated reviewed automation key.
const principal = "arkoren-releases@arkoren.dev"

// version limits publication to plain, canonical v0 versions.
var version = regexp.MustCompile(`^v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Batch preserves the full original approved plan before the first ref write.
type Batch struct {
	// Kind is the immutable original format identifier.
	Kind string `json:"kind"`
	// Plan includes skipped entries and exact notes, not only selected tags.
	Plan release.Plan `json:"plan"`
	// Digest is SHA-256 of the Python-compatible canonical plan bytes.
	Digest string `json:"digest"`
}

// NewBatch computes the signed-v1 plan digest without changing its wire shape.
func NewBatch(plan release.Plan) (Batch, error) {
	encoded, err := Canonical(plan)
	if err != nil {
		return Batch{}, err
	}
	digest := sha256.Sum256([]byte(encoded))

	return Batch{Kind: "arkoren-module-publication-v1", Plan: plan, Digest: hex.EncodeToString(digest[:])}, nil
}

// Proposals selects versions without discarding original plan members.
func (batch Batch) Proposals() []release.Entry {
	result := []release.Entry{}
	for _, entry := range batch.Plan.Modules {
		if entry.NextVersion != nil {
			result = append(result, entry)
		}
	}

	return result
}

// TagName forms one canonical directory-prefixed immutable version ref.
func TagName(entry release.Entry) string { return entry.Directory + "/" + *entry.NextVersion }

// Annotation returns the exact bytes signed by both historical and new code.
func (batch Batch) Annotation() (string, error) {
	encoded, err := Canonical(batch)
	if err != nil {
		return "", err
	}

	return marker + encoded + "\n", nil
}

// git runs local read operations without any App or signing credentials.
func git(ctx context.Context, root string, args ...string) (string, error) {
	data, err := process.Run(ctx, root, "git", "", false, args...)

	return strings.TrimSpace(string(data)), err
}

// approved checks current reviewed namespaces even for historically signed plans.
func approved(root string, plan release.Plan) error {
	data, err := os.ReadFile(filepath.Join(root, ".github/release-modules.json"))
	if err != nil {
		return err
	}
	policy, err := repository.ParsePolicy(data)
	if err != nil {
		return err
	}
	actual := []string{}
	for _, entry := range plan.Modules {
		actual = append(actual, entry.Directory)
		if entry.SHA != plan.SHA || (entry.NextVersion != nil && !version.MatchString(*entry.NextVersion)) {
			return fmt.Errorf("publication permits only approved exact-SHA plain v0 module versions")
		}
	}
	slices.Sort(actual)
	slices.Sort(policy.Public)
	if !slices.Equal(actual, policy.Public) {
		return fmt.Errorf("plan inventory differs from reviewed public namespaces")
	}

	return nil
}

// VerifyTag checks cryptography, canonical bytes, target, headers and membership.
func VerifyTag(ctx context.Context, root, trust, name string, expected *Batch) (Batch, error) {
	if _, err := git(ctx, root, "-c", "gpg.format=ssh", "-c", "gpg.ssh.allowedSignersFile="+trust, "verify-tag", "--", name); err != nil {
		return Batch{}, fmt.Errorf("tag signature is not trusted: %w", err)
	}
	data, err := process.Run(ctx, root, "git", "", false, "cat-file", "tag", "refs/tags/"+name)
	if err != nil {
		return Batch{}, err
	}
	headers, message, ok := strings.Cut(string(data), "\n\n")
	if !ok || !strings.HasPrefix(message, marker) {
		return Batch{}, fmt.Errorf("signed tag lacks publication annotation")
	}
	index := strings.LastIndex(message, "-----BEGIN SSH SIGNATURE-----")
	if index < 0 {
		return Batch{}, fmt.Errorf("signed tag lacks SSH signature")
	}
	unsigned := message[:index]
	batch := Batch{}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(unsigned, marker)), &batch, json.RejectUnknownMembers(true)); err != nil {
		return Batch{}, err
	}
	annotation, err := batch.Annotation()
	if err != nil {
		return Batch{}, err
	}
	recomputed, err := NewBatch(batch.Plan)
	if err != nil {
		return Batch{}, err
	}
	if unsigned != annotation || batch.Kind != recomputed.Kind || batch.Digest != recomputed.Digest {
		return Batch{}, fmt.Errorf("noncanonical signed annotation or digest mismatch")
	}
	if expected != nil {
		wanted, err := expected.Annotation()
		if err != nil {
			return Batch{}, err
		}
		if annotation != wanted {
			return Batch{}, fmt.Errorf("existing tag records a different approved batch")
		}
	}
	target, err := git(ctx, root, "rev-parse", "refs/tags/"+name+"^{commit}")
	if err != nil {
		return Batch{}, err
	}
	member := slices.ContainsFunc(batch.Proposals(), func(entry release.Entry) bool { return TagName(entry) == name && entry.SHA == target })
	if !member || !slices.Contains(strings.Split(headers, "\n"), "type commit") || !slices.Contains(strings.Split(headers, "\n"), "tag "+name) {
		return Batch{}, fmt.Errorf("signed tag target, header or membership conflict")
	}

	return batch, nil
}

// Recorded recovers original plans; bootstrap v0.1.0 tags remain untouched.
func Recorded(ctx context.Context, root, trust string) ([]Batch, error) {
	data, err := os.ReadFile(filepath.Join(root, ".github/release-modules.json"))
	if err != nil {
		return nil, err
	}
	policy, err := repository.ParsePolicy(data)
	if err != nil {
		return nil, err
	}
	raw, err := git(ctx, root, "tag", "--list")
	if err != nil {
		return nil, err
	}
	batches := map[string]Batch{}
	members := map[string]string{}
	for name := range strings.SplitSeq(raw, "\n") {
		if name == "" {
			continue
		}
		directory, tag := filepath.ToSlash(filepath.Dir(name)), filepath.Base(name)
		if !slices.Contains(policy.Public, directory) || !version.MatchString(tag) {
			return nil, fmt.Errorf("tag outside approved canonical namespaces")
		}
		if tag == "v0.1.0" {
			continue
		}
		batch, err := VerifyTag(ctx, root, trust, name, nil)
		if err != nil {
			return nil, err
		}
		if err := approved(root, batch.Plan); err != nil {
			return nil, err
		}
		batches[batch.Digest], members[name] = batch, batch.Digest
	}
	result := []Batch{}
	for _, batch := range batches {
		for _, entry := range batch.Proposals() {
			if digest, ok := members[TagName(entry)]; ok && digest != batch.Digest {
				return nil, fmt.Errorf("conflicting signed batch membership")
			}
		}
		result = append(result, batch)
	}

	return result, nil
}

// Recompute removes only batch refs in a disposable clone before historical planning.
func Recompute(ctx context.Context, root string, batch Batch) error {
	directory, err := os.MkdirTemp("", "release-recompute-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	if _, err := git(ctx, root, "clone", "--bare", "--no-hardlinks", root, directory); err != nil {
		return err
	}
	for _, entry := range batch.Proposals() {
		if _, err := git(ctx, directory, "update-ref", "-d", "refs/tags/"+TagName(entry)); err != nil {
			return err
		}
	}
	plan, err := release.PrepareHistorical(ctx, directory, batch.Plan.SHA, nil)
	if err != nil {
		return err
	}
	expected, err := NewBatch(plan)
	if err != nil {
		return err
	}
	actual, err := batch.Annotation()
	if err != nil {
		return err
	}
	wanted, err := expected.Annotation()
	if err != nil {
		return err
	}
	if actual != wanted {
		return fmt.Errorf("signed plan differs from recomputed trusted source")
	}

	return approved(root, plan)
}
