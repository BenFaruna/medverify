// Package httpx holds the HTTP building blocks shared by every service:
// RFC 7807 problem responses, middleware, and the graceful server wrapper.
//
// Every middleware here is independently testable with net/http/httptest.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// ContentTypeProblemJSON is the media type of every error body we return.
const ContentTypeProblemJSON = "application/problem+json"

// ProblemTypeBase is the prefix for human readable problem type URIs. The
// trailing slug is the stable machine code.
const ProblemTypeBase = "https://medverify.ng/problems/"

// Stable machine codes. Clients branch on these, never on the detail text.
const (
	CodeBadRequest       = "bad_request"
	CodeInternalError    = "internal_error"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeNotFound         = "not_found"
	CodePayloadTooLarge  = "payload_too_large"
	CodeTimeout          = "timeout"
	CodeUnavailable      = "unavailable"
)

// Problem is an RFC 7807 problem detail document plus a stable machine code.
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	Code     string `json:"code"`
}

// NewProblem builds a problem with a title derived from the status code. The
// type URI is derived from code.
func NewProblem(status int, code, detail string) *Problem {
	return &Problem{
		Type:   ProblemTypeBase + code,
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
		Code:   code,
	}
}

// WithInstance returns the problem with the instance set, usually the request
// path.
func (p *Problem) WithInstance(instance string) *Problem {
	p.Instance = instance
	return p
}

// Error lets a Problem be used where an error is expected.
func (p *Problem) Error() string {
	return fmt.Sprintf("%s: %s", p.Code, p.Detail)
}

// WriteProblem writes p as the response body, defaulting the instance to the
// request path and the title to the status text.
func WriteProblem(w http.ResponseWriter, r *http.Request, p *Problem) {
	if p.Type == "" {
		p.Type = ProblemTypeBase + p.Code
	}
	if p.Title == "" {
		p.Title = http.StatusText(p.Status)
	}
	if p.Instance == "" {
		p.Instance = r.URL.Path
	}

	body, err := json.Marshal(p)
	if err != nil {
		// Marshalling a Problem cannot fail in practice; keep the response
		// valid rather than panicking inside an error path.
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", ContentTypeProblemJSON)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(p.Status)
	_, _ = w.Write(body)
}

// NotFoundHandler answers unmatched routes with a problem document.
func NotFoundHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteProblem(w, r, NewProblem(http.StatusNotFound, CodeNotFound, "No route matches this path."))
	})
}

// MethodNotAllowedHandler answers matched paths with an unsupported method.
func MethodNotAllowedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteProblem(w, r, NewProblem(http.StatusMethodNotAllowed, CodeMethodNotAllowed, "This method is not supported for this path."))
	})
}

// ErrPayloadTooLarge is returned by ReadBody when the body exceeds the limit
// configured in MaxBodyBytes.
var ErrPayloadTooLarge = errors.New("request body too large")

// BodyTooLargeProblem is the problem returned for an oversized body.
func BodyTooLargeProblem() *Problem {
	return NewProblem(http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "The request body is larger than this endpoint allows.")
}
