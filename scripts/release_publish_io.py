#!/usr/bin/env python3
"""Narrow publication side effects: immutable refs, exact releases and public Go builds."""
import base64
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

from release_preview import git
from release_publish_plan import PRINCIPAL, TRUST, annotation, proposals, tag_name, verify_tag

REPO = "arkoren/arkoren"
API = "repos/" + REPO


def command(args, *, cwd=None, env=None, data=None):
    """Suppress remote errors: credentials must never enter failure diagnostics."""
    result = subprocess.run(args, cwd=cwd, env=env, input=data, capture_output=True)
    if result.returncode:
        raise RuntimeError("publication command failed; inspect stage and retry the same SHA (remote output suppressed)")
    return result.stdout


def api(endpoint, payload=None, token=None):
    env = os.environ.copy()
    env.pop("SIGNING_KEY", None)
    env.pop("APP_TOKEN", None)
    if token is not None:
        env["GH_TOKEN"] = token
    args = ["gh", "api", endpoint]
    if payload is not None:
        args += ["--method", "POST", "--input", "-"]
    return json.loads(command(args, env=env, data=None if payload is None else json.dumps(payload).encode()))


def releases(request=api):
    records = {}
    for page in range(1, 1001):
        items = request(f"{API}/releases?per_page=100&page={page}")
        for item in items:
            if item["tag_name"] in records:
                raise ValueError("duplicate GitHub release tag")
            records[item["tag_name"]] = item
        if len(items) < 100:
            return records
    raise ValueError("release pagination limit exceeded")


def check_release(release, entry):
    if (release["tag_name"] != tag_name(entry) or release["body"] != entry["notes"]
            or release["name"] != tag_name(entry) or release["draft"]
            or release["prerelease"] is not True or release.get("assets")):
        raise ValueError("existing release conflicts with immutable approved plan; never overwrite it")


def app_identity(slug, installation, token, request=api):
    if slug != "arkoren-releases" or str(installation) != "176874315":
        raise ValueError("unexpected publication App installation")
    scope = request("installation/repositories?per_page=100", token=token)
    if scope["total_count"] != 1 or [item["full_name"] for item in scope["repositories"]] != [REPO]:
        raise ValueError("publication token must access only arkoren/arkoren")


def signing_environment(key):
    env = os.environ.copy()
    for name in ("APP_TOKEN", "SIGNING_KEY", "GH_TOKEN", "GITHUB_TOKEN"):
        env.pop(name, None)
    env.update(GIT_COMMITTER_NAME="Arkoren Releases automation",
               GIT_COMMITTER_EMAIL=PRINCIPAL, GIT_CONFIG_COUNT="3",
               GIT_CONFIG_KEY_0="gpg.format", GIT_CONFIG_VALUE_0="ssh",
               GIT_CONFIG_KEY_1="user.signingkey", GIT_CONFIG_VALUE_1=str(key),
               GIT_CONFIG_KEY_2="gpg.ssh.allowedSignersFile", GIT_CONFIG_VALUE_2=str(TRUST))
    return env


def with_signer(secret, operation):
    """Private key exists only in a private temporary directory, cleaned on errors too."""
    if not secret:
        raise ValueError("ARKOREN_RELEASES_TAG_SIGNING_KEY is not configured")
    with tempfile.TemporaryDirectory(prefix="release-signing-") as directory:
        key = Path(directory) / "signing-key"
        fd = os.open(key, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as file:
            file.write(secret)
            if not secret.endswith("\n"):
                file.write("\n")
        public = command(["ssh-keygen", "-y", "-f", str(key)]).decode().strip()
        fields = TRUST.read_text().split()
        if public.split()[:2] != fields[1:3] or fields[0] != PRINCIPAL:
            raise ValueError("signing secret does not match the reviewed automation public key")
        return operation(signing_environment(key))


def preflight(secret):
    """Sign/verify only inside a disposable repository; create no production refs."""
    def run(env):
        with tempfile.TemporaryDirectory(prefix="release-signature-proof-") as directory:
            command(["git", "init", "-b", "proof"], cwd=directory)
            local = env.copy()
            local.update(GIT_AUTHOR_NAME="Arkoren Releases automation", GIT_AUTHOR_EMAIL=PRINCIPAL)
            command(["git", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "local signature preflight"], cwd=directory, env=local)
            command(["git", "tag", "-s", "proof", "-m", "local-only signature proof"], cwd=directory, env=local)
            command(["git", "verify-tag", "proof"], cwd=directory, env=local)
    with_signer(secret, run)


def remote_refs(repo, names):
    raw = command(["git", "ls-remote", "--refs", "origin", *["refs/tags/" + name for name in names]], cwd=repo).decode()
    return {ref.removeprefix("refs/tags/"): oid for oid, ref in (line.split() for line in raw.splitlines())}


def reconcile(repo, state):
    """After a lost push response, compare remote tag objects, not just commit targets."""
    names = [tag_name(entry) for entry in proposals(state)]
    actual = remote_refs(repo, names)
    for name, oid in actual.items():
        if oid != git(repo, "rev-parse", "refs/tags/" + name).strip():
            raise ValueError("remote immutable tag object conflict")
        verify_tag(repo, name, state)
    return len(actual) == len(names)


def push_tags(repo, state, secret, token):
    names = [tag_name(entry) for entry in proposals(state)]
    def run(env):
        existing = set(git(repo, "tag", "--list").splitlines())
        for name in names:
            if name in existing:
                verify_tag(repo, name, state)
            else:
                command(["git", "tag", "--cleanup=verbatim", "-s", name, state["plan"]["sha"], "-F", "-"],
                        cwd=repo, env=env, data=annotation(state).encode())
                verify_tag(repo, name, state)
        # Never persist an App token in .git/config, a URL, argv or diagnostic output.
        auth = base64.b64encode(("x-access-token:" + token).encode()).decode()
        env.update(GIT_CONFIG_COUNT="4", GIT_CONFIG_KEY_3="http.https://github.com/.extraheader",
                   GIT_CONFIG_VALUE_3="AUTHORIZATION: basic " + auth)
        try:
            command(["git", "push", "--atomic", "origin", *[f"refs/tags/{name}:refs/tags/{name}" for name in names]], cwd=repo, env=env)
        except RuntimeError:
            if not reconcile(repo, state):
                raise RuntimeError("atomic publication unconfirmed; rerun same SHA, never force/delete tags") from None
        if not reconcile(repo, state):
            raise RuntimeError("remote batch incomplete; rerun same SHA")
    with_signer(secret, run)


def publish_releases(state, token, request=api):
    existing = releases(request)
    for entry in proposals(state):
        name = tag_name(entry)
        if name not in existing:
            # The App, NOT GITHUB_TOKEN, creates the release so release events run notes sync.
            request(API + "/releases", {"tag_name": name, "target_commitish": state["plan"]["sha"],
                    "name": name, "body": entry["notes"], "draft": False, "prerelease": True,
                    "make_latest": "false", "generate_release_notes": False}, token=token)
        else:
            check_release(existing[name], entry)
    confirmed = releases(request)
    for entry in proposals(state):
        check_release(confirmed[tag_name(entry)], entry)


def public_verify(state, runner=command, pause=time.sleep):
    """Only call after remote refs exist. Use normal public proxy and checksum database."""
    with tempfile.TemporaryDirectory(prefix="release-consumer-") as directory:
        env = os.environ.copy()
        for name in ("APP_TOKEN", "SIGNING_KEY", "GH_TOKEN", "GITHUB_TOKEN"):
            env.pop(name, None)
        env.update(GOWORK="off", GOPROXY="https://proxy.golang.org", GOSUMDB="sum.golang.org",
                   GOPRIVATE="", GONOPROXY="", GONOSUMDB="", GOFLAGS="-mod=readonly",
                   GOMODCACHE=str(Path(directory) / "modules"), GOCACHE=str(Path(directory) / "build"),
                   GOBIN=str(Path(directory) / "bin"))
        for entry in proposals(state):
            spec = entry["module_path"] + "@" + entry["next_version"]
            for attempt, delay in enumerate((0, 15, 30, 60, 120, 180)):
                pause(delay)
                try:
                    result = json.loads(runner(["go", "mod", "download", "-json", spec], cwd=directory, env=env))
                    if result.get("Error") or not result.get("Sum") or not result.get("GoModSum"):
                        raise RuntimeError("public Go checksum/download incomplete")
                    break
                except RuntimeError:
                    if attempt == 5:
                        raise RuntimeError("public Go verification failed after publication; retry same SHA, do not bump again") from None
            runner(["go", "build", "./..."], cwd=result["Dir"], env=env)
            if entry["directory"] == "cmd/arkoren":
                runner(["go", "install", spec], cwd=directory, env=env)
