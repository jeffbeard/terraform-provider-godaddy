package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

const group = "/v1/domains/example.org/records/TXT/www"

func TestDoSendsHeadersAndNoBodyOnRead(t *testing.T) {
	f := &fakeAPI{}
	c, _ := testClient(t, f)

	if _, err := c.GetRecordGroup(context.Background(), "5678", "example.org", "TXT", "www"); err != nil {
		t.Fatal(err)
	}

	h := f.headers[0]
	if got := h.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q", got)
	}
	if got := h.Get("X-Shopper-Id"); got != "5678" {
		t.Errorf("X-Shopper-Id = %q", got)
	}
	if got := h.Get("Content-Type"); got != "" {
		t.Errorf("Content-Type = %q on a request with no body", got)
	}
}

func TestDoOmitsShopperHeaderWithoutCustomer(t *testing.T) {
	f := &fakeAPI{}
	c, _ := testClient(t, f)

	if _, err := c.GetRecordGroup(context.Background(), "", "example.org", "TXT", "www"); err != nil {
		t.Fatal(err)
	}
	if _, set := f.headers[0]["X-Shopper-Id"]; set {
		t.Error("X-Shopper-Id was sent although no customer was given")
	}
}

// GoDaddy answers a bad token and a missing scope alike: 401, empty body.
func TestErrorWithEmptyBodyKeepsStatusAndNamesTheScope(t *testing.T) {
	cases := []struct {
		name  string
		call  func(*Client) error
		key   string
		scope string
	}{
		{"read", func(c *Client) error {
			_, err := c.GetRecordGroup(context.Background(), "", "example.org", "TXT", "www")
			return err
		}, "GET " + group, ScopeRead},
		{"record write", func(c *Client) error {
			return c.ReplaceRecordGroup(context.Background(), "", "example.org", "TXT", "www", []Record{{Data: "x", TTL: 600}})
		}, "PUT " + group, ScopeDNS},
		{"record delete", func(c *Client) error {
			return c.DeleteRecordGroup(context.Background(), "", "example.org", "TXT", "www")
		}, "DELETE " + group, ScopeDNS},
		{"nameservers", func(c *Client) error {
			return c.SetNameservers(context.Background(), "", "example.org", []string{"ns1.example.net", "ns2.example.net"})
		}, "PATCH /v1/domains/example.org", ScopeNameservers},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAPI{}
			f.on(tc.key, reply{status: http.StatusUnauthorized})
			c, waits := testClient(t, f)

			err := tc.call(c)

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error is %T (%v), want *APIError", err, err)
			}
			if apiErr.Status != 401 || apiErr.Scope != tc.scope {
				t.Errorf("status %d scope %q, want 401 and %q", apiErr.Status, apiErr.Scope, tc.scope)
			}
			if !strings.Contains(err.Error(), "the token is invalid or lacks the "+tc.scope+" scope") {
				t.Errorf("message does not name both causes: %s", err)
			}
			if len(f.requests) != 1 || len(*waits) != 0 {
				t.Errorf("a 401 was retried: %d requests, waits %v", len(f.requests), *waits)
			}
		})
	}
}

func TestErrorBodyIsParsed(t *testing.T) {
	f := &fakeAPI{}
	f.on("PUT "+group, reply{status: 422, body: `{"code":"INVALID_BODY","message":"Request body doesn't fulfill schema",` +
		`"fields":[{"code":"UNKNOWN_RESTRICTION","message":"must have a minimum value of 600","path":"records[0].ttl"}]}`})
	c, _ := testClient(t, f)

	err := c.ReplaceRecordGroup(context.Background(), "", "example.org", "TXT", "www", []Record{{Data: "x", TTL: 1}})

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is %T, want *APIError", err)
	}
	if apiErr.Status != 422 || apiErr.Code != "INVALID_BODY" {
		t.Errorf("status %d code %q", apiErr.Status, apiErr.Code)
	}
	if want := []string{"records[0].ttl must have a minimum value of 600"}; !reflect.DeepEqual(apiErr.Fields, want) {
		t.Errorf("fields = %v, want %v", apiErr.Fields, want)
	}
	if !strings.Contains(err.Error(), "must have a minimum value of 600") {
		t.Errorf("message lacks the field detail: %s", err)
	}
}

func TestNotFoundCodesAreToldApart(t *testing.T) {
	f := &fakeAPI{}
	f.on("DELETE "+group,
		reply{status: 404, body: `{"code":"RECORD_NOT_FOUND","message":"Record not found"}`},
		reply{status: 404, body: `{"code":"UNKNOWN_DOMAIN","message":"The given domain is not registered, or does not have a zone file"}`},
		reply{status: 404, body: `not json`},
	)
	c, _ := testClient(t, f)
	del := func() error { return c.DeleteRecordGroup(context.Background(), "", "example.org", "TXT", "www") }

	if err := del(); !IsRecordNotFound(err) || IsUnknownDomain(err) {
		t.Errorf("first: %v", err)
	}
	if err := del(); !IsUnknownDomain(err) || IsRecordNotFound(err) {
		t.Errorf("second: %v", err)
	}
	err := del()
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 || IsRecordNotFound(err) || IsUnknownDomain(err) {
		t.Errorf("a 404 that is not JSON must keep its status and match neither code: %v", err)
	}
}

func TestRateLimitWaits(t *testing.T) {
	cases := []struct {
		name  string
		reply reply
		want  time.Duration
	}{
		{"Retry-After header", reply{status: 429, header: map[string]string{"Retry-After": "7"}}, 7 * time.Second},
		{"retryAfterSec in body", reply{status: 429, body: `{"code":"TOO_MANY_REQUESTS","retryAfterSec":12}`}, 12 * time.Second},
		{"header wins over body", reply{status: 429, header: map[string]string{"Retry-After": "3"}, body: `{"retryAfterSec":50}`}, 3 * time.Second},
		{"nothing stated", reply{status: 429}, 30 * time.Second},
		{"capped", reply{status: 429, header: map[string]string{"Retry-After": "900"}}, 60 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAPI{}
			f.on("GET "+group, tc.reply)
			c, waits := testClient(t, f)

			if _, err := c.GetRecordGroup(context.Background(), "", "example.org", "TXT", "www"); err != nil {
				t.Fatalf("the second attempt should have succeeded: %v", err)
			}
			if want := []time.Duration{tc.want}; !reflect.DeepEqual(*waits, want) {
				t.Errorf("waits = %v, want %v", *waits, want)
			}
		})
	}
}

func TestServerErrorsAreRetriedWithBackoffThenReturned(t *testing.T) {
	f := &fakeAPI{}
	f.on("PUT "+group, reply{status: 500}, reply{status: 502, body: "bad gateway"}, reply{status: 503})
	c, waits := testClient(t, f)

	err := c.ReplaceRecordGroup(context.Background(), "", "example.org", "TXT", "www", []Record{{Data: "x", TTL: 600}})

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 503 {
		t.Fatalf("want the last error (503), got %v", err)
	}
	if len(f.requests) != 3 {
		t.Errorf("sent %d requests, want 3", len(f.requests))
	}
	if want := []time.Duration{2 * time.Second, 4 * time.Second}; !reflect.DeepEqual(*waits, want) {
		t.Errorf("waits = %v, want %v", *waits, want)
	}
}

func TestServerErrorThenSuccess(t *testing.T) {
	f := &fakeAPI{}
	f.on("GET "+group, reply{status: 500}, reply{status: 200, body: `[{"data":"x","name":"www","ttl":600,"type":"TXT"}]`})
	c, _ := testClient(t, f)

	records, err := c.GetRecordGroup(context.Background(), "", "example.org", "TXT", "www")
	if err != nil || len(records) != 1 {
		t.Fatalf("records %v, err %v", records, err)
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	for _, status := range []int{400, 403, 404, 409, 422} {
		f := &fakeAPI{}
		f.on("GET "+group, reply{status: status})
		c, waits := testClient(t, f)

		_, err := c.GetRecordGroup(context.Background(), "", "example.org", "TXT", "www")
		if err == nil || len(f.requests) != 1 || len(*waits) != 0 {
			t.Errorf("status %d: err %v, %d requests, waits %v", status, err, len(f.requests), *waits)
		}
	}
}

func TestCancelledContextStopsRetrying(t *testing.T) {
	f := &fakeAPI{}
	f.on("GET "+group, reply{status: 500}, reply{status: 500}, reply{status: 500})
	c, _ := testClient(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	c.sleep = func(context.Context, time.Duration) error {
		cancel()
		return ctx.Err()
	}

	_, err := c.GetRecordGroup(ctx, "", "example.org", "TXT", "www")

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 500 {
		t.Errorf("want the GoDaddy error from the attempt that ran, got %v", err)
	}
	if len(f.requests) != 1 {
		t.Errorf("sent %d requests after cancellation, want 1", len(f.requests))
	}
}
