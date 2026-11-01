package scaffold

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"go/format"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"arkoren.dev/cmd/arkoren/internal/files"
	"arkoren.dev/cmd/arkoren/internal/skill"
)

// Render discovers templates in lexical order and mirrors their layout in
// memory, removing only the .tmpl suffix from each output-relative path.
// Every .go file must pass gofmt before any writes can start.
// templates contains templates/; source contains skills/arkoren/.
func Render(ctx context.Context, options Options, templates fs.FS, source fs.FS) ([]files.File, error) {
	data := map[string]string{
		"Module":    options.Module,
		"Name":      filepath.Base(options.Folder),
		"SkillsDir": cmp.Or(options.SkillsDir, skill.DefaultSkillsDir),
	}

	if !options.NoAgentFiles {
		data["AgentFiles"] = "yes"
	}

	inventory := make([]files.File, 0)

	err := fs.WalkDir(templates, "templates", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if err := ctx.Err(); err != nil {
			return err
		}

		if entry.IsDir() || !strings.HasSuffix(path, ".tmpl") {
			return nil
		}

		relative := strings.TrimSuffix(strings.TrimPrefix(path, "templates/"), ".tmpl")

		if relative == "AGENTS.md" && options.NoAgentFiles {
			return nil
		}

		content, err := RenderFile(relative, data, templates)

		if err != nil {
			return fmt.Errorf("render %s: %w", relative, err)
		}

		inventory = append(inventory, files.File{
			Path:    filepath.FromSlash(relative),
			Content: content,
		})

		return nil
	})

	if err != nil {
		return nil, err
	}

	if !options.NoAgentFiles {
		agents, err := RenderSkill(ctx, data["SkillsDir"], source)

		if err != nil {
			return nil, err
		}

		inventory = append(inventory, agents...)
	}

	slices.SortFunc(inventory, func(left, right files.File) int {
		// The ownership record marks a complete installation, even in a scaffold.
		if left.Path == right.Path {
			return 0
		}

		if filepath.Base(left.Path) == skill.SkillManifestName {
			return 1
		}

		if filepath.Base(right.Path) == skill.SkillManifestName {
			return -1
		}

		return strings.Compare(left.Path, right.Path)
	})

	return inventory, nil
}

// RenderSkill prefixes the same installation bytes used by skills install.
func RenderSkill(ctx context.Context, dir string, source fs.FS) ([]files.File, error) {
	bundle, err := skill.LoadBundle(ctx, source)

	if err != nil {
		return nil, err
	}

	inventory, err := skill.Files(bundle)

	if err != nil {
		return nil, err
	}

	for index := range inventory {
		inventory[index].Path = filepath.Join(filepath.FromSlash(dir), "arkoren", filepath.FromSlash(inventory[index].Path))
	}

	return inventory, nil
}

// RenderFile selects a template by its complete relative path without the
// .tmpl suffix.
func RenderFile(path string, data map[string]string, templates fs.FS) ([]byte, error) {
	definition, err := template.ParseFS(templates, "templates/"+path+".tmpl")

	if err != nil {
		return nil, err
	}

	var buffer bytes.Buffer

	if err := definition.Execute(&buffer, data); err != nil {
		return nil, err
	}

	if strings.HasSuffix(path, ".go") {
		return format.Source(buffer.Bytes())
	}

	return buffer.Bytes(), nil
}
