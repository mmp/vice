#!/usr/bin/env python3
"""Automate cutting a vice release."""

import os
import re
import subprocess
import sys
import time
from datetime import date


def run(cmd, **kwargs):
    """Run a command and return its output, exiting on failure."""
    result = subprocess.run(cmd, shell=True, capture_output=True, text=True, **kwargs)
    if result.returncode != 0:
        print(f"Command failed: {cmd}")
        if result.stderr:
            print(result.stderr)
        sys.exit(1)
    return result.stdout.strip()


def get_latest_tag():
    """Get the most recent release tag."""
    output = run("git tag --sort=-v:refname")
    for line in output.splitlines():
        if re.match(r'^v0\.\d+\.\d+$', line):
            return line
    print("Error: no existing v0.xx.yy tags found")
    sys.exit(1)


def parse_version(tag):
    """Parse v0.xx.yy into (xx, yy)."""
    m = re.match(r'^v0\.(\d+)\.(\d+)$', tag)
    if not m:
        return None
    return int(m.group(1)), int(m.group(2))


def validate_tag(tag):
    """Validate the release tag is well-formed and a proper version bump."""
    if not tag.startswith("v0."):
        print(f"Error: tag must start with 'v0.' (got '{tag}')")
        sys.exit(1)

    new_ver = parse_version(tag)
    if new_ver is None:
        print(f"Error: tag must be of the form v0.xx.yy (got '{tag}')")
        sys.exit(1)

    latest = get_latest_tag()
    old_ver = parse_version(latest)
    new_major, new_minor = new_ver
    old_major, old_minor = old_ver

    is_major_bump = (new_major == old_major + 1 and new_minor == 0)
    is_minor_bump = (new_major == old_major and new_minor == old_minor + 1)

    if not (is_major_bump or is_minor_bump):
        print(f"Error: tag {tag} is not a valid bump from {latest}")
        print(f"  Expected either v0.{old_major + 1}.0 (major) or v0.{old_major}.{old_minor + 1} (minor)")
        sys.exit(1)

    return latest


def parse_whatsnew_md(path):
    """Parse whatsnew.md, combining all sections (beta1, beta2, final).

    Returns a list of (text, children) tuples where children is a list of
    strings (empty for leaf items).
    """
    with open(path) as f:
        lines = f.readlines()

    # First pass: identify which top-level items have nested children
    has_children = set()
    for i, line in enumerate(lines):
        if re.match(r'^  - ', line):
            for j in range(i - 1, -1, -1):
                if re.match(r'^- ', lines[j]):
                    has_children.add(j)
                    break

    items = []
    current_parent_idx = None

    for i, line in enumerate(lines):
        stripped = line.rstrip('\n')

        if re.match(r'^--', stripped):
            continue

        if re.match(r'^- ', stripped):
            text = stripped[2:]
            if i in has_children:
                current_parent_idx = len(items)
                items.append((text, []))
            else:
                current_parent_idx = None
                items.append((text, []))
        elif re.match(r'^  - ', stripped):
            text = stripped[4:]
            if current_parent_idx is not None:
                items[current_parent_idx][1].append(text)
            else:
                items.append((text, []))

    return items


def flatten_items(items):
    """Flatten structured items into a flat list with parent prefix for children."""
    flat = []
    for text, children in items:
        if children:
            prefix = text.rstrip().rstrip(':') + ':'
            for child in children:
                flat.append(prefix + ' ' + child)
        else:
            flat.append(text)
    return flat


def update_whatsnew_go(items):
    """Rewrite whatsnew.go with the new items."""
    path = "cmd/vice/whatsnew.go"
    with open(path) as f:
        content = f.read()

    # Build the new entries as Go string literals, excluding facility engineering items
    go_strings = []
    for item in items:
        if re.match(r'^Facility [Ee]ngineering:', item):
            continue
        # Escape backticks by switching to double-quoted strings
        if '`' in item:
            # Use double-quoted Go string; escape backslashes and double quotes
            escaped = item.replace('\\', '\\\\').replace('"', '\\"')
            go_strings.append(f'\t"{escaped}",')
        elif '"' in item:
            # Use backtick-quoted Go string (already no backticks)
            go_strings.append(f'\t`{item}`,')
        else:
            # Either works; prefer backticks for readability
            go_strings.append(f'\t`{item}`,')

    new_entries = '\n'.join(go_strings)

    # Append new entries before the closing }
    # Find the last entry line and insert after it
    closing = '\n}'
    idx = content.rfind(closing)
    if idx == -1:
        print("Warning: whatsnew.go was not modified (closing brace not found)")
        return

    new_content = content[:idx] + '\n' + new_entries + closing
    with open(path, 'w') as f:
        f.write(new_content)

    # Run gofmt
    subprocess.run(['gofmt', '-w', path], check=True)
    print(f"  Updated {path}")


def update_metainfo_xml(tag, items):
    """Add a new release entry to Vice.metainfo.xml."""
    path = "linux/io.github.mmp.Vice.metainfo.xml"
    with open(path) as f:
        content = f.read()

    version = tag[1:]  # strip leading 'v'
    today = date.today().isoformat()

    # Build list items
    li_items = '\n'.join(f'          <li>{escape_xml(item)}</li>' for item in items)

    new_release = f'''    <release version="{version}" date="{today}">
      <url type="details">https://pharr.org/vice/#release-{version}</url>
      <description>
        <ul>
{li_items}
        </ul>
      </description>
    </release>'''

    # Insert after <releases>
    content = content.replace('<releases>\n', f'<releases>\n{new_release}\n', 1)

    with open(path, 'w') as f:
        f.write(content)
    print(f"  Updated {path}")


def escape_xml(text):
    """Escape special XML characters and strip markdown backticks."""
    text = text.replace('&', '&amp;')
    text = text.replace('<', '&lt;')
    text = text.replace('>', '&gt;')
    text = text.replace('"', '&quot;')
    # Strip markdown backticks (not valid in AppStream XML)
    text = text.replace('`', '')
    return text


def update_website(tag, old_tag, items):
    """Update website/index.html: download links, submenu, release history."""
    path = "website/index.html"
    with open(path) as f:
        content = f.read()

    version = tag[1:]  # e.g., "0.14.2"
    old_version = old_tag[1:]  # e.g., "0.14.1"

    # 1. Update download links (Windows and Mac)
    content = content.replace(
        f'releases/download/{old_tag}/Vice-{old_tag}-installer.msi',
        f'releases/download/{tag}/Vice-{tag}-installer.msi'
    )
    content = content.replace(
        f'Download Vice {old_tag} for Windows',
        f'Download Vice {tag} for Windows'
    )
    content = content.replace(
        f'releases/download/{old_tag}/Vice-{old_tag}-osx.zip',
        f'releases/download/{tag}/Vice-{tag}-osx.zip'
    )
    content = content.replace(
        f'Download Vice {old_tag} for Mac',
        f'Download Vice {tag} for Mac'
    )

    # 2. Add submenu entry at the top of the release history submenu
    new_submenu = f'      <li class="submenu-item"><a class="submenu-link scrollto" href="#release-{version}">{version}</a></li>'
    pattern = re.compile(r'(href="#releases">Release History</a>\s*\n\s*<ul class="submenu">\n)')
    new_content, n = pattern.subn(lambda m: m.group(1) + new_submenu + '\n', content, count=1)
    if n != 1:
        print("Error: could not find Release History submenu in website/index.html")
        sys.exit(1)
    content = new_content

    # 3. Add release history entry
    today_str = date.today().strftime("%-d %b %Y")

    li_lines = []
    for text, children in items:
        if children:
            li_lines.append(f'              <li>{escape_html(text)}')
            li_lines.append('                <ul>')
            for child in children:
                li_lines.append(f'                  <li>{escape_html(child)}</li>')
            li_lines.append('                </ul>')
            li_lines.append('              </li>')
        else:
            li_lines.append(f'              <li>{escape_html(text)}</li>')

    li_items = '\n'.join(li_lines)

    new_section = f'''
            <h3 id="release-{version}">{version} ({today_str})</h3>
            <ul>
{li_items}
            </ul>
'''

    # Insert after the Release History heading
    marker = '<h2 class="section-heading">Release History</h2>'
    content = content.replace(marker, marker + '\n' + new_section, 1)

    with open(path, 'w') as f:
        f.write(content)
    print(f"  Updated {path}")


def escape_html(text):
    """Escape special HTML characters, but preserve backtick-code as <code>."""
    text = text.replace('&', '&amp;')
    text = text.replace('<', '&lt;')
    text = text.replace('>', '&gt;')
    text = text.replace('"', '&quot;')
    # Convert `code` to <code>code</code>
    text = re.sub(r'`([^`]+)`', r'<code>\1</code>', text)
    return text


def wait_for_approval():
    """Pause for manual review."""
    print()
    print("=" * 60)
    print("Files have been updated. Please review the changes:")
    print("  - cmd/vice/whatsnew.go")
    print("  - linux/io.github.mmp.Vice.metainfo.xml")
    print("  - website/index.html")
    print()
    print("Make any manual edits now.")
    print("=" * 60)
    response = input("Ready to commit, tag, and push? [y/N] ")
    if response.lower() != 'y':
        print("Aborted.")
        sys.exit(0)


def commit_and_tag(tag):
    """Commit changes, apply tag."""
    version = tag[1:]
    # Print and clear whatsnew.md
    with open("whatsnew.md") as f:
        contents = f.read()
    if contents:
        print("whatsnew.md contents:")
        print(contents)
    with open("whatsnew.md", 'w') as f:
        pass
    run("git add cmd/vice/whatsnew.go linux/io.github.mmp.Vice.metainfo.xml website/index.html whatsnew.md")
    run(f'git commit -m "Release {version}"')
    run(f'git tag {tag}')
    print(f"  Committed and tagged {tag}")


def push_repo(tag):
    """Push repo and tag to GitHub."""
    print("Pushing to GitHub...")
    run("git push origin master")
    run(f"git push origin {tag}")
    print("  Pushed master and tag")


def push_website():
    """Sync website to cloudflare repo and push."""
    print("Pushing website...")
    run("rsync -a --delete --exclude beta/ ~/vice/master/website/ ~/web/cloudflare/pharr.org/vice")
    run("git add -A", cwd=os.path.expanduser("~/web/cloudflare/pharr.org"))
    run('git commit -m update', cwd=os.path.expanduser("~/web/cloudflare/pharr.org"))
    run("git push", cwd=os.path.expanduser("~/web/cloudflare/pharr.org"))
    print("  Website pushed")


def monitor_github_actions(tag):
    """Monitor the GitHub Actions workflow triggered by the tag push."""
    print()
    print(f"Monitoring GitHub Actions for {tag}...")
    print()

    # Wait a moment for the workflow to be triggered
    time.sleep(5)

    # Find the CI workflow run for this tag
    for attempt in range(12):
        result = subprocess.run(
            f'gh run list --repo mmp/vice --workflow ci.yml --limit 5 --json databaseId,headBranch,status,conclusion,name',
            shell=True, capture_output=True, text=True
        )
        if result.returncode != 0:
            print("  Waiting for workflow to appear...")
            time.sleep(10)
            continue

        import json
        runs = json.loads(result.stdout)
        run_id = None
        for r in runs:
            if r.get('headBranch') == tag or r.get('headBranch') == tag[1:]:
                run_id = r['databaseId']
                break

        if run_id:
            break

        print("  Waiting for CI workflow to appear...")
        time.sleep(10)

    if not run_id:
        print("  Could not find workflow run. Check manually:")
        print(f"  https://github.com/mmp/vice/actions")
        return

    print(f"  Found workflow run: https://github.com/mmp/vice/actions/runs/{run_id}")
    print()

    # Poll for completion
    last_status = {}
    while True:
        result = subprocess.run(
            f'gh run view {run_id} --repo mmp/vice --json status,conclusion,jobs',
            shell=True, capture_output=True, text=True
        )
        if result.returncode != 0:
            time.sleep(15)
            continue

        import json
        data = json.loads(result.stdout)

        # Print job status updates
        for job in data.get('jobs', []):
            name = job.get('name', 'unknown')
            status = job.get('status', 'unknown')
            conclusion = job.get('conclusion', '')
            current = f"{status}/{conclusion}" if conclusion else status

            if last_status.get(name) != current:
                last_status[name] = current
                if conclusion == 'success':
                    print(f"  ✓ {name}: SUCCESS")
                elif conclusion == 'failure':
                    print(f"  ✗ {name}: FAILED")
                elif status == 'in_progress':
                    print(f"  ⋯ {name}: running...")
                elif status == 'queued':
                    print(f"  ⋯ {name}: queued")

        overall_status = data.get('status', '')
        overall_conclusion = data.get('conclusion', '')

        if overall_status == 'completed':
            print()
            if overall_conclusion == 'success':
                print("BUILD SUCCESSFUL on all platforms!")
                print(f"Release: https://github.com/mmp/vice/releases/tag/{tag}")
            else:
                print(f"BUILD FINISHED with conclusion: {overall_conclusion}")
                print(f"Check: https://github.com/mmp/vice/actions/runs/{run_id}")
            break

        time.sleep(15)


def main():
    if len(sys.argv) != 2:
        print(f"Usage: {sys.argv[0]} <tag>")
        print("  e.g., release.py v0.14.2")
        sys.exit(1)

    tag = sys.argv[1]

    os.chdir(os.path.expanduser("~/vice/master"))

    old_tag = validate_tag(tag)
    print(f"Releasing {tag} (previous: {old_tag})")

    # Parse whatsnew.md
    structured_items = parse_whatsnew_md("whatsnew.md")
    if not structured_items:
        print("Error: no items found in whatsnew.md")
        sys.exit(1)

    flat_items = flatten_items(structured_items)
    print(f"Found {len(flat_items)} changelog items")
    print()

    # Update files
    print("Updating files...")
    update_whatsnew_go(flat_items)
    update_metainfo_xml(tag, flat_items)
    update_website(tag, old_tag, structured_items)

    # Pause for review
    wait_for_approval()

    # Commit, tag, push
    commit_and_tag(tag)
    push_repo(tag)
    push_website()

    # Monitor CI
    monitor_github_actions(tag)


if __name__ == "__main__":
    main()
