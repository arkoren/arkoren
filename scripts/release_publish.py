#!/usr/bin/env python3
"""Unattended publisher: validate first, then finish immutable recorded batches."""
import argparse
import json
import os
from pathlib import Path
import signal
import sys

from release_evidence import collect, select_target
from release_preview import ancestor, git
from release_publish_plan import (fresh, proposals, recompute,
                                  recorded, tag_name, verify_tag)
from release_publish_io import (API, REPO, api, app_identity, check_release,
                                preflight, public_verify, publish_releases,
                                push_tags, releases)


def main_sha(request=api):
    return request(API + "/git/ref/heads/main")["object"]["sha"]


def validate_batch(repo, state, main, allowed=None):
    """Recompute every field, and never start a stale or regressive batch."""
    sha = state["plan"]["sha"]
    if not ancestor(repo, sha, main):
        raise ValueError("publication SHA is not on trusted main history")
    recompute(repo, state, allowed)
    tags = set(git(repo, "tag", "--list").splitlines())
    present = [entry for entry in proposals(state) if tag_name(entry) in tags]
    for entry in present:
        verify_tag(repo, tag_name(entry), state)
    if len(present) != len(proposals(state)):
        # No older new version may be created after a later main/tag was observed.
        fresh(repo, sha, main, allowed)


def prepare(repo, sha, main, *, request=api, evidence=collect, consumer=public_verify):
    code_sha = git(repo, "rev-parse", "HEAD").strip()
    if not ancestor(repo, code_sha, main):
        raise ValueError("publisher code must come from trusted main")
    if code_sha != sha:
        # Recovery may target an older signed batch. The current implementation
        # executing with secrets must ALSO have its own exact-green main evidence.
        evidence(REPO, code_sha, "main", "publisher-code")
    history = recorded(repo)
    matching = [state for state in history if state["plan"]["sha"] == sha]
    if sha != main and not matching:
        raise ValueError("stale main candidate has no signed batch to resume; latest green main must plan accumulated changes")
    known = releases(request)
    queue = []
    existing_tags = set(git(repo, "tag", "--list").splitlines())
    candidates = history if sha == main else matching
    for state in candidates:
        missing = any(tag_name(entry) not in existing_tags for entry in proposals(state))
        for entry in proposals(state):
            release = known.get(tag_name(entry))
            if release is None:
                missing = True
            else:
                check_release(release, entry)
        if missing:
            validate_batch(repo, state, main)
            evidence(REPO, state["plan"]["sha"], "main", "publisher-recovery")
            queue.append(state)
        elif state in matching:
            validate_batch(repo, state, main)
            evidence(REPO, state["plan"]["sha"], "main", "publisher-completed-recheck")
            # Tags exist: public proxy reads here cannot cache a nonexistent version.
            # A prior consumer-verification failure is retried without another bump.
            consumer(state)
    if sha == main:
        evidence(REPO, sha, "main", "publisher")
        state = fresh(repo, sha, main)
        if proposals(state):
            queue.append(state)
    # Ordering follows ancestry (not lexicographic SHA). All publication jobs share one lock.
    queue.sort(key=lambda item: int(git(repo, "rev-list", "--count", item["plan"]["sha"]).strip()))
    return {"candidate": sha, "batches": queue}


def publish(repo, manifest, token, secret, *, request=api, evidence=collect,
            tags=push_tags, release=publish_releases, consumer=public_verify):
    # GH_TOKEN remains the ordinary read-only Actions token throughout this job.
    main = main_sha(request)
    code_sha = git(repo, "rev-parse", "HEAD").strip()
    if not ancestor(repo, code_sha, main):
        raise ValueError("publisher code is no longer on trusted main history")
    evidence(REPO, code_sha, "main", "publisher-code-before-write")
    for state in manifest["batches"]:
        validate_batch(repo, state, main)
        evidence(REPO, state["plan"]["sha"], "main", "publisher-before-write")
    # No private key file, tag, API mutation or public-version probe before validation.
    for state in manifest["batches"]:
        # Main may have advanced while another recovered batch was completing.
        validate_batch(repo, state, main_sha(request))
        print(f"Publishing/resuming {state['plan']['sha']} plan {state['digest']} ({len(proposals(state))} modules)", flush=True)
        print("Publisher stage: signed atomic tag push and remote reconciliation", flush=True)
        tags(repo, state, secret, token)
        # A complete GitHub release set is the durable completion checkpoint:
        # every public build must pass BEFORE publishing any release in this batch.
        # If verification fails, missing releases keep this signed plan recoverable.
        print("Publisher stage: public Go proxy/checksum downloads, module builds and exact CLI install", flush=True)
        consumer(state)
        print("Publisher stage: idempotent App-created GitHub prereleases", flush=True)
        release(state, token)
        print(f"Confirmed signed refs, exact GitHub notes and public Go builds: {state['digest']}", flush=True)


def target():
    if os.environ["GITHUB_REPOSITORY"] != REPO or os.environ["GITHUB_REF"] != "refs/heads/main":
        raise ValueError("publisher runs only in arkoren/arkoren on main")
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    sha, branch = select_target(os.environ["GITHUB_EVENT_NAME"], event, REPO,
                                os.environ["GITHUB_REF"], os.environ["GITHUB_SHA"])
    if branch != "main":
        raise ValueError("publisher requires main")
    return sha


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("prepare", "publish", "preflight"))
    parser.add_argument("manifest", type=Path)
    args = parser.parse_args()
    sha = target()
    print(f"Publisher stage: {args.mode}; exact event candidate {sha}", flush=True)
    if args.mode == "prepare":
        manifest = prepare(Path.cwd(), sha, main_sha())
        args.manifest.write_text(json.dumps(manifest, sort_keys=True))
        changed = bool(manifest["batches"])
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            output.write(f"changed={'true' if changed else 'false'}\n")
        message = f"Validated exact-green main {sha}: {len(manifest['batches'])} publication batches."
        if not changed:
            message += " No-op: no App token, signing key file, tag or release needed."
    else:
        manifest = json.loads(args.manifest.read_text())
        if manifest["candidate"] != sha:
            raise ValueError("manifest target differs from trusted event")
        app_identity(os.environ["APP_SLUG"], os.environ["INSTALLATION_ID"], os.environ["APP_TOKEN"])
        if args.mode == "preflight":
            preflight(os.environ["SIGNING_KEY"])
            message = "App identity/repository scope and dedicated SSH signing-key match verified; local-only signature passed; no remote writes."
        else:
            publish(Path.cwd(), manifest, os.environ["APP_TOKEN"], os.environ["SIGNING_KEY"])
            message = "Publication and public-consumer verification completed for every approved batch."
    print(message)
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as output:
        output.write(message + "\n")


if __name__ == "__main__":
    # Unwind private temporary files on runner termination as well as normal errors.
    signal.signal(signal.SIGTERM, lambda *_: sys.exit("publisher terminated; resume the same SHA"))
    try:
        main()
    except (ValueError, RuntimeError) as error:
        # These helpers expose only local validation messages; remote command
        # output is suppressed at the I/O boundary.
        sys.exit(f"module publication blocked: {error}")
    except Exception:
        sys.exit("module publication blocked; see the last validated stage; retry the same SHA for signed-batch recovery (remote error text suppressed)")
