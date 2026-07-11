#!/bin/sh
# Point git at the repo's version-controlled .githooks/ dir.
# Run once after cloning (or after pulling new hooks): sh scripts/install-hooks.sh
cd "$(dirname "$0")/.." || exit 1
git config core.hooksPath .githooks
chmod +x .githooks/* 2>/dev/null
chmod +x scripts/changelog-check.sh 2>/dev/null
echo "hooks installed: core.hooksPath = .githooks"