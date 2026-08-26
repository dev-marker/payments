package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxBodyBytes caps request bodies. Our requests are tiny, and the cap stops a
// client from making us read a huge body into memory.
const maxBodyBytes = 1 << 20 // 1 MiB

// WriteJSON sends v as JSON with the given status. It encodes v before writing
// anything, so an encoding bug becomes a clean 500 instead of half a response.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		Error(w, r, fmt.Errorf("encode response: %w", err))
		return
	}
	writeBody(w, status, body)
}

func writeBody(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(append(body, '\n'))
}

// DecodeJSON reads the request body into dst. It is strict on purpose: one
// JSON value, at most 1 MiB, and no fields that dst doesn't have. Problems
// come back as errors that Error turns into a 400 or a 413.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return badRequest("body must contain a single JSON value")
	}
	return nil
}

// requestError is a problem with the request itself, such as a body that
// isn't valid JSON, rather than with what it asks us to do.
type requestError struct {
	status int
	code   string
	msg    string
}

func (e *requestError) Error() string { return e.msg }

func badRequest(msg string) error {
	return &requestError{http.StatusBadRequest, "bad_request", msg}
}

// decodeError turns encoding/json's errors into messages a client can act on.
func decodeError(err error) error {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var sizeErr *http.MaxBytesError
	switch {
	case errors.Is(err, io.EOF):
		return badRequest("body must not be empty")
	case errors.As(err, &syntaxErr):
		return badRequest(fmt.Sprintf("malformed JSON at byte %d", syntaxErr.Offset))
	case errors.Is(err, io.ErrUnexpectedEOF):
		return badRequest("malformed JSON")
	case errors.As(err, &typeErr):
		return badRequest(fmt.Sprintf("field %q must be of type %s", typeErr.Field, typeErr.Type))
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		// encoding/json has no error type for this case, only the message.
		return badRequest(strings.TrimPrefix(err.Error(), "json: "))
	case errors.As(err, &sizeErr):
		msg := fmt.Sprintf("body must be at most %d bytes", sizeErr.Limit)
		return &requestError{http.StatusRequestEntityTooLarge, "body_too_large", msg}
	default:
		return badRequest("invalid JSON body")
	}
}
