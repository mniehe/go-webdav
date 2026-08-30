package carddav

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"github.com/mniehe/davkit/internal"
)

// syncURNPrefix marks a sync token as this library's. DAV:sync-token is
// opaque to clients, so the format is ours to choose and ours to police.
const syncURNPrefix = "urn:x-go-webdav:sync:"

// bookScope distinguishes validators issued for different calendars, so a
// tag or token copied from one cannot match a same-numbered revision in
// another.
//
// It hashes the BookID rather than the reference. That is what makes a
// deleted-and-recreated calendar a different calendar here: the owner and name
// are the same and the revisions start over, but the incarnation differs, so a
// stale If-Match from before the deletion matches nothing.
func bookScope(id BookID) uint64 {
	sum := sha256.Sum256([]byte(id))
	return binary.BigEndian.Uint64(sum[:8])
}

func scopeText(scope uint64) string { return fmt.Sprintf("%016x", scope) }

// etagFor renders an item's entity tag.
//
// A revision is already a validator: it changes when the bytes change, which is
// all an ETag has to do. Hashing every body instead would read and digest a
// whole calendar to answer the commonest request in CalDAV, a PROPFIND asking
// for nothing but getetag. Backends that implement neither writing nor sync
// leave the revision zero, and only those pay for the hash.
func etagFor(scope uint64, item Item) internal.ETag {
	if item.Revision != 0 {
		return internal.ETag(fmt.Sprintf("%s-%x", scopeText(scope), item.Revision))
	}
	sum := sha256.Sum256(item.Content)
	return internal.ETag(fmt.Sprintf("%x", sum[:16]))
}

// parseETag recovers the revision from a tag this library issued for this
// calendar. Anything else — a weak tag, one scoped to another calendar, one
// from another server, a hash from a revisionless backend — reports false.
func parseETag(scope uint64, tag string) (Revision, bool) {
	tag = strings.TrimSpace(tag)
	if len(tag) < 2 || tag[0] != '"' || tag[len(tag)-1] != '"' {
		return 0, false
	}
	gotScope, revText, found := strings.Cut(tag[1:len(tag)-1], "-")
	if !found || gotScope != scopeText(scope) {
		return 0, false
	}
	rev, err := strconv.ParseUint(revText, 16, 64)
	if err != nil {
		return 0, false
	}
	return Revision(rev), true
}

// preconditionsFrom turns a request's conditional headers into the write's
// preconditions.
//
// Both headers at once is legal HTTP and no CalDAV client sends it, and
// Preconditions cannot express the conjunction. Refusing is the honest answer;
// honouring one and dropping the other would silently weaken a guard the client
// asked for.
func preconditionsFrom(scope uint64, ifMatch, ifNoneMatch string) (Preconditions, error) {
	ifMatch, ifNoneMatch = strings.TrimSpace(ifMatch), strings.TrimSpace(ifNoneMatch)
	switch {
	case ifMatch == "" && ifNoneMatch == "":
		return Unconditional(), nil
	case ifMatch != "" && ifNoneMatch != "":
		return Preconditions{}, fmt.Errorf("carddav: If-Match and If-None-Match cannot be combined")
	case ifMatch == "*":
		return IfTargetExists(), nil
	case ifNoneMatch == "*":
		return IfTargetMissing(), nil
	case ifMatch != "":
		return IfRevision(revisionsIn(scope, ifMatch)...), nil
	default:
		return IfNotRevision(revisionsIn(scope, ifNoneMatch)...), nil
	}
}

// revisionsIn parses an entity-tag list, dropping every tag this library did
// not issue for this calendar.
//
// Dropping is safe in both directions, which is why it is not an error: an
// If-Match left with no revisions can never be satisfied, and an If-None-Match
// left with none always is. Both are what a tag from somewhere else should
// mean, and neither is "ignore the header".
func revisionsIn(scope uint64, list string) []Revision {
	var revs []Revision
	for _, entry := range strings.Split(list, ",") {
		if rev, ok := parseETag(scope, entry); ok {
			revs = append(revs, rev)
		}
	}
	return revs
}

// syncTokenFor renders a client's position in a calendar's history.
func syncTokenFor(scope uint64, rev Revision) string {
	return fmt.Sprintf("%s%s:%x", syncURNPrefix, scopeText(scope), rev)
}

// parseSyncToken recovers the position from a token this library issued for
// this calendar. Reporting false must become DAV:valid-sync-token, never a
// silent full listing: the client asked for a delta, and a listing carries no
// deletions, so it would keep removed items forever.
//
// An empty token means initial sync and is the caller's to recognise; it is
// rejected here like any other token that is not ours.
func parseSyncToken(scope uint64, token string) (Revision, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(token), syncURNPrefix)
	if !ok {
		return 0, false
	}
	gotScope, revText, found := strings.Cut(rest, ":")
	if !found || gotScope != scopeText(scope) {
		return 0, false
	}
	rev, err := strconv.ParseUint(revText, 16, 64)
	if err != nil {
		return 0, false
	}
	return Revision(rev), true
}
