package httpapi

import (
	"encoding/json"
	"net/http"
)

// DecodeJSON decodes the request body into dst.
func DecodeJSON(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}

// JSON writes data as a JSON response with the supplied status code.
func JSON(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(data)
}

// Error writes a JSON error response using the service's common response shape.
func Error(w http.ResponseWriter, code int, msg string) {
	JSON(w, code, map[string]string{"error": msg})
}
