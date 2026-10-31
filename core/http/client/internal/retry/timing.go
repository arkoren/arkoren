package retry

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// retryAfter accepts decimal seconds or an HTTP date. Oversized decimal values
// saturate rather than bypassing the wait cap; signed values remain malformed.
func retryAfter(resp *http.Response) (wait time.Duration, ok bool) {
	value := strings.TrimSpace(resp.Header.Get("Retry-After"))

	if value != "" && strings.Trim(value, "0123456789") == "" {
		seconds, err := strconv.ParseUint(value, 10, 64)

		if err != nil {
			return time.Duration(math.MaxInt64), true
		}

		return time.Duration(min(seconds, uint64(math.MaxInt64/int64(time.Second)))) * time.Second, true
	}

	if date, err := http.ParseTime(value); err == nil {
		return max(time.Until(date), 0), true
	}

	return 0, false
}
