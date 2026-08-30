# AGENTS.md — davkit

This is `github.com/mniehe/davkit`, a WebDAV / CalDAV / CardDAV **library** in
Go. The CalDAV and CardDAV packages are built on a ref-based backend contract,
released on their own line starting at `v0.1.0`. Work lands directly on `main`,
the default branch; Almanac consumes it as a normal versioned dependency.

It is a library, not an application. It has **no** HTTP framework, database,
templating, auth system, or observability stack of its own — a consumer (Almanac)
supplies all of that by implementing the backend interfaces. Reviews and changes
should be judged as library work.

## Layout

- `webdav.go`, `client.go`, `server.go`, `fs_local.go` — the base WebDAV client,
  server dispatch, and a local-filesystem backend.
- `caldav/` — the CalDAV server (the ref-based `Backend` plus optional capability
  interfaces, the REPORT handlers, iCalendar via `github.com/emersion/go-ical`,
  RRULE matching). Server only; the sole client is the base `webdav` package.
- `carddav/` — the CardDAV server, mirroring `caldav/` (vCard via
  `github.com/emersion/go-vcard`).
- `internal/` — shared wire types (`elements.go`), XML encode/decode with depth
  and body limits (`xml.go`), the server dispatch and path helpers
  (`server.go`), and the HTTP client (`client.go`). Not importable by consumers.
- `cmd/` — small example binaries.

## Why this rebuild exists (the discipline that matters)

Two themes drive every change here; treat a deviation from either as a finding,
not a nit:

1. **Hardening against hostile clients.** Untrusted clients send the XML request
   bodies (REPORT/PROPFIND), the iCal/vCard payloads (PUT), and the request
   paths. New code on these paths must inherit the existing guards rather than
   reintroduce the classes they close:
   - Bound every allocation and iteration driven by client input:
     `MaxResourceSize` (PUT bodies), `internal.MaxXMLBodySize` (XML bodies),
     `maxXMLDepth` (nesting), `maxRecurrenceIterations` (RRULE expansion). A new
     unbounded loop or read over client input is a finding.
   - Canonicalise and scope paths: `internal.CleanPath` rejects non-canonical
     request paths at the top of `ServeHTTP`; `internal.ChildHref` confines
     client-supplied REPORT hrefs to the request collection. Client-supplied
     paths that skip these are a finding; backend-returned paths are trusted.
   - Sanitise outbound detail: `internal.safeErrorText` returns verbatim text
     only for <500 errors the library itself constructed and generic status text
     for 5xx, so backend detail (SQL, filesystem paths) does not leak. XML output
     goes through the encoder, which escapes.
   - Refuse `Depth: infinity` PROPFIND; default an absent `Depth` to 1.
2. **No new panics; no swallowed errors.** The hardening work specifically
   replaced panics (e.g. nil-data in RRULE matching) with returned errors. A
   `panic`/`os.Exit` in non-test code outside `main`, or a fallible call whose
   error is dropped (`_ = ...`), is a finding. Wrap errors with `%w`. Status is
   carried by `*internal.HTTPError`.

## Workflow: RED before GREEN

Ordering *between* tasks does not matter here — nothing consumes the library
between commits, so pick them up in whatever order suits. The ordering that does
matter is the one *inside* each change:

1. **Prove the defect exists.** Read the code and confirm the failure is real
   before writing anything. A bug inferred from the shape of an API, or inherited
   from a plan or a review without checking it against the tree, is not yet a
   bug.
2. **Write the RED test first.** It must fail against the current tree, and fail
   for the stated reason — run it and read the failure. A new test that passes
   before the fix proves nothing.
3. **Agree the approach before implementing.** With the failure pinned, the
   options are usually clearer than they looked from the plan. Surface them
   rather than committing to the first one.
4. **GREEN, then refactor.** Smallest change that turns the test green, then
   clean up with it still green.

This applies to spec conformance as much as to bugs: an unimplemented RFC
requirement gets a failing test naming the section it comes from, then the code.

Assertions must discriminate. A `strings.Contains` on a leaf tag that would still
pass if the production code were wrong — a flattened namespace, an omitted
wrapper, a field left unpopulated — is not a test. Assert the thing the change is
actually about, and make sure a value the test checks could only have come from
where the test claims it did.

## Conventions

- **Optional capability interfaces.** New server capabilities are added as
  optional interfaces a `Backend` may implement (e.g. `SyncBackend`), type-
  asserted at dispatch and answered `501` when absent — so existing consumers
  keep compiling. Prefer this to widening a required interface.
- **Mirror caldav ↔ carddav.** A change to one server almost always has a twin in
  the other. A one-sided change is usually an omission.
- **Lint discipline.** `.golangci.yaml` is the config (gocritic all, gosec,
  staticcheck all, govet all bar fieldalignment, prealloc, misspell UK, gofmt
  simplify). A `//nolint` MUST carry a justification comment naming the check and
  why; a bare `//nolint` or `//nolint:all` is a finding.
- **Tests pin behaviour.** Table-driven `_test.go` beside the code. `httptest`
  against the handler is the idiom for server behaviour. The `_test.go` panic /
  `t.Fatal` exemption applies to test code only. They are written RED first — see
  *Workflow: RED before GREEN* above.
- **Commits.** Conventional commits, single line, no body, no AI attribution.

## Tooling

Everything builds and tests with the ambient Go toolchain — there is no devshell.

- `go build ./...`
- `go vet ./...`
- `gofmt -l .` (must be empty; config uses `simplify`)
- `golangci-lint run ./...` (must be clean)
- `go test ./...`
- `CGO_ENABLED=1 go test -race ./...`
- `govulncheck ./...`

Do not run generators — there are none (`sqlc`, `mockery`, `templ` do not apply
to this repo).
