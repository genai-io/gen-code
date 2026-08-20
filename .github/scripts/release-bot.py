#!/usr/bin/env python3
"""
Release Bot — Automates release PR creation for genai-io/san.

On each run:
  1. Finds the latest release tag (vX.Y.Z)
  2. Collects all merged PRs since that tag
  3. Categorises them into Added / Changed / Fixed / Removed
  4. Bumps the version (patch by default)
  5. Rewrites CHANGELOG.md and cmd/san/main.go
  6. Creates (or updates) a release/vX.Y.Z PR against main
  7. Triggers CI on the release branch via workflow_dispatch (PRs created
     with GITHUB_TOKEN are approval-gated, workflow_dispatch is not)
  8. Waits for CI to pass, then requests review from REVIEW_REQUESTEE
     (a single OWNERS member — pinging the whole team is noisy)

Usage from GitHub Actions workflow:
  python3 .github/scripts/release-bot.py

Environment variables (set by the workflow):
  GH_TOKEN       — GitHub token with contents:write + pull-requests:write
  VERSION_BUMP   — "patch" (default) or "minor"
  DRY_RUN        — "true" to preview without pushing
"""

import json
import os
import re
import subprocess
import sys
import time
from datetime import datetime, timezone, date, timedelta
from pathlib import Path

REPO = "genai-io/san"
MAIN_BRANCH = "main"

# Only this OWNERS member gets a review request on release PRs — pinging
# the whole team on every weekly release is noisy.
REVIEW_REQUESTEE = "yanmxa"

# ── helpers ──────────────────────────────────────────────────────────────


def run(cmd, **kwargs):
    """Run a command and return stripped stdout. Exit on failure."""
    result = subprocess.run(cmd, capture_output=True, text=True, **kwargs)
    if result.returncode != 0:
        print(f"::error:: command failed: {' '.join(cmd)}\n{result.stderr.strip()}")
        sys.exit(result.returncode)
    return result.stdout.strip()


def gh_api(endpoint, method="GET", fields=None):
    """Call gh api and return parsed JSON, or None on failure."""
    args = ["gh", "api", endpoint, "-X", method, "--jq", "."]
    if fields:
        for f in fields:
            args.extend(["-f", f])
    raw = subprocess.run(args, capture_output=True, text=True)
    if raw.returncode != 0:
        print(f"::error:: gh api call failed: {endpoint}\n{raw.stderr.strip()}")
        return None
    if not raw.stdout.strip():
        return None
    return json.loads(raw.stdout.strip())


# ── version helpers ──────────────────────────────────────────────────────


def get_latest_tag():
    """Return the newest v-prefixed tag, or None."""
    tags = run(["git", "tag", "--list", "v*", "--sort=-version:refname"])
    if not tags:
        return None
    return tags.split("\n")[0]


def version_key(version):
    """Parse 'X.Y.Z' (optional leading 'v') into a sortable tuple."""
    m = re.match(r"(\d+)\.(\d+)\.(\d+)", version.lstrip("v"))
    if not m:
        return (0, 0, 0)
    return tuple(int(x) for x in m.groups())


def max_version(a, b):
    """Return the higher of two version strings, or the one that is not None."""
    if a is None:
        return b
    if b is None:
        return a
    return a if version_key(a) >= version_key(b) else b


def bump_version(current, bump_type="patch"):
    """Bump a semver string (without leading 'v')."""
    m = re.match(r"(\d+)\.(\d+)\.(\d+)", current)
    if not m:
        raise ValueError(f"cannot parse version: {current}")
    major, minor, patch = int(m.group(1)), int(m.group(2)), int(m.group(3))
    if bump_type == "major":
        return f"{major + 1}.0.0"
    if bump_type == "minor":
        return f"{major}.{minor + 1}.0"
    return f"{major}.{minor}.{patch + 1}"


# ── PR helpers ───────────────────────────────────────────────────────────


def parse_conventional_commit(title):
    """
    Return (prefix, scope, description) or (None, None, title) when no
    conventional-commit prefix is found.
    """
    m = re.match(r"(\w+)(?:\(([^)]*)\))?:\s*(.*)", title)
    if m:
        return m.group(1), m.group(2) or "", m.group(3)
    return None, None, title


def categorize_pr(pr):
    """
    Map a merged PR to a changelog section:
      Added | Changed | Fixed | Removed
    """
    labels = {l["name"] for l in pr.get("labels", [])}
    title = pr.get("title", "")

    # Label-based — most reliable signal
    if "bug" in labels:
        return "Fixed"
    if "dependencies" in labels:
        return "Changed"

    # Conventional commit prefix
    prefix, _scope, _desc = parse_conventional_commit(title)
    CATEGORY_MAP = {
        "feat": "Added",
        "feature": "Added",
        "fix": "Fixed",
        "refactor": "Changed",
        "chore": "Changed",
        "ci": "Changed",
        "docs": "Changed",
        "perf": "Changed",
        "test": "Changed",
        "style": "Changed",
        "revert": "Fixed",
    }
    if prefix and prefix in CATEGORY_MAP:
        return CATEGORY_MAP[prefix]

    # Title-keyword heuristics
    low = title.lower()
    if re.search(r"\b(add|new|support|introduce|raise)\b", low):
        return "Added"
    if re.search(
        r"\b(fix|prevent|avoid|correct|guard|keep|stop|resolve|"
        r"restore|survive|rebuild|recover|drop)\b",
        low,
    ):
        return "Fixed"
    if re.search(r"\b(remove|deprecate|delete|clean)\b", low):
        return "Removed"

    return "Changed"


def format_entry(pr):
    """
    Turn a merged PR into a single changelog line:
      - Description ([@author](link) in [#NNN](link))
    """
    title = pr.get("title", "")
    _prefix, _scope, desc = parse_conventional_commit(title)
    text = (desc or title).strip()
    # Capitalise first letter
    if text and text[0].islower():
        text = text[0].upper() + text[1:]

    author = pr.get("user", {}).get("login", "unknown")
    number = pr.get("number", "")
    return (
        f"- {text}"
        f" ([@{author}](https://github.com/{author})"
        f" in [#{number}](https://github.com/{REPO}/pull/{number}))"
    )


# ── changelog generation ────────────────────────────────────────────────


def generate_changelog(version_tag, date_str, prs):
    """
    Produce a full changelog section:
      ## [vX.Y.Z] - YYYY-MM-DD

      ### Added
      - ...

      ### Changed
      - ...

      ### Fixed
      - ...
    """
    sections = {}
    for pr in prs:
        cat = categorize_pr(pr)
        sections.setdefault(cat, []).append(format_entry(pr))

    parts = [f"## [{version_tag}] - {date_str}\n"]
    for cat in ("Added", "Changed", "Fixed", "Removed", "Security"):
        items = sections.pop(cat, None)
        if items:
            parts.append(f"\n### {cat}\n" + "\n".join(items))
    # Any uncategorised leftovers (unlikely)
    for cat, items in sections.items():
        parts.append(f"\n### {cat}\n" + "\n".join(items))

    return "".join(parts) + "\n"


# ── main ─────────────────────────────────────────────────────────────────


def read_version_from_main_go():
    """Read the current version string from cmd/san/main.go."""
    m = re.search(r'var version = "(.*?)"', Path("cmd/san/main.go").read_text())
    return m.group(1) if m else None


def read_owners():
    """
    Parse reviewer usernames from the OWNERS file — the YAML `reviewers:`
    list, falling back to `approvers:`, then plain line-per-username.
    """
    try:
        raw = Path("OWNERS").read_text()
    except FileNotFoundError:
        return []
    for key in ("reviewers", "approvers"):
        m = re.search(rf"^{key}:\s*$([\s\S]*?)(?=^[a-z]+:\s*$|\Z)", raw, re.MULTILINE)
        if m:
            users = re.findall(r"^\s*-\s*(\S+)", m.group(1), re.MULTILINE)
            if users:
                return users
    # Plain text: one username per line, skip empty lines, comments, and keys
    return [
        line.strip()
        for line in raw.splitlines()
        if line.strip() and not line.strip().startswith("#") and ":" not in line
    ]


def wait_for_ci_and_request_review(pr_number, branch, owners):
    """
    Poll the dispatched CI run on the release branch until it finishes.
    When it passes, request review from the designated OWNERS member so
    the release PR gets noticed.
    """
    deadline = time.time() + 30 * 60  # give CI up to 30 minutes
    state = None
    while time.time() < deadline:
        query = subprocess.run(
            [
                "gh", "run", "list", "--repo", REPO, "--workflow", "ci.yml",
                "--branch", branch,
                "--json", "databaseId,event,status,conclusion",
                "--limit", "5",
            ],
            capture_output=True,
            text=True,
        )
        if query.returncode == 0 and query.stdout.strip():
            runs = json.loads(query.stdout)
            # Only the run we dispatched — pull_request runs on this branch
            # may exist and are approval-gated, so ignore them.
            mine = [r for r in runs if r.get("event") == "workflow_dispatch"]
            if mine:
                state = mine[0]
                if state["status"] == "completed":
                    break
        time.sleep(30)

    if not state or state["status"] != "completed":
        print(f"::warning:: Timed out waiting for CI on {branch} — review not requested")
        return
    if state["conclusion"] != "success":
        print(
            f"::warning:: CI on {branch} concluded {state['conclusion']} "
            f"— review not requested"
        )
        return
    if not owners:
        print("::warning:: OWNERS list is empty — no reviewers to request")
        return
    if REVIEW_REQUESTEE not in owners:
        print(
            f"::warning:: {REVIEW_REQUESTEE} is not in OWNERS "
            f"— review not requested"
        )
        return

    edit = subprocess.run(
        [
            "gh", "pr", "edit", str(pr_number), "--repo", REPO,
            "--add-reviewer", REVIEW_REQUESTEE,
        ],
        capture_output=True,
        text=True,
    )
    if edit.returncode != 0:
        print(
            f"::warning:: could not request review on PR #{pr_number}: "
            f"{edit.stderr.strip()}"
        )
    else:
        print(f"👀 CI passed — requested review from {REVIEW_REQUESTEE}")


def main():
    dry_run = os.environ.get("DRY_RUN", "false").lower() == "true"
    bump_type = os.environ.get("VERSION_BUMP", "patch")

    # 1. Determine current version — the higher of the latest tag and the
    #    in-file version string. The tag records what has been *released*,
    #    but cmd/san/main.go can be ahead of it: a release PR may have been
    #    merged before its tag was pushed. Trusting only the tag then makes
    #    the bot regress and re-create an already-released version, so take
    #    the max of both sources.
    latest_tag = get_latest_tag()
    tag_version = latest_tag.lstrip("v") if latest_tag else None
    file_version = read_version_from_main_go()
    current_version = max_version(tag_version, file_version)
    if not current_version:
        print("::error:: No tag and no version in cmd/san/main.go — cannot proceed.")
        sys.exit(1)
    if tag_version and tag_version != current_version:
        print(
            f"::warning:: cmd/san/main.go ({file_version}) is ahead of the "
            f"latest tag ({latest_tag}) — treating {current_version} as current"
        )
    elif latest_tag:
        print(f"Latest tag: {latest_tag}  (version {current_version})")
    else:
        print(f"::warning:: No version tag found. Using in-file version {current_version}")

    # 2. Determine the merged-PR cutoff. Prefer the latest tag date: even
    #    when main.go is ahead of the tag, everything merged since the last
    #    real release is unreleased. Without a tag, fall back to the last
    #    7 days (weekly window).
    if latest_tag:
        tag_date_str = run(["git", "log", "-1", "--format=%cI", latest_tag])
        try:
            # fromisoformat rejects the trailing 'Z' before Python 3.11
            tag_date = datetime.fromisoformat(tag_date_str.replace("Z", "+00:00"))
        except Exception:
            print(
                f"::warning:: Cannot parse tag date '{tag_date_str}', "
                f"falling back to 7 days ago"
            )
            tag_date = datetime.now(timezone.utc).replace(
                hour=0, minute=0, second=0, microsecond=0
            ) - timedelta(days=7)
    else:
        tag_date = datetime.now(timezone.utc).replace(
            hour=0, minute=0, second=0, microsecond=0
        ) - timedelta(days=7)
        print(f"Collecting PRs merged after {tag_date.date()} (no-tag fallback)")

    # 3. Determine next version
    next_version = bump_version(current_version, bump_type)
    version_tag = f"v{next_version}"
    branch = f"release/{version_tag}"
    print(f"Next version: {version_tag}  branch: {branch}")

    # 4. Check whether a release PR already exists for this version
    existing = run([
        "gh", "pr", "list",
        "--repo", REPO,
        "--head", branch,
        "--state", "open",
        "--json", "number,url",
    ])
    existing_prs = json.loads(existing) if existing.strip() else []
    if existing_prs:
        print(f"::notice:: Release PR already exists: {existing_prs[0]['url']}")
        return

    # Guard against re-releasing a version that is already in the CHANGELOG
    # (belt-and-braces for a merged release PR whose tag was never pushed).
    changelog_text = Path("CHANGELOG.md").read_text()
    if re.search(
        rf"^## \[{re.escape(version_tag)}\].*$", changelog_text, re.MULTILINE
    ):
        print(
            f"::notice:: {version_tag} already released (section present in "
            f"CHANGELOG.md) — nothing to do."
        )
        return

    # 5. Fetch merged PRs since the cutoff using the Search API
    merged_after = tag_date.strftime("%Y-%m-%d")
    print(f"Collecting PRs merged after {merged_after}")

    result = gh_api(
        f"search/issues?q=repo:{REPO}+is:pr+is:merged+merged:>={merged_after}",
        fields=["sort=updated", "order=desc", "per_page=100"],
    )
    if result is None:
        print("::error:: Failed to fetch merged PRs from GitHub API")
        sys.exit(1)

    merged_prs = result.get("items", [])
    # The search API already filters by merged date so we trust its results,
    # but double-check since there can be subtle timezone mismatches.
    # Note: the search API returns merge time under pull_request.merged_at;
    # the top-level merged_at field is always null.
    merged_prs = [
        p
        for p in merged_prs
        if p.get("pull_request", {}).get("merged_at")
        and datetime.fromisoformat(
            p["pull_request"]["merged_at"].replace("Z", "+00:00")
        ) > tag_date
    ]
    print(f"Found {len(merged_prs)} merged PRs since last release")

    # 6. Filter out release PRs themselves, and drop PRs already mentioned
    #    in the CHANGELOG — when the latest tag is behind main.go (missing
    #    tag), the search window reaches into PRs from an earlier release.
    new_prs = [
        p for p in merged_prs if not p["title"].startswith("chore: bump version")
    ]
    print(f"  {len(new_prs)} after filtering out release-version bumps")
    already_listed = set(re.findall(r"\[#(\d+)\]", changelog_text))
    new_prs = [p for p in new_prs if str(p["number"]) not in already_listed]
    print(f"  {len(new_prs)} after dropping PRs already in the CHANGELOG")

    if not new_prs:
        print("::notice:: No new PRs found. Nothing to release.")
        return

    # 7. Generate changelog entry
    today_str = date.today().isoformat()
    entry = generate_changelog(version_tag, today_str, new_prs)
    print(f"\n── Changelog entry ──\n{entry}\n────────────────────")

    if dry_run:
        print("::notice:: DRY RUN — no files changed, no PR created.")
        return

    # 8. Update CHANGELOG.md
    changelog = Path("CHANGELOG.md")
    original = changelog.read_text()

    # The file starts with "# Changelog\n\n...". Insert before the first
    # existing version heading "## [v" so the newest release goes on top.
    insert_before = original.find("\n## [")
    if insert_before == -1:
        print("::error:: Cannot find '## [' in CHANGELOG.md")
        sys.exit(1)
    new_changelog = (
        original[:insert_before] + "\n" + entry + original[insert_before:].lstrip("\n")
    )
    changelog.write_text(new_changelog)
    print("Updated CHANGELOG.md")

    # 9. Update cmd/san/main.go
    main_go = Path("cmd/san/main.go")
    main_content = main_go.read_text()
    main_content = re.sub(
        r'var version = ".*?"',
        f'var version = "{next_version}"',
        main_content,
    )
    main_go.write_text(main_content)
    print(f"Updated cmd/san/main.go -> version {next_version}")

    # 10. Commit, push, create PR
    # Commit as github-actions[bot] — the same identity that pushes the
    # branch via GITHUB_TOKEN. DCO skips bot-authored commits, and its
    # noreply email maps to a real account, so no human identity is
    # involved. -s keeps a Signed-off-by trailer for good measure.
    run(["git", "config", "user.name", "github-actions[bot]"])
    run(["git", "config", "user.email", "41898282+github-actions[bot]@users.noreply.github.com"])
    run(["git", "checkout", "-b", branch])
    run(["git", "add", "CHANGELOG.md", "cmd/san/main.go"])
    run(["git", "commit", "-s", "-m", f"chore: bump version to {next_version}"])
    run(["git", "push", "origin", branch])

    body = (
        f"This automated PR bumps the version from `{current_version}` to"
        f" `{next_version}` and updates the CHANGELOG with changes merged"
        f" since `{latest_tag}`.\n\n"
        f"{entry}"
    )
    result = run([
        "gh", "pr", "create",
        "--repo", REPO,
        "--base", MAIN_BRANCH,
        "--head", branch,
        "--title", f"chore: bump version to {next_version}",
        "--body", body,
    ])
    print(f"\n✅ Release PR created: {result}")

    # Trigger CI directly on the release branch. PRs created with
    # GITHUB_TOKEN have their pull_request-triggered runs gated behind
    # manual approval, but workflow_dispatch runs always execute — so the
    # release PR gets its checks without anyone clicking "Approve
    # workflows to run". Non-fatal: the PR stays valid without it.
    trigger = subprocess.run(
        ["gh", "workflow", "run", "ci.yml", "--repo", REPO, "--ref", branch],
        capture_output=True,
        text=True,
    )
    if trigger.returncode != 0:
        print(f"::warning:: could not trigger CI on {branch}: {trigger.stderr.strip()}")
    else:
        print(f"🔁 CI triggered on {branch}")
        # Once CI passes, notify the OWNERS so the release PR gets reviewed.
        wait_for_ci_and_request_review(result.split("/")[-1], branch, read_owners())


if __name__ == "__main__":
    main()
