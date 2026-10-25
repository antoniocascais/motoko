"""Print a sha256sum-format manifest of an upstream plugin dir at a commit.

Usage: gen-telegram-manifest.py <owner/repo> <commit> <plugin-path>
Fetches every blob under <plugin-path> at <commit> via `gh api` (no clone).
"""
import base64
import hashlib
import json
import subprocess
import sys

repo, commit, path = sys.argv[1:4]


def gh(endpoint):
    out = subprocess.run(["gh", "api", endpoint], check=True, capture_output=True, text=True).stdout
    return json.loads(out)


tree = gh(f"repos/{repo}/git/trees/{commit}?recursive=1")
assert not tree.get("truncated"), "tree truncated"
prefix = path.rstrip("/") + "/"
lines = []
for e in tree["tree"]:
    if e["type"] != "blob" or not e["path"].startswith(prefix):
        continue
    if e["mode"] not in ("100644", "100755"):
        sys.exit(f"unexpected mode {e['mode']} for {e['path']}")
    blob = gh(f"repos/{repo}/git/blobs/{e['sha']}")
    data = base64.b64decode(blob["content"])
    rel = e["path"][len(prefix):]
    lines.append((rel, hashlib.sha256(data).hexdigest(), e["mode"]))
lines.sort()
for rel, h, mode in lines:
    print(f"{h}  {rel}")
    if mode != "100644":
        print(f"note: {rel} mode {mode}", file=sys.stderr)
