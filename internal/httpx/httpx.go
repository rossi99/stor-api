// Package httpx holds the JSON request/response conventions shared by all
// handlers: camelCase bodies, {"error":{"code","message"}} envelopes.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const maxBodyBytes = 64 << 10 // 64 KB — nothing this API accepts is bigger

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func Error(w http.ResponseWriter, status int, code, message string) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = message
	JSON(w, status, body)
}

// Common responses, kept deliberately vague where detail would leak state.
func Unauthorized(w http.ResponseWriter) {
	Error(w, http.StatusUnauthorized, "unauthorized", "authentication required")
}

func Forbidden(w http.ResponseWriter) {
	Error(w, http.StatusForbidden, "forbidden", "you do not have access to this resource")
}

func NotFound(w http.ResponseWriter) {
	Error(w, http.StatusNotFound, "not_found", "resource not found")
}

func BadRequest(w http.ResponseWriter, message string) {
	Error(w, http.StatusBadRequest, "invalid_request", message)
}

func Internal(w http.ResponseWriter) {
	Error(w, http.StatusInternalServerError, "internal", "something went wrong")
}

// Decode reads a JSON body with a hard size cap and strict field checking.
func Decode(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return fmt.Errorf("request body too large")
		}
		return fmt.Errorf("invalid JSON body")
	}
	if dec.More() {
		return fmt.Errorf("request body must contain a single JSON object")
	}
	_, _ = io.Copy(io.Discard, r.Body)
	return nil
}
