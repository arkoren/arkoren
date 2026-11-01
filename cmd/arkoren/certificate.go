package main

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/urfave/cli/v3"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/trust"
	"arkoren.dev/foundation/certificate"
)

// certificatePaths names separate public certificate and private key files.
// Generation requires both; check permits an omitted key for bundle-only parsing.
type certificatePaths struct {
	// cert is resolved against environment.Dir, never the process directory.
	cert string

	// key is never interpreted as a stdout selector or implicit default.
	key string
}

// certificateServerArgs holds the issuer, SANs, and explicit output pair for
// offline server issuance. hosts contains DNS names and normalized IP literals.
type certificateServerArgs struct {
	// issuer identifies an existing CA certificate/private-key pair.
	issuer certificatePaths

	// hosts accumulates repeated --dns and --ip values in command-line order.
	hosts []string

	// output identifies new files; neither destination may already exist.
	output certificatePaths
}

// generateCertificateRoot validates its typed selection and preflights both
// destinations before generation. It never installs the newly created root.
func generateCertificateRoot(ctx context.Context, name string, paths certificatePaths, env console.Environment) error {
	if name == "" {
		return console.UsageError("certificate root generate requires --name NAME")
	}

	inventory, err := prepareCertificateFiles(ctx, paths, env.Dir)

	if err != nil {
		return err
	}

	defer closeCertificateFiles(inventory)

	identity, err := certificate.GenerateRoot(name)

	if err != nil {
		return fmt.Errorf("generate root certificate: %w", err)
	}

	return persistCertificate(ctx, inventory, identity, env)
}

// generateCertificateServer loads only the explicitly selected issuer. Both
// outputs are preflighted before reading the issuer or generating a private key.
// SANs arrive in command-line order, with IP literals already normalized.
func generateCertificateServer(ctx context.Context, options certificateServerArgs, env console.Environment) error {
	if options.issuer.cert == "" || options.issuer.key == "" {
		return console.UsageError("certificate generate requires --ca-cert FILE and --ca-key FILE")
	}

	if len(options.hosts) == 0 {
		return console.UsageError("certificate generate requires at least one --dns HOST or --ip IP")
	}

	inventory, err := prepareCertificateFiles(ctx, options.output, env.Dir)

	if err != nil {
		return err
	}

	defer closeCertificateFiles(inventory)

	issuer, err := certificate.LoadIdentity(keyPath(env.Dir, options.issuer.cert), keyPath(env.Dir, options.issuer.key))

	if err != nil {
		return fmt.Errorf("load certificate issuer: %w", err)
	}

	identity, err := certificate.GenerateServer(issuer, options.hosts)

	if err != nil {
		return fmt.Errorf("generate server certificate: %w", err)
	}

	return persistCertificate(ctx, inventory, identity, env)
}

// checkCertificate reports structural parsing and optional key matching only.
// It deliberately does not call x509.Verify or assert temporal validity or trust.
func checkCertificate(ctx context.Context, paths certificatePaths, env console.Environment) error {
	if paths.cert == "" {
		return console.UsageError("certificate check requires --cert FILE")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if paths.key == "" {
		if _, err := certificate.LoadBundle(keyPath(env.Dir, paths.cert)); err != nil {
			return fmt.Errorf("check certificate material: %w", err)
		}
	} else {
		if _, err := certificate.LoadIdentity(keyPath(env.Dir, paths.cert), keyPath(env.Dir, paths.key)); err != nil {
			return fmt.Errorf("check certificate/key pair: %w", err)
		}
	}

	report := map[string]any{
		"certificate": keyPath(env.Dir, paths.cert),
		"parsed":      true, "key_matched": paths.key != "",
		"expiry_checked": false, "trust_checked": false,
	}

	if paths.key != "" {
		report["key"] = keyPath(env.Dir, paths.key)
	}

	return env.Result(report)
}

// certificateCLICommand keeps certificate provisioning offline and declares
// each operation's allowed flags separately. Help is rendered by the root before
// this tree validates arguments, so missing inputs never obstruct generated help.
func certificateCLICommand(env console.Environment) *cli.Command {
	return &cli.Command{
		Name:  "certificate",
		Usage: "Provision and inspect TLS certificates offline",
		Description: "Generation never installs trust. Output files and symlinks are never overwritten; " +
			"parent directories must exist and must not be symlinks. Private files use mode 0600 on POSIX " +
			"and an owner-only protected ACL on Windows. Failed pair writes can leave files; inspect reported paths before retrying.",
		Commands: []*cli.Command{
			{
				Name:  "root",
				Usage: "Generate local roots or explicitly change machine-wide trust",
				Commands: []*cli.Command{
					certificateRootGenerateCLICommand(env),
					certificateTrustCLICommand(env, "trust"),
					certificateTrustCLICommand(env, "untrust"),
				},
				Action: func(context.Context, *cli.Command) error {
					return console.UsageError("certificate root requires generate, trust, or untrust")
				},
			},
			certificateGenerateCLICommand(env),
			certificateCheckCLICommand(env),
		},
		Action: func(context.Context, *cli.Command) error {
			return console.UsageError("use 'arkoren certificate --help' for certificate commands")
		},
	}
}

// certificatePathFlags binds the public/private pair shared by generation and
// structural checks. Only checks permit an omitted key; operations enforce that.
func certificatePathFlags(paths *certificatePaths) []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "cert", Destination: &paths.cert, Usage: "Certificate PEM file"},
		&cli.StringFlag{Name: "key", Destination: &paths.key, Usage: "Private key PEM file"},
	}
}

// certificateRootGenerateCLICommand binds an explicit root name and output
// pair. The operation preflights both files and never changes system trust.
func certificateRootGenerateCLICommand(env console.Environment) *cli.Command {
	paths := certificatePaths{}
	flags := append(certificatePathFlags(&paths), &cli.StringFlag{Name: "name", Usage: "Local root name"})

	return &cli.Command{
		Name: "generate", Flags: flags,
		Usage:        "Generate a local root certificate and private key without installing trust",
		ArgValidator: certificateNoArguments,
		Action: func(ctx context.Context, command *cli.Command) error {
			return generateCertificateRoot(ctx, command.String("name"), paths, env)
		},
	}
}

// certificateGenerateCLICommand captures each SAN as the parser encounters it.
// String validators run per occurrence, preserving DNS/IP interleaving without
// comma splitting or reconstructing argv. No captured state survives invocation.
func certificateGenerateCLICommand(env console.Environment) *cli.Command {
	options := certificateServerArgs{}
	flags := append(certificatePathFlags(&options.output),
		&cli.StringFlag{Name: "ca-cert", Destination: &options.issuer.cert, Usage: "Issuer certificate PEM file"},
		&cli.StringFlag{Name: "ca-key", Destination: &options.issuer.key, Usage: "Issuer private key PEM file"},
		&cli.StringFlag{Name: "dns", Usage: "DNS SAN (repeatable)", Validator: func(host string) error {
			options.hosts = append(options.hosts, host)

			return nil
		}},
		&cli.StringFlag{Name: "ip", Usage: "IP SAN (repeatable)", Validator: func(host string) error {
			address, err := netip.ParseAddr(host)

			if err != nil {
				return console.UsageError("invalid IP SAN")
			}

			options.hosts = append(options.hosts, address.String())

			return nil
		}},
	)

	return &cli.Command{
		Name: "generate", Flags: flags,
		Usage:        "Issue a server certificate with DNS or IP SANs; never installs trust",
		ArgValidator: certificateNoArguments,
		Action: func(ctx context.Context, _ *cli.Command) error {
			return generateCertificateServer(ctx, options, env)
		},
	}
}

// certificateCheckCLICommand limits structural checking to its own pair flags.
// It cannot accept issuer, generation, or trust approval options.
func certificateCheckCLICommand(env console.Environment) *cli.Command {
	paths := certificatePaths{}

	return &cli.Command{
		Name: "check", Flags: certificatePathFlags(&paths),
		Usage:        "Parse a certificate bundle and optionally check its matching private key",
		Description:  "Checks structural material only, not expiry, hostname, TLS purpose, revocation, or trust.",
		ArgValidator: certificateNoArguments,
		Action: func(ctx context.Context, _ *cli.Command) error {
			return checkCertificate(ctx, paths, env)
		},
	}
}

// certificateNoArguments rejects cancellation and positional input before any
// flags-only operation can read an issuer, preflight outputs, or generate keys.
func certificateNoArguments(ctx context.Context, command *cli.Command) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if command.Args().Present() {
		return console.UsageError("certificate %s accepts flags only", command.Name)
	}

	return nil
}

// certificateTrustCLICommand binds exactly one public root and optional bare
// approval. The root also calls validateCertificateApproval before parsing to
// reject value-bearing boolean spellings, which cli.BoolFlag otherwise accepts.
func certificateTrustCLICommand(env console.Environment, action string) *cli.Command {
	request := trust.Request{Action: action}

	return &cli.Command{
		Name:  action,
		Usage: "Explicitly " + action + " a public root in the machine-wide system store",
		Description: "Changes machine-wide trust. Requires default-no confirmation or bare --yes. " +
			"Never elevates privileges automatically. Supply a public root PEM file, not a private key.",
		Arguments: []cli.Argument{
			&cli.StringArg{Name: "FILE", Required: true, Destination: &request.Path},
		},
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "yes", OnlyOnce: true, Destination: &request.Yes, Usage: "Approve system-store changes"},
		},
		Action: func(ctx context.Context, command *cli.Command) error {
			if command.Args().Present() {
				return console.UsageError("certificate root %s requires exactly one public root PEM file", action)
			}

			if request.Path == "" || strings.HasPrefix(request.Path, "-") {
				return console.UsageError("certificate root %s requires a public root PEM file", action)
			}

			return trust.Run(ctx, request, env)
		},
	}
}

// validateCertificateApproval preserves the strict bare --yes approval grammar.
// The root passes tokens after "certificate", after reserved-global/help
// extraction. Only trust/untrust need this spelling guard; urfave still owns
// command routing, argument parsing, and duplicate detection through OnlyOnce.
func validateCertificateApproval(args []string) error {
	if len(args) < 2 || args[0] != "root" {
		return nil
	}

	if args[1] != "trust" && args[1] != "untrust" {
		return nil
	}

	for _, arg := range args {
		valueBearing := strings.HasPrefix(arg, "--yes=") || strings.HasPrefix(arg, "-yes=")

		if arg == "-yes" || valueBearing {
			return console.UsageError("certificate approval accepts only bare --yes")
		}
	}

	return nil
}
