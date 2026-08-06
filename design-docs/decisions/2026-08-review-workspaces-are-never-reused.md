# Review workspaces are never reused (deferred)

**Date**: 2026-08-06
**Pins**: observed against v0.27.0 + the Roll Call work (`a9d3bcb`). Code-internal.
**Status**: finding recorded, change deliberately NOT made. No code was
touched for this; the fixes sketched below are future work.

Repos and handles below are placeholders.

## What was found

Every review allocates a brand new workspace. `reviewOne` calls
`os.MkdirTemp` with a per-PR prefix, and `MkdirTemp` appends a random suffix
by construction, so it can never return an existing directory. The comment
beside it says a future run may reuse the directory; nothing implements that
half, and nothing can while the call is `MkdirTemp`.

Measured on one long-running deployment: **2776 workspaces, 116 MB**, one per
review attempt, none reused.

The three paths differ, and only one carries anything across:

| Path | Queue row | What carries over |
| --- | --- | --- |
| Re-review at a new SHA (Refreshed) | new row (`Complete` deleted the old one, so `work_dir` is NULL) | **nothing** |
| Manual add / promote | new row | **nothing** |
| Interrupted review resumed | same row survives, keeps its `work_dir` | the **session id** only, read back out of the old `agent.log`; a new directory is still created |

So a PR reviewed five times in a week re-derives its entire context five
times. The old transcripts are still on disk and referenced by
`history.work_dir`, but only so `queue log` and the dashboard can show them to
a human; nothing feeds them back into a later review.

## Why it was left alone

The directories are not garbage. Postmortem log access is a real, load-bearing
use of them, so "stop leaving them behind" is not the fix, and deleting them
would break the dashboard's per-review page for everything already recorded.

More importantly, what *should* be shared is a product question rather than a
bug. A re-review at a new SHA legitimately wants fresh state for most things:
the diff changed, the previous verdict may be stale, and reusing a dirty
workspace risks the agent reasoning about files that no longer reflect the PR.
The wasteful part is re-deriving the *stable* context (the repository itself,
linked issues, the previous review's own conclusions) at roughly a million
tokens a run. Deciding which of those is safe to carry forward is a design
question that wants an owner, not an opportunistic refactor.

## What it would take

Two independent pieces, in this order:

1. **A stable path.** `MkdirTemp`'s random suffix is what makes reuse
   impossible, so a deterministic per-PR directory is the precondition for
   anything else. On its own it shares nothing (nothing reads the directory
   back), but it also collapses the 2776 directories into one per PR.

2. **A sandbox allowance.** The engines are confined to the workspace:
   codex runs `--sandbox workspace-write --cd <workdir>`, and claude has its
   permission modes. Reads and network are NOT the constraint (verified live:
   codex under `workspace-write` with `network_access=true` ran a
   machine-local CLI, read credentials from the home directory, and reached an
   external API with no sandbox errors). WRITES outside the workspace are the
   constraint, so a shared cache elsewhere would be denied at write time even
   though the tooling itself works. codex exposes the levers
   (`sandbox_workspace_write.*`, `sandbox_permissions`,
   `shell_environment_policy.inherit`), claude the analogous
   `permission_mode` / `--add-dir`.

## The narrow case worth doing first

Resuming an interrupted review is the one place reuse is unambiguously
correct: the conversation is being continued, so the workspace it was
continuing in is the right one. Today `reviewOne` reads the old directory for
the session id and then replaces it with a new empty one.

codex tolerates this because `codex exec resume` restores its working
directory from its own rollout. claude does not: its resumed run gets the new
empty directory as its process working directory, so any files the agent had
created are no longer where it left them, even though the conversation itself
resumes correctly.

That fix is small and self-contained (keep the existing `work_dir` when
resuming instead of minting a new one) and does not require any of the sharing
design above.
