#!/usr/bin/env python3
"""Transitional disabled-publisher evidence helpers; live previews use Go."""
import json
import re
import subprocess
import time
import urllib.parse


def full_sha(sha):
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("an exact full commit SHA is required")
    return sha


def select_target(event_name, event, repository, ref, default_sha):
    # default_sha is deliberately NOT used for workflow_run: GitHub sets it to
    # the default branch tip, which can differ from the upstream tested commit.
    if event.get("repository", {}).get("full_name") != repository:
        raise ValueError("event repository mismatch")
    if event_name == "workflow_run":
        run = event["workflow_run"]
        if (run.get("head_repository", {}).get("full_name") != repository
                or run.get("head_branch") != "main" or run.get("event") != "push"
                or run.get("path") != ".github/workflows/ci.yml"
                or run.get("status") != "completed" or run.get("conclusion") != "success"):
            raise ValueError("automatic preview requires successful trusted main push CI")
        return full_sha(run["head_sha"]), "main"
    if event_name == "workflow_dispatch" and ref.startswith("refs/heads/"):
        return full_sha(event["inputs"]["sha"]), ref.removeprefix("refs/heads/")
    raise ValueError("preview supports main CI completion or explicit selected-branch dispatch")


def workflow_state(runs, repository, sha, branch, workflow):
    eligible = [r for r in runs if r.get("head_sha") == sha and r.get("head_branch") == branch
                and r.get("head_repository", {}).get("full_name") == repository
                and r.get("path") == ".github/workflows/" + workflow
                and r.get("event") in (("push", "workflow_dispatch") if branch == "main" else ("workflow_dispatch",))]
    if not eligible:
        return "pending", None
    run = max(eligible, key=lambda r: (r["id"], r["run_attempt"]))
    if run["status"] != "completed":
        return "pending", run
    if run["conclusion"] != "success":
        raise ValueError(f"{workflow} is {run['conclusion']} at {sha}: {run['html_url']}")
    return "success", run


def job_evidence(jobs, name, sha):
    matches = [j for j in jobs if j.get("name") == name]
    if len(matches) != 1 or any(j.get("head_sha") != sha or j.get("status") != "completed" or j.get("conclusion") != "success" for j in matches):
        raise ValueError(f"missing, ambiguous or unsuccessful required job {name} at {sha}")
    return matches[0]


def api_pages(path, key):
    # gh handles authentication; only GET metadata, never artifacts or caches.
    pages = json.loads(subprocess.check_output(["gh", "api", "--paginate", "--slurp", path], text=True))
    return [item for page in pages for item in page[key]]


def collect(repository, sha, branch, mode):
    """Read fresh exact-SHA evidence; callers establish checkout and ancestry."""
    full_sha(sha)
    deadline = time.monotonic() + 900
    while True:
        evidence = {}
        for workflow, job in (("ci.yml", "CI gate"), ("website.yml", "website")):
            query = urllib.parse.urlencode({"head_sha": sha, "branch": branch, "per_page": 100})
            runs = api_pages(f"repos/{repository}/actions/workflows/{workflow}/runs?{query}", "workflow_runs")
            state, run = workflow_state(runs, repository, sha, branch, workflow)
            if state == "success":
                jobs = api_pages(f"repos/{repository}/actions/runs/{run['id']}/attempts/{run['run_attempt']}/jobs?per_page=100", "jobs")
                required = job_evidence(jobs, job, sha)
                evidence[workflow] = {"run_id": run["id"], "run_attempt": run["run_attempt"], "url": run["html_url"], "job": required["name"], "job_url": required["html_url"]}
        if len(evidence) == 2:
            return {"validated": True, "sha": sha, "branch": branch, "repository": repository, "mode": mode, "workflows": evidence}
        if time.monotonic() >= deadline:
            raise ValueError(f"timed out waiting for exact-SHA CI gate AND website: {sha}")
        print(f"Waiting for CI gate AND website at {sha}", flush=True)
        time.sleep(15)


if __name__ == "__main__":
    raise SystemExit("use arkoren-ci release evidence; this module remains only for the disabled publisher")
