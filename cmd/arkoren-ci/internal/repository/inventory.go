package repository

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"arkoren.dev/cmd/arkoren-ci/internal/process"
)

// Module describes a validated manifest and its policy classification.
type Module struct {
	// Directory is the canonical checkout-relative root.
	Directory string `json:"directory"`

	// ModulePath is the manifest's canonical Go namespace.
	ModulePath string `json:"module_path"`

	// Classification is public or development; only public modules may release.
	Classification string `json:"classification"`
}

// Inventory reports all CI modules while retaining the release boundary.
type Inventory struct {
	// Modules contains sorted, validated module records.
	Modules []Module `json:"modules"`

	// Integration contains roots with an ordinary compose.yaml marker.
	Integration []string `json:"integration"`
}

// view provides either working filesystem metadata or an exact committed tree.
type view struct {
	// modes records bounded directory entries and their real file modes.
	modes map[string]fs.FileMode

	// read reads bytes from the same source as modes.
	read func(string) ([]byte, error)
}

// manifest matches one complete module declaration without running Go tooling.
var manifest = regexp.MustCompile(`(?m)^module\s+(\S+)\s*$`)

// commit matches only full object IDs; refs cannot drift during inspection.
var commit = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Inspect validates the working tree when revision is empty, or an exact commit.
// Working discovery includes ignored modules. Committed discovery excludes all
// untracked files and reads policy and manifests at the same immutable revision.
func Inspect(ctx context.Context, root, revision string) (Inventory, error) {
	source, err := inspectSource(ctx, root, revision)

	if err != nil {
		return Inventory{}, err
	}
	if mode, ok := source.modes[".github/release-modules.json"]; !ok || !mode.IsRegular() ||
		source.modes[".github"] != fs.ModeDir || (revision != "" && mode.Perm() != 0) {
		return Inventory{}, fmt.Errorf("expected real module policy")
	}
	data, err := source.read(".github/release-modules.json")

	if err != nil {
		return Inventory{}, fmt.Errorf("read module policy: %w", err)
	}
	policy, err := ParsePolicy(data)

	if err != nil {
		return Inventory{}, err
	}

	return source.inventory(ctx, policy, revision != "")
}

// inspectSource selects one source without reading the other source's files.
func inspectSource(ctx context.Context, root, revision string) (view, error) {
	if revision != "" {
		return committed(ctx, root, revision)
	}

	return working(ctx, root)
}

// working enumerates only canonical family children and core's nested manifests.
func working(ctx context.Context, root string) (view, error) {
	source := view{
		modes: make(map[string]fs.FileMode),
		read: func(name string) ([]byte, error) {
			return os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		},
	}
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, name)

		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		parts := strings.Split(relative, "/")
		family := parts[0]
		if relative == "." {
			return nil
		}
		if !slices.Contains([]string{"core", "cmd", "driver", "foundation", "ark", ".github"}, family) {
			if entry.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}
		source.modes[relative] = entry.Type()
		if entry.IsDir() && len(parts) >= 3 && family != "core" {
			return filepath.SkipDir
		}

		return nil
	})

	return source, err
}

// committed reads modes and bytes through Git, without consulting local files.
func committed(ctx context.Context, root, revision string) (view, error) {
	if !commit.MatchString(revision) {
		return view{}, fmt.Errorf("revision must be an exact full commit SHA")
	}
	kind, err := git(ctx, root, "cat-file", "-t", revision)

	if err != nil {
		return view{}, err
	}
	if strings.TrimSpace(string(kind)) != "commit" {
		return view{}, fmt.Errorf("revision must identify a commit")
	}
	data, err := git(ctx, root, "ls-tree", "-rz", "--full-tree", revision)

	if err != nil {
		return view{}, err
	}
	source := view{
		modes: make(map[string]fs.FileMode),
		read: func(name string) ([]byte, error) {
			return git(ctx, root, "show", revision+":"+name)
		},
	}
	for record := range strings.SplitSeq(string(data), "\x00") {
		if record == "" {
			continue
		}
		header, name, ok := strings.Cut(record, "\t")
		if !ok {
			return view{}, fmt.Errorf("invalid Git tree record")
		}
		mode := fs.FileMode(0)
		if strings.HasPrefix(header, "100755 ") {
			mode = 0o111
		}
		if !strings.HasPrefix(header, "100644 ") && !strings.HasPrefix(header, "100755 ") {
			mode = fs.ModeSymlink
		}
		source.modes[name] = mode
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			source.modes[parent] = fs.ModeDir
		}
	}

	return source, nil
}

// git runs a read-only operation against an explicitly selected repository.
func git(ctx context.Context, root string, args ...string) ([]byte, error) {
	data, err := process.Run(ctx, root, "git", "", false, args...)

	if err != nil {
		return nil, fmt.Errorf("read Git source: %w", err)
	}

	return data, nil
}

// inventory verifies structural roots before returning any partial inventory.
func (source view) inventory(ctx context.Context, policy Policy, exact bool) (Inventory, error) {
	for _, family := range []string{"core", "cmd", "driver", "foundation"} {
		if source.modes[family] != fs.ModeDir {
			return Inventory{}, fmt.Errorf("expected real directory: %s", family)
		}
	}
	if mode, ok := source.modes["ark"]; ok && mode != fs.ModeDir {
		return Inventory{}, fmt.Errorf("expected real directory: ark")
	}
	if mode, ok := source.modes[".github/release-modules.json"]; !ok || !mode.IsRegular() {
		return Inventory{}, fmt.Errorf("expected real module policy")
	}
	roots := []string{"core"}
	for name, mode := range source.modes {
		parts := strings.Split(name, "/")
		if strings.HasPrefix(name, "core/") && strings.HasSuffix(name, "/go.mod") && name != "core/go.mod" {
			return Inventory{}, fmt.Errorf("nested core module: %s", name)
		}
		if len(parts) == 2 && slices.Contains([]string{"cmd", "driver", "foundation", "ark"}, parts[0]) &&
			(mode.IsDir() || mode&fs.ModeSymlink != 0) {
			roots = append(roots, name)
		}
	}
	slices.Sort(roots)
	result := Inventory{Integration: []string{}}
	public := []string{}
	development := []string{}
	for _, directory := range roots {
		if err := ctx.Err(); err != nil {
			return Inventory{}, err
		}
		classification := "public"
		if slices.Contains(policy.Development, directory) {
			classification = "development"
			development = append(development, directory)
		} else if !canonical.MatchString(directory) {
			return Inventory{}, fmt.Errorf("unreviewed module root: %s", directory)
		} else {
			public = append(public, directory)
		}
		module, err := source.module(directory, classification)

		if err != nil {
			return Inventory{}, err
		}
		if exact && source.modes[directory+"/go.mod"].Perm() != 0 {
			return Inventory{}, fmt.Errorf("invalid manifest mode: %s", directory)
		}
		result.Modules = append(result.Modules, module)
		if mode, ok := source.modes[directory+"/compose.yaml"]; ok {
			if !mode.IsRegular() {
				return Inventory{}, fmt.Errorf("invalid Compose file: %s", directory)
			}
			result.Integration = append(result.Integration, directory)
		}
	}
	want := slices.Clone(policy.Public)
	slices.Sort(want)
	for _, directory := range want {
		if !slices.Contains(public, directory) {
			return Inventory{}, fmt.Errorf("missing approved public module: %s", directory)
		}
	}
	if exact && !slices.Equal(public, want) {
		return Inventory{}, fmt.Errorf("committed public inventory differs from module policy")
	}
	want = slices.Clone(policy.Development)
	slices.Sort(want)
	if !slices.Equal(development, want) {
		return Inventory{}, fmt.Errorf("development inventory differs from module policy")
	}
	if !slices.ContainsFunc(public, func(name string) bool { return strings.HasPrefix(name, "driver/") }) ||
		!slices.ContainsFunc(public, func(name string) bool { return strings.HasPrefix(name, "foundation/") }) {
		return Inventory{}, fmt.Errorf("empty driver or foundation family")
	}

	return result, nil
}

// module rejects linked roots and manifests before reading the declaration.
func (source view) module(directory, classification string) (Module, error) {
	mode, ok := source.modes[directory+"/go.mod"]
	if source.modes[directory] != fs.ModeDir || !ok || !mode.IsRegular() {
		return Module{}, fmt.Errorf("expected real module and manifest: %s", directory)
	}
	data, err := source.read(directory + "/go.mod")

	if err != nil {
		return Module{}, err
	}
	declarations := manifest.FindAllStringSubmatch(string(data), -1)
	if len(declarations) != 1 || declarations[0][1] != "arkoren.dev/"+directory {
		return Module{}, fmt.Errorf("unexpected module path: %s", directory)
	}

	return Module{Directory: directory, ModulePath: declarations[0][1], Classification: classification}, nil
}
