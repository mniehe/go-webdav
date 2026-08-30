package webdav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mniehe/davkit/internal"
)

// failingBody yields prefix and then fails, standing in for a client that
// disconnects part-way through a PUT.
type failingBody struct {
	prefix io.Reader
	err    error
}

func (b *failingBody) Read(p []byte) (int, error) {
	n, err := b.prefix.Read(p)
	if errors.Is(err, io.EOF) {
		return n, b.err
	}
	return n, err
}

func (b *failingBody) Close() error { return nil }

// A PUT that cannot be read to completion must leave the stored resource as it
// was. Truncating the destination before the body is known good turns any client
// disconnect into unrecoverable data loss.
func TestCreateFailureLeavesExistingResourceIntact(t *testing.T) {
	dir := t.TempDir()
	fs := LocalFileSystem(dir)
	ctx := context.Background()

	const original = "the original contents that must survive\n"
	if err := os.WriteFile(filepath.Join(dir, "existing.txt"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	readErr := errors.New("injected read failure")
	body := &failingBody{prefix: strings.NewReader("partial replacement"), err: readErr}

	_, _, err := fs.Create(ctx, "/existing.txt", body, &CreateOptions{})
	if err == nil {
		t.Fatal("Create succeeded despite an unreadable body")
	}

	rc, err := fs.Open(ctx, "/existing.txt")
	if err != nil {
		t.Fatalf("the previous resource was destroyed by the failed write: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("contents = %q, want the untouched original %q", got, original)
	}
}

// The same failure on a resource that did not exist must not leave a partial
// file behind for the next reader to find.
func TestCreateFailureLeavesNoPartialResource(t *testing.T) {
	dir := t.TempDir()
	fs := LocalFileSystem(dir)
	ctx := context.Background()

	body := &failingBody{prefix: strings.NewReader("partial"), err: errors.New("injected read failure")}
	if _, _, err := fs.Create(ctx, "/new.txt", body, &CreateOptions{}); err == nil {
		t.Fatal("Create succeeded despite an unreadable body")
	}

	if _, err := fs.Stat(ctx, "/new.txt"); err == nil {
		t.Error("a failed create left the new resource in place")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Errorf("directory is not empty after a failed create: %s", entry.Name())
	}
}

// The success path must still replace the contents and report whether the
// resource was newly created.
func TestCreateReplacesContents(t *testing.T) {
	dir := t.TempDir()
	fs := LocalFileSystem(dir)
	ctx := context.Background()

	fi, created, err := fs.Create(ctx, "/f.txt", io.NopCloser(strings.NewReader("first")), &CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("created = false for a resource that did not exist")
	}
	if fi.Size != int64(len("first")) {
		t.Errorf("size = %d, want %d", fi.Size, len("first"))
	}

	fi, created, err = fs.Create(ctx, "/f.txt", io.NopCloser(strings.NewReader("second body")), &CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("created = true for a resource that already existed")
	}
	if fi.Size != int64(len("second body")) {
		t.Errorf("size = %d, want %d", fi.Size, len("second body"))
	}

	rc, err := fs.Open(ctx, "/f.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second body" {
		t.Errorf("contents = %q, want %q", got, "second body")
	}
}

// filepath.Walk visits every entry under the source, but the callback ignored
// the entry it was given and always acted on the source root, so the second
// visit retried the directory itself as a regular file.
func TestCopyRecursesIntoDirectories(t *testing.T) {
	dir := t.TempDir()
	fs := LocalFileSystem(dir)
	ctx := context.Background()

	if err := os.MkdirAll(filepath.Join(dir, "src", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "nested", "b.txt"), []byte("beta"), 0o600); err != nil {
		t.Fatal(err)
	}

	created, err := fs.Copy(ctx, "/src", "/dst", &CopyOptions{})
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if !created {
		t.Error("created = false for a destination that did not exist")
	}

	for path, want := range map[string]string{
		"/dst/a.txt":        "alpha",
		"/dst/nested/b.txt": "beta",
	} {
		rc, err := fs.Open(ctx, path)
		if err != nil {
			t.Errorf("%s was not copied: %v", path, err)
			continue
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}

// RFC 4918 §9.8.3: a Depth 0 COPY of a collection creates the collection
// without its members.
func TestCopyNonRecursiveCopiesOnlyTheCollection(t *testing.T) {
	dir := t.TempDir()
	fs := LocalFileSystem(dir)
	ctx := context.Background()

	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := fs.Copy(ctx, "/src", "/dst", &CopyOptions{NoRecursive: true}); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if _, err := fs.Stat(ctx, "/dst"); err != nil {
		t.Errorf("the collection itself was not created: %v", err)
	}
	if _, err := fs.Stat(ctx, "/dst/a.txt"); err == nil {
		t.Error("a Depth 0 COPY copied a member")
	}
}

// A PUT over an existing resource must not widen its permissions: renaming a
// fresh temp file into place replaces the inode, so the stored mode has to be
// carried across explicitly.
func TestCreatePreservesExistingMode(t *testing.T) {
	dir := t.TempDir()
	fs := LocalFileSystem(dir)
	ctx := context.Background()

	p := filepath.Join(dir, "private.ics")
	if err := os.WriteFile(p, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}

	body := io.NopCloser(strings.NewReader("replacement"))
	if _, _, err := fs.Create(ctx, "/private.ics", body, &CreateOptions{}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %v, want %v", got, os.FileMode(0o600))
	}
}

// A new resource still gets the ordinary umask-filtered mode rather than
// inheriting anything from a previous file.
func TestCreateNewResourceUsesDefaultMode(t *testing.T) {
	dir := t.TempDir()
	fs := LocalFileSystem(dir)
	ctx := context.Background()

	body := io.NopCloser(strings.NewReader("fresh"))
	if _, _, err := fs.Create(ctx, "/new.ics", body, &CreateOptions{}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "new.ics"))
	if err != nil {
		t.Fatal(err)
	}
	// Compared against a file the standard library creates the same way, so the
	// expected mode is pinned exactly without changing the process-wide umask. A
	// mask test would accept 0600 — the staging file's own mode, and the very
	// thing inheriting it would look like.
	reference := filepath.Join(dir, "reference")
	f, err := os.Create(reference)
	if err != nil {
		t.Fatal(err)
	}
	if cerr := f.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	refInfo, err := os.Stat(reference)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), refInfo.Mode().Perm(); got != want {
		t.Errorf("mode = %v, want %v", got, want)
	}
}

// RFC 4918 §9.8.3 leaves a COPY of a collection into its own subtree undefined
// and it cannot terminate: the walk keeps discovering the directories it is
// creating. It must be refused before anything is written.
func TestCopyIntoOwnSubtreeIsRefused(t *testing.T) {
	dir := t.TempDir()
	fs := LocalFileSystem(dir)
	ctx := context.Background()

	if err := os.MkdirAll(filepath.Join(dir, "A", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "A", "S", "f.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := fs.Copy(ctx, "/A", "/A/S/T", &CopyOptions{})
	if err == nil {
		t.Fatal("Copy into its own subtree succeeded")
	}
	var httpErr *internal.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v (%T), want an *internal.HTTPError", err, err)
	}
	if httpErr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", httpErr.Code, http.StatusForbidden)
	}

	var count int
	if werr := filepath.Walk(dir, func(string, os.FileInfo, error) error {
		count++
		return nil
	}); werr != nil {
		t.Fatal(werr)
	}
	if want := 4; count != want {
		t.Errorf("%d entries under the root, want %d — the refused copy wrote to disk", count, want)
	}
}

// A COPY onto an existing destination must not destroy it until the whole tree
// has been read successfully; a mid-walk failure has to leave the old
// destination in place.
func TestCopyFailureLeavesDestinationIntact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses the directory permission that makes the copy fail")
	}

	dir := t.TempDir()
	fs := LocalFileSystem(dir)
	ctx := context.Background()

	locked := filepath.Join(dir, "src", "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "f.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dst"), 0o755); err != nil {
		t.Fatal(err)
	}
	const important = "must survive a failed copy\n"
	if err := os.WriteFile(filepath.Join(dir, "dst", "important.txt"), []byte(important), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	// t.TempDir cleanup cannot remove a directory it may not write to.
	t.Cleanup(func() {
		if cerr := os.Chmod(locked, 0o755); cerr != nil {
			t.Error(cerr)
		}
	})

	if _, err := fs.Copy(ctx, "/src", "/dst", &CopyOptions{}); err == nil {
		t.Fatal("Copy succeeded despite an unwritable target directory")
	}

	got, err := os.ReadFile(filepath.Join(dir, "dst", "important.txt"))
	if err != nil {
		t.Fatalf("the previous destination was destroyed by the failed copy: %v", err)
	}
	if string(got) != important {
		t.Errorf("contents = %q, want the untouched original %q", got, important)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "src" && entry.Name() != "dst" {
			t.Errorf("failed copy left %s behind", entry.Name())
		}
	}
}

// A MOVE onto itself is RFC 4918 §9.9.4's 403 case. Removing the destination
// first would delete the source along with it, since they name the same tree.
func TestMoveOntoItselfIsRefused(t *testing.T) {
	dir := t.TempDir()
	fs := LocalFileSystem(dir)
	ctx := context.Background()

	if err := os.MkdirAll(filepath.Join(dir, "A"), 0o755); err != nil {
		t.Fatal(err)
	}
	const secret = "must survive a refused move\n"
	if err := os.WriteFile(filepath.Join(dir, "A", "secret.txt"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := fs.Move(ctx, "/A", "/A", &MoveOptions{})
	if err == nil {
		t.Fatal("Move onto itself succeeded")
	}
	var httpErr *internal.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v (%T), want an *internal.HTTPError", err, err)
	}
	if httpErr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", httpErr.Code, http.StatusForbidden)
	}

	got, err := os.ReadFile(filepath.Join(dir, "A", "secret.txt"))
	if err != nil {
		t.Fatalf("the refused move destroyed the collection: %v", err)
	}
	if string(got) != secret {
		t.Errorf("contents = %q, want the untouched original %q", got, secret)
	}
}

func TestMoveIntoOwnSubtreeIsRefused(t *testing.T) {
	dir := t.TempDir()
	fs := LocalFileSystem(dir)
	ctx := context.Background()

	if err := os.MkdirAll(filepath.Join(dir, "A", "B"), 0o755); err != nil {
		t.Fatal(err)
	}
	const secret = "must survive a refused move\n"
	if err := os.WriteFile(filepath.Join(dir, "A", "B", "secret.txt"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := fs.Move(ctx, "/A", "/A/B", &MoveOptions{})
	if err == nil {
		t.Fatal("Move into its own subtree succeeded")
	}
	var httpErr *internal.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v (%T), want an *internal.HTTPError", err, err)
	}
	if httpErr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", httpErr.Code, http.StatusForbidden)
	}

	got, err := os.ReadFile(filepath.Join(dir, "A", "B", "secret.txt"))
	if err != nil {
		t.Fatalf("the refused move destroyed the destination: %v", err)
	}
	if string(got) != secret {
		t.Errorf("contents = %q, want the untouched original %q", got, secret)
	}
}

// The destination is only safe to touch once the source is known to exist:
// source and destination need no relationship at all for a destroy-first move
// to lose data.
func TestMoveFromMissingSourceLeavesDestinationIntact(t *testing.T) {
	dir := t.TempDir()
	fs := LocalFileSystem(dir)
	ctx := context.Background()

	const important = "must survive a failed move\n"
	if err := os.WriteFile(filepath.Join(dir, "important.txt"), []byte(important), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := fs.Move(ctx, "/missing.txt", "/important.txt", &MoveOptions{}); err == nil {
		t.Fatal("Move from a missing source succeeded")
	}

	got, err := os.ReadFile(filepath.Join(dir, "important.txt"))
	if err != nil {
		t.Fatalf("the failed move destroyed the destination: %v", err)
	}
	if string(got) != important {
		t.Errorf("contents = %q, want the untouched original %q", got, important)
	}
}

// RFC 4918 §9.7.1 requires a PUT whose parent collection is missing to fail
// with 409 Conflict. Answering 404 tells the client the target is merely absent
// and the request is safe to repeat, so it retries a doomed PUT forever instead
// of creating the collection first.
func TestCreateMissingParentIsConflict(t *testing.T) {
	dir := t.TempDir()
	fs := LocalFileSystem(dir)
	ctx := context.Background()

	body := io.NopCloser(strings.NewReader("hello"))
	_, _, err := fs.Create(ctx, "/nonesuch/f.txt", body, &CreateOptions{})
	if err == nil {
		t.Fatal("Create into a missing collection succeeded")
	}

	var httpErr *internal.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v (%T), want an *internal.HTTPError", err, err)
	}
	if httpErr.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", httpErr.Code, http.StatusConflict)
	}
}

func TestCopyMissingDestinationParentIsConflict(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, root string)
		src   string
	}{
		{
			name: "a regular file",
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("hello"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			src: "/f.txt",
		},
		{
			name: "a collection",
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(root, "coll"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			src: "/coll",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)
			fs := LocalFileSystem(root)

			_, err := fs.Copy(ctx, tc.src, "/nonesuch/dst", &CopyOptions{})
			if err == nil {
				t.Fatal("Copy into a missing collection succeeded")
			}

			var httpErr *internal.HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("error = %v (%T), want an *internal.HTTPError", err, err)
			}
			if httpErr.Code != http.StatusConflict {
				t.Errorf("status = %d, want %d (RFC 4918 §9.8.5)", httpErr.Code, http.StatusConflict)
			}
		})
	}
}
