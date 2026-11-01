"""Offline publication tests: no release, tag, branch or PR API is contacted."""

import base64
import copy
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("release_notes_pr", Path(__file__).with_name("release_notes_pr.py"))
SYNC = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SYNC)
FILE = "website/src/content/releases/core/v0.1.0.md"


class GitHub:
    """Model GitHub signing, branch leases and PR upserts at the transport seam."""

    def __init__(self):
        self.writes = []
        self.tip = None
        self.pull = None
        self.main = "main-sha"
        self.comparison = {"total_commits": 0, "commits": [], "files": []}
        self.is_signed = True
        self.has_race = False

    def request(self, endpoint, payload=None, method="GET"):
        if method != "GET":
            self.writes.append((endpoint, copy.deepcopy(payload), method))
        if endpoint == "installation/repositories?per_page=100":
            return {"total_count": 1, "repositories": [{"full_name": SYNC.REPO}]}
        if "/pulls?" in endpoint:
            return [self.pull] if self.pull else []
        if "/git/matching-refs/" in endpoint:
            return [{"ref": f"refs/heads/{SYNC.BRANCH}", "object": {"sha": self.tip["sha"]}}] if self.tip else []
        if endpoint.endswith("/commits/main"):
            return {"sha": self.main, "commit": {"tree": {"sha": "main-tree"}}}
        if "/commits/" in endpoint:
            return self.tip
        if "/compare/" in endpoint:
            return self.comparison
        if endpoint.endswith("/git/trees"):
            return {"sha": "desired-tree"}
        if endpoint.endswith("/git/commits"):
            return {"sha": "new-sha", "verification": {"verified": self.is_signed}}
        if "/git/refs" in endpoint:
            if self.has_race:
                raise RuntimeError("non-fast-forward")
            self.tip = {"sha": "new-sha", "author": {"login": SYNC.BOT}, "committer": {"login": SYNC.BOT},
                        "commit": {"verification": {"verified": True}, "tree": {"sha": "desired-tree"}}}
            return {}
        if "/pulls" in endpoint:
            self.pull = {"number": 7, "html_url": "https://github.com/arkoren/arkoren/pull/7",
                         "user": {"login": SYNC.BOT}, "base": {"ref": "main"},
                         "head": {"sha": "new-sha", "repo": {"full_name": SYNC.REPO}}}
            return self.pull
        raise AssertionError(endpoint)


class ReleaseNotesPRTest(unittest.TestCase):
    """Protect idempotency, ownership and failures before any remote mutation."""

    def setUp(self):
        self.github = GitHub()
        self.state = {"base": "main-sha", "records": [{"path": FILE, "content": base64.b64encode(b"notes\n").decode()}]}

    def test_no_changes_never_authenticates_or_calls_github(self):
        self.state["records"] = []
        self.assertIn("No changes", SYNC.publish(self.state, lambda *args: self.fail("unexpected API")))

    def test_new_publication_creates_signed_commit_and_one_review_pr(self):
        self.assertIn("Human review", SYNC.publish(self.state, self.github.request))
        self.assertEqual([item[0].split("/")[-1] for item in self.github.writes], ["trees", "commits", "refs", "pulls"])
        self.assertEqual(self.github.writes[1][1]["parents"], ["main-sha"])
        self.assertEqual(self.github.writes[0][1]["tree"][0]["content"], "notes\n")
        self.assertFalse(self.github.writes[-1][1]["maintainer_can_modify"])

    def test_identical_retry_updates_pr_without_another_commit(self):
        SYNC.publish(self.state, self.github.request)
        self.github.writes.clear()
        SYNC.publish(self.state, self.github.request)
        self.assertEqual([item[0].split("/")[-1] for item in self.github.writes], ["trees", "7"])

    def test_edited_notes_and_advanced_main_append_a_fast_forward_merge(self):
        SYNC.publish(self.state, self.github.request)
        self.github.writes.clear()
        self.github.tip["commit"]["tree"]["sha"] = "previous-tree"
        self.github.main = "advanced-main"
        self.state["base"] = "advanced-main"
        self.state["records"][0]["content"] = base64.b64encode(b"corrected notes").decode()
        SYNC.publish(self.state, self.github.request)
        self.assertEqual(self.github.writes[1][1]["parents"], ["new-sha", "advanced-main"])
        self.assertEqual(self.github.writes[2][1], {"sha": "new-sha", "force": False})

    def test_main_advance_requires_a_fresh_import(self):
        self.github.main = "advanced-main"
        with self.assertRaisesRegex(RuntimeError, "main advanced"):
            SYNC.publish(self.state, self.github.request)
        self.assertEqual(self.github.writes, [])

    def test_foreign_pr_is_not_modified(self):
        SYNC.publish(self.state, self.github.request)
        self.github.writes.clear()
        self.github.pull["user"]["login"] = "human"
        with self.assertRaisesRegex(RuntimeError, "foreign PR"):
            SYNC.publish(self.state, self.github.request)
        self.assertEqual(self.github.writes, [])

    def test_foreign_branch_is_not_overwritten(self):
        SYNC.publish(self.state, self.github.request)
        self.github.writes.clear()
        self.github.tip["author"]["login"] = "human"
        with self.assertRaisesRegex(RuntimeError, "foreign or unsigned"):
            SYNC.publish(self.state, self.github.request)
        self.assertEqual(self.github.writes, [])

    def test_branch_diff_outside_collection_is_rejected(self):
        SYNC.publish(self.state, self.github.request)
        self.github.writes.clear()
        self.github.comparison["files"] = [{"filename": "core/go.mod", "status": "modified"}]
        with self.assertRaisesRegex(RuntimeError, "outside managed"):
            SYNC.publish(self.state, self.github.request)
        self.assertEqual(self.github.writes, [])

    def test_missing_pending_publication_is_preserved(self):
        SYNC.publish(self.state, self.github.request)
        self.github.writes.clear()
        self.github.comparison["files"] = [{"filename": "website/src/content/releases/core/v0.2.0.md", "status": "added"}]
        with self.assertRaisesRegex(RuntimeError, "pending publication absent"):
            SYNC.publish(self.state, self.github.request)
        self.assertEqual(self.github.writes, [])

    def test_existing_branch_without_pr_recovers_after_interruption(self):
        SYNC.publish(self.state, self.github.request)
        self.github.pull = None
        self.github.writes.clear()
        SYNC.publish(self.state, self.github.request)
        self.assertEqual([item[0].split("/")[-1] for item in self.github.writes], ["trees", "pulls"])

    def test_unsigned_api_commit_never_advances_branch(self):
        self.github.is_signed = False
        with self.assertRaisesRegex(RuntimeError, "did not sign"):
            SYNC.publish(self.state, self.github.request)
        self.assertEqual([item[0].split("/")[-1] for item in self.github.writes], ["trees", "commits"])

    def test_concurrent_branch_change_fails_without_pr_creation(self):
        SYNC.publish(self.state, self.github.request)
        self.github.writes.clear()
        self.github.tip["commit"]["tree"]["sha"] = "old-tree"
        self.github.has_race = True
        with self.assertRaisesRegex(RuntimeError, "non-fast-forward"):
            SYNC.publish(self.state, self.github.request)
        self.assertFalse(any("/pulls" in item[0] for item in self.github.writes))

    def test_pagination_failure_permits_no_mutations(self):
        def request(endpoint, payload=None, method="GET"):
            if "/pulls?" in endpoint and "page=2" in endpoint:
                raise RuntimeError("page unavailable")
            if "/pulls?" in endpoint:
                return [{}] * 100
            return self.github.request(endpoint, payload, method)
        with self.assertRaisesRegex(RuntimeError, "page unavailable"):
            SYNC.publish(self.state, request)
        self.assertEqual(self.github.writes, [])

    def test_prepare_rejects_untracked_outside_path_before_credentials(self):
        with patch.object(SYNC, "command", side_effect=[b"https://github.com/arkoren/arkoren\n", b"base\n", b"?? rogue.txt\0"]):
            with self.assertRaisesRegex(RuntimeError, "outside managed"):
                SYNC.prepare()

    def test_prepare_collects_only_ordinary_changed_records(self):
        with tempfile.TemporaryDirectory() as directory:
            file = Path(directory) / "notes.md"
            file.write_text("notes")
            with patch.object(SYNC, "command", side_effect=[b"https://github.com/arkoren/arkoren\n", b"base\n", f"?? {FILE}\0".encode()]):
                with patch.object(SYNC, "Path", return_value=file):
                    self.assertEqual(SYNC.prepare(), {"base": "base", "records": [{"path": FILE, "content": "bm90ZXM="}]})

    def test_prepare_rejects_deleted_history(self):
        with patch.object(SYNC, "command", side_effect=[b"https://github.com/arkoren/arkoren\n", b"base\n", f" D {FILE}\0".encode()]):
            with self.assertRaisesRegex(RuntimeError, "only additions"):
                SYNC.prepare()

    def test_prepare_in_a_real_isolated_git_checkout(self):
        previous = Path.cwd()
        with tempfile.TemporaryDirectory() as directory:
            try:
                os.chdir(directory)
                subprocess.run(["git", "init", "-q"], check=True)
                subprocess.run(["git", "remote", "add", "origin", "https://github.com/arkoren/arkoren"], check=True)
                subprocess.run(["git", "-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "--allow-empty", "-m", "fixture"], check=True)
                file = Path(FILE)
                file.parent.mkdir(parents=True)
                file.write_text("published notes")
                state = SYNC.prepare()
                self.assertEqual([record["path"] for record in state["records"]], [FILE])
                Path("outside.txt").write_text("must not be committed")
                with self.assertRaisesRegex(RuntimeError, "outside managed"):
                    SYNC.prepare()
            finally:
                os.chdir(previous)

    def test_foreign_commit_in_otherwise_owned_branch_is_rejected(self):
        SYNC.publish(self.state, self.github.request)
        self.github.writes.clear()
        self.github.comparison.update({"total_commits": 1, "commits": [{"author": None, "commit": {"verification": {"verified": True}}}]})
        with self.assertRaisesRegex(RuntimeError, "foreign or unsigned"):
            SYNC.publish(self.state, self.github.request)
        self.assertEqual(self.github.writes, [])

    def test_branch_that_deletes_a_record_is_rejected(self):
        SYNC.publish(self.state, self.github.request)
        self.github.writes.clear()
        self.github.comparison["files"] = [{"filename": FILE, "status": "removed"}]
        with self.assertRaisesRegex(RuntimeError, "removes or renames"):
            SYNC.publish(self.state, self.github.request)
        self.assertEqual(self.github.writes, [])

    def test_truncated_history_fails_closed(self):
        SYNC.publish(self.state, self.github.request)
        self.github.writes.clear()
        self.github.comparison["total_commits"] = 101
        with self.assertRaisesRegex(RuntimeError, "history exceeds"):
            SYNC.publish(self.state, self.github.request)
        self.assertEqual(self.github.writes, [])

    def test_app_token_cannot_cover_other_repositories(self):
        with self.assertRaisesRegex(RuntimeError, "scoped only"):
            SYNC.verify(lambda endpoint: {"total_count": 2, "repositories": []})


if __name__ == "__main__":
    unittest.main()
