#!/usr/bin/env python3
"""Real git/cliff/signatures; disposable remotes and fake GitHub side effects only."""
import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import release_publish as publisher
import release_publish_io as io
import release_publish_plan as plans
import release_tag_rules as rules
from release_preview import plan


class FakeGitHub:
    def __init__(self):
        self.records = {}
        self.writes = []
        self.fail_after = None

    def __call__(self, endpoint, payload=None, token=None):
        if payload is not None:
            if self.fail_after == len(self.writes):
                self.fail_after = None
                raise RuntimeError("network interruption")
            if endpoint != io.API + "/releases" or token != "fixture-app-token":
                raise AssertionError("only App-created releases may emit downstream events")
            self.writes.append(copy.deepcopy(payload))
            self.records[payload["tag_name"]] = dict(payload, assets=[])
            return self.records[payload["tag_name"]]
        if "/releases?" in endpoint:
            return list(self.records.values())
        raise AssertionError(endpoint)


class PublisherTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="publisher-fixture-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "source"
        self.repo.mkdir()
        self.git("init", "-b", "main")
        self.git("config", "user.name", "Fixture")
        self.git("config", "user.email", "fixture@example.invalid")
        self.git("config", "commit.gpgsign", "false")
        self.git("config", "tag.gpgsign", "false")
        self.modules = ["core", "cmd/arkoren", "driver/example", "foundation/example"]
        self.commit("chore: baseline", {module + "/go.mod": "module arkoren.dev/" + module + "\n" for module in self.modules})
        for module in self.modules:
            self.git("tag", module + "/v0.1.0")
        self.remote = self.root / "remote.git"
        subprocess.run(["git", "clone", "--bare", str(self.repo), str(self.remote)], check=True, capture_output=True)
        self.git("remote", "add", "origin", str(self.remote))
        key = self.root / "fixture-key"
        subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(key)], check=True)
        self.secret = key.read_text()
        self.trust = self.root / "allowed-signers"
        public = subprocess.check_output(["ssh-keygen", "-y", "-f", str(key)], text=True).strip()
        self.trust.write_text(plans.PRINCIPAL + " " + public + "\n")
        self.policy = self.root / "policy.json"
        self.policy.write_text(json.dumps(self.modules))
        for target, name, value in ((plans, "TRUST", self.trust), (io, "TRUST", self.trust), (plans, "POLICY", self.policy)):
            manager = patch.object(target, name, value)
            manager.start()
            self.addCleanup(manager.stop)
        self.github = FakeGitHub()

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.repo, text=True, stderr=subprocess.PIPE)

    def commit(self, message, files):
        for name, text in files.items():
            file = self.repo / name
            file.parent.mkdir(parents=True, exist_ok=True)
            file.write_text(text)
        self.git("add", ".")
        self.git("commit", "-m", message)
        return self.git("rev-parse", "HEAD").strip()

    def state(self, message="fix: repair", files=None):
        sha = self.commit(message, files or {"core/payload.go": "package core\n"})
        return plans.fresh(self.repo, sha, sha)

    def push(self, state):
        io.push_tags(self.repo, state, self.secret, "fixture-app-token")

    def release(self, state):
        io.publish_releases(state, "fixture-app-token", self.github)

    def test_no_payload_no_signer_or_writes(self):
        state = self.state("ci: publisher only", {"scripts/a.py": "# no payload"})
        self.assertFalse(plans.proposals(state))
        self.assertEqual(self.github.writes, [])
        with patch.object(publisher, "recorded", return_value=[]):
            manifest = publisher.prepare(self.repo, state["plan"]["sha"], state["plan"]["sha"],
                                         request=self.github, evidence=lambda *args: None)
        self.assertEqual(manifest["batches"], [])

    def test_independent_same_commit_signed_batch_and_recompute_after_tags(self):
        state = self.state(files={"core/a.go": "core", "driver/example/a.go": "driver"})
        self.push(state)
        self.assertEqual(len(plans.proposals(state)), 2)
        self.assertEqual(plans.recorded(self.repo), [state])
        self.assertEqual(plans.recompute(self.repo, state), state)
        self.assertTrue(io.reconcile(self.repo, state))
        self.assertFalse(any(entry["next_version"] for entry in plan(self.repo, state["plan"]["sha"])["modules"]))

    def test_partial_release_failure_resumes_original_no_extra_version(self):
        state = self.state("feat: additions", {"core/a.go": "core", "driver/example/a.go": "driver"})
        self.push(state)
        self.github.fail_after = 1
        with self.assertRaises(RuntimeError):
            self.release(state)
        recovered = plans.recorded(self.repo)[0]
        plans.recompute(self.repo, recovered)
        self.release(recovered)
        self.release(recovered)
        self.assertEqual(len(self.github.writes), 2)
        self.assertTrue(all(item["prerelease"] and item["make_latest"] == "false" for item in self.github.writes))
        self.assertEqual({item["body"] for item in self.github.writes}, {entry["notes"] for entry in plans.proposals(state)})

    def test_already_complete_no_app_on_duplicate_event(self):
        state = self.state()
        self.push(state)
        self.release(state)
        reads = []
        manifest = publisher.prepare(self.repo, state["plan"]["sha"], state["plan"]["sha"],
                                     request=self.github, evidence=lambda *args: None, consumer=reads.append)
        self.assertEqual(manifest["batches"], [])
        self.assertEqual(reads, [state])

    def test_lost_atomic_push_response_reconciles_exact_remote_objects(self):
        state = self.state()
        original = io.command
        def uncertain(args, **kwargs):
            result = original(args, **kwargs)
            if args[:3] == ["git", "push", "--atomic"]:
                raise RuntimeError("response lost after successful push")
            return result
        with patch.object(io, "command", uncertain):
            self.push(state)
        self.assertTrue(io.reconcile(self.repo, state))

    def test_atomic_batch_rejection_creates_no_remote_tags(self):
        state = self.state(files={"core/a.go": "core", "driver/example/a.go": "driver"})
        hook = self.remote / "hooks/pre-receive"
        hook.write_text("#!/bin/sh\nexit 1\n")
        hook.chmod(0o755)
        with self.assertRaises(RuntimeError):
            self.push(state)
        self.assertEqual(io.remote_refs(self.repo, [plans.tag_name(e) for e in plans.proposals(state)]), {})

    def test_tampered_notes_digest_or_sha_refused(self):
        state = self.state()
        for field, value in (("notes", "tampered"), ("next_version", "v0.9.9"), ("sha", "0" * 40)):
            altered = copy.deepcopy(state)
            next(e for e in altered["plan"]["modules"] if e["next_version"])[field] = value
            with self.assertRaises(ValueError):
                plans.recompute(self.repo, altered)

    def test_wrong_signer_and_tampered_tag_refused(self):
        state = self.state()
        self.push(state)
        name = plans.tag_name(plans.proposals(state)[0])
        bad = self.root / "untrusted"
        bad.write_text("")
        with self.assertRaises(subprocess.CalledProcessError):
            plans.verify_tag(self.repo, name, state, trust=bad)
        self.git("tag", "-d", name)
        self.git("tag", "-a", name, "-m", plans.annotation(state))
        with self.assertRaises(subprocess.CalledProcessError):
            plans.verify_tag(self.repo, name, state)

    def test_remote_tag_conflict_never_moved(self):
        state = self.state()
        self.push(state)
        name = plans.tag_name(plans.proposals(state)[0])
        self.git("tag", "-d", name)
        self.git("tag", name)
        with self.assertRaises(ValueError):
            io.reconcile(self.repo, state)

    def test_valid_signature_wrong_commit_target_refused(self):
        state = self.state()
        name = plans.tag_name(plans.proposals(state)[0])
        baseline = self.git("rev-parse", "core/v0.1.0^{commit}").strip()
        def sign(env):
            io.command(["git", "tag", "--cleanup=verbatim", "-s", name, baseline, "-F", "-"],
                       cwd=self.repo, env=env, data=plans.annotation(state).encode())
        io.with_signer(self.secret, sign)
        with self.assertRaisesRegex(ValueError, "target"):
            plans.verify_tag(self.repo, name, state)

    def test_notes_can_quote_ssh_signature_delimiters(self):
        state = self.state("fix: parse -----BEGIN SSH SIGNATURE----- text")
        self.push(state)
        self.assertEqual(plans.recorded(self.repo), [state])

    def test_newer_main_stops_unsigned_batch_before_effects(self):
        state = self.state()
        main = self.commit("ci: later main", {"scripts/later.py": ""})
        with self.assertRaisesRegex(ValueError, "stale"):
            publisher.validate_batch(self.repo, state, main)

    def test_old_fully_signed_batch_can_finish_releases_after_main_advances(self):
        state = self.state()
        self.push(state)
        main = self.commit("ci: later main", {"scripts/later.py": ""})
        publisher.validate_batch(self.repo, state, main)
        manifest = publisher.prepare(self.repo, state["plan"]["sha"], main, request=self.github, evidence=lambda *args: None)
        self.assertEqual(manifest["batches"], [state])
        self.release(state)

    def test_old_batch_recovery_also_requires_exact_green_executing_code(self):
        state = self.state()
        self.push(state)
        main = self.commit("ci: newer publisher implementation", {"scripts/publisher.py": ""})
        observed = []
        def evidence(repo, sha, branch, mode):
            observed.append((sha, mode))
            if mode == "publisher-code":
                raise ValueError("current publisher code CI is pending")
        with self.assertRaisesRegex(ValueError, "publisher code CI"):
            publisher.prepare(self.repo, state["plan"]["sha"], main, request=self.github, evidence=evidence)
        self.assertEqual(observed, [(main, "publisher-code")])
        self.assertEqual(self.github.writes, [])

    def test_failed_pending_missing_evidence_stops_before_any_side_effect(self):
        state = self.state()
        for status in ("failed", "cancelled", "pending", "missing", "wrong-ref"):
            effects = []
            def reject(*args):
                raise ValueError(status)
            with self.assertRaises(ValueError):
                publisher.publish(self.repo, {"batches": [state]}, "not-used", "not-used",
                                  request=lambda path: {"object": {"sha": state["plan"]["sha"]}}, evidence=reject,
                                  tags=lambda *args: effects.append("tag"), release=lambda *args: effects.append("release"), consumer=lambda *args: effects.append("proxy"))
            self.assertEqual(effects, [])

    def test_unknown_root_hatchet_major_namespaces_fail(self):
        for tag in ("v0.1.0", "driver/hatchet/v0.1.0", "unknown/v0.1.0", "core/v1.0.0"):
            self.git("tag", tag)
            with self.assertRaises(ValueError):
                plans.recorded(self.repo)
            self.git("tag", "-d", tag)

    def test_inventory_addition_requires_explicit_policy_review(self):
        state = self.state()
        for allowed in (self.modules + ["driver/hatchet"], self.modules[:-1], self.modules + ["core"]):
            with self.assertRaises(ValueError):
                plans.policy(state["plan"], allowed)

    def test_signature_preflight_only_temporary_repo_and_private_key_cleanup(self):
        observed = []
        original = io.command
        def observe(args, **kwargs):
            if args[:2] == ["ssh-keygen", "-y"]:
                key = Path(args[-1])
                self.assertEqual(key.stat().st_mode & 0o777, 0o600)
                self.assertEqual(key.parent.stat().st_mode & 0o777, 0o700)
                observed.append(key)
            self.assertNotIn(self.secret, repr(args))
            return original(args, **kwargs)
        before = self.git("tag", "--list")
        with patch.object(io, "command", observe):
            io.preflight(self.secret)
        self.assertTrue(observed)
        self.assertTrue(all(not key.exists() for key in observed))
        self.assertEqual(before, self.git("tag", "--list"))

    def test_private_key_cleanup_on_signing_error(self):
        observed = []
        def fail(env):
            observed.append(Path(env["GIT_CONFIG_VALUE_1"]))
            raise RuntimeError("fixture fail")
        with self.assertRaises(RuntimeError):
            io.with_signer(self.secret, fail)
        self.assertFalse(observed[0].exists())

    def test_public_verification_normal_proxy_exact_cli_no_main_or_exemptions(self):
        state = self.state(files={"cmd/arkoren/skills/guide.md": "embedded"})
        calls = []
        def runner(args, **kwargs):
            calls.append((args, kwargs))
            if args[:3] == ["go", "mod", "download"]:
                return json.dumps({"Sum": "fixture", "GoModSum": "fixture", "Dir": str(self.repo)}).encode()
            return b""
        io.public_verify(state, runner=runner, pause=lambda _: None)
        self.assertEqual(calls[-1][0], ["go", "install", "arkoren.dev/cmd/arkoren@v0.1.1"])
        for args, kwargs in calls:
            self.assertNotIn("@main", " ".join(args))
            self.assertEqual(kwargs["env"]["GOWORK"], "off")
            self.assertEqual(kwargs["env"]["GOPROXY"], "https://proxy.golang.org")
            self.assertEqual(kwargs["env"]["GOSUMDB"], "sum.golang.org")
            self.assertEqual(kwargs["env"]["GONOSUMDB"], "")

    def test_public_failure_retry_same_signed_versions(self):
        state = self.state()
        self.push(state)
        self.release(state)
        calls = []
        def failed(args, **kwargs):
            calls.append(args)
            raise RuntimeError("not yet available")
        with self.assertRaisesRegex(RuntimeError, "retry same SHA"):
            io.public_verify(state, runner=failed, pause=lambda _: None)
        self.assertEqual(len(calls), 6)
        recovered = plans.recorded(self.repo)[0]
        self.assertEqual(recovered, state)
        self.release(recovered)
        self.assertEqual(len(self.github.writes), 1)

    def test_public_build_failure_leaves_recoverable_release_checkpoint(self):
        state = self.state()
        effects = []
        def reader(endpoint):
            if endpoint.endswith("heads/main"):
                return {"object": {"sha": state["plan"]["sha"]}}
            return self.github(endpoint)
        def failed_consumer(*args):
            effects.append("public-build-failed")
            raise RuntimeError("public build unavailable")
        with self.assertRaises(RuntimeError):
            publisher.publish(self.repo, {"batches": [state]}, "fixture-app-token", self.secret,
                              request=reader, evidence=lambda *args: None,
                              tags=lambda *args: self.push(state), release=lambda *args: self.release(state), consumer=failed_consumer)
        self.assertEqual(self.github.writes, [])
        manifest = publisher.prepare(self.repo, state["plan"]["sha"], state["plan"]["sha"], request=reader, evidence=lambda *args: None)
        self.assertEqual(manifest["batches"], [state])
        publisher.publish(self.repo, manifest, "fixture-app-token", self.secret,
                          request=reader, evidence=lambda *args: None,
                          tags=lambda *args: self.push(state), release=lambda *args: self.release(state), consumer=lambda *args: effects.append("public-build-passed"))
        self.assertEqual(effects, ["public-build-failed", "public-build-passed"])
        self.assertEqual(len(self.github.writes), 1)
        self.assertEqual(plans.recorded(self.repo), [state])

    def test_proxy_not_probed_before_confirmed_tag_push(self):
        state = self.state()
        calls = []
        def failed_tags(*args):
            calls.append("tags")
            raise RuntimeError("push rejected")
        with self.assertRaises(RuntimeError):
            publisher.publish(self.repo, {"batches": [state]}, "fixture", "fixture",
                              request=lambda _: {"object": {"sha": state["plan"]["sha"]}}, evidence=lambda *args: None,
                              tags=failed_tags, release=lambda *args: calls.append("release"), consumer=lambda *args: calls.append("proxy"))
        self.assertEqual(calls, ["tags"])

    def test_existing_release_conflict_stops_no_overwrite(self):
        state = self.state()
        self.push(state)
        self.release(state)
        next(iter(self.github.records.values()))["body"] = "foreign notes"
        with self.assertRaises(ValueError):
            self.release(state)
        self.assertEqual(len(self.github.writes), 1)

    def test_app_identity_repository_scope(self):
        request = lambda *args, **kwargs: {"total_count": 1, "repositories": [{"full_name": io.REPO}]}
        io.app_identity("arkoren-releases", "176874315", "fixture", request)
        for slug, installation in (("foreign", "176874315"), ("arkoren-releases", "5540931")):
            with self.assertRaises(ValueError):
                io.app_identity(slug, installation, "fixture", request)
        with self.assertRaises(ValueError):
            io.app_identity("arkoren-releases", "176874315", "fixture", lambda *args, **kwargs: {"total_count": 2, "repositories": []})


class RulesTests(unittest.TestCase):
    def setUp(self):
        self.main = {"name": "Protect main", "target": "branch", "enforcement": "active", "bypass_actors": [], "conditions": {},
                     "rules": [{"type": "required_status_checks", "parameters": {"strict_required_status_checks_policy": True,
                               "required_status_checks": [{"context": "CI gate"}, {"context": "website"}]}}]}
        self.original = {"name": "immutable", "target": "tag", "enforcement": "active", "bypass_actors": [],
                         "conditions": {"ref_name": {"include": rules.GLOBAL, "exclude": []}},
                         "rules": [{"type": item} for item in ("creation", "update", "deletion", "non_fast_forward")]}
        self.records = {rules.MAIN: self.main, rules.IMMUTABLE: self.original}
        self.writes = []
        self.fail_create = None

    def read(self, endpoint):
        if endpoint.endswith("heads/main"):
            return {"object": {"sha": "a" * 40}}
        if "?" in endpoint:
            return [dict(value, id=key) for key, value in self.records.items()]
        return copy.deepcopy(self.records[int(endpoint.rsplit("/", 1)[1])])

    def write(self, method, endpoint, payload):
        self.writes.append((method, copy.deepcopy(payload)))
        if method == "POST":
            if self.fail_create == len(self.writes):
                raise RuntimeError("fixture rejected Integration actor")
            identifier = 900 + len(self.writes)
        else:
            identifier = int(endpoint.rsplit("/", 1)[1])
        self.records[identifier] = copy.deepcopy(payload)
        return dict(payload, id=identifier)

    def test_two_locks_before_original_creation_removed_main_unchanged(self):
        with tempfile.TemporaryDirectory() as directory:
            ids = rules.activate(["core", "cmd/arkoren"], Path(directory) / "proof", self.read, self.write, lambda *args: {})
        self.assertEqual([method for method, _ in self.writes], ["POST", "POST", "PUT"])
        self.assertEqual(self.records[rules.MAIN], self.main)
        rules.immutable(self.records[rules.IMMUTABLE], creation=False)
        self.assertEqual(self.records[ids[0]]["bypass_actors"], [])
        self.assertEqual(self.records[ids[1]]["bypass_actors"], rules.ACTOR)
        rules.check(["core", "cmd/arkoren"], self.read)

    def test_rejected_app_lock_leaves_original_creation_locked(self):
        self.fail_create = 2
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(RuntimeError):
                rules.activate(["core"], Path(directory) / "proof", self.read, self.write, lambda *args: {})
        rules.immutable(self.records[rules.IMMUTABLE], creation=True)
        self.assertEqual(len(self.writes), 2)

    def test_lost_policy_update_response_restores_creation_lock_first(self):
        failed = []
        def uncertain(method, endpoint, payload):
            result = self.write(method, endpoint, payload)
            if method == "PUT" and not failed:
                failed.append(True)
                raise RuntimeError("response lost")
            return result
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(RuntimeError):
                rules.activate(["core"], Path(directory) / "proof", self.read, uncertain, lambda *args: {})
        rules.immutable(self.records[rules.IMMUTABLE], creation=True)
        self.assertEqual([method for method, _ in self.writes], ["POST", "POST", "PUT", "PUT"])

    def test_committed_allowlist_equals_actual_tracked_inventory(self):
        root = Path(__file__).resolve().parent.parent
        tracked = subprocess.check_output(["git", "ls-files", "*/go.mod"], cwd=root, text=True).splitlines()
        expected = sorted(path[:-7] for path in tracked if plans.re.fullmatch(r"(?:core|cmd/arkoren|(?:driver|foundation|ark)/[a-z0-9][a-z0-9-]*)/go.mod", path))
        self.assertEqual(sorted(json.loads((root / ".github/release-modules.json").read_text())), expected)


if __name__ == "__main__":
    unittest.main()
