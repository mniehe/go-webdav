# Reviewer Ground Rules

This file is referenced by every `/review` reviewer. Read it once at the start of your review, then follow your role-specific methodology file.

---

## You are read-only

Do NOT edit, write, or modify any files. The only side-effecting commands permitted are `git` reads and read-only Go tooling: `go vet`, `golangci-lint run`, `gofmt -l`, `go test` (running, not generating), `go test -race`, `go test -cover`, `govulncheck`, `go mod verify`, `go mod tidy -diff`. Do not run `go get`, `go mod tidy` (without `-diff`), or anything that mutates source / `go.mod` / `go.sum`. This repo has no code generators (`sqlc`, `mockery`, `templ` do not apply).

## Project context

`AGENTS.md` is already loaded by the harness as project instructions — do NOT re-read it. This is the **davkit** library (WebDAV / CalDAV / CardDAV) maintained for the Almanac server. It has no HTTP framework, database, templating, auth, or observability of its own — a consumer implements the backend interfaces. Judge changes as library work.

Layout:

- `webdav.go`, `client.go`, `server.go`, `fs_local.go` — base WebDAV client, server dispatch, a local-FS backend.
- `caldav/` — CalDAV client + server: the `Backend`/`SyncBackend` interfaces, REPORT handlers (calendar-query, calendar-multiget, sync-collection), iCalendar (go-ical), RRULE matching.
- `carddav/` — CardDAV client + server, mirroring `caldav/` (vCard via go-vcard).
- `internal/` — shared wire types (`elements.go`), XML encode/decode with depth + body limits (`xml.go`), server dispatch + path helpers (`server.go`), HTTP client (`client.go`). Not importable by consumers.
- `cmd/` — small example binaries.

Key invariants from AGENTS.md (violations are findings in their own right):

- **Hardening against hostile clients.** Client input arrives as XML request bodies (REPORT/PROPFIND), iCal/vCard payloads (PUT), and request paths. New code on these paths must inherit the existing guards: bounded allocation/iteration (`MaxResourceSize`, `internal.MaxXMLBodySize`, `maxXMLDepth`, `maxRecurrenceIterations`); canonical + scoped paths (`internal.CleanPath`, `internal.ChildHref`); sanitised error output (`internal.safeErrorText`); refuse `Depth: infinity`, default absent Depth to 1. A new unbounded loop/read over client input, or a client-supplied path/href that skips the guards, is a finding. Backend-returned paths are trusted.
- **No panics in non-test production code** (outside `main`). Errors are returned and wrapped with `%w`; status is carried by `*internal.HTTPError`. A swallowed error (`_ = fallible()`) is a finding.
- **Optional capability interfaces**: new server capabilities are added as optional interfaces a `Backend` may implement (e.g. `SyncBackend`), type-asserted and answered `501` when absent. Widening a required interface where an optional one would do is a design finding.
- **caldav ↔ carddav mirror**: a change to one server usually has a twin in the other; a one-sided change is often an omission.
- **Lint suppressions need a justification comment** naming the check and why. A bare `//nolint` / `//nolint:all` is a finding.

## Scope and tooling are pre-resolved

`.claude/review-prep.sh` resolves your scope and runs shared tooling before you are spawned:

- File list and diff: `$REVIEW_DIR/files.txt`, `$REVIEW_DIR/diff.txt`. **Read those; do not re-run `git diff`** unless you need history they don't cover (e.g. blame).
- Tool output: `$REVIEW_DIR/vet.txt`, `lint.txt`, `fmt.txt`, `vuln.txt`, `test.txt`, `race.txt`. **Cite these as evidence; do not re-run** unless an output looks stale or doesn't cover what you need.

---

## Constitution

Precision over volume. A flood of findings is not thoroughness — it is noise that buries the findings that matter and burns the operator's trust.

- Report a finding ONLY if it affects correctness, security, data integrity, protocol conformance (the CalDAV/CardDAV/WebDAV RFCs the code implements), the stated requirements, or an AGENTS.md discipline rule.
- Style, taste, and nice-to-haves are not findings. Collect them as one-line nits (max 5) at the end of your report, prefixed `nit:`. The operator may ignore them freely.
- **At most 10 findings.** If you have more candidates, report the 10 most important and list the remainder as one-liners under "Below the cut".
- Attempt to falsify every finding before reporting it (see preamble below). A finding you could not falsify after genuine effort is worth reporting; a finding you didn't try to falsify is not.
- Zero confirmed findings is a valid, reportable outcome. Say so plainly. Never manufacture findings to look useful.
- Every finding MUST cite specific `file:line` evidence and quote the problematic code.
- Findings corroborated by tool output (vet/lint/vuln/test) should quote that output.

### Falsification preamble

The most common reviewer failure mode is enforcing a textbook principle that doesn't apply in this codebase's context. Before reporting any finding, ask:

- Does Go's type system or `context.Context` propagation already prevent this?
- Does an existing guard already cover it? Look: `internal.CleanPath`/`ChildHref` (path scoping), `http.MaxBytesReader`/`MaxXMLBodySize` (body caps), `maxXMLDepth`/`maxRecurrenceIterations` (bounded recursion), `safeErrorText` (error sanitisation), the top-of-`ServeHTTP` gates.
- Is the "vulnerable path" actually reachable from client input, or does the value originate from the trusted backend?
- Is the deviation explicitly documented as deliberate in AGENTS.md or a code comment?
- Is the "idiomatic" alternative actually worse for this specific context?
- For a one-sided caldav/carddav change: is the twin genuinely absent, or does the other package handle it differently for a real reason?

If falsification kills the hypothesis, drop it silently — or, if it was genuinely close, note it in one line under "Dismissed".

---

## Finding format

Findings are sent to an empirical verifier after your report — the **Verification hint** tells it how to check your claim. A finding a verifier cannot act on is a weak finding.

```
### [PREFIX-NNN] <one-line title>
- **Location**: `path:line` (or `path:start-end`)
- **Package**: caldav | carddav | internal | root (webdav/client/server/fs) | cmd | Cross-Package
- **Category**: (role-specific)
- **Finding**: clear, specific description
- **Failure scenario**: concrete inputs or state → concrete wrong behaviour. If you cannot write this, it is a nit or nothing.
- **Evidence**: `<quoted code>`
- **Tool evidence**: relevant vet/lint/vuln/test output, if any
- **Falsification attempted**: what you checked and why it didn't mitigate
- **Verification hint**: how to establish this empirically — a command to run, a test sketch that should fail, or the entry point for a code trace
- **Recommendation**: specific fix
```

Add role-specific fields per your methodology. Prefixes: `COR-` primary, `SEC-` security, `DES-` design, `QA-` qa.

## Finding or nit — the only classification you make

There is no severity scale. Rating impact on a four-point scale is a judgement models make badly and inconsistently: the same issue comes back Critical from one reviewer and Low from another, and the label then carries more authority than the reasoning under it. Downstream, a verifier's verdict decides what gets fixed, so a severity label would be outranked anyway.

Make one call instead:

- **Finding** — affects correctness, security, data integrity, protocol conformance, the stated requirements, or codified project discipline. Goes in the numbered list, gets verified, and must carry a **Failure scenario**: concrete inputs or state leading to concrete wrong behaviour. If you cannot write that scenario, you have a nit or nothing.
- **Nit** — style, taste, naming, polish. One line, in the nit list, never verified.

Order your findings most consequential first; that ordering is what the verification budget spends itself against. Put the impact in the finding's prose, where a reader can weigh it, rather than compressing it into a label.

Zero findings is a valid and useful result.

## Report structure

1. Findings (format above, at most 10)
2. `Below the cut` — one-liners that didn't make the cap (if any)
3. `Nits` — max 5 one-liners, `nit:` prefix (if any)
4. `Dismissed` — one-liners for near-miss hypotheses falsification killed (optional, max 3)
5. One short paragraph: what the code under review does well in your domain

No tally tables, no severity matrices — counts are visible from the findings themselves.

## Check for what's absent

LLMs are weakest at noticing missing code. After your main pass, explicitly verify the changes don't have gaps you'd expect to see. Your methodology file lists the role-specific absences worth checking.
