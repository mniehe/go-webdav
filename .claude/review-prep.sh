#!/usr/bin/env bash
# Deterministic prep for /review. Resolves the scope, writes every artifact the
# reviewers and verifiers read, runs the shared tooling once, and proposes
# escalation candidates from the file list.
#
# This is the go-webdav fork's copy: a pure Go CalDAV/CardDAV/WebDAV library.
# There is no templ/sqlc/nix/web layer here, so the tooling and taggers are
# trimmed accordingly and the default base branch is `master` (the upstream
# mirror), while our own work lands on `main`.
#
# Everything here is mechanical on purpose. The only judgement left in routing
# is trimming the candidates to at most two, which the router agent does.
#
# Usage:
#   review-prep.sh                 # auto: dirty tree, else branch delta vs master
#   review-prep.sh BASE            # explicit base, HEAD as head
#   review-prep.sh BASE HEAD_REF   # explicit committed range
#
# Prints a routing block on stdout and exits non-zero only if it cannot produce
# a usable review state.

set -uo pipefail

die() {
	echo "review-prep: $*" >&2
	exit 1
}

git rev-parse --show-toplevel >/dev/null 2>&1 || die "not inside a git repository"
ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT" || die "cannot enter $ROOT"

# `diff.external` (difftastic and friends) turns `git diff` into a side-by-side
# rendering rather than a patch. Every diff written here must be appliable and
# must carry line numbers reviewers can cite, so the driver stays off.
gitdiff() { git --no-pager diff --no-ext-diff --no-color "$@"; }

warnings=()
warn() { warnings+=("$1"); }

# The base branch this fork's work is measured against. `master` mirrors
# upstream go-webdav; our own work lands on `main`. Fall back to `main` only if
# `master` is somehow absent.
base_branch="master"
git rev-parse --verify --quiet "$base_branch" >/dev/null 2>&1 ||
	base_branch="main"

# ── Scope ────────────────────────────────────────────────────────────────

dirty=0
git diff --quiet HEAD 2>/dev/null || dirty=1

if [ "$#" -ge 1 ]; then
	base="$1"
	head_ref="${2:-HEAD}"
	scope_kind="explicit"
	# A tree object is a valid base — passing the empty tree
	# (`git hash-object -t tree /dev/null`) reviews a root commit. Trees have
	# no merge-base, so the diff must be two-dot instead of three-dot.
	if ! git rev-parse --verify --quiet "$base^{commit}" >/dev/null 2>&1; then
		git rev-parse --verify --quiet "$base^{tree}" >/dev/null || die "base '$base' does not resolve"
		scope_kind="explicit-tree"
	fi
	git rev-parse --verify --quiet "$head_ref^{commit}" >/dev/null || die "head '$head_ref' does not resolve"
elif [ "$dirty" -eq 1 ]; then
	base="HEAD"
	head_ref="HEAD"
	scope_kind="uncommitted"
else
	base="$(git merge-base "$base_branch" HEAD 2>/dev/null)" || die "cannot find merge-base with $base_branch"
	head_ref="HEAD"
	scope_kind="branch"
fi

mkdir -p "$ROOT/.claude/review-state" || die "cannot create review state root"
REVIEW_DIR="$(mktemp -d "$ROOT/.claude/review-state/run-XXXXXX")" || die "cannot create review state dir"
# The operator's freeform scope text is the router agent's input, not this
# script's; it arrives out of band so the args stay a clean base/head pair.
printf '%s\n' "${REVIEW_SCOPE_TEXT:-<none>}" >"$REVIEW_DIR/scope.txt"

if [ "$scope_kind" = "explicit-tree" ]; then
	range=("$base" "$head_ref")
elif [ "$scope_kind" = "uncommitted" ]; then
	range=("HEAD")
else
	range=("$base...$head_ref")
fi

if [ "$scope_kind" = "uncommitted" ]; then
	git diff --name-only HEAD >"$REVIEW_DIR/files.txt"
	gitdiff HEAD >"$REVIEW_DIR/diff.txt"
	base_label="HEAD (uncommitted)"
else
	git diff --name-only "${range[@]}" >"$REVIEW_DIR/files.txt"
	gitdiff "${range[@]}" >"$REVIEW_DIR/diff.txt"
	base_label="$(git rev-parse --short "$base") ($scope_kind)"
	# A dirty tree on top of a committed range still belongs in the review.
	if [ "$dirty" -eq 1 ] && [ "$head_ref" = "HEAD" ]; then
		git diff --name-only HEAD >>"$REVIEW_DIR/files.txt"
		gitdiff HEAD >>"$REVIEW_DIR/diff.txt"
		warn "working tree is dirty; uncommitted changes are appended to the diff"
	fi
fi
sort -u -o "$REVIEW_DIR/files.txt" "$REVIEW_DIR/files.txt"

# ── What a verifier needs to materialise the reviewed tree ───────────────
# Verifier worktrees share this repository's object store — so a commit id is
# enough and no patch transport is needed. Untracked new files do not appear in
# `git diff HEAD`; stage them (`git add -N`) before an uncommitted-scope review
# if they must be covered.
git rev-parse "$head_ref" >"$REVIEW_DIR/head.sha"
if [ "$dirty" -eq 1 ]; then
	gitdiff HEAD >"$REVIEW_DIR/uncommitted.patch"
fi

# ── Self-check: is diff.txt actually a patch? ────────────────────────────
if [ -s "$REVIEW_DIR/diff.txt" ]; then
	if ! head -1 "$REVIEW_DIR/diff.txt" | grep -q '^diff --git '; then
		warn "diff.txt is NOT a unified patch — an external diff driver leaked through"
	fi
elif [ "$scope_kind" != "explicit" ]; then
	warn "diff is empty for this scope"
fi

file_count=$(wc -l <"$REVIEW_DIR/files.txt" | tr -d ' ')
# Every added/removed line, minus the two `---`/`+++` header lines each file
# contributes. A naive `^[+-][^+-]` silently drops blank added/removed lines.
diff_lines=$(grep -cE '^[+-]' "$REVIEW_DIR/diff.txt" 2>/dev/null)
diff_files=$(grep -c '^diff --git ' "$REVIEW_DIR/diff.txt" 2>/dev/null)
changed_lines=$((diff_lines - 2 * diff_files))
[ "$changed_lines" -gt 400 ] && warn "diff is $changed_lines changed lines; review effectiveness falls off sharply past ~400 — consider splitting"

# ── Tag the files ────────────────────────────────────────────────────────

# `grep -c` prints 0 when nothing matches and exits 1 doing so, so a `|| echo 0`
# fallback would append a second zero and break every arithmetic test below.
tagged() { grep -cE "$1" "$REVIEW_DIR/files.txt" 2>/dev/null; }

n_caldav=$(tagged '^caldav/.*\.go$')
n_carddav=$(tagged '^carddav/.*\.go$')
n_internal=$(tagged '^internal/.*\.go$')
n_root=$(tagged '^[^/]+\.go$')
n_cmd=$(tagged '^cmd/.*\.go$')
n_deps=$(tagged '^go\.(mod|sum)$')
n_ci=$(tagged '(^\.golangci\.ya?ml$|^\.build\.yml$|^\.github/)')
n_docs=$(tagged '\.md$')
n_test=$(tagged '_test\.go$')

# Non-test Go touching the request-handling / parsing / path surface is the
# security signal: these packages decode untrusted client XML (REPORT/PROPFIND
# bodies), parse iCal/vCard, and resolve request paths.
surface_prod=$(grep -E '^(caldav|carddav|internal)/.*\.go$|^[^/]+\.go$' "$REVIEW_DIR/files.txt" | grep -vcE '_test\.go$')

packages_touched=$(grep -E '\.go$' "$REVIEW_DIR/files.txt" | sed -E 's#/[^/]+$##; s#^[^/]+\.go$#.#' | sort -u | wc -l | tr -d ' ')
added_files=$(git diff --name-only --diff-filter=A "${range[@]}" 2>/dev/null | grep -c '\.go$')
# Exported API added or reshaped — the public surface consumers depend on.
public_api=$(grep -cE '^\+(func|type|var|const) [A-Z]|^\+\t[A-Z][A-Za-z0-9_]* ' "$REVIEW_DIR/diff.txt" 2>/dev/null)

# Production logic landing with no test change is the qa signal that matters
# most: this library's guarantees only exist as far as its tests pin them.
go_prod=$(grep -E '\.go$' "$REVIEW_DIR/files.txt" | grep -vcE '_test\.go$')
code_without_tests=0
[ "$go_prod" -gt 0 ] && [ "$n_test" -eq 0 ] && code_without_tests=1

# ── Propose escalation candidates ────────────────────────────────────────
# Mechanical, and deliberately generous: the router agent trims to two.

candidates=()
propose() { candidates+=("$1: $2"); }

{ [ "$surface_prod" -gt 0 ] || [ "$n_deps" -gt 0 ] || [ "$n_ci" -gt 0 ]; } &&
	propose security "surface=$surface_prod deps=$n_deps ci=$n_ci"
{ [ "$n_test" -gt 0 ] || [ "$code_without_tests" -eq 1 ]; } &&
	propose qa "test files=$n_test code-without-tests=$code_without_tests"
{ [ "$packages_touched" -ge 2 ] || [ "$added_files" -gt 0 ] || [ "$public_api" -gt 0 ]; } &&
	propose design "packages=$packages_touched new .go=$added_files new exported items=$public_api"

# ── Shared tooling, once ─────────────────────────────────────────────────
# Run unconditionally: escalation is decided after prep, and the test run
# reuses the vet/lint build cache, so the marginal cost of always having it
# is small. An explicit historical range is the one case where the working-tree
# captures do not reflect the reviewed commit.
case "$scope_kind" in explicit*)
	[ "$head_ref" != "HEAD" ] &&
		warn "tool captures reflect the working tree, not $head_ref — cite them for scope, not as evidence about the reviewed commit"
	;;
esac

# The library builds with the ambient Go toolchain; there is no devshell to
# enter. Tools must be on PATH.
run_tool() {
	local out="$1"
	shift
	"$@" >"$REVIEW_DIR/$out" 2>&1 || true
}

command -v go >/dev/null 2>&1 || warn "go is not on PATH; vet/test/race/vuln captures are empty"

run_tool vet.txt go vet ./...
if command -v golangci-lint >/dev/null 2>&1; then
	run_tool lint.txt golangci-lint run ./...
else
	printf 'golangci-lint not found on PATH\n' >"$REVIEW_DIR/lint.txt"
	warn "golangci-lint is not installed; lint.txt records the absence, not a clean run"
fi
run_tool fmt.txt gofmt -l .
if command -v govulncheck >/dev/null 2>&1; then
	run_tool vuln.txt govulncheck ./...
else
	printf 'govulncheck not found on PATH\n' >"$REVIEW_DIR/vuln.txt"
	warn "govulncheck is not installed; vuln.txt records the absence, not a clean scan"
fi
run_tool test.txt go test ./...
run_tool race.txt env CGO_ENABLED=1 go test -race ./...
grep -qE '^FAIL' "$REVIEW_DIR/test.txt" "$REVIEW_DIR/race.txt" &&
	warn "test.txt/race.txt report failing tests"

# ── Routing block ────────────────────────────────────────────────────────

printf 'Base: %s\n' "$base_label"
printf 'Head: %s%s\n' "$(git rev-parse --short "$head_ref")" \
	"$([ -s "$REVIEW_DIR/uncommitted.patch" ] && echo ' + uncommitted.patch')"
printf 'Files: %s files, %s changed lines (caldav=%s carddav=%s internal=%s root=%s cmd=%s deps=%s ci=%s docs=%s test=%s)\n' \
	"$file_count" "$changed_lines" "$n_caldav" "$n_carddav" "$n_internal" "$n_root" "$n_cmd" "$n_deps" "$n_ci" "$n_docs" "$n_test"
printf 'Candidates: %s\n' "$([ ${#candidates[@]} -eq 0 ] && echo none || printf '%s; ' "${candidates[@]}")"
printf 'Review state: %s\n' "$REVIEW_DIR"
printf 'Warnings: %s\n' "$([ ${#warnings[@]} -eq 0 ] && echo none || printf '%s; ' "${warnings[@]}")"
