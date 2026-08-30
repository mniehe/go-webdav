Run an evidence-first code review: one primary correctness reviewer, escalation specialists only when the diff crosses their surface, empirical verification of serious findings, and binary triage. Reviewers run in fresh, isolated agent contexts with no knowledge of this conversation or each other.

The shape: finding volume is coupled to noise, LLM-graded 0–100 confidence is near-random, and same-model agreement is not independence — so this pipeline uses few precision-prompted reviewers and executable evidence instead of rhetorical challenge.

This is the davkit library's copy of the pipeline: packages `caldav`, `carddav`, `internal`, and the base `webdav`. There is no web/ux surface, so those reviewers do not exist here; the base branch is `master`.

---

## Phase 0: Prep (script) then route (agent)

Prep is deterministic and belongs to a script, not a model. Run it yourself:

```sh
REVIEW_SCOPE_TEXT="$ARGUMENTS" .claude/review-prep.sh [BASE [HEAD]]
```

Pass `BASE`/`HEAD` only when the operator named an explicit range; otherwise let it resolve the scope (uncommitted delta if the tree is dirty, else the branch delta against `master`). It writes `scope.txt`, `files.txt`, `diff.txt`, `head.sha`, `uncommitted.patch` when dirty, and the six tool captures (`vet`, `lint`, `fmt`, `vuln`, `test`, `race`), then prints:

```
Base: {ref the diff is taken against}
Head: {short sha, and whether uncommitted.patch was written}
Files: {count} files, {changed-line count} lines ({breakdown by tag})
Candidates: {mechanically proposed escalations, with the counts that proposed them}
Review state: {REVIEW_DIR path}
Warnings: {oversized diff, unappliable diff, tooling failures — or "none"}
```

Note: for an **uncommitted-scope** review, untracked new files are not in `git diff HEAD`; if the change adds files, `git add -N` them before running prep so they are covered. Stop and surface the problem if the script exits non-zero, `Review state` is missing, or `Warnings` reports `diff.txt is NOT a unified patch`. Relay an oversized-diff warning and proceed.

Then spawn one agent with `subagent_type: "review-router"` to trim the candidates. It has no Bash tool — selection is the only thing left that needs judgement.

Spawn prompt:

> Choose the escalations for this review. Review state is at `$REVIEW_DIR`.
>
> [paste the script's routing block]
>
> The operator's scope text follows (may be empty):
>
> $ARGUMENTS

It returns:

```
Escalations: {0-2 of: security, qa, design — or "none"}
Skipped: {each candidate dropped, with a one-line reason}
```

**Relay the script's block plus the router's selection to the user** before continuing. Substitute `$REVIEW_DIR` into every spawn prompt below.

---

## Phase 1: Review

Spawn the primary reviewer plus any escalation specialists **in parallel, in a single message**. Use `subagent_type: "review"` for all of them.

**Primary reviewer** (always runs):

> Read `.claude/review-agents/_shared.md` for ground rules, then `.claude/review-agents/primary.md` for your methodology. Follow them.
>
> Pre-resolved review state lives in `$REVIEW_DIR` (`scope.txt`, `files.txt`, `diff.txt`, `vet.txt`, `lint.txt`, `fmt.txt`, `vuln.txt`, `test.txt`, `race.txt`). Read from there; do not re-run git diff or the captured Go tools.
>
> Report findings in the format in `_shared.md`.

**Security** (if escalated): same shape, methodology `.claude/review-agents/sec-review.md`.

**QA** (if escalated): same shape, methodology `.claude/review-agents/qa-review.md`.

**Design** (if escalated): same shape, methodology `.claude/review-agents/design-review.md`.

Append the script's `Warnings` caveats to each spawn prompt when present.

---

## Phase 2: Empirical verification

Collect every **finding** from the reports, in the order each reviewer listed them — reviewers order most consequential first. Verify up to 10, keeping ~2 spawn slots in reserve for second opinions. Note anything dropped beyond the cap in the final output.

**Nits are never verified.** They are the reviewer's own "this is taste, not correctness" call, and spending a verifier on one is spending it on the wrong thing.

Verification runs in two tiers. Most findings settle by reading; only some need a build. Spawning a worktree costs a cold environment setup, so it is earned, not default.

### Phase 2a — Trace tier (no worktree)

Spawn one verifier per finding with `subagent_type: "review-verifier"` and **no** `isolation`, all in parallel. Spawn prompt:

> Establish whether the following code-review finding is real. You are in **trace mode**: no worktree, read-only. Settle it from the review state and the code as committed.
>
> Pre-resolved review state: `$REVIEW_DIR` (`diff.txt`, `files.txt`, `head.sha`, Go tool outputs). `diff.txt` is a real unified patch — read it, do not apply it.
>
> Do not write, edit, or mutate anything anywhere. If the finding turns on runtime behaviour you cannot establish by reading — whether a test actually discriminates, what a handler returns, which of two arrivals wins a race — return `NEEDS-EXECUTION` and name the exact command or mutation that would settle it. Never refute a finding because you could not run it.
>
> --- FINDING ---
> [Paste the complete finding, including its Verification hint]

### Phase 2b — Execution tier (worktree)

Only for findings returned as `NEEDS-EXECUTION`. Re-spawn each with `subagent_type: "review-verifier"` and `isolation: "worktree"`, in parallel. Spawn prompt:

> Establish whether the following code-review finding is real. You are in **execution mode**, in a disposable git worktree cut for this verification. All writes stay inside your CWD; nothing leaks out.
>
> Your worktree shares the repository's object store, so the code under review is already present. As your first action:
>
> ```sh
> git switch --detach "$(cat "$REVIEW_DIR/head.sha")"
> [ -s "$REVIEW_DIR/uncommitted.patch" ] && git apply "$REVIEW_DIR/uncommitted.patch"
> ```
>
> Do NOT reconstruct the tree by hand and do NOT `git apply "$REVIEW_DIR/diff.txt"` — its base may not exist in your worktree. `head.sha` is the source of truth.
>
> Isolation contract (non-negotiable): never write, edit, `git apply`, `git stash`, or navigate to any path outside your CWD. Re-pointing your own HEAD as above is the one exception and is required. Never touch sibling worktrees — any path from `git worktree list` other than your own is off-limits. Never run `docker build` or any packaging step — `go build ./...` and `go test` are enough.
>
> The trace-tier verifier said this needs execution. Its reasoning: [paste the Phase 2a Notes]
>
> --- FINDING ---
> [Paste the complete finding, including its Verification hint]

### Both tiers

Verdicts are binary-with-evidence: `REPRODUCED` (executed evidence), `SUPPORTED` (complete code trace), `REFUTED` (concrete disproving evidence), `NEEDS-EXECUTION` (trace tier only), `UNVERIFIABLE`. If a verifier returns `UNVERIFIABLE`, spawn one second verifier for that finding with the first's notes appended and the instruction to try a different evidence path. Two `UNVERIFIABLE` verdicts → **needs human eyes**. Total verifier spawns across both tiers must not exceed 16.

If Phase 2a returns `NEEDS-EXECUTION` for a finding and the spawn budget is exhausted, surface it as needs-human-eyes with the command it named — never downgrade it to `SUPPORTED` on the strength of a trace that already declared itself insufficient.

Do not ask verifiers to score confidence, re-rate severity, or find new issues.

---

## Phase 3: Triage

Binary rules — no confidence thresholds and no severity. The verdict does the sorting:

- **Must Fix**: verdict `REPRODUCED` or `SUPPORTED`; or any AGENTS.md zero-tolerance violation (panic in non-test code, ignored error from a fallible operation, unjustified linter suppression) corroborated by tool output.
- **Needs human eyes**: two `UNVERIFIABLE` verdicts, or `NEEDS-EXECUTION` with the spawn budget spent. Include the verifiers' notes and the command they named — the operator decides.
- **Rejected**: verdict `REFUTED` — list with the disproving evidence quoted, so the reviewers' miss is visible.
- **Optional**: nits, and any finding dropped past the verification cap. One line each, prefixed `nit:`.

An unverified finding is not a Must Fix. If the cap or a spawn failure left something unverified and you judge its evidence sound after reading the cited code yourself, say so explicitly and put it under Needs human eyes rather than quietly promoting it.

If you disagree with a verifier after reading the code yourself, say so and explain; do not silently reclassify.

---

## Output format

1. **Routing summary** — the script's block plus the router's selection, plus total agents spawned.
2. **Triage** — Must Fix / Needs human eyes / Rejected / Optional. Each verified finding shows its verdict and the verifier's evidence (one short quote, not the full transcript).
3. **Per-reviewer note** — one paragraph per reviewer that ran: finding count, what it covered, anything notable. No full report duplication; the reports already informed triage.
