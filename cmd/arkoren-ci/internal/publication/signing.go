package publication

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"arkoren.dev/cmd/arkoren-ci/internal/process"
)

// execute bounds Git/SSH operations and excludes all child diagnostic output.
func execute(ctx context.Context, root string, env []string, binary, input string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	return process.Execute(ctx, root, env, binary, input, args...)
}

// withSigner confines the existing secret to a 0600 file in a 0700 directory.
// Cancellation unwinds this owner as well as ordinary errors and successful use.
func withSigner(ctx context.Context, trust, secret string, operation func([]string) error) error {
	if secret == "" {
		return fmt.Errorf("dedicated tag signing secret is not configured")
	}
	directory, err := os.MkdirTemp("", "release-signing-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	key := filepath.Join(directory, "signing-key")
	file, err := os.OpenFile(key, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = file.WriteString(strings.TrimSuffix(secret, "\n") + "\n")
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("write temporary signing key")
	}
	if closeErr != nil {
		return fmt.Errorf("close temporary signing key")
	}
	public, err := execute(ctx, "", process.Environment(false), "ssh-keygen", "", "-y", "-f", key)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(trust)
	if err != nil {
		return err
	}
	fields, actual := strings.Fields(string(data)), strings.Fields(string(public))
	if len(fields) != 3 || fields[0] != principal || len(actual) < 2 || actual[0] != fields[1] || actual[1] != fields[2] {
		return fmt.Errorf("signing secret differs from reviewed automation public key")
	}
	env := append(process.Environment(false),
		"GIT_COMMITTER_NAME=Arkoren Releases automation", "GIT_COMMITTER_EMAIL="+principal,
		"GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=gpg.format", "GIT_CONFIG_VALUE_0=ssh",
		"GIT_CONFIG_KEY_1=user.signingkey", "GIT_CONFIG_VALUE_1="+key,
		"GIT_CONFIG_KEY_2=gpg.ssh.allowedSignersFile", "GIT_CONFIG_VALUE_2="+trust)

	return operation(env)
}

// Preflight proves the dedicated SSH key locally without production ref writes.
func Preflight(ctx context.Context, trust, secret string) error {
	return withSigner(ctx, trust, secret, func(env []string) error {
		directory, err := os.MkdirTemp("", "release-signature-proof-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(directory)
		if _, err := execute(ctx, directory, env, "git", "", "init", "-b", "proof"); err != nil {
			return err
		}
		env = append(env, "GIT_AUTHOR_NAME=Arkoren Releases automation", "GIT_AUTHOR_EMAIL="+principal)
		if _, err := execute(ctx, directory, env, "git", "", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "local signature preflight"); err != nil {
			return err
		}
		if _, err := execute(ctx, directory, env, "git", "", "tag", "-s", "proof", "-m", "local-only signature proof"); err != nil {
			return err
		}
		_, err = execute(ctx, directory, env, "git", "", "verify-tag", "proof")

		return err
	})
}

// Reconcile compares remote annotated tag object IDs, not just peeled commits.
func Reconcile(ctx context.Context, root, trust string, batch Batch) (bool, error) {
	args := []string{"ls-remote", "--refs", "origin"}
	names := map[string]bool{}
	for _, entry := range batch.Proposals() {
		name := TagName(entry)
		names[name] = true
		args = append(args, "refs/tags/"+name)
	}
	raw, err := git(ctx, root, args...)
	if err != nil {
		return false, err
	}
	actual := map[string]string{}
	for line := range strings.SplitSeq(raw, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return false, fmt.Errorf("malformed remote ref response")
		}
		name, ok := strings.CutPrefix(fields[1], "refs/tags/")
		if !ok || !names[name] || actual[name] != "" {
			return false, fmt.Errorf("unexpected remote ref")
		}
		oid, err := git(ctx, root, "rev-parse", "refs/tags/"+name)
		if err != nil {
			return false, err
		}
		if oid != fields[0] {
			return false, fmt.Errorf("remote immutable tag object conflict")
		}
		if _, err := VerifyTag(ctx, root, trust, name, &batch); err != nil {
			return false, err
		}
		actual[name] = oid
	}

	return len(actual) == len(names), nil
}

// PushTags creates signed annotated refs atomically, reconciling lost responses.
// The App token exists only in the HTTP auth environment for the push itself.
func PushTags(ctx context.Context, root, trust string, batch Batch, secret, token string) error {
	if len(batch.Proposals()) == 0 {
		return fmt.Errorf("empty batch cannot authorize a Git push")
	}
	return withSigner(ctx, trust, secret, func(env []string) error {
		annotation, err := batch.Annotation()
		if err != nil {
			return err
		}
		args := []string{"push", "--atomic", "origin"}
		for _, entry := range batch.Proposals() {
			name := TagName(entry)
			existing, err := git(ctx, root, "tag", "--list", name)
			if err != nil {
				return err
			}
			if existing == "" {
				if _, err := execute(ctx, root, env, "git", annotation, "tag", "--cleanup=verbatim", "-s", name, batch.Plan.SHA, "-F", "-"); err != nil {
					return err
				}
			}
			if _, err := VerifyTag(ctx, root, trust, name, &batch); err != nil {
				return err
			}
			args = append(args, "refs/tags/"+name+":refs/tags/"+name)
		}
		// Replace, not duplicate, GIT_CONFIG_COUNT; signing is finished first.
		pushEnv := append(process.Environment(false), "GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
			"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)))
		_, pushErr := execute(ctx, root, pushEnv, "git", "", args...)
		complete, err := Reconcile(ctx, root, trust, batch)
		if err != nil {
			return err
		}
		if !complete {
			if pushErr != nil {
				return fmt.Errorf("atomic publication unconfirmed; retry same SHA, never force or delete tags: %w", pushErr)
			}
			return fmt.Errorf("remote signed batch incomplete; retry same SHA, never force or delete tags")
		}

		return nil
	})
}
