#!/usr/bin/env python3
"""Gate fixtures use API-shaped data without altering production checks."""
import copy
import unittest
from release_evidence import select_target, workflow_state, job_evidence

SHA = "a" * 40
REPO = "arkoren/arkoren"


class EvidenceTests(unittest.TestCase):
    def setUp(self):
        self.run = {"id": 1, "run_attempt": 1, "head_sha": SHA, "head_branch": "main", "event": "push", "status": "completed", "conclusion": "success", "path": ".github/workflows/ci.yml", "head_repository": {"full_name": REPO}, "html_url": "https://github.com/arkoren/arkoren/actions/runs/1"}
        self.event = {"repository": {"full_name": REPO}, "workflow_run": self.run}

    def test_trusted_main_exact_upstream_sha_not_default_sha(self):
        self.assertEqual(select_target("workflow_run", self.event, REPO, "refs/heads/main", "b" * 40), (SHA, "main"))

    def test_wrong_repository_branch_event_conclusion_and_sha_rejected(self):
        for field, value in (("head_branch", "feature"), ("event", "pull_request"), ("conclusion", "failure"), ("conclusion", "cancelled"), ("status", "in_progress"), ("head_sha", "HEAD"), ("path", ".github/workflows/other.yml"), ("head_repository", {"full_name": "fork/repo"})):
            with self.subTest(field=field, value=value):
                event = copy.deepcopy(self.event)
                event["workflow_run"][field] = value
                with self.assertRaises(ValueError):
                    select_target("workflow_run", event, REPO, "refs/heads/main", "")

    def test_manual_selected_branch_requires_exact_sha(self):
        self.assertEqual(select_target("workflow_dispatch", {"repository": {"full_name": REPO}, "inputs": {"sha": SHA}}, REPO, "refs/heads/feature", ""), (SHA, "feature"))
        for ref, sha in (("refs/tags/core/v0.1.0", SHA), ("refs/heads/main", "main")):
            with self.assertRaises(ValueError):
                select_target("workflow_dispatch", {"repository": {"full_name": REPO}, "inputs": {"sha": sha}}, REPO, ref, "")

    def test_required_exact_sha_and_latest_run_not_earlier_green(self):
        self.assertEqual(workflow_state([self.run], REPO, SHA, "main", "ci.yml")[0], "success")
        for field, value in (("head_sha", "b" * 40), ("head_branch", "feature"), ("event", "pull_request"), ("head_repository", {"full_name": "fork/repo"})):
            run = dict(self.run, **{field: value})
            self.assertEqual(workflow_state([run], REPO, SHA, "main", "ci.yml")[0], "pending")
        later = dict(self.run, id=2, conclusion="failure")
        with self.assertRaises(ValueError):
            workflow_state([self.run, later], REPO, SHA, "main", "ci.yml")

    def test_failed_cancelled_pending_missing_workflow_and_jobs(self):
        for conclusion in ("failure", "cancelled", "skipped", "neutral", "timed_out"):
            with self.assertRaises(ValueError):
                workflow_state([dict(self.run, conclusion=conclusion)], REPO, SHA, "main", "ci.yml")
        self.assertEqual(workflow_state([], REPO, SHA, "main", "ci.yml")[0], "pending")
        self.assertEqual(workflow_state([dict(self.run, status="in_progress", conclusion=None)], REPO, SHA, "main", "ci.yml")[0], "pending")
        job = {"name": "CI gate", "head_sha": SHA, "status": "completed", "conclusion": "success"}
        self.assertEqual(job_evidence([job], "CI gate", SHA), job)
        for jobs in ([], [dict(job, head_sha="b" * 40)], [dict(job, conclusion="failure")], [dict(job, status="queued")], [job, job]):
            with self.assertRaises(ValueError):
                job_evidence(jobs, "CI gate", SHA)


if __name__ == "__main__":
    unittest.main()
