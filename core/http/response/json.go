package response

import (
	"encoding/json/v2"
	"net/http"
)

// JSON serializes and writes JSON followed by a newline.
// Encoding uses encoding/json/v2 and finishes before commitment; an encoding
// failure leaves the router able to render an error. Successful encoding sets
// Content-Type to application/json and follows [Raw]'s write-error behavior.
//
// Example:
//
//	return response.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
func JSON[T any](writer http.ResponseWriter, status int, data T) error {
	encoded, err := json.Marshal(data)

	if err != nil {
		return err
	}

	writer.Header().Set("Content-Type", "application/json")

	return Raw(writer, status, append(encoded, '\n'))
}
