// Package webdav provides a client and server WebDAV filesystem implementation.
//
// WebDAV is defined in RFC 4918.
package webdav

import (
	"strings"
	"time"

	"github.com/mniehe/davkit/internal"
)

// FileInfo holds information about a WebDAV file.
type FileInfo struct {
	Path     string
	Size     int64
	ModTime  time.Time
	IsDir    bool
	MIMEType string
	ETag     string
}

type CreateOptions struct {
	IfMatch     ConditionalMatch
	IfNoneMatch ConditionalMatch
}

type RemoveAllOptions struct {
	IfMatch     ConditionalMatch
	IfNoneMatch ConditionalMatch
}

type CopyOptions struct {
	NoRecursive bool
	NoOverwrite bool
}

type MoveOptions struct {
	NoOverwrite bool
}

// ConditionalMatch represents the value of a conditional header
// according to RFC 2068 section 14.25 and RFC 2068 section 14.26
// The (optional) value can either be a wildcard or an ETag.
type ConditionalMatch string

func (val ConditionalMatch) IsSet() bool {
	return val != ""
}

func (val ConditionalMatch) IsWildcard() bool {
	return val == "*"
}

func (val ConditionalMatch) ETag() (string, error) {
	var e internal.ETag
	if err := e.UnmarshalText([]byte(val)); err != nil {
		return "", err
	}
	return string(e), nil
}

// MatchETag reports whether the conditional header matches the resource's
// current ETag. A wildcard matches any existing resource, so callers must pass
// an empty etag when (and only when) the resource does not exist. The header
// may be a comma-separated list of entity-tags (RFC 9110 §13.1.1/§13.1.2); a
// match against any one of them counts.
func (val ConditionalMatch) MatchETag(etag string) (bool, error) {
	if val.IsWildcard() {
		return etag != "", nil
	}
	if etag == "" {
		return false, nil
	}
	for _, part := range strings.Split(string(val), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var e internal.ETag
		if err := e.UnmarshalText([]byte(part)); err != nil {
			return false, err
		}
		if string(e) == etag {
			return true, nil
		}
	}
	return false, nil
}
