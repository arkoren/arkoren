package main

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/urfave/cli/v3"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/foundation/key"
)

// runKeyCheck reads one validated category/path selection and reports only the
// category. The command boundary rejects extra arguments before this file I/O.
func runKeyCheck(category, path string, env console.Environment) error {
	if err := checkKey(category, keyPath(env.Dir, path)); err != nil {
		return fmt.Errorf("check key: %w", err)
	}

	return env.Result(map[string]any{"category": category, "path": keyPath(env.Dir, path), "valid": true})
}

// runKeyPublic validates the typed category/format combination before file
// access. Only public material can reach stdout, regardless of input category.
func runKeyPublic(category, path, format string, env console.Environment) error {
	if category == "shared" {
		return console.UsageError("shared keys have no public key")
	}

	if format != "pem" && format != "vapid" {
		return console.UsageError("public format must be pem or vapid")
	}

	if format == "vapid" && category != "signing" {
		return console.UsageError("vapid public format requires signing")
	}

	encoded, err := publicKeyMaterial(category, keyPath(env.Dir, path), format)

	if err != nil {
		return fmt.Errorf("load public key: %w", err)
	}

	_, err = env.Stdout.Write(encoded)

	return err
}

// publicKeyMaterial loads an already validated category/format combination.
// The PEM and VAPID branches deliberately expose only public representations.
func publicKeyMaterial(category, path, format string) ([]byte, error) {
	if category == "exchange" {
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
// Its category has already been validated by the command boundary.
func generateKey(ctx context.Context, category, out string, env console.Environment) error {
	if out == "" {
		return console.UsageError("key generate requires --out FILE (or --out - for private stdout)")
	}

	encoded, err := generateKeyMaterial(category)

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

	if err := writePrivateKey(keyPath(env.Dir, out), encoded); err != nil {
		return err
	}

	return env.Result(map[string]any{"category": category, "created": true, "path": keyPath(env.Dir, out)})
}

// generateKeyMaterial provisions the selected category without inventing
// application-purpose aliases. The caller owns persistence and output policy.
func generateKeyMaterial(category string) ([]byte, error) {
	switch category {
	case "shared":
		secret, err := key.GenerateShared()

		if err != nil {
			return nil, err
		}

		return []byte(secret.Base64() + "\n"), nil
	case "signing":
		identity, err := key.GenerateSigning()

		return identity.PEM(), err
	default:
		identity, err := key.GenerateExchange()

		return identity.PEM(), err
	}
}

// checkKey validates a category without exporting material or contacting a
// host. The caller has already rejected unsupported category names.
func checkKey(category, path string) error {
	switch category {
	case "shared":
		_, err := key.LoadShared(path)

		return err
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
		Description: "Types: shared (32-byte base64), signing (ECDSA P-256), exchange (X25519). " +
			"Use independent keys for each purpose. Exchange keys alone do not provide peer authentication or encryption.",
		Commands: []*cli.Command{
			keyGenerateCLICommand(env),
			keyCheckCLICommand(env),
			keyPublicCLICommand(env),
		},
		Action: func(context.Context, *cli.Command) error {
			return console.UsageError("use 'arkoren key --help' for key commands")
		},
	}
}

// keyGenerateCLICommand binds the category and explicit private destination.
// An omitted/empty destination is rejected before any key material is generated.
func keyGenerateCLICommand(env console.Environment) *cli.Command {
	var category string

	return &cli.Command{
		Name:  "generate",
		Usage: "Generate an independent private key",
		Description: "Requires an explicit --out destination. Private files use mode 0600 and must not exist, " +
			"including symlinks. Only --out - explicitly sends private material to stdout.",
		Arguments: []cli.Argument{
			&cli.StringArg{Name: "TYPE", Required: true, Destination: &category},
		},
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "out", Usage: "Explicit private-key destination; - selects stdout"},
		},
		Action: func(ctx context.Context, command *cli.Command) error {
			if err := validateKeyCommand(ctx, command, category); err != nil {
				return err
			}

			return generateKey(ctx, category, command.String("out"), env)
		},
	}
}

// keyCheckCLICommand binds exactly one file without exposing private bytes.
// Argument and category validation precede the operation's file access.
func keyCheckCLICommand(env console.Environment) *cli.Command {
	var category, path string

	return &cli.Command{
		Name:  "check",
		Usage: "Validate local key material without displaying private bytes",
		Arguments: []cli.Argument{
			&cli.StringArg{Name: "TYPE", Required: true, Destination: &category},
			&cli.StringArg{Name: "FILE", Required: true, Destination: &path},
		},
		Action: func(ctx context.Context, command *cli.Command) error {
			if err := validateKeyCommand(ctx, command, category); err != nil {
				return err
			}

			return runKeyCheck(category, path, env)
		},
	}
}

// keyPublicCLICommand binds public export syntax, accepting format flags on
// either side of positionals. The typed operation validates format/category pairs.
func keyPublicCLICommand(env console.Environment) *cli.Command {
	var category, path string

	return &cli.Command{
		Name:  "public",
		Usage: "Export a signing or exchange public key as SPKI PEM or signing-only VAPID",
		Arguments: []cli.Argument{
			&cli.StringArg{Name: "TYPE", Required: true, Destination: &category},
			&cli.StringArg{Name: "FILE", Required: true, Destination: &path},
		},
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "format", Value: "pem", Usage: "Public format: pem or vapid (signing only)"},
		},
		Action: func(ctx context.Context, command *cli.Command) error {
			if err := validateKeyCommand(ctx, command, category); err != nil {
				return err
			}

			return runKeyPublic(category, path, command.String("format"), env)
		},
	}
}

// validateKeyCommand rejects cancellation, leftover positionals, and unknown
// categories before actions perform I/O. Declared arguments are already consumed.
func validateKeyCommand(ctx context.Context, command *cli.Command, category string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if command.Args().Present() {
		return console.UsageError("unexpected key arguments; use 'arkoren key --help'")
	}

	if !slices.Contains([]string{"shared", "signing", "exchange"}, category) {
		return console.UsageError("key type must be shared, signing, or exchange")
	}

	return nil
}
