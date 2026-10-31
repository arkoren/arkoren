package stream

import "net/http"

// Channel renews deadlines before commitment and each write, propagating every
// flush failure after commitment without consuming another value.
func Channel(
	writer http.ResponseWriter,
	request *http.Request,
	values <-chan []byte,
	done <-chan error,
	encode func([]byte) []byte,
	policy Policy,
) error {
	if _, ok := writer.(http.Flusher); !ok {
		return policy.NonFlushable
	}

	timeout := policy.Timeout()

	if writer.Header().Get("Content-Type") == "" {
		writer.Header().Set("Content-Type", "application/octet-stream")
	}

	writer.Header().Set("Cache-Control", "no-cache")

	controller := http.NewResponseController(writer)

	if err := refreshDeadline(controller, timeout); err != nil {
		return err
	}

	writer.WriteHeader(http.StatusOK)

	if err := controller.Flush(); err != nil {
		return err
	}

	write := func(value []byte) error {
		if err := refreshDeadline(controller, timeout); err != nil {
			return err
		}

		if _, err := writer.Write(encode(value)); err != nil {
			return err
		}

		return controller.Flush()
	}

	return streamValues(request.Context(), values, done, write)
}
