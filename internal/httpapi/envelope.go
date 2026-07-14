package httpapi

import (
	"encoding/json"
	"net/http"
)

type envelope struct {
	Data  any       `json:"data"`
	Meta  any       `json:"meta"`
	Error *apiError `json:"error"`
}
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, body envelope) {
	w.Header().Set("Content-Type", "application/json")
	// Marshal before writing the header: an encode failure (e.g. NaN float from
	// the database) must surface as a 500, not a silent empty 200.
	payload, err := json.Marshal(body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		payload, _ = json.Marshal(envelope{Meta: map[string]any{}, Error: &apiError{Code: "INTERNAL_ERROR", Message: "response encoding failed"}})
		_, _ = w.Write(payload)
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, envelope{Data: nil, Meta: map[string]any{}, Error: &apiError{Code: code, Message: message}})
}
