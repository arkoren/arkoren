package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/foundation/key"
)

// ringStdin is the FILE value that reads a ring from standard input and writes
// the new ring to standard output, so the commands compose with a secrets
// manager's own CLI in a pipe.
const ringStdin = "-"

// fingerprintPrefixDigits is the shortest fingerprint prefix that promote and
// retire accept: the 12 hexadecimal digits the text output prints. Shorter
// prefixes are refused so a typo cannot select a key by accident.
const fingerprintPrefixDigits = 12

// keyEntry describes one key of a ring without exposing it.
type keyEntry struct {
	// Fingerprint is the full lowercase hexadecimal SHA-256 fingerprint of the
	// key. The text presentation prints its first 12 digits.
	Fingerprint string `json:"fingerprint"`

	// Current is true for the one key that signs and encrypts new data; every
	// other key of the ring is only accepted for verification and decryption.
	Current bool `json:"current,omitzero"`
}

// keyRingResult is the JSON result of rotate, promote and retire: a local
// action in the result envelope. Keys lists the ring after the change, current
// first, so the caller can read the fingerprints the next command needs.
type keyRingResult struct {
	// Summary says what happened, such as "Rotated shared key ring".
	Summary string `json:"summary"`

	// Kind is always shared: rings exist only for shared keys.
	Kind string `json:"kind"`

	// Keys holds the fingerprints of the resulting ring, current first.
	Keys []keyEntry `json:"keys"`

	// Changes holds the one edited file; it is absent when the ring went to
	// standard output.
	Changes []console.Change `json:"changes,omitzero"`

	// NextSteps says how to roll the change out.
	NextSteps []console.Step `json:"next_steps,omitzero"`
}

// ringEntries lists the keys of ring, current first, by fingerprint.
func ringEntries(ring key.Ring[key.Shared]) []keyEntry {
	entries := []keyEntry{{Fingerprint: ring.Current().Fingerprint(), Current: true}}

	for _, previous := range ring.Previous() {
		entries = append(entries, keyEntry{Fingerprint: previous.Fingerprint()})
	}

	return entries
}

// readRing reads and parses the ring in source: a path resolved against the
// working directory, or "-" for standard input. A file that cannot be read is
// an input failure; text that is not a valid ring wraps [key.ErrInvalid] and
// never repeats the text. One trailing newline is accepted, so the output of
// "arkoren key generate shared" and openssl rand -base64 32 are one-key rings.
func readRing(env console.Environment, source string) (key.Ring[key.Shared], error) {
	var (
		data []byte
		err  error
	)

	if source == ringStdin {
		data, err = io.ReadAll(env.Stdin)
	} else {
		data, err = os.ReadFile(keyPath(env.Dir, source))
	}

	if err != nil {
		return key.Ring[key.Shared]{}, inputFailure(fmt.Errorf("read key ring: %w", err))
	}

	return key.ParseRing[key.Shared](string(data))
}

// invalidRing reports text that is not a valid ring as invalid input without
// echoing it.
func invalidRing(source string) error {
	if source == ringStdin {
		source = "standard input"
	}

	return console.Fail(console.TypeInvalidInput, nil, "%s does not hold a valid shared key ring", source)
}

// selectKey returns the index in the ring order (0 is current) of the one key
// whose fingerprint starts with prefix. A prefix shorter than
// [fingerprintPrefixDigits], one that matches no key, and one that matches more
// than one are invalid input; the message never contains a full fingerprint
// the caller did not already type.
func selectKey(ring key.Ring[key.Shared], prefix string) (int, error) {
	prefix = strings.ToLower(prefix)

	if len(prefix) < fingerprintPrefixDigits {
		return 0, console.Fail(console.TypeInvalidInput, []console.Step{{Text: "Give at least the 12 digits printed by key check"}},
			"fingerprint prefix needs at least %d digits", fingerprintPrefixDigits)
	}

	match := -1

	for index, entry := range ringEntries(ring) {
		if !strings.HasPrefix(entry.Fingerprint, prefix) {
			continue
		}

		if match >= 0 {
			return 0, console.Fail(console.TypeInvalidInput, []console.Step{{Text: "Give more digits of the fingerprint"}},
				"fingerprint prefix matches more than one key")
		}

		match = index
	}

	if match < 0 {
		return 0, console.Fail(console.TypeInvalidInput, []console.Step{{Text: "List the fingerprints of the ring", Command: "arkoren key check shared FILE"}},
			"no key of the ring matches that fingerprint prefix")
	}

	return match, nil
}

// ringKeys returns the keys of ring in order, current first.
func ringKeys(ring key.Ring[key.Shared]) []key.Shared {
	return append([]key.Shared{ring.Current()}, ring.Previous()...)
}

// rebuildRing builds a ring from keys in order, current first.
func rebuildRing(keys []key.Shared) (key.Ring[key.Shared], error) {
	return key.NewRing(keys[0], keys[1:]...)
}

// rotateRing adds a freshly generated key to ring. By default it becomes the
// current key and the old ones follow as previous. With stage it is appended
// after them: it only decrypts and verifies until promoted, so every instance
// can receive it before any instance starts using it.
func rotateRing(ring key.Ring[key.Shared], stage bool) (key.Ring[key.Shared], error) {
	fresh, err := key.GenerateShared()

	if err != nil {
		return key.Ring[key.Shared]{}, err
	}

	keys := ringKeys(ring)

	if stage {
		return rebuildRing(append(keys, fresh))
	}

	return rebuildRing(append([]key.Shared{fresh}, keys...))
}

// promoteRing moves the key selected by prefix to the front of ring, making it
// current. The previous order of the other keys is kept.
func promoteRing(ring key.Ring[key.Shared], prefix string) (key.Ring[key.Shared], error) {
	index, err := selectKey(ring, prefix)

	if err != nil {
		return key.Ring[key.Shared]{}, err
	}

	keys := ringKeys(ring)
	promoted := keys[index]
	keys = append(keys[:index], keys[index+1:]...)

	return rebuildRing(append([]key.Shared{promoted}, keys...))
}

// retireRing removes the key selected by prefix from ring. The current key,
// which is also the last key of a one-key ring, cannot be retired: promote
// another key first.
func retireRing(ring key.Ring[key.Shared], prefix string) (key.Ring[key.Shared], error) {
	index, err := selectKey(ring, prefix)

	if err != nil {
		return key.Ring[key.Shared]{}, err
	}

	if index == 0 {
		return key.Ring[key.Shared]{}, console.Fail(console.TypeInvalidInput,
			[]console.Step{{Text: "Promote another key first, then retire this one", Command: "arkoren key promote FINGERPRINT FILE"}},
			"the current key cannot be retired")
	}

	keys := ringKeys(ring)

	return rebuildRing(append(keys[:index], keys[index+1:]...))
}

// rewriteRing replaces the ring text of source with ring and reports the
// result. For a file the rewrite is atomic ([rewriteKeyFile]) and the result,
// carrying the edit, goes to standard output. For "-" only the raw ring goes to
// standard output, so the command can feed a pipe, and the result goes to
// standard error as a diagnostic. Key material is written only to the file or
// to standard output; reports hold fingerprints.
func rewriteRing(env console.Environment, source string, ring key.Ring[key.Shared], report keyRingResult) error {
	encoded := []byte(ring.Encode() + "\n")
	report.Kind = "shared"
	report.Keys = ringEntries(ring)

	if source == ringStdin {
		if _, err := env.Stdout.Write(encoded); err != nil {
			return err
		}

		return console.WriteJSON(env.Stderr, report, env.DiagnosticStyle())
	}

	path := keyPath(env.Dir, source)

	if err := rewriteKeyFile(path, encoded); err != nil {
		return err
	}

	report.Changes = []console.Change{{Action: console.ActionEdit, Path: path, Detail: "key ring"}}

	return env.Result(report)
}

// rewriteKeyFile atomically replaces the file at path with content: it writes a
// temporary file in the same directory with mode 0600, syncs it and renames it
// over the target, so readers see either the old ring or the new one and an
// interrupted write leaves the old file intact. A symbolic link is followed and
// its target replaced. The mode is 0600 whatever the old file had, since the
// file holds secrets. On failure the temporary file is removed.
func rewriteKeyFile(path string, content []byte) error {
	resolved, err := filepath.EvalSymlinks(path)

	if err != nil {
		return inputFailure(fmt.Errorf("resolve key file: %w", err))
	}

	temporary, err := os.CreateTemp(filepath.Dir(resolved), ".key-*.tmp")

	if err != nil {
		return inputFailure(fmt.Errorf("create temporary key file: %w", err))
	}

	err = temporary.Chmod(0o600)

	if err == nil {
		_, err = temporary.Write(content)
	}

	if err == nil {
		err = temporary.Sync()
	}

	err = errors.Join(err, temporary.Close())

	if err == nil {
		err = os.Rename(temporary.Name(), resolved)
	}

	if err != nil {
		// The temporary file holds key material; a failed removal is reported
		// with the original failure instead of hidden.
		return errors.Join(fmt.Errorf("rewrite key file: %w", err), removeIfExists(temporary.Name()))
	}

	return nil
}

// removeIfExists removes path and ignores its absence.
func removeIfExists(path string) error {
	err := os.Remove(path)

	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	return err
}

// keyRotateCLICommand binds `key rotate [--stage] FILE`: a new key joins the
// ring, as current by default or, with --stage, as the last previous key.
func keyRotateCLICommand(env console.Environment) *cli.Command {
	var source string

	return &cli.Command{
		Name:      "rotate",
		Usage:     "Add a new shared key to a ring",
		ArgsUsage: "FILE",
		Description: "Generates a shared key and adds it to the ring in FILE, or in standard input when FILE is -. " +
			"By default the new key becomes current and the old keys stay as previous. With --stage it is appended " +
			"as previous instead, so every instance can receive it before promote makes it current. " +
			"A file is rewritten atomically with mode 0600; with - the new ring goes to standard output.",
		Arguments: []cli.Argument{
			&cli.StringArg{Name: "FILE", Destination: &source},
		},
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "stage", Usage: "Append the new key as previous instead of making it current"},
		},
		Action: func(ctx context.Context, command *cli.Command) error {
			if err := validateRingCommand(ctx, command, source); err != nil {
				return err
			}

			return runKeyRotate(env, source, command.Bool("stage"))
		},
	}
}

// runKeyRotate reads the ring of source, adds a new key and rewrites it.
func runKeyRotate(env console.Environment, source string, stage bool) error {
	ring, err := readRing(env, source)

	if errors.Is(err, key.ErrInvalid) {
		return invalidRing(source)
	}

	if err != nil {
		return err
	}

	rotated, err := rotateRing(ring, stage)

	if err != nil {
		return err
	}

	file := ringCommandTarget(source)
	report := keyRingResult{
		Summary: "Rotated shared key ring",
		NextSteps: []console.Step{
			{Text: "Deploy the ring to every instance, then retire the old keys once nothing depends on them", Command: "arkoren key retire FINGERPRINT " + file},
		},
	}

	if stage {
		staged := ringEntries(rotated)[rotated.Len()-1].Fingerprint[:fingerprintPrefixDigits]
		report.Summary = "Staged a new shared key"
		report.NextSteps = []console.Step{
			{Text: "Deploy the ring to every instance, then make the new key current", Command: "arkoren key promote " + staged + " " + file},
		}
	}

	return rewriteRing(env, source, rotated, report)
}

// ringCommandTarget spells source for a next-step command.
func ringCommandTarget(source string) string {
	if source == ringStdin {
		return ringStdin
	}

	return console.Quote(source)
}

// keyPromoteCLICommand binds `key promote FINGERPRINT FILE`.
func keyPromoteCLICommand(env console.Environment) *cli.Command {
	return keyRingEditCommand(env, "promote", "Make a key of a ring current",
		"Moves the key whose fingerprint starts with FINGERPRINT to the front of the ring, making it current. "+
			"FINGERPRINT is at least the 12 digits `key check` prints and must match exactly one key.",
		func(ring key.Ring[key.Shared], prefix string) (key.Ring[key.Shared], keyRingResult, error) {
			promoted, err := promoteRing(ring, prefix)

			return promoted, keyRingResult{Summary: "Promoted a shared key to current", NextSteps: []console.Step{{Text: "Deploy the ring to every instance before retiring any key"}}}, err
		})
}

// keyRetireCLICommand binds `key retire FINGERPRINT FILE`.
func keyRetireCLICommand(env console.Environment) *cli.Command {
	return keyRingEditCommand(env, "retire", "Remove a previous key from a ring",
		"Removes the key whose fingerprint starts with FINGERPRINT from the ring. FINGERPRINT is at least the 12 digits "+
			"`key check` prints and must match exactly one key. The current key, which includes the only key of a "+
			"one-key ring, is refused: promote another key first. Data protected only by a retired key can no longer be read.",
		func(ring key.Ring[key.Shared], prefix string) (key.Ring[key.Shared], keyRingResult, error) {
			retired, err := retireRing(ring, prefix)

			return retired, keyRingResult{Summary: "Retired a shared key", NextSteps: []console.Step{{Text: "Deploy the ring to every instance"}}}, err
		})
}

// keyRingEditCommand declares a FINGERPRINT FILE command that reads the ring of
// FILE, applies edit with the fingerprint and rewrites the ring. edit returns
// the new ring and the report to complete.
func keyRingEditCommand(
	env console.Environment,
	name, usage, description string,
	edit func(key.Ring[key.Shared], string) (key.Ring[key.Shared], keyRingResult, error),
) *cli.Command {
	var fingerprint, source string

	return &cli.Command{
		Name:        name,
		Usage:       usage,
		ArgsUsage:   "FINGERPRINT FILE",
		Description: description + " A file is rewritten atomically with mode 0600; with FILE - the ring is read from standard input and the new ring goes to standard output.",
		Arguments: []cli.Argument{
			&cli.StringArg{Name: "FINGERPRINT", Destination: &fingerprint},
			&cli.StringArg{Name: "FILE", Destination: &source},
		},
		Action: func(ctx context.Context, command *cli.Command) error {
			if err := validateRingCommand(ctx, command, fingerprint, source); err != nil {
				return err
			}

			ring, err := readRing(env, source)

			if errors.Is(err, key.ErrInvalid) {
				return invalidRing(source)
			}

			if err != nil {
				return err
			}

			edited, report, err := edit(ring, fingerprint)

			if err != nil {
				return err
			}

			return rewriteRing(env, source, edited, report)
		},
	}
}

// validateRingCommand rejects cancellation, missing and surplus positionals of
// the ring commands before any I/O. arguments are the declared argument values
// in order, FINGERPRINT before FILE where both exist.
func validateRingCommand(ctx context.Context, command *cli.Command, arguments ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	names := []string{"FINGERPRINT", "FILE"}[2-len(arguments):]

	return requireArguments(command, names, arguments)
}
