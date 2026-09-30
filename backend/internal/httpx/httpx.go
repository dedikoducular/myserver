// Package httpx contains the shared HTTP primitives used by every module:
// the response envelope, sanitized errors, request decoding and the router.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// Envelope is the single response shape returned by the API.
type Envelope struct {
	Success bool       `json:"success"`
	Data    any        `json:"data"`
	Error   *ErrorBody `json:"error"`
}

// ErrorBody is the user-facing part of an error.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error is an error that is safe to show to the user. Message is Turkish and
// user-facing; Err is the internal cause and is only ever logged.
type Error struct {
	Status  int
	Code    string
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Code + ": " + e.Err.Error()
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// Wrap attaches an internal cause to a user-facing error.
func (e *Error) Wrap(err error) *Error {
	c := *e
	c.Err = err
	return &c
}

func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

func BadRequest(message string) *Error {
	return NewError(http.StatusBadRequest, "bad_request", message)
}

func Unauthorized() *Error {
	return NewError(http.StatusUnauthorized, "unauthorized", "Oturum açmanız gerekiyor.")
}

func Forbidden() *Error {
	return NewError(http.StatusForbidden, "forbidden", "Yetkiniz bulunmuyor.")
}

func NotFound(message string) *Error {
	if message == "" {
		message = "Kayıt bulunamadı."
	}
	return NewError(http.StatusNotFound, "not_found", message)
}

func Conflict(message string) *Error {
	return NewError(http.StatusConflict, "conflict", message)
}

func TooManyRequests(message string) *Error {
	return NewError(http.StatusTooManyRequests, "rate_limited", message)
}

func Unavailable(code, message string) *Error {
	return NewError(http.StatusServiceUnavailable, code, message)
}

// Internal hides the cause behind a generic message.
func Internal(err error) *Error {
	return &Error{
		Status:  http.StatusInternalServerError,
		Code:    "internal_error",
		Message: "Beklenmeyen bir sunucu hatası oluştu.",
		Err:     err,
	}
}

// JSON writes v inside the success envelope.
func JSON(w http.ResponseWriter, status int, v any) {
	writeEnvelope(w, status, Envelope{Success: true, Data: v})
}

// OK writes a 200 success envelope.
func OK(w http.ResponseWriter, v any) { JSON(w, http.StatusOK, v) }

// Fail writes err as a sanitized error envelope. Errors that are not *Error
// are logged and replaced by a generic message, so internals never leak.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = Internal(err)
	}
	if e.Status >= 500 || e.Err != nil {
		slog.Error("istek başarısız",
			"method", r.Method, "path", r.URL.Path,
			"status", e.Status, "code", e.Code, "cause", causeString(e))
	}
	writeEnvelope(w, e.Status, Envelope{
		Success: false,
		Error:   &ErrorBody{Code: e.Code, Message: e.Message},
	})
}

func causeString(e *Error) string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Message
}

func writeEnvelope(w http.ResponseWriter, status int, env Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

const maxBodyBytes = 1 << 20

// Decode reads a JSON body into v, rejecting oversized bodies and unknown
// fields.
func Decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return BadRequest("İstek gövdesi boş.")
		}
		return BadRequest("İstek gövdesi geçersiz.").Wrap(err)
	}
	if dec.More() {
		return BadRequest("İstek gövdesi geçersiz.")
	}
	return nil
}

// ClientIP returns the peer address. Proxy headers are deliberately ignored:
// the panel is served directly, and trusting them would let clients spoof
// the address used for rate limiting and audit records.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// SameOrigin reports whether the request's Origin header matches its Host.
// WebSocket endpoints must call this, since browsers do not apply CORS to
// WebSocket handshakes.
func SameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || r.Host == "" {
		return false
	}
	i := strings.Index(origin, "://")
	if i < 0 {
		return false
	}
	return strings.EqualFold(origin[i+3:], r.Host)
}
