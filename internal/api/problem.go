package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Problem is an RFC 9457 problem details body, extended with a stable
// machine-readable code and the request id.
type Problem struct {
	Type       string            `json:"type"`
	Title      string            `json:"title"`
	Status     int               `json:"status"`
	Detail     string            `json:"detail,omitempty"`
	Code       string            `json:"code"`
	ErrorClass string            `json:"error_class,omitempty"`
	Retryable  *bool             `json:"retryable,omitempty"`
	RequestID  string            `json:"request_id,omitempty"`
	Fields     map[string]string `json:"fields,omitempty"`
	// Problems lists document validation problems (firewall designer).
	Problems any `json:"problems,omitempty"`
	// Reauth is set on reauth_required: the action needs the user to
	// confirm who they are again (POST /auth/reauth), then repeat it.
	Reauth *reauthInfo `json:"reauth,omitempty"`
}

type reauthInfo struct {
	MFA     bool `json:"mfa"`      // ask for the authenticator code too
	WindowS int  `json:"window_s"` // how long a confirmation lasts
}

func (p *Problem) Error() string { return p.Code + ": " + p.Detail }

func problem(status int, code, detail string) *Problem {
	return &Problem{
		Type:   "urn:drool:problem:" + code,
		Title:  http.StatusText(status),
		Status: status,
		Code:   code,
		Detail: detail,
	}
}

func errBadRequest(detail string) *Problem {
	return problem(http.StatusBadRequest, "bad_request", detail)
}
func errValidation(detail string) *Problem {
	return problem(http.StatusUnprocessableEntity, "validation", detail)
}
func errNotFound(what string) *Problem {
	return problem(http.StatusNotFound, "not_found", what+" not found")
}
func errConflict(code, detail string) *Problem { return problem(http.StatusConflict, code, detail) }
func errForbidden(detail string) *Problem      { return problem(http.StatusForbidden, "forbidden", detail) }

var errUnauthenticated = problem(http.StatusUnauthorized, "unauthenticated", "authentication required")
var errCSRF = problem(http.StatusForbidden, "csrf", "missing or invalid X-DROOL-CSRF header")
var errInternal = problem(http.StatusInternalServerError, "internal", "internal server error")

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeProblem(w http.ResponseWriter, r *http.Request, p *Problem) {
	cp := *p
	cp.RequestID = requestIDFrom(r.Context())
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(cp.Status)
	_ = json.NewEncoder(w).Encode(cp)
}

// maxBodyBytes bounds JSON request bodies.
const maxBodyBytes = 1 << 20

// decodeJSON strictly decodes a request body into v.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) *Problem {
	return decodeJSONLimit(w, r, v, maxBodyBytes)
}

// decodeJSONLimit is decodeJSON with a different size limit.
func decodeJSONLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) *Problem {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		return problem(http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		switch {
		case errors.As(err, &mbe):
			return problem(http.StatusRequestEntityTooLarge, "body_too_large", "request body too large")
		case errors.Is(err, io.EOF):
			return errBadRequest("request body is empty")
		default:
			return errBadRequest(fmt.Sprintf("invalid JSON: %v", err))
		}
	}
	if dec.More() {
		return errBadRequest("request body must contain a single JSON object")
	}
	return nil
}
