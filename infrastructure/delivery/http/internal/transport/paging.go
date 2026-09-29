package transport

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"
)

// PageMeta is the offset envelope shared by the paged list endpoints —
// best-effort, not a snapshot: writes between page reads shift offsets.
type PageMeta struct {
	Offset  int  `json:"offset"`
	Limit   int  `json:"limit"`
	Total   int  `json:"total"`
	HasMore bool `json:"hasMore"`
}

// ParsePageParams validates ?offset/&limit — offset ≥ 0, 1 ≤ limit ≤
// maxLimit, both integer. Failures answer 400 and return ok=false.
func ParsePageParams(c *gin.Context, defLimit, maxLimit int) (offset, limit int, ok bool) {
	offset, err := intQuery(c, "offset", 0)
	if err != nil || offset < 0 {
		WriteProblem(c, 400, "invalid_argument", "offset must be a non-negative integer")
		return 0, 0, false
	}
	limit, err = intQuery(c, "limit", defLimit)
	if err != nil || limit < 1 || limit > maxLimit {
		WriteProblem(c, 400, "invalid_argument",
			"limit must be an integer between 1 and "+strconv.Itoa(maxLimit))
		return 0, 0, false
	}
	return offset, limit, true
}

// ParseWindow validates the message-history range: start is any integer
// (negative counts back from the end of history), limit 1..maxLimit.
func ParseWindow(c *gin.Context, defStart, defLimit, maxLimit int) (start, limit int, ok bool) {
	start, err := intQuery(c, "start", defStart)
	if err != nil {
		WriteProblem(c, 400, "invalid_argument", "start must be an integer")
		return 0, 0, false
	}
	limit, err = intQuery(c, "limit", defLimit)
	if err != nil || limit < 1 || limit > maxLimit {
		WriteProblem(c, 400, "invalid_argument",
			"limit must be an integer between 1 and "+strconv.Itoa(maxLimit))
		return 0, 0, false
	}
	return start, limit, true
}

// intQuery reads one integer query parameter, returning def when absent.
// A present-but-empty value (?limit=) is a 400 like any malformed integer.
func intQuery(c *gin.Context, key string, def int) (int, error) {
	raw, present := c.GetQuery(key)
	if !present {
		return def, nil
	}
	if raw == "" {
		return 0, errors.New("empty")
	}
	return strconv.Atoi(raw)
}

// SlicePage cuts [offset, offset+limit) out of a fully materialized list
// — the repository reads all rows; the transport pages over them. Offset
// is clamped before the limit is added, so a near-MaxInt offset can never
// overflow the end bound.
func SlicePage[T any](all []T, offset, limit int) []T {
	if offset > len(all) {
		offset = len(all)
	}
	end := len(all)
	if limit <= len(all)-offset {
		end = offset + limit
	}
	return all[offset:end]
}
