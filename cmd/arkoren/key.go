package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/foundation/key"
)

// keyKinds lists the accepted KIND values of every key command, in the order
// the help and usage errors name them.
var keyKinds = []string{"shared", "signing", "exchange"}

// keyCheckResult is the JSON result of `arkoren key check`: a verdict in the
// local check envelope. Material that does not parse is a problem, not an
// error; only a file that cannot be read is an error.
type keyCheckResult struct {
	// Summary reads "Valid shared key" or "Invalid shared key".
	Summary string `json:"summary"`

	// OK is false when the file is not valid key material of Kind.
	OK bool `json:"ok"`

	// Kind is the checked key kind: shared, signing or exchange.
	Kind string `json:"kind"`

	// Path is the checked file, never its contents; "-" for a shared key ring
	// read from standard input.
	Path string `json:"path"`

	// Keys lists the fingerprints of a valid shared key ring, current first. It
	// is absent for other kinds and for invalid material. The text presentation
	// prints the first 12 digits of each fingerprint.
	Keys []keyEntry `json:"keys,omitzero"`

	// Problems says what is wrong with the material; an empty array when OK.
	Problems []console.Problem `json:"problems"`
}

// keyGenerateResult is the JSON result of `arkoren key generate --out FILE`.
type keyGenerateResult struct {
	// Summary reads "Generated shared key".
	Summary string `json:"summary"`

	// Kind is the generated key kind: shared, signing or exchange.
	Kind string `json:"kind"`

	// Changes holds the one created private key file.
	Changes []console.Change `json:"changes"`
}

// runKeyCheck reads one validated kind/path selection and reports only the
// kind and the verdict. The command boundary rejects extra arguments before
// this file I/O. Invalid material ends with exit status 1 after its report; a
// file that cannot be read is an error.
func runKeyCheck(kind, path string, env console.Environment) error {
	full := keyPath(env.Dir, path)

	if kind == "shared" && path == ringStdin {
		full = ringStdin
	}

	report := keyCheckResult{Summary: "Valid " + kind + " key", OK: true, Kind: kind, Path: full, Problems: []console.Problem{}}

	var err error

	if kind == "shared" {
		var ring key.Ring[key.Shared]

		ring, err = readRing(env, path)

		if err == nil {
			report.Summary = "Valid shared key ring"
			report.Keys = ringEntries(ring)
		}
	} else {
		err = checkKey(kind, full)
	}

	if err != nil && !errors.Is(err, key.ErrInvalid) {
		return inputFailure(fmt.Errorf("check key: %w", err))
	}

	if err != nil {
		report.Summary = "Invalid " + kind + " key"
		report.OK = false
		report.Problems = []console.Problem{{Field: "file", Message: "not valid " + kind + " key material", Path: full}}
	}

	if err := env.Result(report); err != nil {
		return err
	}

	if !report.OK {
		return &console.ExitError{Code: console.ExitFailure}
	}

	return nil
}

// inputFailure classifies a failure to read a file the user named: a missing or
// unreadable file is invalid input; anything else is returned unchanged and
// reports as an internal error.
func inputFailure(err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		return console.Fail(console.TypeInvalidInput, nil, "%w", err)
	}

	return err
}

// runKeyPublic validates the typed kind/format combination before file
// access. Only public material can reach stdout, regardless of input kind.
func runKeyPublic(kind, path, format string, env console.Environment) error {
	if kind == "shared" {
		return console.UsageError("KIND shared has no public key; accepted: signing, exchange")
	}

	if format != "pem" && format != "vapid" {
		return console.UsageError("invalid value for flag --format: accepted values are pem, vapid")
	}

	if format == "vapid" && kind != "signing" {
		return console.UsageError("--format vapid requires KIND signing")
	}

	encoded, err := publicKeyMaterial(kind, keyPath(env.Dir, path), format)

	if err != nil {
		return inputFailure(fmt.Errorf("load public key: %w", err))
	}

	_, err = env.Stdout.Write(encoded)

	return err
}

// publicKeyMaterial loads an already validated kind/format combination.
// The PEM and VAPID branches deliberately expose only public representations.
func publicKeyMaterial(kind, path, format string) ([]byte, error) {
	if kind == "exchange" {
		identity, err := key.LoadExchange(path)

		return identity.PublicPEM(), err
	}

	identity, err := key.LoadSigning(path)

	if err != nil {
		return nil, err
	}

	if format == "vapid" {
		return []byte(identity.VAPIDPublicKey() + "\n"), nil
	}

	return identity.PublicPEM(), nil
}

// generateKey requires explicit secret output and refuses replacing any file.
// Its kind has already been validated by the command boundary.
func generateKey(ctx context.Context, kind, out string, env console.Environment) error {
	if out == "" {
		return console.UsageError("missing flag --out: give a new file, or - for private stdout")
	}

	encoded, err := generateKeyMaterial(kind)

	if err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if out == "-" {
		_, err := env.Stdout.Write(encoded)

		return err
	}

	path := keyPath(env.Dir, out)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}

	if err := writePrivateKey(path, encoded); err != nil {
		return err
	}

	return env.Result(keyGenerateResult{
		Summary: "Generated " + kind + " key",
		Kind:    kind,
		Changes: []console.Change{{Action: console.ActionCreate, Path: path, Detail: "private key"}},
	})
}

// generateKeyMaterial provisions the selected kind without inventing
// application-purpose aliases. The caller owns persistence and output policy.
func generateKeyMaterial(kind string) ([]byte, error) {
	switch kind {
	case "shared":
		secret, err := key.GenerateShared()

		if err != nil {
			return nil, err
		}

		ring, err := key.NewRing(secret)

		if err != nil {
			return nil, err
		}

		return []byte(ring.Encode() + "\n"), nil
	case "signing":
		identity, err := key.GenerateSigning()

		return identity.PEM(), err
	default:
		identity, err := key.GenerateExchange()

		return identity.PEM(), err
	}
}

// checkKey validates a signing or exchange key file without exporting
// material or contacting a host. Shared keys are rings and go through
// [readRing]. The caller has already rejected unsupported kind names.
func checkKey(kind, path string) error {
	switch kind {
	case "signing":
		_, err := key.LoadSigning(path)

		return err
	default:
		_, err := key.LoadExchange(path)

		return err
	}
}

// keyPath resolves only against the explicit command working directory.
func keyPath(dir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}

	return filepath.Join(dir, path)
}

// keyCLICommand declares only offline key syntax. Fresh argument destinations
// keep invocations independent; actions pass typed selections to provisioning.
// Generated help remains available before material validation or file access.
func keyCLICommand(env console.Environment) *cli.Command {
	return &cli.Command{
		Name:  "key",
		Usage: "Provision and inspect shared, signing, or exchange keys offline",
		Description: "Kinds: shared (a ring of 32-byte base64 keys, current first), signing (ECDSA P-256), exchange (X25519). " +
			"Use independent keys for each purpose. Exchange keys alone do not provide peer authentication or encryption.",
		Commands: []*cli.Command{
			keyGenerateCLICommand(env),
			keyCheckCLICommand(env),
			keyPublicCLICommand(env),
			keyRotateCLICommand(env),
			keyPromoteCLICommand(env),
			keyRetireCLICommand(env),
		},
	}
}

// keyGenerateCLICommand binds the kind and explicit private destination.
// An omitted/empty destination is rejected before any key material is generated.
func keyGenerateCLICommand(env console.Environment) *cli.Command {
	var kind string

	return &cli.Command{
		Name:      "generate",
		Usage:     "Generate an independent private key",
		ArgsUsage: "KIND",
		Description: "Requires an explicit --out destination. Private files use mode 0600 and must not exist, " +
			"including symlinks. Only --out - explicitly sends private material to stdout. " +
			"A shared key is written as a one-key ring: one line of padded base64 that rotate extends.",
		Arguments: []cli.Argument{
			&cli.StringArg{Name: "KIND", Destination: &kind},
		},
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "out", Usage: "Explicit private-key destination; - selects stdout"},
		},
		Action: func(ctx context.Context, command *cli.Command) error {
			if err := validateKeyCommand(ctx, command, kind); err != nil {
				return err
			}

			return generateKey(ctx, kind, command.String("out"), env)
		},
	}
}

// keyCheckCLICommand binds exactly one file without exposing private bytes.
// Argument and kind validation precede the operation's file access.
func keyCheckCLICommand(env console.Environment) *cli.Command {
	var kind, path string

	return &cli.Command{
		Name:      "check",
		Usage:     "Validate local key material without displaying private bytes",
		ArgsUsage: "KIND FILE",
		Description: "Reads FILE and checks that it holds a valid key of KIND: shared, signing or exchange. " +
			"A shared FILE is a key ring, one key or several separated by commas, and may be - for standard input; " +
			"the result lists each key's fingerprint, current first. " +
			"Invalid material is reported as a problem with exit 1; private bytes are never printed.",
		Arguments: []cli.Argument{
			&cli.StringArg{Name: "KIND", Destination: &kind},
			&cli.StringArg{Name: "FILE", Destination: &path},
		},
		Action: func(ctx context.Context, command *cli.Command) error {
			if err := validateKeyCommand(ctx, command, kind, path); err != nil {
				return err
			}

			return runKeyCheck(kind, path, env)
		},
	}
}

// keyPublicCLICommand binds public export syntax, accepting format flags on
// either side of positionals. The typed operation validates format/kind pairs.
func keyPublicCLICommand(env console.Environment) *cli.Command {
	var kind, path string

	return &cli.Command{
		Name:      "public",
		Usage:     "Export a signing or exchange public key as SPKI PEM or signing-only VAPID",
		ArgsUsage: "KIND FILE",
		Description: "Reads the private key in FILE and prints its public half. KIND is signing or exchange; " +
			"--format vapid is available for signing keys only. Shared keys have no public half.",
		Arguments: []cli.Argument{
			&cli.StringArg{Name: "KIND", Destination: &kind},
			&cli.StringArg{Name: "FILE", Destination: &path},
		},
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "format", Value: "pem", Usage: "Public format: pem or vapid (signing only)"},
		},
		Action: func(ctx context.Context, command *cli.Command) error {
			if err := validateKeyCommand(ctx, command, kind, path); err != nil {
				return err
			}

			return runKeyPublic(kind, path, command.String("format"), env)
		},
	}
}

// validateKeyCommand rejects cancellation, missing or surplus positionals and
// unknown kinds before actions perform I/O. arguments are the values of the
// declared arguments in order: KIND, then FILE when the command takes one.
// Declared arguments are already consumed, so leftovers are surplus.
//
// A command that takes KIND and FILE and received a single value that is not a
// kind is told that KIND is missing, since the value is most likely the FILE
// of a command typed without its kind.
func validateKeyCommand(ctx context.Context, command *cli.Command, arguments ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	accepted := strings.Join(keyKinds, ", ")
	kind := arguments[0]

	switch {
	case kind == "":
		return console.UsageError("missing argument KIND: accepted values are %s", accepted)
	case !slices.Contains(keyKinds, kind) && len(arguments) == 2 && arguments[1] == "":
		return console.UsageError("missing argument KIND: accepted values are %s", accepted)
	case !slices.Contains(keyKinds, kind):
		return console.UsageError("invalid argument KIND %q: accepted values are %s", kind, accepted)
	}

	return requireArguments(command, []string{"KIND", "FILE"}[:len(arguments)], arguments)
}
