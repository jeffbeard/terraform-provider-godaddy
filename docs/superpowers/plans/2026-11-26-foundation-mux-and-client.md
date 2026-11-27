# Foundation: SDK Upgrade, Mux, New Provider Skeleton and API Client Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put in place everything the two new resources will stand on, without adding a resource and without changing what `godaddy_domain_record` does: a current Terraform SDK, a second (framework) provider served beside the old one, a rate limit shared across the process, and API client calls that address one record group or one domain's nameservers.

**Architecture:** `main.go` serves two providers through `terraform-plugin-mux` on protocol 5: the existing SDKv2 provider in `plugin/godaddy` (untouched) and a new `terraform-plugin-framework` provider in `internal/provider` (no resources yet). Both build `api.Client`. The client gains a second request path, `do`, with typed errors and bounded retry, used only by new calls in `api/records.go` and `api/nameservers.go`; the old path, `execute`, is not changed.

**Tech Stack:** Go 1.26 or later (the SDK upgrade raises the module's `go` directive), `terraform-plugin-sdk/v2` v2.41.1, `terraform-plugin-framework` v1.19.0, `terraform-plugin-mux` v0.23.1, `terraform-plugin-go` v0.31.0, `terraform-plugin-log`. Tests use the standard library and `net/http/httptest` only.

**Spec:** `docs/superpowers/specs/2026-11-26-safe-dns-resources-design.md`, sections "Architecture", "API client additions" and "Retry". This plan covers steps 3 and 4 of its "Order of work".

---

## Where this plan sits

| Plan | Covers | Status |
|---|---|---|
| 1 | Probes, behaviour document, tests pinning the old resource | Done (pull request 11) |
| 2 (this one) | SDK upgrade, mux, framework provider skeleton, shared limiter, client calls with typed errors and retry | Ready |
| 3 | `godaddy_dns_record`, `godaddy_domain_nameservers`, deprecation, documentation, migration guide, OpenTofu, lint, release | Write after this plan is merged |

Everything in this plan was built and run in a scratch copy of the repository first, task by task in the order below. The code blocks are those files, and the "Expected" outputs are what was observed.

## Rules for whoever executes this

- **`godaddy_domain_record` is frozen.** Do not change any file under `plugin/godaddy/`, or `api/domains.go`, `api/types.go`, `api/credentials.go`. In `api/client.go` change only what Tasks 3 and 4 show; the functions `execute` and `validate` must not change.
- **The pinning tests are the alarm.** `plugin/godaddy/resource_dns_record_requests_test.go` and `resource_dns_record_plan_test.go` must pass after every task, unchanged. If one fails, stop and report. Never edit their expected values.
- Do not contact the real GoDaddy API. Nothing in this plan needs a token.
- Commit after each task. Do not push or open a pull request unless the maintainer asks. Humans merge pull requests.
- Work on a branch created from the branch that holds this plan (`chore/p19-and-plan-2`), or from `master` once that has been merged. Pull request 11 is merged; the checks below compare against `master`.
- In the maintainer's shell `rm` is aliased to prompt; use `/bin/rm -f` if a file must be deleted.

## Deferred on purpose

- **Lint in CI.** `golangci-lint run ./...` reports about 40 findings today, most in frozen files that must not be edited and in the probe tool. Adding it to CI needs an exclusion policy and a clean-up of the probe tool, which belongs with plan 3.
- **The unsafe-`baseurl` refusal and the nameserver warning** from the spec live in the resources, so they are in plan 3.

## File structure

| File | Change | Responsibility |
|---|---|---|
| `go.mod`, `go.sum` | modify | New dependency versions. |
| `api/client.go` | modify (Tasks 3, 4) | Shared throttle; one new field. `execute` and `validate` untouched. |
| `api/limiter_test.go` | create | Clients for one credential share the throttle. |
| `api/errors.go` | create | `APIError`, scope names, error-code helpers. |
| `api/request.go` | create | `do`: the request path for new calls, with retry. |
| `api/records.go` | create | `Record`, `GetRecordGroup`, `ReplaceRecordGroup`, `DeleteRecordGroup`. |
| `api/nameservers.go` | create | `GetNameservers`, `SetNameservers`. |
| `api/fake_test.go` | create | Fake GoDaddy server for client tests. |
| `api/request_test.go`, `api/records_test.go`, `api/nameservers_test.go` | create | Tests for the above. |
| `internal/provider/provider.go` | create | Framework provider: schema identical to the old provider's, settings from configuration and environment, client construction. No resources. |
| `internal/provider/provider_test.go` | create | Settings resolution. |
| `main.go` | modify | Mux server; drops the framework half's duplicate deprecation warnings. |
| `main_test.go` | create | The two provider schemas match; configuration works through the mux; one error, not two; two deprecation warnings, not four. |

---

### Task 1: Branch and baseline

- [ ] **Step 1: Create the branch**

```bash
git checkout chore/p19-and-plan-2   # the branch that holds this plan; use master if it has been merged
git checkout -b feat/safe-dns-foundation
```

- [ ] **Step 2: Confirm the starting point is green**

Run: `go build ./... && go vet ./... && go test -race -count=1 ./... 2>&1 | grep -v '^20[0-9][0-9]/'`
Expected: `ok` for `api`, `plugin/godaddy` (about 10 seconds) and `scripts/probe`. If anything fails, stop and report.

No commit in this task.

---

### Task 2: Upgrade the Terraform SDK

`terraform-plugin-sdk/v2` goes from v2.6.0 (2021) to v2.41.1. The mux library needs a current SDK. This is the only change underneath the frozen resource, and the pinning tests are how we know it changed nothing: they assert both the requests the resource sends and what it plans.

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Upgrade and tidy**

```bash
go get github.com/hashicorp/terraform-plugin-sdk/v2@v2.41.1
go mod tidy
```

Expected: `go.mod` now requires `github.com/hashicorp/terraform-plugin-sdk/v2 v2.41.1`, and its `go` line reads `go 1.26.0` (it was `go 1.19`). The higher `go` line is required by the new SDK and is expected. CI already builds with Go 1.27.

- [ ] **Step 2: Run everything**

Run: `go build ./... && go vet ./... && go test -race -count=1 ./... 2>&1 | grep -v '^20[0-9][0-9]/'`
Expected: `ok` for `api`, `plugin/godaddy` and `scripts/probe`.

If `plugin/godaddy` fails, the SDK upgrade changed what the old resource sends or plans. Stop and report the failing test and its output. Do not edit the test.

- [ ] **Step 3: Confirm only the dependency files changed**

Run: `git status --short`
Expected: exactly ` M go.mod` and ` M go.sum`.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: upgrade terraform-plugin-sdk to v2.41.1"
```

---

### Task 3: Share the request throttle across clients

GoDaddy's rate limit applies to a credential. Today every `api.Client` has its own one-request-per-second throttle. After Task 6 one process holds two clients for the same credential (one per provider half), which would double the request rate. This task makes clients for the same base URL and credential share one throttle.

**Files:**
- Modify: `api/client.go`
- Test: `api/limiter_test.go`

- [ ] **Step 1: Write the failing test**

Create `api/limiter_test.go`:

````go
package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func get(t *testing.T, c *Client, url string) {
	t.Helper()
	resp, err := c.client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// Two clients built for the same base URL and credential share one throttle,
// so two requests, one from each, are a second apart. A client for another
// credential is not held back by them.
func TestClientsForOneCredentialShareTheThrottle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	first, err := NewClient(srv.URL, Credentials{Token: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewClient(srv.URL, Credentials{Token: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewClient(srv.URL, Credentials{Token: "another"})
	if err != nil {
		t.Fatal(err)
	}
	if first.client.Transport != second.client.Transport {
		t.Fatal("clients for one credential have different transports")
	}
	if first.client.Transport == other.client.Transport {
		t.Fatal("clients for different credentials share a transport")
	}

	start := time.Now()
	get(t, first, srv.URL)
	get(t, other, srv.URL)
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("a client for another credential waited %s", d)
	}
	get(t, second, srv.URL)
	if d := time.Since(start); d < 900*time.Millisecond {
		t.Errorf("the second client for the same credential sent after only %s, want about 1s", d)
	}
}
````
- [ ] **Step 2: Run it to see it fail**

Run: `go test -count=1 ./api -run TestClientsForOneCredentialShareTheThrottle 2>&1 | grep -v '^20[0-9][0-9]/'`
Expected: FAIL with `clients for one credential have different transports`.

- [ ] **Step 3: Share the transport**

In `api/client.go`, add two imports to the import block, after `"bytes"`:

```go
	"crypto/sha256"
	"encoding/hex"
```

Then replace the end of `NewClient`, from `var netTransport = &http.Transport{` through the function's closing brace, with the following. Everything above it in `NewClient` (the credential and URL checks) stays as it is.

```go
	return &Client{
		baseURL:       baseURL,
		authorization: authorization,
		client: &http.Client{
			Timeout:   time.Second * 30,
			Transport: sharedTransport(baseURL, authorization),
		},
	}, nil
}

// transports holds one throttled transport for each base URL and credential
// in use. GoDaddy's limit applies to the credential, not to a Client, and one
// provider process can hold several clients for the same credential (the two
// halves of the muxed provider, or aliased provider blocks). Sharing the
// transport keeps them at one request per second between them.
var transports = struct {
	sync.Mutex
	byKey map[string]*rateLimitedTransport
}{byKey: map[string]*rateLimitedTransport{}}

func sharedTransport(baseURL, authorization string) *rateLimitedTransport {
	// The key is a digest so the credential is not kept as a map key.
	sum := sha256.Sum256([]byte(baseURL + "\x00" + authorization))
	key := hex.EncodeToString(sum[:])

	transports.Lock()
	defer transports.Unlock()
	if t, ok := transports.byKey[key]; ok {
		return t
	}
	t := &rateLimitedTransport{
		delegate: &http.Transport{
			Dial: (&net.Dialer{
				Timeout: 10 * time.Second,
			}).Dial,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		throttle: time.Now().Add(-(rateLimit)),
	}
	transports.byKey[key] = t
	return t
}
```

Do not change `rateLimitedTransport`, `RoundTrip`, `execute`, `validate` or `formatURL`.

- [ ] **Step 4: Run everything**

Run: `gofmt -l api/client.go; go build ./... && go test -race -count=1 ./... 2>&1 | grep -v '^20[0-9][0-9]/'`
Expected: `gofmt` prints nothing; `ok` for `api`, `plugin/godaddy` and `scripts/probe`. The pinning tests still pass because each of their cases uses its own fake server, so its own throttle.

- [ ] **Step 5: Commit**

```bash
git add api/client.go api/limiter_test.go
git commit -m "feat(api): share the request throttle between clients for one credential"
```

---

### Task 4: Client calls for one record group and for nameservers

Background for the reader. These calls are what the new resources will use. They differ from the old client in four ways, each learned from the live probes recorded in `docs/godaddy-api-behaviour.md`:

- They address one type and name (`/records/{type}/{name}`), so a write cannot touch another record.
- They return a typed error. GoDaddy answers a bad token and a missing token scope the same way, 401 with an empty body, which the old parser cannot read at all; the new one keeps the status and names the scope the request needed.
- They retry. Every call here is idempotent, so a retry cannot compound a write.
- An SRV group cannot be read at its own path (GoDaddy returns an empty array), so it is read from the whole-zone list and filtered.

The tests replace the one-second throttle and the retry sleeps, so the suite stays fast.

**Files:**
- Modify: `api/client.go`
- Create: `api/errors.go`, `api/request.go`, `api/records.go`, `api/nameservers.go`
- Test: `api/fake_test.go`, `api/request_test.go`, `api/records_test.go`, `api/nameservers_test.go`

- [ ] **Step 1: Write the fake server**

Create `api/fake_test.go`:

````go
package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// reply is one canned response.
type reply struct {
	status int
	header map[string]string
	body   string
}

// fakeAPI answers each request with the next reply queued for its
// "METHOD path?query" key, or with 200 and an empty body when none is
// queued. It records what it received.
type fakeAPI struct {
	mu       sync.Mutex
	requests []string           // "METHOD path?query body"
	headers  []http.Header      // parallel to requests
	queue    map[string][]reply // consumed in order
}

func (f *fakeAPI) on(key string, replies ...reply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.queue == nil {
		f.queue = map[string][]reply{}
	}
	f.queue[key] = append(f.queue[key], replies...)
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.RequestURI()

	f.mu.Lock()
	line := key
	if len(body) > 0 {
		line += " " + string(body)
	}
	f.requests = append(f.requests, line)
	f.headers = append(f.headers, r.Header.Clone())
	var next reply
	if q := f.queue[key]; len(q) > 0 {
		next, f.queue[key] = q[0], q[1:]
	} else {
		next = reply{status: http.StatusOK}
	}
	f.mu.Unlock()

	for k, v := range next.header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(next.status)
	io.WriteString(w, next.body)
}

// testClient returns a client pointed at the fake, with the one-second
// throttle removed and retry waits recorded instead of slept.
func testClient(t *testing.T, f *fakeAPI) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	c, err := NewClient(srv.URL, Credentials{Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	c.client.Transport = http.DefaultTransport

	var waits []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	return c, &waits
}

func intPtr(n int) *int { return &n }
````
- [ ] **Step 2: Write the tests for errors and retry**

Create `api/request_test.go`:

````go
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
````
- [ ] **Step 3: Write the tests for record groups**

Create `api/records_test.go`:

````go
package api

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestGetRecordGroupDecodesEveryField(t *testing.T) {
	f := &fakeAPI{}
	f.on("GET /v1/domains/example.org/records/CAA/@", reply{status: 200,
		body: `[{"data":"letsencrypt.org","flags":0,"name":"@","tag":"issue","ttl":3600,"type":"CAA"}]`})
	f.on("GET /v1/domains/example.org/records/MX/@", reply{status: 200,
		body: `[{"data":"mx1.example.net","name":"@","priority":0,"ttl":600,"type":"MX"}]`})
	c, _ := testClient(t, f)

	caa, err := c.GetRecordGroup(context.Background(), "", "example.org", "CAA", "@")
	if err != nil {
		t.Fatal(err)
	}
	want := []Record{{Name: "@", Type: "CAA", Data: "letsencrypt.org", TTL: 3600, Flags: intPtr(0), Tag: "issue"}}
	if !reflect.DeepEqual(caa, want) {
		t.Errorf("CAA = %+v", caa)
	}

	mx, err := c.GetRecordGroup(context.Background(), "", "example.org", "MX", "@")
	if err != nil {
		t.Fatal(err)
	}
	if len(mx) != 1 || mx[0].Priority == nil || *mx[0].Priority != 0 {
		t.Errorf("priority 0 must decode as a pointer to 0, not nil: %+v", mx)
	}
}

func TestGetRecordGroupOfMissingGroupIsEmptyNotAnError(t *testing.T) {
	f := &fakeAPI{}
	f.on("GET /v1/domains/example.org/records/TXT/gone", reply{status: 200, body: `[]`})
	c, _ := testClient(t, f)

	records, err := c.GetRecordGroup(context.Background(), "", "example.org", "TXT", "gone")
	if err != nil || len(records) != 0 {
		t.Errorf("records %v, err %v", records, err)
	}
}

func TestGroupPathEscaping(t *testing.T) {
	cases := map[string]string{
		"@":              "/v1/domains/example.org/records/TXT/@",
		"*":              "/v1/domains/example.org/records/TXT/%2A",
		"*.sub":          "/v1/domains/example.org/records/TXT/%2A.sub",
		"_dmarc":         "/v1/domains/example.org/records/TXT/_dmarc",
		"sel._domainkey": "/v1/domains/example.org/records/TXT/sel._domainkey",
		"a/b":            "/v1/domains/example.org/records/TXT/a%2Fb",
		"a?limit=1":      "/v1/domains/example.org/records/TXT/a%3Flimit=1",
	}
	for name, want := range cases {
		if got := groupPath("example.org", "TXT", name); got != want {
			t.Errorf("groupPath(%q) = %s, want %s", name, got, want)
		}
	}
}

// Fields a type uses must reach the wire even when zero; fields it does not
// use must be absent; name and type are never sent.
func TestReplaceRecordGroupBodies(t *testing.T) {
	cases := []struct {
		name       string
		recordType string
		records    []Record
		want       string
	}{
		{"TXT", "TXT", []Record{{Name: "www", Type: "TXT", Data: "x", TTL: 600}},
			`[{"data":"x","ttl":600}]`},
		{"MX with priority 0", "MX", []Record{{Data: "mx1.example.net", TTL: 3600, Priority: intPtr(0)}},
			`[{"data":"mx1.example.net","ttl":3600,"priority":0}]`},
		{"CAA with flags 0", "CAA", []Record{{Data: "letsencrypt.org", TTL: 3600, Flags: intPtr(0), Tag: "issue"}},
			`[{"data":"letsencrypt.org","ttl":3600,"flags":0,"tag":"issue"}]`},
		{"SRV with weight 0", "SRV", []Record{{Data: "sip.example.net", TTL: 3600, Priority: intPtr(10), Weight: intPtr(0),
			Port: intPtr(5060), Service: "_sip", Protocol: "_tcp"}},
			`[{"data":"sip.example.net","ttl":3600,"priority":10,"weight":0,"port":5060,"service":"_sip","protocol":"_tcp"}]`},
		{"two values", "A", []Record{{Data: "192.0.2.1", TTL: 600}, {Data: "192.0.2.2", TTL: 600}},
			`[{"data":"192.0.2.1","ttl":600},{"data":"192.0.2.2","ttl":600}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAPI{}
			c, _ := testClient(t, f)

			if err := c.ReplaceRecordGroup(context.Background(), "", "example.org", tc.recordType, "www", tc.records); err != nil {
				t.Fatal(err)
			}
			want := "PUT /v1/domains/example.org/records/" + tc.recordType + "/www " + tc.want
			if len(f.requests) != 1 || f.requests[0] != want {
				t.Errorf("sent\n  %v\nwant\n  %s", f.requests, want)
			}
		})
	}
}

func TestReplaceRecordGroupDoesNotChangeItsArgument(t *testing.T) {
	f := &fakeAPI{}
	c, _ := testClient(t, f)
	records := []Record{{Name: "www", Type: "TXT", Data: "x", TTL: 600}}

	if err := c.ReplaceRecordGroup(context.Background(), "", "example.org", "TXT", "www", records); err != nil {
		t.Fatal(err)
	}
	if records[0].Name != "www" || records[0].Type != "TXT" {
		t.Errorf("the caller's slice was modified: %+v", records)
	}
}

// An empty replace is what the old resource sends. The new client must
// never send one.
func TestReplaceRecordGroupRefusesAnEmptySet(t *testing.T) {
	f := &fakeAPI{}
	c, _ := testClient(t, f)

	for _, records := range [][]Record{nil, {}} {
		if err := c.ReplaceRecordGroup(context.Background(), "", "example.org", "TXT", "www", records); err == nil {
			t.Error("want an error for an empty set")
		}
	}
	if len(f.requests) != 0 {
		t.Errorf("an empty replace reached the server: %v", f.requests)
	}
}

func TestDeleteRecordGroup(t *testing.T) {
	f := &fakeAPI{}
	f.on("DELETE /v1/domains/example.org/records/TXT/www", reply{status: 204})
	c, _ := testClient(t, f)

	if err := c.DeleteRecordGroup(context.Background(), "", "example.org", "TXT", "www"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"DELETE /v1/domains/example.org/records/TXT/www"}; !reflect.DeepEqual(f.requests, want) {
		t.Errorf("sent %v", f.requests)
	}
}

// An SRV group is read from the whole-zone list: GoDaddy returns nothing for
// a per-name read of SRV. Paging is one-based and ends at an empty page.
func TestGetRecordGroupForSRVPagesTheWholeZone(t *testing.T) {
	f := &fakeAPI{}
	f.on("GET /v1/domains/example.org/records?limit=500&offset=1", reply{status: 200, body: `[
	  {"data":"192.0.2.1","name":"@","ttl":3600,"type":"A"},
	  {"data":"sip.example.net","name":"voip","port":5060,"priority":10,"protocol":"_tcp","service":"_sip","ttl":3600,"type":"SRV","weight":5},
	  {"data":"other.example.net","name":"elsewhere","port":1,"priority":1,"protocol":"_tcp","service":"_sip","ttl":3600,"type":"SRV","weight":1},
	  {"data":"voip-target.example.net","name":"voip","ttl":3600,"type":"CNAME"}
	]`})
	f.on("GET /v1/domains/example.org/records?limit=500&offset=2", reply{status: 200, body: `[
	  {"data":"xmpp.example.net","name":"VOIP","port":5222,"priority":10,"protocol":"_tcp","service":"_xmpp-client","ttl":3600,"type":"SRV","weight":5}
	]`})
	f.on("GET /v1/domains/example.org/records?limit=500&offset=3", reply{status: 200, body: `[]`})
	c, _ := testClient(t, f)

	records, err := c.GetRecordGroup(context.Background(), "", "example.org", "SRV", "voip")
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, r := range records {
		got = append(got, r.Service+" "+r.Data)
	}
	if want := []string{"_sip sip.example.net", "_xmpp-client xmpp.example.net"}; !reflect.DeepEqual(got, want) {
		t.Errorf("group = %v, want %v", got, want)
	}
	if len(f.requests) != 3 {
		t.Errorf("sent %d requests, want three pages: %v", len(f.requests), f.requests)
	}
	for _, r := range f.requests {
		if strings.Contains(r, "/records/SRV") {
			t.Errorf("the SRV read must not use a per-type or per-name SRV path: %s", r)
		}
	}
}

func TestGetRecordGroupForSRVStopsOnError(t *testing.T) {
	f := &fakeAPI{}
	f.on("GET /v1/domains/example.org/records?limit=500&offset=1", reply{status: 404,
		body: `{"code":"UNKNOWN_DOMAIN","message":"The given domain is not registered, or does not have a zone file"}`})
	c, _ := testClient(t, f)

	records, err := c.GetRecordGroup(context.Background(), "", "example.org", "SRV", "voip")
	if !IsUnknownDomain(err) || records != nil {
		t.Errorf("records %v, err %v", records, err)
	}
}
````
- [ ] **Step 4: Write the tests for nameservers**

Create `api/nameservers_test.go`:

````go
package api

import (
	"context"
	"reflect"
	"testing"
)

func TestGetNameserversReturnsThemAsGiven(t *testing.T) {
	f := &fakeAPI{}
	f.on("GET /v1/domains/example.org", reply{status: 200,
		body: `{"domain":"example.org","nameServers":["NS10.DOMAINCONTROL.COM","NS09.DOMAINCONTROL.COM"],"status":"ACTIVE"}`})
	c, _ := testClient(t, f)

	got, err := c.GetNameservers(context.Background(), "", "example.org")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"NS10.DOMAINCONTROL.COM", "NS09.DOMAINCONTROL.COM"}; !reflect.DeepEqual(got, want) {
		t.Errorf("nameservers = %v, want %v", got, want)
	}
}

func TestSetNameserversBodyAndPath(t *testing.T) {
	f := &fakeAPI{}
	f.on("PATCH /v1/domains/example.org", reply{status: 204})
	c, _ := testClient(t, f)

	err := c.SetNameservers(context.Background(), "", "example.org", []string{"ns1.example.net", "ns2.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`PATCH /v1/domains/example.org {"nameServers":["ns1.example.net","ns2.example.net"]}`}
	if !reflect.DeepEqual(f.requests, want) {
		t.Errorf("sent %v, want %v", f.requests, want)
	}
}

func TestSetNameserversRefusesFewerThanTwo(t *testing.T) {
	f := &fakeAPI{}
	c, _ := testClient(t, f)

	for _, ns := range [][]string{nil, {"ns1.example.net"}} {
		if err := c.SetNameservers(context.Background(), "", "example.org", ns); err == nil {
			t.Errorf("want an error for %v", ns)
		}
	}
	if len(f.requests) != 0 {
		t.Errorf("a request was sent: %v", f.requests)
	}
}
````
- [ ] **Step 5: Run the tests to see them fail**

Run: `go test -count=1 ./api 2>&1 | grep -v '^20[0-9][0-9]/' | head`
Expected: the package fails to build, with errors such as `c.sleep undefined`, `c.GetNameservers undefined`, `c.SetNameservers undefined`.

- [ ] **Step 6: Add the retry hook to the client**

In `api/client.go`:

Add `"context"` to the import block, after `"bytes"`.

Replace the `Client` struct with:

```go
type Client struct {
	baseURL       string
	authorization string
	client        *http.Client

	// sleep waits between retries of the calls in request.go. Tests replace
	// it so they do not wait.
	sleep func(context.Context, time.Duration) error
}
```

In `NewClient`, add the `sleep` field to the returned client, so the return statement reads:

```go
	return &Client{
		baseURL:       baseURL,
		authorization: authorization,
		client: &http.Client{
			Timeout:   time.Second * 30,
			Transport: sharedTransport(baseURL, authorization),
		},
		sleep: sleepContext,
	}, nil
```

- [ ] **Step 7: Add the typed error**

Create `api/errors.go`:

````go
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
````
- [ ] **Step 8: Add the request path**

Create `api/request.go`:

````go
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const (
	// maxAttempts is how many times one call is sent before its error is
	// returned.
	maxAttempts = 3

	// defaultRateLimitWait is used for a 429 that does not say how long to
	// wait. No probe could produce a 429, so its format is unverified.
	defaultRateLimitWait = 30 * time.Second
	maxRateLimitWait     = 60 * time.Second

	firstBackoff = 2 * time.Second
)

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// do sends one request for the calls in records.go and nameservers.go, and
// retries it when retrying can help. It is separate from execute, which the
// frozen godaddy_domain_record resource uses and which must not change.
//
// Every call made through do is idempotent, so sending it again cannot
// compound a write. It retries a 429 after the wait GoDaddy asks for, and a
// 5xx or a transport failure after a growing pause, up to maxAttempts in all.
//
// Bodies are not logged. A non-nil result receives the decoded response.
func (c *Client) do(ctx context.Context, customerID, method, path string, body, result interface{}) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}

	backoff := firstBackoff
	for attempt := 1; ; attempt++ {
		wait, err := c.attempt(ctx, customerID, method, path, payload, result)
		if err == nil {
			return nil
		}
		if wait < 0 || attempt == maxAttempts || ctx.Err() != nil {
			return err
		}
		if wait == 0 {
			wait = backoff
			backoff *= 2
		}
		tflog.Debug(ctx, "retrying GoDaddy request", map[string]interface{}{
			"method": method, "path": path, "attempt": attempt, "wait": wait.String(), "error": err.Error(),
		})
		if sleepErr := c.sleep(ctx, wait); sleepErr != nil {
			return err
		}
	}
}

// attempt sends the request once. On failure it also says how to retry: a
// negative wait means do not; zero means retry after the caller's backoff;
// a positive wait means retry after exactly that long.
func (c *Client) attempt(ctx context.Context, customerID, method, path string, payload []byte, result interface{}) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return -1, err
	}
	if customerID != "" {
		req.Header.Set(headerCustomerID, customerID)
	}
	req.Header.Set(headerAccept, mediaTypeJSON)
	if payload != nil {
		req.Header.Set(headerContent, mediaTypeJSON)
	}
	req.Header.Set(headerAuthorization, c.authorization)

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)

	tflog.Debug(ctx, "GoDaddy request", map[string]interface{}{
		"method": method, "path": path, "status": resp.StatusCode,
	})

	if resp.StatusCode >= http.StatusBadRequest {
		apiErr := newAPIError(method, path, resp.StatusCode, resp.Header, respBody)
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			wait := apiErr.RetryAfter
			if wait <= 0 {
				wait = defaultRateLimitWait
			}
			if wait > maxRateLimitWait {
				wait = maxRateLimitWait
			}
			return wait, apiErr
		case resp.StatusCode >= http.StatusInternalServerError:
			return 0, apiErr
		default:
			return -1, apiErr
		}
	}
	if readErr != nil {
		return 0, readErr
	}
	if result == nil || len(respBody) == 0 {
		return -1, nil
	}
	if err := json.Unmarshal(respBody, result); err != nil {
		return -1, err
	}
	return -1, nil
}
````
- [ ] **Step 9: Add the record calls**

Create `api/records.go`:

````go
package api

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Record is one DNS record as the per-name endpoints send and return it.
//
// A field that a record type does not use is left out of a request; a field
// it does use is sent even when its value is zero. GoDaddy refuses a CAA
// record without "flags", so flags 0 has to reach the wire. That is why the
// numeric fields are pointers: nil means "not used", a pointer to 0 means 0.
type Record struct {
	// Name and Type are set on records GoDaddy returns. They are not sent:
	// the path of a per-name request already carries both.
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`

	Data string `json:"data"`
	TTL  int    `json:"ttl"`

	Priority *int `json:"priority,omitempty"` // MX, SRV

	Weight   *int   `json:"weight,omitempty"`   // SRV
	Port     *int   `json:"port,omitempty"`     // SRV
	Service  string `json:"service,omitempty"`  // SRV
	Protocol string `json:"protocol,omitempty"` // SRV

	Flags *int   `json:"flags,omitempty"` // CAA
	Tag   string `json:"tag,omitempty"`   // CAA
}

// zonePageSize is the page size for the whole-zone list.
const zonePageSize = 500

func groupPath(domain, recordType, name string) string {
	return "/v1/domains/" + url.PathEscape(domain) + "/records/" + url.PathEscape(recordType) + "/" + url.PathEscape(name)
}

// GetRecordGroup returns every record of one type at one name. An empty
// result with a nil error means the group does not exist.
//
// SRV is read differently. GoDaddy answers a per-name read of an SRV group
// with an empty array even when records exist, so an SRV group is read from
// the whole-zone list and filtered.
func (c *Client) GetRecordGroup(ctx context.Context, customerID, domain, recordType, name string) ([]Record, error) {
	if recordType == SRVType {
		return c.getSRVGroup(ctx, customerID, domain, name)
	}
	var records []Record
	if err := c.do(ctx, customerID, "GET", groupPath(domain, recordType, name), nil, &records); err != nil {
		return nil, err
	}
	return records, nil
}

// getSRVGroup pages through the whole zone. GoDaddy's offset is a page
// number that starts at 1, and the list ends at the first empty page.
func (c *Client) getSRVGroup(ctx context.Context, customerID, domain, name string) ([]Record, error) {
	var group []Record
	for page := 1; ; page++ {
		path := fmt.Sprintf("/v1/domains/%s/records?limit=%d&offset=%d", url.PathEscape(domain), zonePageSize, page)
		var records []Record
		if err := c.do(ctx, customerID, "GET", path, nil, &records); err != nil {
			return nil, err
		}
		if len(records) == 0 {
			return group, nil
		}
		for _, r := range records {
			if r.Type == SRVType && strings.EqualFold(r.Name, name) {
				group = append(group, r)
			}
		}
	}
}

// ReplaceRecordGroup makes records the complete set of records of one type
// at one name. It touches no other record. It refuses an empty set before
// sending anything: removing a group is DeleteRecordGroup's job.
func (c *Client) ReplaceRecordGroup(ctx context.Context, customerID, domain, recordType, name string, records []Record) error {
	if len(records) == 0 {
		return errors.New("refusing to replace a record group with nothing; delete the group instead")
	}
	body := make([]Record, len(records))
	for i, r := range records {
		r.Name, r.Type = "", ""
		body[i] = r
	}
	return c.do(ctx, customerID, "PUT", groupPath(domain, recordType, name), body, nil)
}

// DeleteRecordGroup removes every record of one type at one name. When the
// group does not exist GoDaddy answers 404 RECORD_NOT_FOUND, which this
// returns as an error; see IsRecordNotFound.
func (c *Client) DeleteRecordGroup(ctx context.Context, customerID, domain, recordType, name string) error {
	return c.do(ctx, customerID, "DELETE", groupPath(domain, recordType, name), nil, nil)
}
````
- [ ] **Step 10: Add the nameserver calls**

Create `api/nameservers.go`:

````go
package api

import (
	"context"
	"errors"
	"net/url"
)

// GetNameservers returns the nameservers a domain is delegated to, as
// GoDaddy returns them: case and order are not stable.
func (c *Client) GetNameservers(ctx context.Context, customerID, domain string) ([]string, error) {
	var detail struct {
		NameServers []string `json:"nameServers"`
	}
	if err := c.do(ctx, customerID, "GET", "/v1/domains/"+url.PathEscape(domain), nil, &detail); err != nil {
		return nil, err
	}
	return detail.NameServers, nil
}

// SetNameservers changes the nameservers a domain is delegated to. GoDaddy
// accepts the change with a 204 and reads show it 10 to 20 seconds later, so
// a read straight afterwards still returns the old nameservers.
func (c *Client) SetNameservers(ctx context.Context, customerID, domain string, nameservers []string) error {
	if len(nameservers) < 2 {
		return errors.New("a domain needs at least two nameservers")
	}
	body := map[string][]string{"nameServers": nameservers}
	return c.do(ctx, customerID, "PATCH", "/v1/domains/"+url.PathEscape(domain), body, nil)
}
````
- [ ] **Step 11: Tidy and run everything**

`api/request.go` imports `terraform-plugin-log`, which until now was only an indirect dependency.

```bash
go mod tidy
gofmt -l api
go build ./... && go vet ./api && go test -race -count=1 ./... 2>&1 | grep -v '^20[0-9][0-9]/'
```

Expected: `gofmt -l api` prints only `api/domains.go` (a frozen file that was already unformatted; leave it). `ok` for `api` (about 2 seconds), `plugin/godaddy` and `scripts/probe`.

- [ ] **Step 12: Prove two of the safety tests can fail**

In `api/records.go`, in `ReplaceRecordGroup`, temporarily change `if len(records) == 0 {` to `if false {`.
Run: `go test -count=1 ./api -run TestReplaceRecordGroupRefusesAnEmptySet 2>&1 | grep -v '^20[0-9][0-9]/'`
Expected: FAIL, including `an empty replace reached the server`.
Undo the change.

In `api/records.go`, in the `Record` struct, temporarily change `Flags *int` to `Flags int` (and leave the tag as it is).
Run: `go test -count=1 ./api 2>&1 | grep -v '^20[0-9][0-9]/' | head -5`
Expected: the package fails to build (the tests pass `intPtr(0)`), which shows the pointer is what the tests rely on.
Undo the change.

Run: `grep -n 'if false\|Flags int' api/records.go; go test -count=1 ./api 2>&1 | grep -v '^20[0-9][0-9]/'`
Expected: the `grep` prints nothing (no leftover temporary edit), then `ok`.

- [ ] **Step 13: Confirm the old request path is untouched**

Run: `git diff master -- api/client.go | grep -E '^[-+]' | grep -E 'func \(c \*Client\) execute|func validate|json.Unmarshal\(body, &errResp\)'`
Expected: no output. (`execute` and `validate` do not appear among the changed lines.)

- [ ] **Step 14: Commit**

```bash
git add go.mod go.sum api/client.go api/errors.go api/request.go api/records.go api/nameservers.go api/fake_test.go api/request_test.go api/records_test.go api/nameservers_test.go
git commit -m "feat(api): add record group and nameserver calls with typed errors and retry"
```

---

### Task 5: The framework provider, with no resources

New resources will be written with `terraform-plugin-framework`. Terraform configures both provider halves from the same `provider "godaddy"` block, and `terraform-plugin-mux` refuses to serve two halves whose provider schemas differ. So this provider declares the same four arguments, with the same descriptions, sensitivity and deprecations, as `plugin/godaddy/provider.go:11-43`, and resolves environment variables and the default base URL the same way.

**Files:**
- Create: `internal/provider/provider.go`
- Test: `internal/provider/provider_test.go`

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/hashicorp/terraform-plugin-framework@v1.19.0
```

- [ ] **Step 2: Write the failing test**

Create `internal/provider/provider_test.go`:

````go
package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/n3integration/terraform-provider-godaddy/api"
)

func TestResolve(t *testing.T) {
	env := map[string]string{
		"GODADDY_API_TOKEN":  "env-token",
		"GODADDY_API_KEY":    "env-key",
		"GODADDY_API_SECRET": "env-secret",
	}
	getenv := func(k string) string { return env[k] }
	noEnv := func(string) string { return "" }
	null := types.StringNull()

	cases := []struct {
		name   string
		model  providerModel
		getenv func(string) string
		want   Settings
	}{
		{
			name:   "nothing set uses the environment and the default URL",
			model:  providerModel{Token: null, Key: null, Secret: null, BaseURL: null},
			getenv: getenv,
			want: Settings{
				Credentials: api.Credentials{Token: "env-token", Key: "env-key", Secret: "env-secret"},
				BaseURL:     DefaultBaseURL,
			},
		},
		{
			name: "configuration wins over the environment",
			model: providerModel{
				Token: types.StringValue("cfg-token"), Key: types.StringValue("cfg-key"),
				Secret: types.StringValue("cfg-secret"), BaseURL: types.StringValue("https://api.ote-godaddy.com"),
			},
			getenv: getenv,
			want: Settings{
				Credentials: api.Credentials{Token: "cfg-token", Key: "cfg-key", Secret: "cfg-secret"},
				BaseURL:     "https://api.ote-godaddy.com",
			},
		},
		{
			name:   "an unknown value counts as not set",
			model:  providerModel{Token: types.StringUnknown(), Key: null, Secret: null, BaseURL: types.StringUnknown()},
			getenv: getenv,
			want: Settings{
				Credentials: api.Credentials{Token: "env-token", Key: "env-key", Secret: "env-secret"},
				BaseURL:     DefaultBaseURL,
			},
		},
		{
			name:   "an empty string counts as not set",
			model:  providerModel{Token: types.StringValue(""), Key: null, Secret: null, BaseURL: types.StringValue("")},
			getenv: getenv,
			want: Settings{
				Credentials: api.Credentials{Token: "env-token", Key: "env-key", Secret: "env-secret"},
				BaseURL:     DefaultBaseURL,
			},
		},
		{
			name:   "nothing anywhere leaves the credentials empty",
			model:  providerModel{Token: null, Key: null, Secret: null, BaseURL: null},
			getenv: noEnv,
			want:   Settings{BaseURL: DefaultBaseURL},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolve(tc.model, tc.getenv); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The framework half must use the same default as the SDKv2 half.
func TestDefaultBaseURLMatchesTheOldProvider(t *testing.T) {
	if DefaultBaseURL != "https://api.godaddy.com" {
		t.Errorf("DefaultBaseURL = %q; plugin/godaddy/provider.go defaults baseurl to https://api.godaddy.com", DefaultBaseURL)
	}
}
````
- [ ] **Step 3: Run it to see it fail**

Run: `go test -count=1 ./internal/... 2>&1 | head -5`
Expected: the package fails to build, with `undefined: providerModel` and `undefined: Settings`.

- [ ] **Step 4: Write the provider**

Create `internal/provider/provider.go`:

````go
// Package provider is the terraform-plugin-framework half of the GoDaddy
// provider. It is served beside the older SDKv2 provider in plugin/godaddy
// through terraform-plugin-mux, so both are configured from one provider
// block. New resources are added here; plugin/godaddy is frozen.
package provider

import (
	"context"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/n3integration/terraform-provider-godaddy/api"
)

// DefaultBaseURL is used when baseurl is not set. It must equal the default
// in plugin/godaddy/provider.go.
const DefaultBaseURL = "https://api.godaddy.com"

// The descriptions and deprecation messages below must stay identical to
// plugin/godaddy/provider.go. terraform-plugin-mux refuses to serve two
// providers whose schemas differ; TestMuxServesBothProviders checks this.
const (
	tokenDescription   = "GoDaddy Personal Access Token. Requires the domains.dns:update and domains.domain:read scopes. Takes precedence over key/secret."
	keyDescription     = "GoDaddy API Key (deprecated, use token)."
	secretDescription  = "GoDaddy API Secret (deprecated, use token)."
	baseURLDescription = "GoDaddy Base Url(defaults to production)."
	deprecationMessage = "GoDaddy is sunsetting API key/secret pairs. Use token instead."
)

type godaddyProvider struct {
	version string
}

// New returns a constructor for the framework provider.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &godaddyProvider{version: version}
	}
}

type providerModel struct {
	Token   types.String `tfsdk:"token"`
	Key     types.String `tfsdk:"key"`
	Secret  types.String `tfsdk:"secret"`
	BaseURL types.String `tfsdk:"baseurl"`
}

func (p *godaddyProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "godaddy"
	resp.Version = p.version
}

func (p *godaddyProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: tokenDescription,
			},
			"key": schema.StringAttribute{
				Optional:           true,
				Sensitive:          true,
				Description:        keyDescription,
				DeprecationMessage: deprecationMessage,
			},
			"secret": schema.StringAttribute{
				Optional:           true,
				Sensitive:          true,
				Description:        secretDescription,
				DeprecationMessage: deprecationMessage,
			},
			"baseurl": schema.StringAttribute{
				Optional:    true,
				Description: baseURLDescription,
			},
		},
	}
}

// Settings is the provider configuration after environment variables and
// defaults have been applied.
type Settings struct {
	Credentials api.Credentials
	BaseURL     string
}

// resolve applies the same rules as the SDKv2 provider: a value set in the
// provider block wins; otherwise the environment variable is used; baseurl
// falls back to DefaultBaseURL. An unknown or null value counts as not set.
func resolve(m providerModel, getenv func(string) string) Settings {
	pick := func(v types.String, env string) string {
		if !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
			return v.ValueString()
		}
		return getenv(env)
	}
	s := Settings{
		Credentials: api.Credentials{
			Token:  pick(m.Token, "GODADDY_API_TOKEN"),
			Key:    pick(m.Key, "GODADDY_API_KEY"),
			Secret: pick(m.Secret, "GODADDY_API_SECRET"),
		},
		BaseURL: DefaultBaseURL,
	}
	if !m.BaseURL.IsNull() && !m.BaseURL.IsUnknown() && strings.TrimSpace(m.BaseURL.ValueString()) != "" {
		s.BaseURL = m.BaseURL.ValueString()
	}
	return s
}

// Configure builds the API client that resources in this provider use.
//
// It reports no error of its own when the credentials or base URL are bad.
// The SDKv2 provider is configured by the same call from Terraform and
// already reports that error; reporting it here too would show it twice.
// In that case no client is stored, and a resource that needs one must say
// so (see ClientFrom).
func (p *godaddyProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var m providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}

	settings := resolve(m, os.Getenv)
	client, err := api.NewClient(settings.BaseURL, settings.Credentials)
	if err != nil {
		return
	}
	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *godaddyProvider) Resources(_ context.Context) []func() resource.Resource {
	return nil
}

func (p *godaddyProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}
````
- [ ] **Step 5: Tidy and run everything**

```bash
go mod tidy
gofmt -l internal
go build ./... && go vet ./internal/... && go test -race -count=1 ./... 2>&1 | grep -v '^20[0-9][0-9]/'
```

Expected: `gofmt` prints nothing; `ok` for `api`, `internal/provider`, `plugin/godaddy` and `scripts/probe`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/provider/provider.go internal/provider/provider_test.go
git commit -m "feat: add the framework provider with the same configuration as the old one"
```

---

### Task 6: Serve both providers through the mux

`main.go` stops serving the SDKv2 provider directly and serves a mux of both halves instead, still on protocol 5, so Terraform and OpenTofu see one provider.

One behaviour needs handling. Mux requires `key` and `secret` to be marked deprecated in both halves, and then passes on the deprecation warnings from both, so a user who sets them would see four warnings instead of two. The framework half is wrapped so that its copies are dropped. Errors and any other warning pass through.

**Files:**
- Modify: `main.go`
- Test: `main_test.go`

- [ ] **Step 1: Add the dependencies**

```bash
go get github.com/hashicorp/terraform-plugin-mux@v0.23.1 github.com/hashicorp/terraform-plugin-go@v0.31.0
```

- [ ] **Step 2: Write the failing tests**

Create `main_test.go`:

````go
package main

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov5"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// terraform-plugin-mux compares the provider schemas of the two halves and
// reports any difference as an error diagnostic when the schema is first
// requested. This is the test that fails if a description, a sensitivity
// flag or a deprecation in internal/provider drifts from plugin/godaddy.
func TestMuxServesBothProviders(t *testing.T) {
	ctx := context.Background()
	server, err := muxServer(ctx)
	if err != nil {
		t.Fatalf("muxServer: %s", err)
	}

	resp, err := server().GetProviderSchema(ctx, &tfprotov5.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %s", err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov5.DiagnosticSeverityError {
			t.Errorf("schema error: %s: %s", d.Summary, d.Detail)
		}
	}

	if _, ok := resp.ResourceSchemas["godaddy_domain_record"]; !ok {
		t.Error("godaddy_domain_record is not served; the SDKv2 half is missing")
	}
	for _, name := range []string{"token", "key", "secret", "baseurl"} {
		found := false
		for _, a := range resp.Provider.Block.Attributes {
			if a.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("provider attribute %q is missing from the served schema", name)
		}
	}
}

// providerConfig builds the provider block Terraform would send. A nil
// entry means the attribute is not set.
func providerConfig(t *testing.T, values map[string]interface{}) *tfprotov5.DynamicValue {
	t.Helper()
	objectType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"token": tftypes.String, "key": tftypes.String, "secret": tftypes.String, "baseurl": tftypes.String,
	}}
	attrs := map[string]tftypes.Value{}
	for name := range objectType.AttributeTypes {
		attrs[name] = tftypes.NewValue(tftypes.String, values[name])
	}
	dv, err := tfprotov5.NewDynamicValue(objectType, tftypes.NewValue(objectType, attrs))
	if err != nil {
		t.Fatal(err)
	}
	return &dv
}

func clearCredentialEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GODADDY_API_TOKEN", "GODADDY_API_KEY", "GODADDY_API_SECRET"} {
		t.Setenv(k, "")
	}
}

// configure runs the two calls Terraform makes to set up a provider and
// returns every diagnostic from both.
func configure(t *testing.T, values map[string]interface{}) []*tfprotov5.Diagnostic {
	t.Helper()
	ctx := context.Background()
	server, err := muxServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s := server()
	if _, err := s.GetProviderSchema(ctx, &tfprotov5.GetProviderSchemaRequest{}); err != nil {
		t.Fatal(err)
	}
	config := providerConfig(t, values)

	prepared, err := s.PrepareProviderConfig(ctx, &tfprotov5.PrepareProviderConfigRequest{Config: config})
	if err != nil {
		t.Fatal(err)
	}
	configured, err := s.ConfigureProvider(ctx, &tfprotov5.ConfigureProviderRequest{Config: config})
	if err != nil {
		t.Fatal(err)
	}
	return append(prepared.Diagnostics, configured.Diagnostics...)
}

func count(diags []*tfprotov5.Diagnostic, severity tfprotov5.DiagnosticSeverity) int {
	n := 0
	for _, d := range diags {
		if d.Severity == severity {
			n++
		}
	}
	return n
}

func TestMuxConfiguresFromEnvironmentAlone(t *testing.T) {
	clearCredentialEnv(t)
	t.Setenv("GODADDY_API_TOKEN", "tok")

	diags := configure(t, map[string]interface{}{})

	for _, d := range diags {
		t.Errorf("unexpected diagnostic: %s: %s", d.Summary, d.Detail)
	}
}

// With no credentials at all the user must see the error once. The SDKv2
// half reports it; the framework half stays silent on purpose.
func TestMuxReportsMissingCredentialsOnce(t *testing.T) {
	clearCredentialEnv(t)

	diags := configure(t, map[string]interface{}{})

	if n := count(diags, tfprotov5.DiagnosticSeverityError); n != 1 {
		t.Errorf("got %d error diagnostics, want exactly 1: %v", n, diags)
	}
}

// key and secret are deprecated in both halves' schemas, because mux needs
// the schemas to match. This pins how many warnings the user sees for one
// deprecated pair, so a change in either half is noticed.
func TestMuxDeprecationWarningCount(t *testing.T) {
	clearCredentialEnv(t)

	diags := configure(t, map[string]interface{}{"key": "k", "secret": "s"})

	if n := count(diags, tfprotov5.DiagnosticSeverityError); n != 0 {
		t.Fatalf("unexpected errors: %v", diags)
	}
	warnings := count(diags, tfprotov5.DiagnosticSeverityWarning)
	t.Logf("deprecation warnings shown for key and secret: %d", warnings)
	for _, d := range diags {
		t.Logf("  %s: %s (attribute %v)", d.Summary, d.Detail, d.Attribute)
	}
	if warnings != 2 {
		t.Errorf("got %d warnings, want 2 (one for key, one for secret)", warnings)
	}
}
````
- [ ] **Step 3: Run them to see them fail**

Run: `go test -count=1 . 2>&1 | head -5`
Expected: the package fails to build, with `undefined: muxServer`.

- [ ] **Step 4: Serve the mux**

Replace the whole of `main.go` with:

````go
//go:generate go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov5"
	"github.com/hashicorp/terraform-plugin-go/tfprotov5/tf5server"
	"github.com/hashicorp/terraform-plugin-mux/tf5muxserver"
	"github.com/n3integration/terraform-provider-godaddy/internal/provider"
	"github.com/n3integration/terraform-provider-godaddy/plugin/godaddy"
)

// providerAddress is where the Terraform Registry serves this provider.
const providerAddress = "registry.terraform.io/jeffbeard/godaddy"

var (
	// these will be set by the goreleaser configuration
	// to appropriate values for the compiled binary
	version string = "dev"

	// goreleaser can also pass the specific commit if you want
	// commit  string = ""
)

// muxServer combines the two halves of the provider into one server:
// the SDKv2 provider, which holds the frozen godaddy_domain_record resource,
// and the framework provider, which holds the new resources.
func muxServer(ctx context.Context) (func() tfprotov5.ProviderServer, error) {
	framework := providerserver.NewProtocol5(provider.New(version)())
	mux, err := tf5muxserver.NewMuxServer(ctx,
		godaddy.Provider().GRPCProvider,
		func() tfprotov5.ProviderServer { return withoutDeprecationWarnings{framework()} },
	)
	if err != nil {
		return nil, err
	}
	return mux.ProviderServer, nil
}

// withoutDeprecationWarnings drops the "Attribute Deprecated" warnings the
// framework half raises for the provider block.
//
// terraform-plugin-mux requires both halves to mark key and secret as
// deprecated, and then passes on the warnings from both, so a user who sets
// them would see every warning twice. The SDKv2 half already reports them.
// Errors, and any other warning, pass through untouched.
type withoutDeprecationWarnings struct {
	tfprotov5.ProviderServer
}

func (w withoutDeprecationWarnings) PrepareProviderConfig(ctx context.Context, req *tfprotov5.PrepareProviderConfigRequest) (*tfprotov5.PrepareProviderConfigResponse, error) {
	resp, err := w.ProviderServer.PrepareProviderConfig(ctx, req)
	if resp == nil {
		return resp, err
	}
	kept := make([]*tfprotov5.Diagnostic, 0, len(resp.Diagnostics))
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov5.DiagnosticSeverityWarning && d.Summary == "Attribute Deprecated" {
			continue
		}
		kept = append(kept, d)
	}
	resp.Diagnostics = kept
	return resp, err
}

func main() {
	debug := flag.Bool("debug", false, "run the provider with support for debuggers such as delve")
	flag.Parse()

	ctx := context.Background()
	server, err := muxServer(ctx)
	if err != nil {
		log.Fatal(err)
	}

	var opts []tf5server.ServeOpt
	if *debug {
		opts = append(opts, tf5server.WithManagedDebug())
	}
	if err := tf5server.Serve(providerAddress, server, opts...); err != nil {
		log.Fatal(err)
	}
}
````
- [ ] **Step 5: Tidy and run everything**

```bash
go mod tidy
gofmt -l main.go main_test.go
go build ./... && go vet . && go test -race -count=1 ./... 2>&1 | grep -v '^20[0-9][0-9]/'
```

Expected: `gofmt` prints nothing; `ok` for the root package, `api`, `internal/provider`, `plugin/godaddy` and `scripts/probe`.

- [ ] **Step 6: Prove the schema check can fail**

In `internal/provider/provider.go`, temporarily delete the line `DeprecationMessage: deprecationMessage,` from the `"key"` attribute.
Run: `go test -count=1 . -run TestMuxServesBothProviders 2>&1 | grep -v '^20[0-9][0-9]/'`
Expected: FAIL with `schema error: Invalid Provider Server Combination: The combined provider has differing provider schema implementations across providers.`
Undo the change.

- [ ] **Step 7: Prove the warning filter is needed**

In `main.go`, in `muxServer`, temporarily change `return withoutDeprecationWarnings{framework()}` to `return framework()`.
Run: `go test -count=1 . -run TestMuxDeprecationWarningCount 2>&1 | grep -v '^20[0-9][0-9]/'`
Expected: FAIL with `got 4 warnings, want 2 (one for key, one for secret)`.
Undo the change.

Run: `go test -count=1 . 2>&1 | grep -v '^20[0-9][0-9]/'`
Expected: `ok`. Then `git diff --stat internal/provider/provider.go` must print nothing.

- [ ] **Step 8: Check the binary starts as a plugin**

```bash
go build -o /dev/null . && go run . ; echo "exit=$?"
```

Expected: the text `This binary is a plugin. These are not meant to be executed directly.` followed by `exit=0`. That message comes from the plugin library and shows the server wiring is intact.

- [ ] **Step 9: Confirm the protocol version is unchanged**

Run: `cat terraform-registry-manifest.json`
Expected: `"protocol_versions": ["5.0"]`. Do not change this file.

- [ ] **Step 10: Commit**

```bash
git add go.mod go.sum main.go main_test.go
git commit -m "feat: serve the old and new providers together through terraform-plugin-mux"
```

---

### Task 7: Final checks

- [ ] **Step 1: The module is tidy**

Run: `go mod tidy -diff`
Expected: no output and exit status 0. (CI runs this check.)

- [ ] **Step 2: The whole suite passes with the race detector**

Run: `go build ./... && go vet ./... && go test -race -count=1 ./... 2>&1 | grep -v '^20[0-9][0-9]/'`
Expected: `ok` for the root package, `api`, `internal/provider`, `plugin/godaddy` and `scripts/probe`.

- [ ] **Step 3: The frozen files are untouched**

Run: `git diff --stat master -- plugin/godaddy api/domains.go api/types.go api/credentials.go`
Expected: no output.

Run: `git diff master -- plugin/godaddy/resource_dns_record_requests_test.go plugin/godaddy/resource_dns_record_plan_test.go | wc -l`
Expected: `0`. The pinning tests passed without being edited.

- [ ] **Step 4: The release build still works**

If `goreleaser` is installed:

Run: `goreleaser release --snapshot --clean 2>&1 | tail -3`
Expected: ends with a success line. CI runs the same command. If `goreleaser` is not installed, skip this step and say so in the hand-back; CI will run it.

Afterwards run `git status --short`; if a `dist/` directory appears it is already ignored and must not be committed.

- [ ] **Step 5: Hand back**

Report: the commits made (`git log --oneline master..HEAD`), the output of Steps 1 to 3, whether Step 4 ran, and anything that differed from this plan. Do not push or open a pull request unless asked.

---

## Done when

- `go test -race -count=1 ./...` passes, including the pinning tests, unedited.
- `go mod tidy -diff` is clean.
- No file under `plugin/godaddy/` and none of `api/domains.go`, `api/types.go`, `api/credentials.go` has changed; in `api/client.go`, `execute` and `validate` are unchanged.
- The provider binary serves `godaddy_domain_record` through the mux on protocol 5, with the framework provider beside it holding no resources.
- A user who sets `key` and `secret` sees two deprecation warnings, and a user with no credentials sees one error.
- `api` offers `GetRecordGroup`, `ReplaceRecordGroup`, `DeleteRecordGroup`, `GetNameservers` and `SetNameservers`, with typed errors and retry, and none of them is used by the old resource.
