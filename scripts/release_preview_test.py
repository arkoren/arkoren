#!/usr/bin/env python3
"""Synthetic repositories only: all planner tests invoke real pinned git-cliff."""
import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parent


def load(name):
    spec = importlib.util.spec_from_file_location(name, SCRIPTS / f"{name}.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class PlannerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="release-fixture-")
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name)
        self.git("init", "-b", "main")
        self.git("config", "user.name", "Fixture")
        self.git("config", "user.email", "fixture@example.invalid")
        self.git("config", "commit.gpgsign", "false")
        self.git("config", "tag.gpgsign", "false")
        self.modules = ["core", "cmd/arkoren"] + [f"driver/d{i}" for i in range(36)] + [f"foundation/f{i}" for i in range(6)]
        self.commit("chore: baseline", {f"{m}/go.mod": f"module arkoren.dev/{m}\n" for m in self.modules})
        self.base = self.git("rev-parse", "HEAD").strip()
        for m in self.modules:
            self.git("tag", f"{m}/v0.1.0")

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

    def plan(self, sha=None, initial=()):
        planner = load("release_preview")
        return planner.plan(self.repo, sha or self.git("rev-parse", "HEAD").strip(), initial)

    def entries(self, plan=None):
        return {e["directory"]: e for e in (plan or self.plan())["modules"]}

    def test_shared_baseline_no_phantom_and_repeatable(self):
        self.assertEqual(len(self.plan()["modules"]), 44)
        self.assertTrue(all(e["next_version"] is None for e in self.plan()["modules"]))
        self.assertEqual(self.plan(), self.plan())

    def test_file_scope_multimodule_and_accumulated_changes(self):
        fix = self.commit("fix(api): repair", {"core/a.go": "fix", "driver/d0/a.go": "fix"})
        feat = self.commit("feat(api): add", {"core/b.go": "feat"})
        entries = self.entries()
        self.assertEqual(entries["core"]["next_version"], "v0.2.0")
        self.assertEqual(entries["driver/d0"]["next_version"], "v0.1.1")
        self.assertIsNone(entries["driver/d1"]["next_version"])
        self.assertEqual({c["id"] for c in entries["core"]["commits"]}, {fix, feat})

    def test_breaking_other_module_never_leaks_through_cliff_filters(self):
        self.commit("fix!: driver contract", {"driver/d0/a.go": "breaking"})
        fix = self.commit("fix: core repair", {"core/a.go": "fix"})
        entries = self.entries()
        self.assertEqual(entries["core"]["next_version"], "v0.1.1")
        self.assertNotIn("driver contract", entries["core"]["notes"])
        self.assertEqual([c["id"] for c in entries["core"]["commits"]], [fix])
        self.assertEqual(entries["driver/d0"]["next_version"], "v0.2.0")

    def test_breaking_footer_and_bang_preserve_notes(self):
        for message in ("fix!: change contract", "fix: change\n\nBREAKING CHANGE: old callers must migrate"):
            with self.subTest(message=message):
                self.commit(message, {"core/a.go": message})
                entry = self.entries()["core"]
                self.assertEqual(entry["next_version"], "v0.2.0")
                self.assertIn("BREAKING", entry["notes"])

    def test_docs_tests_website_skip_embedded_docs_patch(self):
        self.commit("feat: docs only", {"website/a.md": "web", "core/README.md": "docs", "core/a_test.go": "test", "core/testdata/a.json": "{}", ".agents/a.md": "rules"})
        self.assertTrue(all(e["next_version"] is None for e in self.plan()["modules"]))
        self.commit("docs(cli): guidance", {"cmd/arkoren/skills/arkoren/guide.md": "embedded"})
        self.assertEqual(self.entries()["cmd/arkoren"]["next_version"], "v0.1.1")

    def test_unknown_and_revert_are_conservative_patch(self):
        self.commit("ordinary update", {"core/a.go": "old"})
        self.commit('Revert "ordinary update"', {"core/a.go": "reverted"})
        entry = self.entries()["core"]
        self.assertEqual(entry["next_version"], "v0.1.1")
        self.assertEqual(len(entry["commits"]), 2)

    def test_feature_then_revert_does_not_claim_semantic_undo(self):
        self.commit("feat: feature", {"core/a.go": "feature"})
        self.commit('revert: remove feature\n\nThis reverts the earlier feature.', {"core/a.go": "removed"})
        self.assertEqual(self.entries()["core"]["next_version"], "v0.2.0")

    def test_divergent_versions_unreachable_tag_and_detached_sha(self):
        self.commit("fix: first", {"core/a.go": "first"})
        self.git("tag", "core/v0.1.3")
        target = self.commit("fix: next", {"core/a.go": "next"})
        self.git("checkout", "-b", "future")
        self.commit("feat: future", {"core/a.go": "future"})
        self.git("tag", "core/v0.9.0")
        self.git("checkout", "--detach", target)
        self.assertEqual(self.entries(self.plan(target))["core"]["next_version"], "v0.1.4")

    def test_ordinary_merge_does_not_duplicate_commits(self):
        self.git("checkout", "-b", "feature")
        commit = self.commit("feat: feature", {"core/a.go": "feature"})
        self.git("checkout", "main")
        self.commit("ci: elsewhere", {"workflow.txt": "ci"})
        self.git("merge", "--no-ff", "feature", "-m", "Merge feature")
        entry = self.entries()["core"]
        self.assertEqual([c["id"] for c in entry["commits"]], [commit])

    def test_missing_baseline_requires_deliberate_new_module(self):
        self.git("tag", "-d", "core/v0.1.0")
        with self.assertRaisesRegex(ValueError, "baseline"):
            self.plan()
        with self.assertRaisesRegex(ValueError, "existing module"):
            self.plan(initial=["core"])

    def test_no_release_history_and_duplicate_initial_candidates_fail(self):
        for module in self.modules:
            self.git("tag", "-d", module + "/v0.1.0")
        with self.assertRaisesRegex(ValueError, "baseline"):
            self.plan()
        with self.assertRaisesRegex(ValueError, "distinct"):
            self.plan(initial=["core", "core"])

    def test_noncanonical_tracked_module_and_duplicate_module_path_fail(self):
        self.commit("chore: invalid inventory", {"driver/Bad/go.mod": "module private"})
        with self.assertRaisesRegex(ValueError, "noncanonical"):
            self.plan()
        self.git("rm", "driver/Bad/go.mod")
        self.git("commit", "-m", "chore: remove invalid inventory")
        self.commit("fix: duplicate path", {"driver/d0/go.mod": "module arkoren.dev/core\n"})
        with self.assertRaisesRegex(ValueError, "unexpected module path"):
            self.plan()

    def test_new_module_explicit_initial_entry(self):
        self.commit("feat: new module", {"ark/new/go.mod": "module arkoren.dev/ark/new\n", "ark/new/new.go": "new"})
        with self.assertRaisesRegex(ValueError, "baseline"):
            self.plan()
        entry = self.entries(self.plan(initial=["ark/new"]))["ark/new"]
        self.assertIsNone(entry["current_version"])
        self.assertEqual(entry["next_version"], "v0.1.0")

    def test_malformed_ambiguous_and_major_tags_fail_closed(self):
        for tag in ("core/v0.01.1", "core/v0.1.0-beta", "core/v1.0.0", "core/v0.1.1"):
            with self.subTest(tag=tag):
                self.git("tag", tag)
                with self.assertRaises(ValueError):
                    self.plan()
                self.git("tag", "-d", tag)

    def test_incomparable_release_histories_rejected(self):
        self.git("checkout", "-b", "left")
        self.commit("fix: left", {"core/left.go": "left"})
        self.git("tag", "core/v0.1.1")
        self.git("checkout", "main")
        self.commit("fix: right", {"core/right.go": "right"})
        self.git("tag", "core/v0.1.2")
        self.git("merge", "--no-ff", "left", "-m", "Merge left")
        with self.assertRaisesRegex(ValueError, "ambiguous"):
            self.plan()

    def test_cliff_ignore_cannot_silently_drop_payload_changes(self):
        commit = self.commit("fix: repair", {"core/a.go": "fix"})
        self.commit("ci: ignore", {".cliffignore": commit + "\n"})
        with self.assertRaisesRegex(ValueError, "omitted or added"):
            self.plan()

    def test_merge_resolution_payload_requires_explicit_commit(self):
        self.commit("fix: base", {"core/a.go": "base"})
        self.git("checkout", "-b", "left")
        self.commit("fix: left", {"core/a.go": "left"})
        self.git("checkout", "main")
        self.commit("fix: right", {"core/a.go": "right"})
        with self.assertRaises(subprocess.CalledProcessError):
            self.git("merge", "left")
        self.commit("Merge resolution", {"core/a.go": "resolution"})
        with self.assertRaisesRegex(ValueError, "merge-only"):
            self.plan()

    def test_git_cliff_baseline_with_no_commits_does_not_bump(self):
        planner = load("release_preview")
        context = planner.cliff(self.repo, "--tag-pattern", "^core/v0\\.1\\.0$", f"{self.base}..{self.base}", "--context")
        self.assertEqual(planner.cliff(self.repo, "--from-context", "-", "--bumped-version", context=context).strip(), "core/v0.1.0")

    def test_invalid_sha_shallow_and_untracked_inventory(self):
        for sha in ("HEAD", "main", self.base + "..HEAD", "a" * 40):
            with self.assertRaises((ValueError, subprocess.CalledProcessError)):
                self.plan(sha)
        (self.repo / "driver/private").mkdir()
        (self.repo / "driver/private/go.mod").write_text("module private")
        self.assertEqual(len(self.plan()["modules"]), 44)
        (self.repo / ".git/shallow").write_text(self.base + "\n")
        with self.assertRaisesRegex(ValueError, "shallow"):
            self.plan()


if __name__ == "__main__":
    unittest.main()
