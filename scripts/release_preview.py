#!/usr/bin/env python3
"""Transitional disabled-publisher dependency; use arkoren-ci for live previews."""
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess

SCRIPTS = Path(__file__).resolve().parent
# Transitional disabled-publisher dependency; the Go command owns live previews.
TOOL = SCRIPTS.parent / "cmd/arkoren-ci/internal/release"
CLIFF_VERSION = json.loads((TOOL / "tool.json").read_text())["git_cliff"]
CANONICAL = re.compile(r"(?:core|cmd/arkoren|(?:driver|foundation|ark)/[a-z0-9][a-z0-9-]*)")
VERSION = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")


def git(repo, *args):
    return subprocess.check_output(["git", *args], cwd=repo, text=True, stderr=subprocess.PIPE)


def ancestor(repo, old, new):
    result = subprocess.run(["git", "merge-base", "--is-ancestor", old, new], cwd=repo, capture_output=True)
    if result.returncode not in (0, 1):
        raise ValueError("cannot establish complete release ancestry")
    return result.returncode == 0


def payload(path, module):
    """Documentation/tests are excluded by path, except the CLI's embedded tree."""
    if not path.startswith(module + "/"):
        return False
    relative = PurePosixPath(path[len(module) + 1:])
    if module == "cmd/arkoren" and relative.parts[0] == "skills":
        return True
    return not (
        relative.suffix.lower() in (".md", ".mdx", ".rst")
        or relative.name.endswith("_test.go")
        or any(part in ("testdata", "tests", "docs", ".github", ".agents", ".claude") for part in relative.parts)
        or relative.name in ("AGENTS.md", "CLAUDE.md", "LICENSE", "LICENSE.txt")
    )


def cliff(repo, *args, context=None):
    binary = os.environ.get("GIT_CLIFF", "git-cliff")
    # No remote metadata, shell preprocessors, or caller-provided cliff options.
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_CLIFF_") and k not in ("GITHUB_TOKEN", "GH_TOKEN", "APP_TOKEN", "SIGNING_KEY")}
    command = [binary, "--config", str(TOOL / "cliff.toml"), "--offline", "--no-exec", *args]
    result = subprocess.run(command, cwd=repo, env=env, input=context, text=True, capture_output=True)
    if result.returncode:
        raise ValueError(f"git-cliff failed: {result.stderr.strip()}")
    return result.stdout


def module_policy(data, legacy=False):
    """Read classification policy without changing signed planner record bytes."""
    value = json.loads(data)
    if legacy and isinstance(value, list):
        value = {"schema": 1, "public": value, "development": []}
    if not isinstance(value, dict) or set(value) != {"schema", "public", "development"} or value["schema"] != 1:
        raise ValueError("invalid module policy schema")
    public, development = value["public"], value["development"]
    if not isinstance(public, list) or not public or not isinstance(development, list):
        raise ValueError("invalid module policy lists")
    if any(not isinstance(m, str) or not CANONICAL.fullmatch(m) for m in public):
        raise ValueError("invalid public module policy")
    if any(m != "cmd/arkoren-ci" for m in development) or len(set(public + development)) != len(public + development):
        raise ValueError("invalid development module policy")
    if not {"core", "cmd/arkoren"} <= set(public):
        raise ValueError("module policy requires core and consumer CLI")
    return public, development


def plan(repo, sha, initial=()):
    repo = Path(repo).resolve()
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("provide an exact full commit SHA, not a ref or range")
    if git(repo, "rev-parse", "--is-shallow-repository").strip() != "false":
        raise ValueError("full tag history is required; shallow repository rejected")
    if git(repo, "rev-parse", f"{sha}^{{commit}}").strip() != sha:
        raise ValueError("SHA must identify a commit")
    if cliff(repo, "--version").strip() != "git-cliff " + CLIFF_VERSION:
        raise ValueError("install the pinned git-cliff from arkoren-ci release tool")

    # Inspect tree names/modes, not the local filesystem: ignored private modules,
    # worktrees, cache entries and untracked manifests cannot enter this inventory.
    tree = {}
    for record in git(repo, "ls-tree", "-rz", "--full-tree", sha).split("\0"):
        if record:
            metadata, path = record.split("\t", 1)
            tree[path] = metadata.split()[0]
    modules = sorted(path[:-7] for path in tree if path.endswith("/go.mod") and CANONICAL.fullmatch(path[:-7]))
    # Historical signed plans predate classification policy. Recompute their
    # original public inventory; never inject current policy into old records.
    development = []
    if ".github/release-modules.json" in tree:
        if tree[".github/release-modules.json"] != "100644":
            raise ValueError("invalid module policy mode")
        public, development = module_policy(git(repo, "show", f"{sha}:.github/release-modules.json"), legacy=True)
        if sorted(public) != modules:
            raise ValueError("committed public inventory differs from module policy")
    for module in development:
        if tree.get(module + "/go.mod") != "100644" or re.findall(r"(?m)^module\s+(\S+)\s*$", git(repo, "show", f"{sha}:{module}/go.mod")) != ["arkoren.dev/" + module]:
            raise ValueError("invalid development module manifest")
    commands = {"/".join(path.split("/")[:2]) for path in tree if path.startswith("cmd/") and len(path.split("/")) > 2}
    if commands - {"cmd/arkoren", *development}:
        raise ValueError("unreviewed command module")
    if "core" not in modules or "cmd/arkoren" not in modules:
        raise ValueError("missing canonical core or CLI manifest")
    family_roots = {"/".join(path.split("/")[:2]) for path in tree if path.split("/")[0] in ("driver", "foundation", "ark") and len(path.split("/")) > 2}
    if family_roots - set(modules) or any(path.startswith("core/") and path.endswith("/go.mod") and path != "core/go.mod" for path in tree):
        raise ValueError("noncanonical or missing tracked module manifest")
    if not any(m.startswith("driver/") for m in modules) or not any(m.startswith("foundation/") for m in modules):
        raise ValueError("empty tracked driver or foundation family")
    for module in modules:
        if tree[module + "/go.mod"] != "100644":
            raise ValueError(f"invalid manifest mode: {module}")
    if len(set(initial)) != len(initial) or set(initial) - set(modules):
        raise ValueError("initial-module must name distinct canonical tracked modules")

    tags = git(repo, "tag", "--list").splitlines()
    selected = {}
    all_reachable = set()
    for module in modules:
        versions = []
        pattern = re.compile(re.escape(module) + r"/(v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))")
        for tag in tags:
            if not tag.startswith(module + "/"):
                continue
            match = pattern.fullmatch(tag)
            if not match:
                raise ValueError(f"malformed release tag: {tag}; only plain v0.X.Y is accepted")
            version = match[1]
            numbers = tuple(map(int, VERSION.fullmatch(version).groups()))
            if numbers[0] != 0:
                raise ValueError(f"v1+ requires separate authorization: {tag}")
            target = git(repo, "rev-parse", f"refs/tags/{tag}^{{commit}}").strip()
            if ancestor(repo, target, sha):
                versions.append((numbers, tag, target, version))
                all_reachable.add(target)
        versions.sort()
        for older, newer in zip(versions, versions[1:]):
            if older[2] == newer[2] or not ancestor(repo, older[2], newer[2]):
                raise ValueError(f"ambiguous release history: {older[1]} and {newer[1]}")
        selected[module] = versions[-1] if versions else None

    history = git(repo, "rev-list", "--reverse", "--topo-order", sha).splitlines()
    commits = {}
    for commit in history:
        parents = git(repo, "show", "-s", "--format=%P", commit).split()
        merge = len(parents) > 1
        # Combined diff catches merge-only resolutions. Those require a separate
        # conventional commit, rather than silently omitting substantive edits.
        files = git(repo, "diff-tree", "--root", "--no-commit-id", "--name-only", "-r", "--no-renames", *( ["--cc"] if merge else []), "-z", commit).split("\0")
        commits[commit] = {"id": commit, "message": git(repo, "show", "-s", "--format=%B", commit).strip(), "files": [f for f in files if f], "merge": merge}

    entries = []
    for module in modules:
        baseline = selected[module]
        if baseline and module in initial:
            raise ValueError(f"initial-module already has a baseline: {module}")
        if not baseline:
            if module not in initial:
                raise ValueError(f"missing release baseline for {module}; new modules require explicit --initial-module")
            if not all_reachable or any(git(repo, "ls-tree", target, "--", module + "/go.mod").strip() for target in all_reachable):
                raise ValueError(f"existing module {module} has no baseline; recover its release tag, do not bootstrap import history")
        included = set(git(repo, "rev-list", f"{baseline[2]}..{sha}" if baseline else sha).splitlines())
        matching = []
        for commit in history:
            entry = commits[commit]
            eligible = [f for f in entry["files"] if payload(f, module)]
            if commit in included and eligible:
                if entry["merge"]:
                    raise ValueError(f"merge-only payload changes in {module} at {commit}; record resolution in a conventional non-merge commit")
                matching.append({"id": commit, "message": entry["message"], "files": eligible})
        current = baseline[3] if baseline else None
        entry = {"directory": module, "module_path": None, "current_version": current, "next_version": None, "sha": sha, "baseline_tag": baseline[1] if baseline else None, "commits": matching, "notes": "", "skip_reason": "no releasable payload changes since this module's tag"}
        manifest = git(repo, "show", f"{sha}:{module}/go.mod")
        declarations = re.findall(r"(?m)^module\s+(\S+)\s*$", manifest)
        if declarations != ["arkoren.dev/" + module]:
            raise ValueError(f"unexpected module path: {module}")
        entry["module_path"] = declarations[0]
        if matching:
            keep = {c["id"] for c in matching}
            # Select tags BEFORE git-cliff constructs its commit-keyed tag map.
            # Exact baseline also excludes valid but unreachable future tags.
            args = ["--tag-pattern", "^" + re.escape(baseline[1]) + "$" if baseline else "^$", f"{baseline[2]}..{sha}" if baseline else sha]
            for commit in sorted(included - keep):
                args.extend(["--skip-commit", commit])
            context = cliff(repo, *args, "--context")
            parsed = json.loads(context)
            actual = [c["id"] for release in parsed for c in release["commits"]]
            if len(actual) != len(keep) or set(actual) != keep:
                raise ValueError(f"git-cliff omitted or added commits for {module}; inspect ignore files and filters")
            if baseline and (len(parsed) != 1 or parsed[0].get("previous", {}).get("version") != baseline[1]):
                raise ValueError(f"git-cliff baseline mismatch for {module}")
            # From-context preserves git-cliff's parsed conventional/breaking
            # fields without requiring the next tag to equal the baseline regex.
            next_tag = cliff(repo, "--from-context", "-", "--bumped-version", context=context).strip()
            next_version = next_tag.removeprefix(module + "/")
            match = VERSION.fullmatch(next_version)
            if not match or int(match[1]) != 0:
                raise ValueError(f"invalid or v1+ proposal for {module}: {next_tag}")
            if current and tuple(map(int, match.groups())) < tuple(map(int, VERSION.fullmatch(current).groups())):
                raise ValueError(f"regressive proposal for {module}: {next_tag}")
            if next_version != current:
                entry.update(next_version=next_version, skip_reason=None, notes=cliff(repo, "--from-context", "-", "--bump", context=context))
        entries.append(entry)
    return {"schema_version": 1, "dry_run": True, "publication_authorized": False, "sha": sha, "git_cliff": CLIFF_VERSION, "validation": "local plan only; no CI evidence attached", "modules": entries}


if __name__ == "__main__":
    raise SystemExit("use arkoren-ci release plan; this module remains only for the disabled publisher")
