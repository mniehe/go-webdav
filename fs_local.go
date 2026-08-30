package webdav

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/mniehe/davkit/internal"
)

// LocalFileSystem implements FileSystem for a local directory.
type LocalFileSystem string

var _ FileSystem = LocalFileSystem("")

func (l LocalFileSystem) localPath(name string) (string, error) {
	if (filepath.Separator != '/' && strings.ContainsRune(name, filepath.Separator)) || strings.Contains(name, "\x00") {
		return "", internal.HTTPErrorf(http.StatusBadRequest, "webdav: invalid character in path")
	}
	name = path.Clean(name)
	if !path.IsAbs(name) {
		return "", internal.HTTPErrorf(http.StatusBadRequest, "webdav: expected absolute path, got %q", name)
	}
	return filepath.Join(string(l), filepath.FromSlash(name)), nil
}

func (l LocalFileSystem) externalPath(name string) (string, error) {
	rel, err := filepath.Rel(string(l), name)
	if err != nil {
		return "", err
	}
	return "/" + filepath.ToSlash(rel), nil
}

func (l LocalFileSystem) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	p, err := l.localPath(name)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func fileInfoFromOS(p string, fi os.FileInfo) *FileInfo {
	return &FileInfo{
		Path:    p,
		Size:    fi.Size(),
		ModTime: fi.ModTime(),
		IsDir:   fi.IsDir(),
		// TODO: fallback to http.DetectContentType?
		MIMEType: mime.TypeByExtension(path.Ext(p)),
		// RFC 2616 section 13.3.3 describes strong ETags. Ideally these would
		// be checksums or sequence numbers, however these are expensive to
		// compute. The modification time with nanosecond granularity is good
		// enough, as it's very unlikely for the same file to be modified twice
		// during a single nanosecond.
		ETag: fmt.Sprintf("%x%x", fi.ModTime().UnixNano(), fi.Size()),
	}
}

func errFromOS(err error) error {
	// An error a call site has already classified knows more than this mapper
	// does. copyTree funnels copyRegularFile's 409 through here, and the ENOENT
	// still wrapped inside it would otherwise be re-read as a 404.
	var httpErr *internal.HTTPError
	if errors.As(err, &httpErr) {
		return err
	}

	// Remove path from path errors so it's not returned to the user
	var perr *fs.PathError
	if errors.As(err, &perr) {
		err = fmt.Errorf("%s: %w", perr.Op, perr.Err)
	}

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return NewHTTPError(http.StatusNotFound, err)
	case errors.Is(err, fs.ErrPermission):
		return NewHTTPError(http.StatusForbidden, err)
	case errors.Is(err, os.ErrDeadlineExceeded):
		return NewHTTPError(http.StatusServiceUnavailable, err)
	default:
		return err
	}
}

func (l LocalFileSystem) Stat(ctx context.Context, name string) (*FileInfo, error) {
	p, err := l.localPath(name)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return nil, errFromOS(err)
	}
	return fileInfoFromOS(name, fi), nil
}

func (l LocalFileSystem) ReadDir(ctx context.Context, name string, recursive bool) ([]FileInfo, error) {
	root, err := l.localPath(name)
	if err != nil {
		return nil, err
	}

	var entries []FileInfo
	err = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil && !errors.Is(err, os.ErrPermission) {
			return err
		}
		if fi == nil {
			return nil
		}

		href, err := l.externalPath(p)
		if err != nil {
			return err
		}

		entries = append(entries, *fileInfoFromOS(href, fi))

		if !recursive && fi.IsDir() && root != p {
			return filepath.SkipDir
		}
		return nil
	})
	return entries, errFromOS(err)
}

func checkConditionalMatches(fi *FileInfo, ifMatch, ifNoneMatch ConditionalMatch) error {
	etag := ""
	if fi != nil {
		etag = fi.ETag
	}

	if ifMatch.IsSet() {
		if ok, err := ifMatch.MatchETag(etag); err != nil {
			return NewHTTPError(http.StatusBadRequest, err)
		} else if !ok {
			return NewHTTPError(http.StatusPreconditionFailed, fmt.Errorf("If-Match condition failed"))
		}
	}

	if ifNoneMatch.IsSet() {
		if ok, err := ifNoneMatch.MatchETag(etag); err != nil {
			return NewHTTPError(http.StatusBadRequest, err)
		} else if ok {
			return NewHTTPError(http.StatusPreconditionFailed, fmt.Errorf("If-None-Match condition failed"))
		}
	}

	return nil
}

func (l LocalFileSystem) Create(ctx context.Context, name string, body io.ReadCloser, opts *CreateOptions) (fi *FileInfo, created bool, err error) {
	p, err := l.localPath(name)
	if err != nil {
		return nil, false, err
	}
	existing, statErr := os.Stat(p)
	if statErr == nil {
		fi = fileInfoFromOS(name, existing)
	}
	created = fi == nil

	if derr := checkConditionalMatches(fi, opts.IfMatch, opts.IfNoneMatch); derr != nil {
		return nil, false, derr
	}

	// Written beside the destination and renamed over it: truncating in place
	// would let a client disconnecting mid-PUT destroy the stored resource.
	wc, err := createSibling(p)
	// A create failing ENOENT means the parent collection is missing, which RFC
	// 4918 §9.7.1 makes 409 rather than the 404 errFromOS would infer. Only the
	// call site can tell them apart: the same error from a stat of the target
	// really is 404.
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, NewHTTPError(http.StatusConflict, err)
	} else if err != nil {
		return nil, false, errFromOS(err)
	}
	tmp := wc.Name()
	defer func() {
		wc.Close()
		if err != nil {
			os.Remove(tmp)
		}
	}()

	if _, err = io.Copy(wc, body); err != nil {
		return nil, false, err
	}
	if cerr := wc.Close(); cerr != nil {
		return nil, false, cerr
	}
	// The rename replaces the inode, so the stored resource's mode has to be
	// carried over explicitly or a private file comes back umask-wide.
	if !created {
		if cerr := os.Chmod(tmp, existing.Mode().Perm()); cerr != nil {
			return nil, false, errFromOS(cerr)
		}
	}
	if rerr := os.Rename(tmp, p); rerr != nil {
		return nil, false, errFromOS(rerr)
	}

	fi, err = l.Stat(ctx, name)
	if err != nil {
		return nil, false, err
	}

	return fi, created, err
}

// createSibling opens a new file beside p, so the rename stays within one
// filesystem. 0666 is umask-filtered exactly as os.Create would have been.
func createSibling(p string) (*os.File, error) {
	dir, base := filepath.Split(p)
	for attempt := 0; attempt < 1000; attempt++ {
		name := filepath.Join(dir, "."+base+".tmp"+rand.Text())
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return f, err
	}
	return nil, fmt.Errorf("webdav: no unused temporary name beside %q", p)
}

func (l LocalFileSystem) RemoveAll(ctx context.Context, name string, opts *RemoveAllOptions) error {
	p, err := l.localPath(name)
	if err != nil {
		return err
	}

	// WebDAV semantics are that it should return a "404 Not Found" error in
	// case the resource doesn't exist. We need to Stat before RemoveAll.
	fi, err := l.Stat(ctx, name)
	if err != nil {
		return errFromOS(err)
	}

	if err := checkConditionalMatches(fi, opts.IfMatch, opts.IfNoneMatch); err != nil {
		return err
	}

	return errFromOS(os.RemoveAll(p))
}

func (l LocalFileSystem) Mkdir(ctx context.Context, name string) error {
	p, err := l.localPath(name)
	if err != nil {
		return err
	}
	err = os.Mkdir(p, 0o755)
	if os.IsExist(err) {
		return NewHTTPError(http.StatusMethodNotAllowed, err)
	}
	return errFromOS(err)
}

func copyRegularFile(src, dst string, perm os.FileMode) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return errFromOS(err)
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, perm)
	if os.IsNotExist(err) {
		return NewHTTPError(http.StatusConflict, err)
	} else if err != nil {
		return errFromOS(err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}

	return dstFile.Close()
}

func (l LocalFileSystem) Copy(ctx context.Context, src, dst string, options *CopyOptions) (created bool, err error) {
	srcPath, err := l.localPath(src)
	if err != nil {
		return false, err
	}
	dstPath, err := l.localPath(dst)
	if err != nil {
		return false, err
	}

	if cerr := refuseContainment("copy", srcPath, dstPath); cerr != nil {
		return false, cerr
	}

	if _, serr := os.Stat(srcPath); serr != nil {
		return false, errFromOS(serr)
	}

	dstExists := true
	if _, serr := os.Stat(dstPath); serr != nil {
		if !os.IsNotExist(serr) {
			return false, errFromOS(serr)
		}
		dstExists = false
		created = true
	} else if options.NoOverwrite {
		return false, NewHTTPError(http.StatusPreconditionFailed, os.ErrExist)
	}

	// Built beside the destination and swapped in only once the whole tree has
	// been copied: removing the destination first would let any mid-walk error
	// destroy it with nothing to put back.
	tmpPath, err := tempSibling(dstPath)
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(tmpPath) //nolint:errcheck // best-effort cleanup of a staging tree the caller never sees
		}
	}()

	if err := copyTree(srcPath, tmpPath, options); err != nil {
		return false, err
	}
	if err := swapTree(tmpPath, dstPath, dstExists); err != nil {
		return false, err
	}

	return created, nil
}

func copyTree(srcPath, dstPath string, options *CopyOptions) error {
	err := filepath.Walk(srcPath, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(srcPath, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dstPath, rel)

		if !fi.IsDir() {
			return copyRegularFile(p, target, fi.Mode()&os.ModePerm)
		}
		if err := os.Mkdir(target, fi.Mode()&os.ModePerm); err != nil {
			// Only the tree's root can fail ENOENT here, and it means the
			// destination's parent collection is missing, which RFC 4918
			// §9.8.5 makes 409. A deeper mkdir cannot: its parent was created a
			// moment earlier in this same walk.
			if errors.Is(err, fs.ErrNotExist) {
				return NewHTTPError(http.StatusConflict, err)
			}
			return errFromOS(err)
		}
		if options.NoRecursive {
			// RFC 4918 §9.8.3: a Depth 0 COPY creates the collection without
			// its members.
			return filepath.SkipDir
		}
		return nil
	})
	return errFromOS(err)
}

// swapTree moves newPath onto dstPath. Renaming over an existing non-empty
// directory fails, so the old tree is moved aside first and put back if the
// swap does not complete.
func swapTree(newPath, dstPath string, dstExists bool) error {
	if !dstExists {
		return errFromOS(os.Rename(newPath, dstPath))
	}

	aside, err := tempSibling(dstPath)
	if err != nil {
		return err
	}
	if rerr := os.Rename(dstPath, aside); rerr != nil {
		return errFromOS(rerr)
	}
	if rerr := os.Rename(newPath, dstPath); rerr != nil {
		if restoreErr := os.Rename(aside, dstPath); restoreErr != nil {
			return errFromOS(restoreErr)
		}
		return errFromOS(rerr)
	}
	return errFromOS(os.RemoveAll(aside))
}

// refuseContainment rejects a destination that names the source or sits inside
// it. RFC 4918 §9.8.5 and §9.9.4 both forbid it, and either operation would
// otherwise delete the source along with the destination it replaces. A COPY
// into a direct child happens to be safe today only because filepath.Walk
// snapshots a directory's names before visiting it.
func refuseContainment(op, srcPath, dstPath string) error {
	rel, err := filepath.Rel(srcPath, dstPath)
	if err != nil {
		return err
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return internal.HTTPErrorf(http.StatusForbidden, "webdav: cannot %s a resource onto itself or into its own subtree", op)
	}
	return nil
}

// tempSibling names an unused path beside p, so a tree built there can be
// renamed into place without crossing a filesystem boundary.
func tempSibling(p string) (string, error) {
	dir, base := filepath.Split(p)
	for attempt := 0; attempt < 1000; attempt++ {
		name := filepath.Join(dir, "."+base+".tmp"+rand.Text())
		_, err := os.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			return name, nil
		}
		if err != nil {
			return "", errFromOS(err)
		}
	}
	return "", fmt.Errorf("webdav: no unused temporary name beside %q", p)
}

func (l LocalFileSystem) Move(ctx context.Context, src, dst string, options *MoveOptions) (created bool, err error) {
	srcPath, err := l.localPath(src)
	if err != nil {
		return false, err
	}
	dstPath, err := l.localPath(dst)
	if err != nil {
		return false, err
	}

	if cerr := refuseContainment("move", srcPath, dstPath); cerr != nil {
		return false, cerr
	}

	// The destination is only safe to touch once the source is known to exist:
	// removing it first loses it outright when the rename cannot follow.
	if _, serr := os.Stat(srcPath); serr != nil {
		return false, errFromOS(serr)
	}

	dstExists := true
	if _, serr := os.Stat(dstPath); serr != nil {
		if !os.IsNotExist(serr) {
			return false, errFromOS(serr)
		}
		dstExists = false
		created = true
	} else if options.NoOverwrite {
		return false, NewHTTPError(http.StatusPreconditionFailed, os.ErrExist)
	}

	if err := swapTree(srcPath, dstPath, dstExists); err != nil {
		return false, err
	}

	return created, nil
}
