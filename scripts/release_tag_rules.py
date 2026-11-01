#!/usr/bin/env python3
"""Administrative one-time fail-closed activation; never used by the publisher App."""
import argparse
import json
from pathlib import Path

from release_evidence import collect
from release_publish_io import API, api, command
from release_publish_plan import POLICY
from release_preview import module_policy

IMMUTABLE = 26273278
MAIN = 26273253
GLOBAL = ["refs/tags/v*", "refs/tags/**/v*"]
LOCK_NAME = "Lock unapproved version tag creation"
APP_NAME = "Arkoren Releases canonical v0 tag creation"
ACTOR = [{"actor_id": 5540931, "actor_type": "Integration", "bypass_mode": "always"}]
FIELDS = ("name", "target", "enforcement", "conditions", "rules", "bypass_actors")


def shape(record):
    return {key: record[key] for key in FIELDS}


def creation_rules(modules):
    patterns = [f"refs/tags/{module}/v0.*" for module in sorted(modules)]
    common = {"target": "tag", "enforcement": "active", "rules": [{"type": "creation"}]}
    locked = dict(common, name=LOCK_NAME, bypass_actors=[], conditions={"ref_name": {"include": GLOBAL, "exclude": patterns}})
    app = dict(common, name=APP_NAME, bypass_actors=ACTOR, conditions={"ref_name": {"include": patterns, "exclude": []}})
    return locked, app


def protected_main(record):
    if record["bypass_actors"] or record["enforcement"] != "active":
        raise ValueError("main protection has changed")
    checks = next(rule["parameters"] for rule in record["rules"] if rule["type"] == "required_status_checks")
    if (not checks["strict_required_status_checks_policy"]
            or {item["context"] for item in checks["required_status_checks"]} != {"CI gate", "website"}):
        raise ValueError("main must retain strict CI gate AND website")


def immutable(record, creation):
    expected = {"deletion", "update", "non_fast_forward"} | ({"creation"} if creation else set())
    if (record["bypass_actors"] or record["target"] != "tag" or record["enforcement"] != "active"
            or record["conditions"] != {"ref_name": {"include": GLOBAL, "exclude": []}}
            or {rule["type"] for rule in record["rules"]} != expected):
        raise ValueError("immutable tag protection differs from approved policy")


def mutation(method, endpoint, payload):
    # Administrative existing gh login only. No new App permission is requested.
    return json.loads(command(["gh", "api", "--method", method, endpoint, "--input", "-"],
                              data=json.dumps(payload).encode()))


def activate(modules, output, request=api, write=mutation, evidence=collect):
    output.mkdir(parents=True, exist_ok=False)
    before = {"main": request(f"{API}/rulesets/{MAIN}"),
              "tags": request(f"{API}/rulesets/{IMMUTABLE}"),
              "inventory": request(API + "/rulesets?per_page=100")}
    (output / "before.json").write_text(json.dumps(before, indent=2) + "\n")
    protected_main(before["main"])
    immutable(before["tags"], creation=True)
    expected = creation_rules(modules)
    if any(item["name"] in (LOCK_NAME, APP_NAME) for item in before["inventory"]):
        raise ValueError("activation rulesets already exist; inspect/read-only check, do not duplicate")
    sha = request(API + "/git/ref/heads/main")["object"]["sha"]
    proof = evidence("arkoren/arkoren", sha, "main", "tag-policy-activation")
    (output / "evidence.json").write_text(json.dumps(proof, indent=2) + "\n")
    created = []
    opened = False
    try:
        # Both new active locks must exist BEFORE any original rule is removed.
        for payload in expected:
            result = write("POST", API + "/rulesets", payload)
            created.append(result["id"])
            (output / "created-ids.json").write_text(json.dumps(created) + "\n")
            if shape(request(f"{API}/rulesets/{result['id']}")) != payload:
                raise ValueError("creation lock readback differs")
        if request(f"{API}/rulesets/{MAIN}") != before["main"]:
            raise ValueError("main changed during activation")
        updated = shape(before["tags"])
        updated["rules"] = [rule for rule in updated["rules"] if rule["type"] != "creation"]
        opened = True  # A lost response may still have applied the PUT.
        write("PUT", f"{API}/rulesets/{IMMUTABLE}", updated)
        immutable(request(f"{API}/rulesets/{IMMUTABLE}"), creation=False)
        for identifier, payload in zip(created, expected):
            if shape(request(f"{API}/rulesets/{identifier}")) != payload:
                raise ValueError("creation policy changed during activation")
        if request(f"{API}/rulesets/{MAIN}") != before["main"]:
            raise ValueError("main changed during activation")
    except Exception:
        # Restore the original creation lock FIRST; never open an immutability gap.
        if opened:
            write("PUT", f"{API}/rulesets/{IMMUTABLE}", shape(before["tags"]))
        # Leave additional locks active for inspection: failure remains fail-closed.
        raise
    after = {"main": request(f"{API}/rulesets/{MAIN}"),
             "tags": request(f"{API}/rulesets/{IMMUTABLE}"),
             "creation": [request(f"{API}/rulesets/{identifier}") for identifier in created]}
    (output / "after.json").write_text(json.dumps(after, indent=2) + "\n")
    return created


def check(modules, request=api):
    protected_main(request(f"{API}/rulesets/{MAIN}"))
    immutable(request(f"{API}/rulesets/{IMMUTABLE}"), creation=False)
    records = request(API + "/rulesets?per_page=100")
    for expected in creation_rules(modules):
        matches = [item for item in records if item["name"] == expected["name"]]
        if len(matches) != 1 or shape(request(f"{API}/rulesets/{matches[0]['id']}")) != expected:
            raise ValueError("creation ruleset mismatch; review inventory/allowlist and exact Integration App ID")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("check", "activate"))
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    modules = module_policy(POLICY.read_text())[0]
    if args.mode == "activate":
        if args.output is None:
            raise ValueError("activation requires a new private snapshot directory")
        print("Activated creation rulesets:", activate(modules, args.output))
    else:
        check(modules)
        print("Canonical v0 creation: only App 5540931; all version-tag updates/deletions non-bypassable; main protected.")


if __name__ == "__main__":
    main()
