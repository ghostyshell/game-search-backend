# Skill: Changelog (game-search-backend)

> Global workflow: `~/.claude/skills/changelog/SKILL.md`

## Repo paths

| Item | Path |
|------|------|
| Changelog | `CHANGELOG.md` (repo root) |
| Pre-commit | `.githooks/pre-commit` runs `scripts/changelog-check.sh` |
| Code prefixes | `internal/`, `cmd/`, `main.go` |

## Before every commit

1. Update `CHANGELOG.md` -> `[Unreleased]` for staged `internal/`, `cmd/`, `main.go`, scraper, or handler changes.
2. Stage `CHANGELOG.md` with the code change.

Pre-commit **fails** if product code is staged without a staged `CHANGELOG.md` diff.

## Release

1. Move `[Unreleased]` into `## [x.y.z] - YYYY-MM-DD`.
2. Reset `[Unreleased]`.

## Shim sync

After editing this file:

```bash
sh scripts/sync-agent-skills.sh
```