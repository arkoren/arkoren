#!/usr/bin/env python3
"""Prepare a bounded import, then upsert a green-only auto-merge PR as the App."""

import argparse
import base64
import json
import os
from pathlib import Path
import re
import subprocess

REPO = "arkoren/arkoren"
API = f"repos/{REPO}"
BRANCH = "automation/release-notes"
BOT = "arkoren-releases[bot]"
ROOT = "website/src/content/releases/"
PATH = re.compile(
    r"website/src/content/releases/(?:core|cmd/arkoren|(?:driver|foundation|ark)/[a-z0-9-]+)/v0\.[0-9]+\.[0-9]+\.md"
)


def command(*args, payload=None):
    """Never invoke a shell or expose remote error text or credentials."""
    result = subprocess.run(args, input=payload, capture_output=True, check=False)
    if result.returncode:
        raise RuntimeError("command failed; check access, API availability and workflow logs")
    return result.stdout


def api(endpoint, payload=None, method="GET"):
    """Use JSON stdin for mutations; paginate list endpoints at their callers."""
    args = ["gh", "api", "--method", method, endpoint]
    if payload is not None:
        args.extend(["--input", "-"])
    return json.loads(command(*args, payload=None if payload is None else json.dumps(payload).encode()))


def pages(endpoint, request=api):
    """Read all pages before a caller can mutate repository state."""
    records = []
    for page in range(1, 101):
        separator = "&" if "?" in endpoint else "?"
        batch = request(f"{endpoint}{separator}per_page=100&page={page}")
        if not isinstance(batch, list):
            raise RuntimeError("expected a paginated list")
        records.extend(batch)
        if len(batch) < 100:
            return records
    raise RuntimeError("pagination limit exceeded; no writes permitted")


def allowed(path):
    """Only ordinary canonical release Markdown paths may enter a PR."""
    if PATH.fullmatch(path) is None:
        raise RuntimeError("change outside managed release records")


def prepare():
    """Inspect staged, unstaged and untracked changes before obtaining a token."""
    if command("git", "remote", "get-url", "origin").decode().strip() != "https://github.com/arkoren/arkoren":
        raise RuntimeError("unexpected checkout repository")
    base = command("git", "rev-parse", "HEAD").decode().strip()
    raw = command("git", "status", "--porcelain=v1", "-z", "--untracked-files=all")
    records = []
    for entry in raw.split(b"\0"):
        if not entry:
            continue
        status, path = entry[:2].decode(), entry[3:].decode()
        allowed(path)
        if status not in (" M", "M ", "MM", "A ", "AM", "??"):
            raise RuntimeError("only additions and modifications are permitted")
        file = Path(path)
        if file.is_symlink() or not file.is_file():
            raise RuntimeError("release record must be an ordinary file")
        records.append({"path": path, "content": base64.b64encode(file.read_bytes()).decode()})
    return {"base": base, "records": sorted(records, key=lambda item: item["path"])}


def owned(state, request=api):
    """Reject foreign PRs, commits or paths, including abandoned bot branches."""
    pulls = pages(f"{API}/pulls?state=open&head=arkoren:{BRANCH}", request)
    if len(pulls) > 1:
        raise RuntimeError("multiple release-note PRs require manual recovery")
    for pull in pulls:
        if (pull["user"]["login"] != BOT or pull["base"]["ref"] != "main"
                or pull["base"]["repo"]["full_name"] != REPO
                or pull["head"]["ref"] != BRANCH
                or pull["head"]["repo"]["full_name"] != REPO):
            raise RuntimeError("foreign PR occupies the automation branch")
    refs = request(f"{API}/git/matching-refs/heads/{BRANCH}")
    refs = [ref for ref in refs if ref["ref"] == f"refs/heads/{BRANCH}"]
    if not refs:
        if pulls:
            raise RuntimeError("PR branch disappeared; retry after manual recovery")
        return None, None
    head = refs[0]["object"]["sha"]
    tip = request(f"{API}/commits/{head}")
    verified_bot(tip)
    comparison = request(f"{API}/compare/{state['base']}...{head}?per_page=100")
    if comparison["total_commits"] != len(comparison["commits"]):
        raise RuntimeError("branch history exceeds ownership inspection limit")
    for commit in comparison["commits"]:
        verified_bot(commit)
    if len(comparison["files"]) >= 300:
        raise RuntimeError("branch diff exceeds inspection limit")
    for file in comparison["files"]:
        allowed(file["filename"])
        if file["status"] not in ("added", "modified"):
            raise RuntimeError("automation branch removes or renames history")
    if pulls and pulls[0]["head"]["sha"] != head:
        raise RuntimeError("branch changed during inspection; retry")
    return tip, pulls[0] if pulls else None


def verified_bot(commit):
    """Require GitHub-verified commits associated with the expected App bot."""
    if ((commit.get("author") or {}).get("login") != BOT
            or (commit.get("committer") or {}).get("login") != BOT
            or not commit["commit"]["verification"]["verified"]):
        raise RuntimeError("foreign or unsigned commit occupies the automation branch")


def verify(request=api):
    """Confirm the installation token is restricted to this repository."""
    repositories = request("installation/repositories?per_page=100")
    if (repositories["total_count"] != 1
            or [repo["full_name"] for repo in repositories["repositories"]] != [REPO]):
        raise RuntimeError("App token must be scoped only to arkoren/arkoren")


def auto_merge(state, pull, expected_head, request=api):
    """Reinspect ownership, then let GitHub enforce all main requirements."""
    tip, current = owned(state, request)
    if (current is None or current["number"] != pull["number"]
            or current["node_id"] != pull["node_id"] or tip["sha"] != expected_head):
        raise RuntimeError("PR or branch changed before auto-merge; rerun sync")
    enabled = current.get("auto_merge")
    if enabled:
        if enabled["merge_method"] != "merge":
            raise RuntimeError("existing auto-merge must preserve signed commits with merge method")
        return
    repository = request(API)
    if not repository.get("allow_auto_merge") or not repository.get("allow_merge_commit"):
        raise RuntimeError("enable repository auto-merge and merge commits; do not relax main requirements")
    try:
        result = request("graphql", {
            "query": "mutation($input: EnablePullRequestAutoMergeInput!) { "
                     "enablePullRequestAutoMerge(input: $input) { pullRequest { id } } }",
            "variables": {"input": {"pullRequestId": current["node_id"],
                                    "expectedHeadOid": expected_head, "mergeMethod": "MERGE"}},
        }, "POST")
        if result.get("errors") or result["data"]["enablePullRequestAutoMerge"]["pullRequest"]["id"] != current["node_id"]:
            raise RuntimeError("auto-merge not confirmed")
    except (RuntimeError, KeyError, TypeError) as error:
        raise RuntimeError("auto-merge request failed; check App Contents/Pull requests write access, "
                           "repository auto-merge and current PR head; rerun sync without bypassing checks") from error


def publish(state, request=api):
    """Create signed API commits and advance only the dedicated branch, never tags."""
    for record in state["records"]:
        allowed(record["path"])
    if not state["records"]:
        return "No changes; no App authentication, commit or PR needed."
    verify(request)
    tip, pull = owned(state, request)
    main = request(f"{API}/commits/main")
    if main["sha"] != state["base"]:
        raise RuntimeError("main advanced after import; rerun sync from current main")
    if tip is not None:
        pending = request(f"{API}/compare/{state['base']}...{tip['sha']}?per_page=100")
        imported = {record["path"] for record in state["records"]}
        for file in pending["files"]:
            if file["status"] == "added" and file["filename"] not in imported:
                raise RuntimeError("pending publication absent from import; preserve PR and retry")
    tree = request(f"{API}/git/trees", {
        "base_tree": main["commit"]["tree"]["sha"],
        "tree": [{"path": record["path"], "mode": "100644", "type": "blob",
                  "content": base64.b64decode(record["content"], validate=True).decode()}
                 for record in state["records"]],
    }, "POST")
    expected_head = tip["sha"] if tip is not None else None
    if tip is None or tip["commit"]["tree"]["sha"] != tree["sha"]:
        parents = [state["base"]] if tip is None else list(dict.fromkeys([tip["sha"], state["base"]]))
        commit = request(f"{API}/git/commits", {
            "message": "docs(website): sync published module release notes", "tree": tree["sha"],
            "parents": parents,
        }, "POST")
        if not commit["verification"]["verified"]:
            raise RuntimeError("GitHub did not sign the App commit; branch was not changed")
        expected_head = commit["sha"]
        if tip is None:
            request(f"{API}/git/refs", {"ref": f"refs/heads/{BRANCH}", "sha": commit["sha"]}, "POST")
        else:
            request(f"{API}/git/refs/heads/{BRANCH}", {"sha": commit["sha"], "force": False}, "PATCH")
    links = "\n".join(
        f"- [{record['path'][len(ROOT):-3]}](https://github.com/{REPO}/releases/tag/{record['path'][len(ROOT):-3]})"
        for record in state["records"]
    )
    body = ("Sync confirmed published GitHub notes into the website collection.\n\n" + links
            + "\n\nOnly release records change. This job publishes no modules, tags or releases. "
              "GitHub native auto-merge uses a merge commit after required CI gate and website checks "
              "pass on an up-to-date branch. Failed, pending or cancelled checks block merging. "
              "No self-approval or main bypass is used.")
    payload = {"title": "Sync published module release notes", "body": body}
    if pull is None:
        payload.update({"head": BRANCH, "base": "main", "maintainer_can_modify": False})
        pull = request(f"{API}/pulls", payload, "POST")
    else:
        pull = request(f"{API}/pulls/{pull['number']}", payload, "PATCH")
    auto_merge(state, pull, expected_head, request)
    return f"GitHub green-only auto-merge requested: {pull['html_url']}"


def main():
    """Keep credentials out of importer dependencies and all command arguments."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=["prepare", "publish", "verify"])
    parser.add_argument("manifest", type=Path)
    args = parser.parse_args()
    if args.mode == "prepare":
        state = prepare()
        args.manifest.write_text(json.dumps(state))
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            output.write(f"changed={'true' if state['records'] else 'false'}\n")
        message = f"{len(state['records'])} changed release records."
        if not state["records"]:
            message += " No-op: no App authentication, commit or PR attempted."
    elif args.mode == "verify":
        verify()
        message = "App installation token verified for arkoren/arkoren only; no writes attempted."
    else:
        message = publish(json.loads(args.manifest.read_text()))
    print(message)
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as summary:
        summary.write(message + "\n")


if __name__ == "__main__":
    try:
        main()
    except RuntimeError as error:
        raise SystemExit(f"release-note PR sync blocked: {error}")
    except Exception:
        raise SystemExit("release-note PR sync failed; check configuration, API access, managed paths and branch ownership; rerun from main (remote error text suppressed)")
