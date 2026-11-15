#!/usr/bin/env bash
# Publishes the pages in wiki/ to the repository's GitHub wiki.
#
# GitHub wikis are a separate git repository (<repo>.wiki.git). This script clones it, replaces
# its contents with wiki/, commits and pushes. It PUBLISHES, so run it only when you are ready.
# The wiki must already exist: create its first page once in the GitHub web interface.
#
# Usage: scripts/publish-wiki.sh            (uses the "origin" remote)
#        scripts/publish-wiki.sh <wiki-url>
set -euo pipefail
cd "$(dirname "$0")/.."

url="${1:-}"
if [ -z "$url" ]; then
  origin="$(git remote get-url origin)"
  url="${origin%.git}.wiki.git"
fi

# Use the identity configured for this repository, not a global one.
name="$(git config user.name || echo BrokerBuddy)"
email="$(git config user.email || echo noreply@example.com)"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
git clone "$url" "$tmp/wiki"
# replace everything except .git
find "$tmp/wiki" -mindepth 1 -maxdepth 1 ! -name .git -exec rm -rf {} +
cp -R wiki/. "$tmp/wiki/"
cd "$tmp/wiki"
git add -A
if git diff --cached --quiet; then
  echo "wiki is already up to date"
  exit 0
fi
git -c user.name="$name" -c user.email="$email" commit -m "Update wiki"
git push
echo "published to $url"
