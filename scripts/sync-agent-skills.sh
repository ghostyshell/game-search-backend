#!/bin/sh
# Regenerate Claude/OpenCode/Cursor shims from docs/agents/skills/*.md
# Run after editing a canonical skill: sh scripts/sync-agent-skills.sh
set -e
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

sync_shim() {
  slug="$1"
  title="$2"
  quick="$3"
  cursor_desc="$4"
  canon="docs/agents/skills/${slug}.md"

  if [ ! -f "$ROOT/$canon" ]; then
    echo "sync-agent-skills: missing $ROOT/$canon" >&2
    exit 1
  fi

  SHIM_BODY="> **SKILL SHIM** - This file is a pointer only. The canonical source of truth lives at:
> \`$canon\`

# Skill: $title (Shim)

## Quick Reference

$quick

## How to Use

Load the canonical skill for the full workflow:

\`\`\`
$canon
\`\`\`

Global workflow (all repos): \`~/.claude/skills/changelog/SKILL.md\`

---
*This shim exists so that agent-specific directories (\`.claude\`, \`.opencode\`, \`.cursor\`) stay in sync. The canonical file is under \`docs/agents/skills/\`.*
"

  mkdir -p "$ROOT/.claude/skills" "$ROOT/.opencode/skills" "$ROOT/.cursor/skills/$slug"

  printf '%s\n' "$SHIM_BODY" > "$ROOT/.claude/skills/${slug}.md"
  printf '%s\n' "$SHIM_BODY" > "$ROOT/.opencode/skills/${slug}.md"

  {
    printf '%s\n' '---'
    printf '%s\n' "name: $slug"
    printf '%s\n' 'description: >-'
    printf '%s\n' "$cursor_desc"
    printf '%s\n' '---'
    printf '\n%s\n' "$SHIM_BODY"
  } > "$ROOT/.cursor/skills/$slug/SKILL.md"

  echo "synced $slug shims (.claude, .opencode, .cursor) from $canon"
}

sync_shim changelog "Changelog" \
  'Before every commit that touches product code, add bullets under `CHANGELOG.md` -> `[Unreleased]`, then stage the changelog. Pre-commit fails if code changes land without a changelog diff.

```bash
# edit CHANGELOG.md, then:
git add CHANGELOG.md
sh scripts/install-hooks.sh               # enable pre-commit guard
```' \
  '  Maintain CHANGELOG.md before commits in game-search-backend. Update [Unreleased]
  for internal/ or cmd/ changes, stage CHANGELOG with code. Use when committing, releasing,
  or when pre-commit fails on changelog check.'