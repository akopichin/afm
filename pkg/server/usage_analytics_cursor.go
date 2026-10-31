package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A jobs cursor is c1.<offset>.<as_of unix ms>.<tag>. The tag seals the offset and as_of together with
// everything that decides what a page holds, so a cursor is only good for the request that issued it.
// It guards consistency, not secrecy.
const usageCursorPrefix = "c1"

type usageCursor struct {
	offset int
	asOf   time.Time
}

func (p usageParams) cursorTag(revision string, mode UsageAnalyticsMode, c usageCursor) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%q|%q|%q|%q|%q|%q|%q|%q|%q|%d|%d|%d",
		revision, mode, p.view, p.scope, p.stageID, p.agent, p.model, p.sort, p.dir, p.limit, c.offset, c.asOf.UnixMilli()))
	return hex.EncodeToString(sum[:8])
}

func (p usageParams) sealCursor(revision string, mode UsageAnalyticsMode, c usageCursor) string {
	return fmt.Sprintf("%s.%d.%d.%s", usageCursorPrefix, c.offset, c.asOf.UnixMilli(), p.cursorTag(revision, mode, c))
}

// openCursor answers 400 for a cursor that is not ours and 409 for one issued for another request state.
func (p usageParams) openCursor(revision string, mode UsageAnalyticsMode) (usageCursor, error) {
	parts := strings.Split(p.cursor, ".")
	if len(parts) != 4 || parts[0] != usageCursorPrefix {
		return usageCursor{}, invalidParam("invalid cursor")
	}
	offset, errOffset := strconv.Atoi(parts[1])
	asOfMS, errAsOf := strconv.ParseInt(parts[2], 10, 64)
	if errOffset != nil || errAsOf != nil || offset < 0 {
		return usageCursor{}, invalidParam("invalid cursor")
	}
	c := usageCursor{offset: offset, asOf: time.UnixMilli(asOfMS).UTC()}
	if parts[3] != p.cursorTag(revision, mode, c) {
		return usageCursor{}, staleCursor()
	}
	return c, nil
}
