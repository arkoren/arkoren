package skill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/files"
)

// DefaultSkillsDir is the project-local skills parent, not the skill itself.
const DefaultSkillsDir = ".agents/skills"

// SkillManifestName is reserved for CLI ownership and content hashes.
const SkillManifestName = ".arkoren-skill.json"

// Manifest records a content-addressed consumer skill installation.
type Manifest struct {
	// Schema identifies the manifest format supported by this CLI.
	Schema int `json:"schema"`
	// Name distinguishes this skill from unrelated installer state.
	Name string `json:"name"`
	// Revision is the SHA-256 of the sorted file paths and content hashes.
	Revision string `json:"revision"`
	// Files maps slash-separated paths to their installed SHA-256 hashes.
	Files map[string]string `json:"files"`
}

// Bundle holds validated source bytes and the corresponding manifest.
type Bundle struct {
	// Files contains every consumer skill file in lexical path order.
	Files []files.File
	// Manifest describes only bundle files, never itself or user-owned files.
	Manifest Manifest
}

// LoadBundle removes the embedded source prefix before validating files.
// Installed paths and content-addressed manifests remain relative to the skill,
// independent of where the authored tree lives inside this module.
// The supplied filesystem contains the authored tree at skills/arkoren/.
func LoadBundle(ctx context.Context, source fs.FS) (Bundle, error) {
	source, err := fs.Sub(source, "skills/arkoren")

	if err != nil {
		return Bundle{}, fmt.Errorf("read bundled skill: %w", err)
	}

	return LoadSource(ctx, source)
}

// LoadSource reads a skill-relative filesystem into an immutable bundle.
// Directories are traversed but not installed; every other entry must be a
// regular file with a safe installation path. Cancellation and filesystem
// failures return no partial bundle. Sorting preserves the manifest format
// independently of filesystem enumeration order.
func LoadSource(ctx context.Context, source fs.FS) (Bundle, error) {
	inventory := make([]files.File, 0)
	hashes := make(map[string]string)

	err := fs.WalkDir(source, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if err := ctx.Err(); err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		if !validSkillPath(path) || !entry.Type().IsRegular() {
			return fmt.Errorf("invalid bundled skill path: %s", path)
		}

		content, err := fs.ReadFile(source, path)

		if err != nil {
			return fmt.Errorf("read bundled skill %s: %w", path, err)
		}

		inventory = append(inventory, files.File{Path: path, Content: content})
		hashes[path] = Hash(content)

		return nil
	})

	if err != nil {
		return Bundle{}, err
	}

	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}

	if _, ok := hashes["SKILL.md"]; !ok {
		return Bundle{}, fmt.Errorf("bundled skill lacks SKILL.md")
	}

	slices.SortFunc(inventory, func(left, right files.File) int {
		return strings.Compare(left.Path, right.Path)
	})

	return Bundle{
		Files: inventory,
		Manifest: Manifest{
			Schema: 1, Name: "arkoren", Revision: Revision(hashes), Files: hashes,
		},
	}, nil
}

// validSkillPath excludes escapes, platform aliases, and the ownership record.
func validSkillPath(path string) bool {
	return fs.ValidPath(path) && path != "." && path != SkillManifestName &&
		!strings.ContainsAny(path, "\\:\r\n\x00") && filepath.IsLocal(filepath.FromSlash(path))
}

// Hash returns a portable lowercase content checksum.
func Hash(content []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(content))
}

// Revision hashes paths as well as content, independently of source file
// metadata. Lexical path order keeps revisions stable across installations.
func Revision(inventory map[string]string) string {
	var content strings.Builder

	for _, path := range slices.Sorted(maps.Keys(inventory)) {
		fmt.Fprintf(&content, "%s\x00%s\n", path, inventory[path])
	}

	return Hash([]byte(content.String()))
}

// ValidateSkillManifest rejects corrupt state before its paths can reach disk.
func ValidateSkillManifest(manifest Manifest) error {
	if manifest.Schema != 1 || manifest.Name != "arkoren" || len(manifest.Files) == 0 {
		return console.UsageError("invalid or unsupported Arkoren skill manifest")
	}

	if _, ok := manifest.Files["SKILL.md"]; !ok {
		return console.UsageError("skill manifest lacks SKILL.md")
	}

	for path, hash := range manifest.Files {
		decoded, err := hex.DecodeString(hash)

		if !validSkillPath(path) || err != nil || len(decoded) != sha256.Size || strings.ToLower(hash) != hash {
			return console.UsageError("invalid skill manifest entry: %s", path)
		}
	}

	if manifest.Revision != Revision(manifest.Files) {
		return console.UsageError("skill manifest revision does not match its file hashes")
	}

	return nil
}

// Files adds the ownership record to the same bytes used by installation.
func Files(bundle Bundle) ([]files.File, error) {
	content, err := json.Marshal(bundle.Manifest, json.Deterministic(true))

	if err != nil {
		return nil, err
	}

	inventory := slices.Clone(bundle.Files)
	inventory = append(inventory, files.File{Path: SkillManifestName, Content: append(content, '\n')})

	return inventory, nil
}
