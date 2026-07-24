# PR-issue-review effectiveness audit (July 2026)

Findings from a data crunch over ~2.5 weeks of real usage (2026-07-07 →
2026-07-24). Source data: the live queue.duckdb history, bulk-fetched GitHub
state for every reviewed PR (reviews, threads, merge/revert outcomes), the
surviving engine work-dir verdicts, and targeted chat context. The analysis DB
and scripts lived in a session scratchpad; nothing in the live DB or on GitHub
was modified. Specifics that would identify the private org (repo names, PR
numbers, authors, ticket IDs) are omitted or anonymized here; the underlying
queries are reproducible from any deployment's own data via the method
appendix.

The subject under audit is the `pr-issue-review` skill as driven by this
tool's scheduler + prompt assembly, so findings cover both layers and say
which layer owns each fix.

## Headline numbers

| Metric | Value |
| --- | --- |
| Review runs | 773 total: 311 approved, 279 commented, 164 skipped, 19 error |
| Distinct PRs touched | 490 across 4 configured repos (one repo: zero — see gaps) |
| Reviews posted to GitHub | 589 (96% aggressive ⚔️, 4% assertive 🔎; passive/neutral never used) |
| First reviewer on the PR | 83% of reviewed PRs |
| Median PR-creation → first review | 48 min (p90 ~42 h) |
| Inline threads opened | 216; ~61% got a reply or resolution; 38% resolved outright |
| Human CHANGES_REQUESTED after our approve | 1 PR (of 307 approvals) |
| Approved-then-reverted (merged) | 5 PRs (all audited below) |
| Merged with our COMMENT standing at the exact reviewed head | 6 PRs (allow-listed authors) |
| Approval withheld citing only P3 nits (allow-listed authors) | ≤14 reviews of 214 COMMENTED |
| Cost | ~54 agent-hours, ~84M tokens; ~5.5 min per real review |

## What the data says, per question

### 1. Are we missing classes of things? (gaps)

From a full read of every lens + focus pack against a defect-class checklist,
the skill's coverage was broad but had four classes with **no owner at all**
(all addressed by edits below):

- **Accidentally-introduced injection vulns** (SQLi, XSS, SSRF, command
  injection, path traversal, unsafe deserialization). `safety.md` targets
  *maliciously authored* diffs; `sql-semantics` is query correctness;
  `auth-permissions` is authz. Nobody owned "this innocent-looking string
  concat is injectable."
- **Infra / Terraform / IaC** — no lens or pack, while one watched repo is
  infra-only and received dozens of review runs on generic lenses alone
  (blast radius, prod scaling values, security-group/IAM width, state-move
  hazards all uncovered).
- **CI/CD config** — pipeline YAML, workflow-injection, runner security.
  Several reviewed PRs were pure CI changes.
- **Cryptography misuse** — weak algos, IV reuse, insecure random, cert
  validation (rare in the watched codebases; lowest priority; not addressed
  yet).

Thinner-than-ideal but partially covered: non-malicious dependency bumps
(CVE/lockfile drift), hardcoded secrets, general caching
(TTL/invalidation/stampede), feature-flag lifecycle, REST pagination,
app-side date/DST math, frontend state staleness, money/rounding precision.
Full class-by-class map in the coverage appendix at the end of this doc.

Structural coverage gaps found in the pipeline (tool layer, not skill):

- **One configured repo has never been reviewed** — 0 history rows despite
  steady PR traffic. Discovery requires an *outstanding review request* and
  that repo's PRs never carry one. A repo-level dead zone owned by the
  tool's candidacy gate, not the skill.
- **Draft PRs are invisible** and users have noticed. Arguably by design;
  worth surfacing the design choice.
- **~88 queued PRs (18%) produced only SKIPPED runs and no review.**
  Bucketed: 51 skipped because a human had already approved (by design), 20
  already merged, 4 self-authored, ~11 lost because the review request
  evaporated between discovery and run (someone reviewed → GitHub cleared
  the request → precheck declined). The 11 are a real, silent coverage leak.

### 2. Are we mis-approving? (false approves)

Strikingly clean. One human change-request after our approval across 307
approvals, five approved-merged-then-reverted chains — and on deep-dive,
**zero clean false approves**. Anonymized verdicts:

| Case | Surface signal | Deep-dive verdict |
| --- | --- | --- |
| A | changes-requested after approve | **A save, not a miss.** A cross-tenant vuln was introduced by a merge-refresh *after* our approvals; a re-review caught it, it was fixed and re-approved before merge. Both our approvals were on heads that never carried the vuln. |
| B | reverted by an incident hotfix | **Partial miss.** Emergency hotfix that masked the symptom by excluding fields from a validation schema; our review traced the mechanism correctly but presented it as a clean solve without naming the dropped-capture consequence (records created without required data). The root cause (a stale-memo bug) wasn't statically diagnosable; the tradeoff was. |
| C | large teardown reverted | **Partial miss (judgement, not code).** Diff was fine; the PR contained an irreversible DROP TABLE migration behind an unlanded stack. Our review explicitly downgraded the rollout gate to "a merge-gate concern rather than a review finding" and approved; a sibling reviewer said *wait* on the same facts. Merged out of order, reverted within seconds. |
| D | CI gate partially reverted | Not a miss. The reverted piece was working-but-redundant CI machinery (a cost/altitude judgment); the substantive changes stayed in. |
| E | feature reverted | **A save.** We initially withheld over a real over-matching-pattern bug, the author fixed it with a regression test, we approved; the later revert was a pure product decision. |
| F | "reverted" | **False premise.** The revert PR was abandoned unmerged; the change is live and built upon. Bonus: our review of the revert PR itself correctly P1'd that reverting alone would be reintroduced by descendant PRs. |

Method note for future audits: check the revert PR's *merge state*, not its
existence — case-F-style precautionary reverts otherwise inflate the miss
count.

The two partials share one recurring weakness, independently reported by two
audit agents: **the reviewer traces the immediate mechanism correctly, then
stops before pricing the downstream/operational consequence** (dropped data
capture behind a symptom fix; irreversible DDL behind an unlanded stack) and
files it under "ops, not review".

One scoring caveat: "no false approves" is measured on *merged* outcomes.
Section 3 shows we did approve heads that carried real defects — they just
never merged because another reviewer blocked and the authors fixed them.
The merge-outcome record is partly the safety net's doing, not only ours.

### 3. Too weak anywhere?

A second-opinion audit of five randomly sampled approvals found **no bad
approvals** — but a consistent effort-allocation inversion: **review depth
tracked diff tractability, not diff risk.**

- Small/mechanical or self-contained PRs got crisp contract-level analysis.
  One CI-migration PR review even held a correct P1 ("your own description
  says stay draft until a real deployment exercises this") and approved only
  after a named staging run proved the path.
- The riskiest diff in the sample — ~700 lines on a live payment path,
  deliberately changing a charging policy — drew **one FYI sentence and zero
  inline comments**, largely restating the PR description. The approval was
  correct (thorough tests; decision ratified elsewhere), but the review
  stayed silent on two risks the PR body itself admitted. Those are exactly
  the "what to watch in prod" notes an aggressive review exists to make.

The sharpest "too weak" evidence comes from three PRs where a colleague's
independently-configured deep-tier AI reviewer went head-to-head with ours.
Caveats: that reviewer over-reached badly on one PR (a huge findings cascade
against an approach that was entirely backed out pre-merge), and on another
it was reviewing its own operator's PR. Even so, on shipped code the score
was lopsided: ours produced **one** substantive unique catch across all
three (a real privilege-downgrade regression) and approved all three, while
the sibling caught — and authors then fixed — a pagination-after-limit
correctness bug, a permission gate enforced only in the web UI with the
server wide open, an unbounded merge loop, a reorder-deletes-protected-row
edge case, a cross-tenant row-level-security hole, a silent
enqueue-failure-reported-as-success, and a check-then-write race.

Two structural conclusions:

1. **Systematic depth gaps** on: concurrency/TOCTOU races, access control
   enforced in the wrong layer, tenant isolation, rollout ordering,
   async-job silent failure, and loop-termination edge cases. Notably the
   lens/pack *text* already covered most of these — the misses were
   application failures, not coverage-list failures.
2. **The one identified lever behind most of them:** the reviewer accepted
   the author's "deferred / out of scope / follow-up" framing as dispositive
   and approved on it — in one case the "deferred" item was a live
   server-side auth hole the author was later pushed to close. The
   sibling's opposite stance (a scope declaration documents a risk, it
   doesn't make unsafe code safe) is what produced its edge.

### 4. Too harsh anywhere?

- The profile fallback rules make **aggressive the de-facto only profile**
  (96% of reviews; the assertive fallback fires rarely; passive/neutral are
  unreachable without explicit caller request). The four-profile system is
  in practice a one-profile system.
- Aggressive withholds approval for any P1/P2/P3. Of 214 COMMENTED reviews
  to approvable authors: 148 cited a P1, 52 P2-only, ≤14 P3-nits-only (by
  top-level body markers; deep-dives showed some "P3-only" bodies carried P2
  inline comments).
- 42 PRs by approvable authors merged with our COMMENT as the last word from
  us; in 36 the head moved afterwards (author kept working), 6 merged the
  exact reviewed head untouched.
- **Deep-dive verdict: the too-harsh thesis mostly collapses.** Across the
  five worst-looking friction candidates, every substantive finding audited
  was correct: two P1s on a dbt PR (CDC tombstone staleness, orphaned child
  rows) drove a real materialization redesign before merge; one P2 was a
  genuine a11y regression (a toggle lost its accessible name); another P2
  was a missing regression test on the exact payment-blocking path the PR
  fixed; the lone pure-P3 hold was a customer-facing docs-vs-behavior
  mismatch fixed in 6 minutes. All ended in our approval after fixes.
- The one real harshness pattern: **revert PRs.** We layered a P2 "add a new
  test" nudge onto a clean unblock-testing revert (while a sibling reviewer
  approved the same head with "findings: none"). It didn't delay the merge,
  but asking a revert to grow coverage is friction the team rightly ignored.
- The merged-at-our-exact-head pool tells the same story with a twist: all
  four audited findings were **technically correct** (verified against the
  merged code); the failures were **severity inflation** and
  **re-litigation**:
  - A real lint-rule bypass that the repo's enforced formatter makes
    unrepresentable in committed code — graded P2 and re-posted *verbatim
    across four review cycles* without engaging the author's correct
    formatter rebuttal. Stubbornness, not diligence.
  - A real hover-state residue inconsistent with the PR's own thesis — but
    P1 for an ephemeral cosmetic is inflated.
  - A legitimate P2/P3 test-coverage nudge on a migration shim; fair but
    strict; merged silently, no harm.
  - **The strongest single win in the dataset**: a first P1 caught a real
    cross-user write hole (fixed before merge); a second P1 caught that the
    fix over-corrected into a fail-closed deny regression — which the
    sibling reviewer audited and *missed*. The author publicly agreed and
    shipped with a documented fast-follow: valid finding, informed human
    override, system working as intended.
- Under a policy where P1/P2/P3 all withhold approval, **over-grading is the
  whole ballgame**: it converts "worth a comment" into "approval withheld".
  Severity calibration, not finding accuracy, is where the aggressive
  profile leaks trust.

### 5. Other observations

- **A fleet of sibling reviewers has emerged.** Colleagues run their own
  independently-configured AI reviewers from their own accounts (different
  markers/formats). 37% of our reviewed PRs also got a sibling review,
  trending up, and near-duplicate outputs across bots are already being
  noticed. Our exact opening markers don't collide with theirs today, but
  nothing guarantees that.
- **Dedup skips burn scheduler runs**: 26 runs re-dispatched heads that
  already had a review; the engine started, checked, and quit. Cheap
  individually (~7 s) but noisy in history and dashboard stats.
- **All 19 ERROR runs are sub-30s engine startup failures** (no agent
  summary written), spread evenly over the window — transient engine spawn
  issues; two PRs errored twice. Invisible unless someone reads history.
- Repeat reviews stack up on long-lived PRs: one PR collected 10 reviews
  from us (three others got 6–7). Each new head within the refresh window
  triggers a full re-review; there is no "nth review of same PR" damping.
- 24 approvals were on PRs later closed unmerged — dominated by one feature
  stack being restacked/consolidated, not defect evidence.

## Recommendations and skill edits

Edits 1–9 landed in the skills repo (2026-07-24); T1–T5 remain open for this
repo. If only two of the skill edits had landed, the right two were Edit 8
(deferral) and Edit 1 (severity calibration): together they address the
biggest miss mechanism and the biggest trust leak.

Two calibration decisions made explicitly during review of these edits:

- Severity de-escalation exists to make grading honest, **not** to lower
  the approval bar — profile thresholds are unchanged.
- A deferral with an explicit tracked reference (linked follow-up PR/issue)
  rides along as FYI for sub-P1 items; P1-grade unsafe-merged-state items
  withhold regardless of tracking.

### Edit 1 — Severity calibration guardrails (SKILL.md, Finding Severity)

De-escalations: tooling-neutralized findings → at most P4; ephemeral UI
cosmetics → at most P3; missing-test findings are P2 only when the untested
path is the behavior the PR exists to deliver; fail-closed regressions one
notch below fail-open. P1 redefined: "merging this plausibly harms users,
data, money, security, or an in-flight rollout." Thresholds unchanged.

### Edit 2 — Engage rebuttals; never re-post verbatim (SKILL.md, Previous findings)

On re-review, an outstanding author rebuttal must be engaged: withdraw or
downgrade with credit, or say why the finding stands. Track findings across
cycles by scenario, not wording.

### Edit 3 — Revert-PR posture (SKILL.md)

Clean reverts don't owe new hygiene: quality/coverage asks ride as FYI. Two
revert-specific P1s stay first-class: unclean/partial restore, and
reintroduction via surviving descendants.

### Edit 4 — Price the downstream consequence (aggressive-objections lens + database-migrations pack)

Trace what a removal/exclusion/disable stops doing downstream and who
depended on it; name what an emergency fix trades away. Irreversible
destructive DDL behind unlanded prerequisites is a P1 review finding, not an
ops concern.

### Edit 5 — Scale scrutiny to blast radius (SKILL.md, Review Procedure)

Money, auth, data deletion, irreversible migrations, and large multi-domain
PRs get the deepest pass. If the PR body names a limitation, the review must
engage it. A high-risk review that adds nothing beyond the PR description is
a failed review even when the verdict is right.

### Edit 6 — Sibling automated reviewers (SKILL.md)

Sibling bot reviews are context, never inputs to our dedup/persona logic,
and never a reason to omit, soften, or defer a finding — there is no
guarantee any sibling runs on a given PR, so the review must stand alone.
Disagreement with a sibling's verdict is stated explicitly. Bot reviews
don't trigger the "someone else reviewed" profile fallback.

### Edit 7 — Three new focus packs

`injection-untrusted-input.md` (accidental injectability, source-to-sink),
`infra-iac.md` (Terraform/IaC blast radius, stateful destruction, security
widening, cost cliffs, env drift), `cicd-workflows.md` (workflow injection,
privileged-trigger foot-guns, unpinned actions, weakened gates, deploy
waiter semantics).

### Edit 8 — Deferral is not a safety argument (SKILL.md + issue-fit lens)

The audit's highest-leverage change. A "deferred / out of scope" label
documents a risk; it does not make the merged state safe. Live auth gaps,
client-only enforcement, tenant holes, data-loss/silent-failure paths, and
unguarded races stay P1 regardless of tracking. Sub-P1 deferred items with
an explicit tracked reference become FYI; untracked deferrals keep their
severity.

### Edit 9 — Force application of the race/layered-auth checks (correctness-edge-cases lens + auth-permissions pack)

Explicit trace steps, because checklists get applied and adjectives don't:
one read-modify-write staleness trace per changed write path; a named
termination bound per data-dependent loop; name the enforcing layer for
every stated restriction — UI-only enforcement is P1.

### Tool-layer changes (this repo, open)

- **T1 — dead-zone repo.** A configured repo gets zero reviews because its
  PRs never carry review requests and the candidacy gate requires one. Add a
  per-repo discovery mode (`review_requested | all_open`) or drop such repos
  from config so the coverage claim is honest.
- **T2 — evaporated-request leak.** Open, unapproved PRs are silently
  dropped when their review request disappears between discovery and run
  (any review submission clears GitHub's request). Consider making
  discovered candidacy stickier: accept *either* an open request *or* "no
  review from us yet and we queued it while a request existed".
- **T3 — dedup-skip waste + repeat-review damping.** Check history/GitHub
  review state before dispatching a run; add per-PR damping (e.g., after
  the 3rd review, re-review only on request or a significantly larger diff
  delta).
- **T4 — ERROR visibility.** Surface an error-rate tile in dashboard stats
  and/or retry once on engine startup failure.
- **T5 — fleet coordination.** Sibling-reviewer overlap is significant and
  rising; agree markers/claims across operators (who reviews what, how bots
  credit each other). Edit 6 is the unilateral half of this.

## Appendix: defect-class coverage map (condensed, pre-edit)

From a full read of all 15 lenses + 11 focus packs as of 2026-07-24, before
Edit 7 landed:

- **Absent (no owner):** accidental injection (SQLi/XSS/SSRF/command/path/
  deserialization), infra/IaC, CI/CD config, cryptography misuse.
- **Partial:** concurrency depth (deadlock/lock ordering), app-side
  algorithmic performance, non-malicious dependency bumps (CVE/lockfile),
  hardcoded secrets, error-handling/observability as a discipline, general
  caching (TTL/invalidation/stampede), feature-flag lifecycle, resource
  leaks (pools/sockets), PII classification/retention, app-code date/DST
  math, Unicode/encoding, frontend state staleness beyond GraphQL caches,
  REST/offset pagination, rate limiting/abuse, log injection,
  money/rounding precision.
- **Well covered:** idempotency, API backwards compat, input validation
  (correctness sense), rollout/version-skew, i18n, SQL semantics, dbt,
  warehouse cost, GraphQL/gRPC contracts, auth/authz, a11y, batch/queue
  failure behavior.

## Appendix: method

Reproducible against any deployment's own data:

- GraphQL bulk fetch: all history PRs × (metadata, reviews, first/last
  thread comments, files, labels) in aliased chunks of 15 via `gh api
  graphql`, landed as JSONL and loaded into a scratch DuckDB alongside the
  history table.
- "Ours" = review by the tool's gh user whose body starts with a
  `🦎<profile>` marker.
- Severity extraction = regex on marker tokens (P0…P4, FYI) in review
  bodies; treat top-level-body counts as an upper bound (inline comments
  can carry severities the body omits).
- Thread engagement uses GitHub reviewThreads (isResolved, comment count).
- Skip-reason buckets join run timestamps against merge/close/first-human-
  approval times; residuals attributed via the Go candidacy gate.
- Revert matching: search revert-titled PRs, match referenced PR
  numbers/titles against approved history, then **verify the revert PR
  merged** before counting it.
- Verdict-quality audits: read-only subagents re-reviewing flagged PRs and
  comparing our reviews against subsequent human/bot activity.
