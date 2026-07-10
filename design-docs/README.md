# design-docs/

Long-form notes that don't belong in code or PR descriptions.

This folder is a **journal**, not a wiki. Most entries are snapshots in time: dated, version-pinned, written in past tense. They don't get rewritten when reality moves on; new entries get added instead. That keeps old entries useful as history rather than dangerous as out-of-date documentation.

## Structure

- **`decisions/`**: choices made, with the alternatives considered and why. One decision per file, prefixed with the date (`2026-07-store-driver.md`). Immutable after merge: if we revisit, that's a new dated file that supersedes the old one.
- **`reference/`**: point-in-time captures of external surfaces (the `gh` JSON we consume, `codex exec` flags, Tailscale behaviour). Each carries a "captured on" date and the version of the thing captured.
- **`concepts/`**: the only place where editing in place is the right move. Short, living docs that explain how a piece of the system fits together. Each carries a "last reviewed" date.

## Rules for new docs

1. **Date in the header.**
2. **Pin the version of what you're describing** (package version, commit SHA, or "nothing to pin: code-internal").
3. **Past tense or as-of tense, never evergreen present.**
4. **Reference code identifiers sparingly, and only as starting points.** Names rename; line numbers shift.
5. **No real-world data.** Synthetic IDs and placeholder repos/handles only; the CLI itself never hardcodes real GitHub handles or repos, and neither do these docs.
6. **Append, don't edit** (except `concepts/`).
