package release

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Summary renders the original safe Markdown artifact, including escaped notes.
func Summary(plan Plan) string {
	lines := []string{"# Release preview — DRY RUN", "", "Publication is not authorized. No tags, releases, pins or version commits are written.", "",
		fmt.Sprintf("Exact SHA: `%s`", plan.SHA), "Validation: " + plan.Validation, "",
		"| Module | Current | Proposed | Decision |", "| --- | --- | --- | --- |"}
	for _, entry := range plan.Modules {
		current, next, decision := "unreleased", "—", "preview only"
		if entry.CurrentVersion != nil {
			current = *entry.CurrentVersion
		}
		if entry.NextVersion != nil {
			next = *entry.NextVersion
		} else if entry.SkipReason != nil {
			decision = *entry.SkipReason
		}
		lines = append(lines, fmt.Sprintf("| %s | %s | %s | %s |", entry.ModulePath, current, next, decision))
	}
	for _, entry := range plan.Modules {
		if entry.NextVersion != nil {
			// Match Python html.escape exactly, including apostrophe spelling.
			escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;")
			lines = append(lines, "", "### "+entry.Directory, "", "<pre>"+escape.Replace(entry.Notes)+"</pre>")
		}
	}

	return strings.Join(lines, "\n") + "\n"
}

// Write confines output outside the source checkout, including symlink parents.
func Write(root, output string, plan Plan) error {
	root, err := filepath.EvalSymlinks(root)

	if err != nil {
		return err
	}
	output, err = filepath.Abs(output)

	if err != nil {
		return err
	}
	parent := output
	for {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			relative, err := filepath.Rel(root, resolved)
			if err != nil || relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
				return fmt.Errorf("write preview outputs outside the source checkout")
			}
			break
		}
		if !os.IsNotExist(err) || parent == filepath.Dir(parent) {
			return err
		}
		parent = filepath.Dir(parent)
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"plan.json", "summary.md"} {
		info, err := os.Lstat(filepath.Join(output, name))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("preview output must be an ordinary file")
		}
	}
	data, err := json.Marshal(plan)

	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, "plan.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(output, "summary.md"), []byte(Summary(plan)), 0o644)
}
