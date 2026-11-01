#!/usr/bin/env python3
"""Validate publication plans and recover them from immutable signed annotations."""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tempfile

from release_preview import ancestor, git, plan

ROOT = Path(__file__).resolve().parent.parent
TRUST = ROOT / ".github/release-signing.allowed-signers"
POLICY = ROOT / ".github/release-modules.json"
MARKER = "Arkoren module publication v1\n"
SIGNATURE = "-----BEGIN SSH SIGNATURE-----"
PRINCIPAL = "arkoren-releases@arkoren.dev"


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True)


def batch(result):
    """Preserve the complete original planner result, including exact notes."""
    return {"kind": "arkoren-module-publication-v1", "plan": result,
            "digest": hashlib.sha256(canonical(result).encode()).hexdigest()}


def proposals(state):
    return [entry for entry in state["plan"]["modules"] if entry["next_version"]]


def tag_name(entry):
    return entry["directory"] + "/" + entry["next_version"]


def annotation(state):
    return MARKER + canonical(state) + "\n"


def policy(result, allowed=None):
    """New modules need a reviewed allowlist AND creation-ruleset update."""
    allowed = json.loads(POLICY.read_text()) if allowed is None else allowed
    if sorted(allowed) != sorted(entry["directory"] for entry in result["modules"]):
        raise ValueError("module inventory differs from release allowlist; review namespace policy and creation rulesets")
    for entry in result["modules"]:
        if entry["next_version"] and not re.fullmatch(r"v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", entry["next_version"]):
            raise ValueError("publication permits plain module-prefixed v0.X.Y only")


def verify_tag(repo, tag, state=None, trust=None):
    """Cryptographic trust is this reviewed key, not GitHub's UI badge."""
    trust = TRUST if trust is None else trust
    subprocess.run(["git", "-c", "gpg.format=ssh", "-c", f"gpg.ssh.allowedSignersFile={trust}",
                    "verify-tag", "--", tag], cwd=repo, check=True, capture_output=True)
    raw = git(repo, "cat-file", "tag", "refs/tags/" + tag)
    headers, message = raw.split("\n\n", 1)
    # Notes may quote a signature delimiter. The actual signature is the last
    # delimiter, outside the canonical single-line JSON annotation.
    unsigned = message.rsplit(SIGNATURE, 1)[0]
    if not unsigned.startswith(MARKER):
        raise ValueError("signed tag lacks a publication plan")
    recovered = json.loads(unsigned[len(MARKER):])
    if unsigned != annotation(recovered) or recovered != batch(recovered["plan"]):
        raise ValueError("noncanonical annotation or plan digest mismatch")
    if state is not None and recovered != state:
        raise ValueError("existing tag records another approved plan")
    expected = {tag_name(entry): entry["sha"] for entry in proposals(recovered)}
    if tag not in expected or git(repo, "rev-parse", f"refs/tags/{tag}^{{commit}}").strip() != expected[tag]:
        raise ValueError("tag target or batch membership conflict")
    if "type commit" not in headers.splitlines() or "tag " + tag not in headers.splitlines():
        raise ValueError("tag header conflict")
    return recovered


def recorded(repo, trust=None):
    """Legacy v0.1.0 baselines are retained; all subsequent tags need provenance."""
    batches = {}
    members = {}
    allowed = json.loads(POLICY.read_text())
    for tag in git(repo, "tag", "--list").splitlines():
        if not any(re.fullmatch(re.escape(module) + r"/v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", tag) for module in allowed):
            raise ValueError("tag outside approved canonical v0 namespaces")
        if tag.endswith("/v0.1.0"):
            continue
        state = verify_tag(repo, tag, trust=trust)
        batches[state["digest"]] = state
        members[tag] = state["digest"]
    for state in batches.values():
        for entry in proposals(state):
            if tag_name(entry) in members and members[tag_name(entry)] != state["digest"]:
                raise ValueError("conflicting signed batch membership")
    return list(batches.values())


def recompute(repo, state, allowed=None):
    """Do not let already-created batch tags erase the original release proposal."""
    sha = state["plan"]["sha"]
    with tempfile.TemporaryDirectory(prefix="release-recompute-") as directory:
        subprocess.run(["git", "clone", "--bare", "--no-hardlinks", str(Path(repo).resolve()), directory],
                       check=True, capture_output=True)
        existing = set(git(directory, "tag", "--list").splitlines())
        for entry in proposals(state):
            tag = tag_name(entry)
            if tag in existing:
                # Delete only refs in this disposable clone, never the source/remote.
                git(directory, "update-ref", "-d", "refs/tags/" + tag)
        expected = batch(plan(directory, sha))
    if state != expected:
        raise ValueError("publication plan differs from recomputed trusted source")
    policy(expected["plan"], allowed)
    return expected


def fresh(repo, sha, main, allowed=None):
    if sha != main:
        raise ValueError("stale candidate: rerun on the latest exact-green main SHA")
    result = plan(repo, sha)
    policy(result, allowed)
    for tag in git(repo, "tag", "--list").splitlines():
        if not ancestor(repo, git(repo, "rev-parse", f"refs/tags/{tag}^{{commit}}").strip(), sha):
            raise ValueError("nonancestor release tag; refuse a stale or conflicting history")
    return batch(result)
