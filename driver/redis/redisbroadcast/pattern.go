package redisbroadcast

import (
	"fmt"
	"strings"
)

// deliverToHandler forwards the channel suffix and unchanged payload while
// recovering panics per delivery. Logging uses the registered pattern so a panic
// does not terminate receive or replace its routing identity with envelope data.
func (driver *Driver) deliverToHandler(handler func(string, []byte), event, name string, payload []byte) {
	defer driver.recoverHandlerPanic(event)

	handler(name, payload)
}

// recoverHandlerPanic logs a recovered Redis event handler panic.
func (driver *Driver) recoverHandlerPanic(event string) {
	if recovered := recover(); recovered != nil {
		driver.logger.Error("panic in Redis event handler", "event", event, "panic", fmt.Sprint(recovered)) //nolint:sloglint // Redis pub/sub callbacks carry no context
	}
}

// redisPattern escapes prefix as literal Redis glob bytes and appends a glob
// broad enough to receive every channel matching the portable pattern. Only the
// event pattern keeps wildcard semantics; receive filters Redis's broader match.
func redisPattern(prefix, pattern string) string {
	prefix = strings.NewReplacer(`\`, `\\`, "*", `\*`, "?", `\?`, "[", `\[`, "]", `\]`).Replace(prefix)

	if pattern == "#" {
		return prefix + "*"
	}

	if !strings.HasSuffix(pattern, "#") {
		return prefix + pattern
	}

	return prefix + strings.TrimSuffix(pattern, ".#") + "*"
}
