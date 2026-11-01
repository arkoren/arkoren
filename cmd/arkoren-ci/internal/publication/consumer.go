package publication

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"arkoren.dev/cmd/arkoren-ci/internal/evidence"
	"arkoren.dev/cmd/arkoren-ci/internal/process"
)

// PublicVerify uses only public proxy/checksum services after signed refs exist.
// No App token or private signing key reaches Go or downloaded module builds.
func PublicVerify(ctx context.Context, root string, batch Batch) error {
	directory, err := os.MkdirTemp("", "release-consumer-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	env := append(process.Environment(false), "GOWORK=off", "GOPROXY=https://proxy.golang.org",
		"GOSUMDB=sum.golang.org", "GOPRIVATE=", "GONOPROXY=", "GONOSUMDB=", "GOFLAGS=-mod=readonly",
		"GOENV=off", "GOINSECURE=",
		"GOMODCACHE="+filepath.Join(directory, "modules"), "GOCACHE="+filepath.Join(directory, "build"),
		"GOBIN="+filepath.Join(directory, "bin"))
	for _, entry := range batch.Proposals() {
		spec := entry.ModulePath + "@" + *entry.NextVersion
		var download struct {
			Dir      string `json:"Dir"`
			GoMod    string `json:"GoMod"`
			Sum      string `json:"Sum"`
			GoModSum string `json:"GoModSum"`
			Error    string `json:"Error"`
			Origin   struct {
				Hash   string `json:"Hash"`
				Ref    string `json:"Ref"`
				URL    string `json:"URL"`
				VCS    string `json:"VCS"`
				Subdir string `json:"Subdir"`
			} `json:"Origin"`
		}
		complete := false
		for _, delay := range []time.Duration{0, 15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 3 * time.Minute} {
			if err := evidence.Delay(ctx, delay); err != nil {
				return err
			}
			data, err := process.Execute(ctx, directory, env, "go", "", "mod", "download", "-json", spec)
			if err != nil {
				continue
			}
			if err := json.Unmarshal(data, &download); err != nil {
				return fmt.Errorf("invalid public Go download metadata")
			}
			if download.Error != "" || download.Sum == "" || download.GoModSum == "" || download.Dir == "" || download.GoMod == "" {
				continue
			}
			if download.Origin.Hash != entry.SHA || download.Origin.Ref != "refs/tags/"+TagName(entry) ||
				download.Origin.VCS != "git" || download.Origin.URL != "https://github.com/arkoren/arkoren" || download.Origin.Subdir != entry.Directory {
				return fmt.Errorf("public module origin differs from signed source and version")
			}
			complete = true
			break
		}
		if !complete {
			return fmt.Errorf("public Go verification failed; retry same SHA, never bump again")
		}
		original, err := process.Run(ctx, root, "git", "", false, "show", entry.SHA+":"+entry.Directory+"/go.mod")
		if err != nil {
			return err
		}
		manifest, err := os.ReadFile(download.GoMod)
		if err != nil {
			return err
		}
		if !slices.Equal(original, manifest) {
			return fmt.Errorf("public module manifest differs from approved source")
		}
		metadata, err := process.Execute(ctx, download.Dir, env, "go", "", "mod", "edit", "-json")
		if err != nil {
			return err
		}
		var module struct {
			Module struct {
				Path string `json:"Path"`
			} `json:"Module"`
			Replace []any `json:"Replace"`
		}
		if err := json.Unmarshal(metadata, &module); err != nil {
			return err
		}
		if module.Module.Path != entry.ModulePath || len(module.Replace) != 0 {
			return fmt.Errorf("public module identity or replacement conflict")
		}
		if _, err := process.Execute(ctx, download.Dir, env, "go", "", "build", "./..."); err != nil {
			return err
		}
		if entry.Directory == "cmd/arkoren" {
			if _, err := process.Execute(ctx, directory, env, "go", "", "install", spec); err != nil {
				return err
			}
		}
	}

	return nil
}
