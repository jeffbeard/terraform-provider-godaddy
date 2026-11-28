package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Token scopes, as GoDaddy names them.
const (
	ScopeRead        = "domains.domain:read"
	ScopeDNS         = "domains.dns:update"
	ScopeNameservers = "domains.nameserver:update"
)

// Error codes GoDaddy returns that callers act on.
const (
	CodeUnknownDomain  = "UNKNOWN_DOMAIN"
	CodeRecordNotFound = "RECORD_NOT_FOUND"
)

// APIError is a response from GoDaddy with a status of 400 or above.
type APIError struct {
	Method string
	Path   string
	Status int

	// Code and Message are GoDaddy's, and are empty when the body was empty
	// or not JSON. GoDaddy's 401 has an empty body.
	Code    string
	Message string
	Fields  []string

	// RetryAfter is how long GoDaddy asked the caller to wait, or zero.
	RetryAfter time.Duration

	// Scope is the token scope this request needs. A token without it gets
	// a 401 with an empty body, exactly like an invalid token.
	Scope string
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "GoDaddy returned %d for %s %s", e.Status, e.Method, e.Path)
	if e.Code != "" || e.Message != "" {
		fmt.Fprintf(&b, ": %s %s", e.Code, e.Message)
	}
	if len(e.Fields) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(e.Fields, "; "))
	}
	if e.Status == http.StatusUnauthorized {
		fmt.Fprintf(&b, ": the token is invalid or lacks the %s scope", e.Scope)
	}
	return b.String()
}

// IsRecordNotFound reports whether err says the record group does not exist.
func IsRecordNotFound(err error) bool { return hasCode(err, http.StatusNotFound, CodeRecordNotFound) }

// IsUnknownDomain reports whether err says GoDaddy holds no DNS zone for the
// domain: it is not registered there, or its DNS is hosted elsewhere.
func IsUnknownDomain(err error) bool { return hasCode(err, http.StatusNotFound, CodeUnknownDomain) }

func hasCode(err error, status int, code string) bool {
	apiErr, ok := err.(*APIError)
	return ok && apiErr.Status == status && apiErr.Code == code
}

// scopeFor returns the scope a request with this method needs. Every GET in
// this client is a read; PATCH is only used to change nameservers; PUT and
// DELETE are only used on DNS records.
func scopeFor(method string) string {
	switch method {
	case http.MethodGet:
		return ScopeRead
	case http.MethodPatch:
		return ScopeNameservers
	default:
		return ScopeDNS
	}
}

// newAPIError builds an APIError from a response. It never fails: a body
// that is empty or not JSON yields an error with the status alone.
func newAPIError(method, path string, status int, header http.Header, body []byte) *APIError {
	e := &APIError{Method: method, Path: path, Status: status, Scope: scopeFor(method)}

	var parsed struct {
		Code          string  `json:"code"`
		Message       string  `json:"message"`
		RetryAfterSec float64 `json:"retryAfterSec"`
		Fields        []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Path    string `json:"path"`
		} `json:"fields"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		e.Code = parsed.Code
		e.Message = parsed.Message
		for _, f := range parsed.Fields {
			e.Fields = append(e.Fields, strings.TrimSpace(f.Path+" "+f.Message))
		}
		if parsed.RetryAfterSec > 0 {
			e.RetryAfter = time.Duration(parsed.RetryAfterSec * float64(time.Second))
		}
	}
	// The header wins over the body when both are present.
	if seconds, err := strconv.Atoi(strings.TrimSpace(header.Get("Retry-After"))); err == nil && seconds > 0 {
		e.RetryAfter = time.Duration(seconds) * time.Second
	}
	return e
}
