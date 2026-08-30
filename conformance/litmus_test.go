// Package conformance runs external protocol test suites against this library.
//
// These tests exercise the server the way a real client does, over HTTP, and
// catch the class of defect hand-written tests miss: the requirement nobody
// thought to write a test for.
package conformance

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/mniehe/davkit"
)

// requireEnv turns the absent-litmus skip into a failure. Set it wherever the
// suite is expected to actually run.
const requireEnv = "WEBDAV_REQUIRE_LITMUS"

// litmusSuites is the set Xandikos runs, and for the same reasons. "props"
// needs dead-property storage, which this library does not offer; "locks" is
// skipped by litmus itself, which reads the DAV compliance header and declines
// to test a class the server does not claim.
const litmusSuites = "basic http copymove"

// TestLitmus runs the litmus WebDAV suite (RFC 4918) against LocalFileSystem.
//
// litmus is not a Go dependency and is not vendored; the test skips when it is
// absent so `go test ./...` stays green on a bare checkout. Put it on PATH to
// exercise it — `nix shell nixpkgs#litmus` is enough.
func TestLitmus(t *testing.T) {
	bin, err := exec.LookPath("litmus")
	if err != nil {
		// A skip that can hide a failure is worse than no test. CI sets
		// requireEnv, so litmus dropping out of the build turns the check red
		// rather than leaving it green and untested.
		if os.Getenv(requireEnv) != "" {
			t.Fatalf("%s is set but litmus is not on PATH: %v", requireEnv, err)
		}
		t.Skip("litmus not on PATH; `nix develop` provides it")
	}

	srv := httptest.NewServer(&webdav.Handler{
		FileSystem: webdav.LocalFileSystem(t.TempDir()),
	})
	defer srv.Close()

	cmd := exec.Command(bin, srv.URL+"/")
	cmd.Env = append(cmd.Environ(), "TESTS="+litmusSuites)
	// litmus writes debug.log and child.log beside itself; keep them out of the
	// package directory.
	cmd.Dir = t.TempDir()

	out, err := cmd.CombinedOutput()
	t.Logf("litmus %s against %s:\n%s", litmusSuites, srv.URL, out)
	if err != nil {
		t.Fatalf("litmus reported failures: %v", err)
	}

	// litmus exits 0 when every suite passes, but a suite that fails to start
	// also produces no FAIL lines. Assert we saw real results.
	if !strings.Contains(string(out), "summary for") {
		t.Fatal("litmus produced no suite summary; it did not run to completion")
	}
}
