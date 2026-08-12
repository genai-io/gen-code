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
  7. Enables auto-merge on it: the two-file version bump merges itself once
     CI passes and a maintainer approves

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
from datetime import datetime, timezone, date, timedelta
from pathlib import Path

REPO = "genai-io/san"
MAIN_BRANCH = "main"

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


def main():
    dry_run = os.environ.get("DRY_RUN", "false").lower() == "true"
    bump_type = os.environ.get("VERSION_BUMP", "patch")

    # 1. Determine current version — prefer the latest tag, fall back to
    #    the in-file version string (used on repos with no tags yet).
    latest_tag = get_latest_tag()
    if not latest_tag:
        fallback = read_version_from_main_go()
        if not fallback:
            print("::error:: No tag and no version in cmd/san/main.go — cannot proceed.")
            sys.exit(1)
        current_version = fallback
        print(f"::warning:: No version tag found. Using in-file version {current_version}")
        # Without a tag, default to PRs from the last 7 days (weekly window).
        tag_date = datetime.now(timezone.utc).replace(
            hour=0, minute=0, second=0, microsecond=0
        ) - timedelta(days=7)
        print(f"Collecting PRs merged after {tag_date.date()} (no-tag fallback)")
    else:
        current_version = latest_tag.lstrip("v")
        print(f"Latest tag: {latest_tag}  (version {current_version})")

        # 2. Get the tag creation date to use as cutoff
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

    # 6. Filter out release PRs themselves
    new_prs = [
        p for p in merged_prs if not p["title"].startswith("chore: bump version")
    ]
    print(f"  {len(new_prs)} after filtering out release-version bumps")

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

    # Enable auto-merge: the PR is a two-file version bump, so once the
    # required checks pass and a maintainer approves it merges itself.
    # Failure is non-fatal — the PR stays open for a human to merge.
    auto_merge = subprocess.run(
        ["gh", "pr", "merge", "--repo", REPO, "--auto", "--squash", result.split("/")[-1]],
        capture_output=True,
        text=True,
    )
    if auto_merge.returncode != 0:
        print(f"::warning:: could not enable auto-merge on {result}: {auto_merge.stderr.strip()}")
    else:
        print(f"🔁 Auto-merge enabled: {result}")


if __name__ == "__main__":
    main()
