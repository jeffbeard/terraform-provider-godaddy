# Phase 0 Probes and Old-Resource Characterization Tests Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Pin the existing `godaddy_domain_record` resource's behaviour with tests, and build and run the live probes that answer the open questions about GoDaddy's API, so that the new resources are designed against facts.

**Architecture:** Two independent pieces. (1) Test-only files in `plugin/godaddy/` that drive the old resource against a fake HTTP server and assert its requests and its plans. (2) A small standalone Go command in `scripts/probe/` that sends fixed requests to GoDaddy, refuses any write outside a per-run record prefix, snapshots the zone before and after, and writes a Markdown transcript. No production code changes.

**Tech Stack:** Go (module declares `go 1.19`; do not use newer language or library features), standard library only, `terraform-plugin-sdk/v2` v2.6.0 as already vendored in `go.mod`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-11-26-safe-dns-resources-design.md`. This plan covers steps 1 and 2 of its "Order of work".

> **Note (2026-11-26):** the code blocks for `scripts/probe/` in Tasks 4 to 6, and the tests `TestDomainRecordRequests` and `TestDomainRecordImport` in Task 2, were revised after code review. The repository is the source of truth for those files; the commands in Tasks 8 to 11 are unchanged.

---

## Where this plan sits

The spec is implemented by three plans. Only this one can be written now, because the spec forbids writing resource code against unverified GoDaddy behaviour.

| Plan | Covers (spec "Order of work") | Status |
|---|---|---|
| 1 (this one) | Step 1 probes and behaviour document; step 2 characterization tests | Ready |
| 2 | Step 3 SDK bump, mux, framework provider skeleton, shared limiter, lint; step 4 client additions and retry | Write after this plan's Task 3 passes. Does not depend on probe results except P8 (retry format). |
| 3 | Steps 5 to 7: both resources, deprecation, documentation, migration guide, OpenTofu, release | Write after Task 11 of this plan, once the spec is amended with probe results. |

## Rules for whoever executes this

- **Do not change any non-test file under `api/` or `plugin/godaddy/`.** The old resource is frozen. If a test in Tasks 2 or 3 fails, the test is wrong or the environment differs; stop and report. Never edit an expected value to make it pass.
- **Tasks 8, 9 and 10 write to a live DNS zone and need the maintainer.** An agent must stop before each of those tasks, show the exact command, and wait for the maintainer to run it or approve it. Approval for one task does not cover the next.
- **Never print, log or commit the API token.**
- Commit after each task. Do not push or open a pull request unless the maintainer asks. Humans merge pull requests.
- Work on a branch created from `docs/safe-dns-resources-spec` (or from `master` if that branch has been merged).

## File structure

| File | Responsibility |
|---|---|
| `plugin/godaddy/fake_godaddy_test.go` | Fake GoDaddy HTTP server that records requests. Test helper only. |
| `plugin/godaddy/resource_dns_record_requests_test.go` | Pins the requests the old resource sends for create, update, delete, read and import, and what read puts in state. |
| `plugin/godaddy/resource_dns_record_plan_test.go` | Pins what the old resource plans for fixed state and configuration. |
| `scripts/probe/guard.go` | Pure safety logic: which writes are allowed, zone snapshot comparison, redaction of contact details. |
| `scripts/probe/runner.go` | Sends requests, enforces the guard, paces requests, writes the transcript, snapshots the zone. |
| `scripts/probe/probes.go` | The probes P1 to P12, one function each (P11 runs inside P6). |
| `scripts/probe/main.go` | Flags, environment, snapshot-before and check-after, interrupt handling. |
| `scripts/probe/*_test.go` | Tests for the three files above against fake servers. |
| `scripts/probe/README.md` | How to run the probes safely. |
| `docs/godaddy-api-behaviour.md` | What we know about GoDaddy's API: inherited knowledge and probe results. |
| `.gitignore` | Ignores `scripts/probe/out/`. |

---

### Task 1: Branch and ignore rule

**Files:**
- Modify: `.gitignore`

- [ ] **Step 1: Create the branch**

```bash
git checkout docs/safe-dns-resources-spec
git checkout -b feat/phase0-probes-and-characterization
```

Expected: `Switched to a new branch 'feat/phase0-probes-and-characterization'`

- [ ] **Step 2: Confirm the starting point is green**

Run: `go build ./... && go vet ./... && go test -race -count=1 ./...`
Expected: every package prints `ok` or `[no test files]`. If anything fails, stop and report; do not continue.

- [ ] **Step 3: Ignore probe transcripts**

Append to `.gitignore`:

```
# Raw probe transcripts. Results are written up in docs/godaddy-api-behaviour.md.
/scripts/probe/out/
```

- [ ] **Step 4: Commit**

```bash
git add .gitignore
git commit -m "chore: ignore probe transcripts"
```

---

### Task 2: Pin the old resource's requests

These tests describe behaviour that already exists, so they pass on the first run. That is expected here and is the one place this plan departs from write-a-failing-test-first. Step 4 proves the tests can fail.

Background for the reader: `godaddy_domain_record` sends one `PUT /v1/domains/{domain}/records/{TYPE}` per record type on every create and update, in random order, and sends an empty array for types with no configured records. Destroy replaces the zone with two default CNAMEs. The expected values below record exactly that. They look alarming on purpose; they are why the resource is being replaced.

The API client waits one second between requests, and each test case has its own client, so cases run in parallel to keep the suite near ten seconds.

**Files:**
- Create: `plugin/godaddy/fake_godaddy_test.go`
- Create: `plugin/godaddy/resource_dns_record_requests_test.go`

- [ ] **Step 1: Write the fake server**

Create `plugin/godaddy/fake_godaddy_test.go`:

````go
package godaddy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/n3integration/terraform-provider-godaddy/api"
)

// fakeGoDaddy is a stand-in for the GoDaddy API that records every request
// it receives. It exists to pin the exact requests godaddy_domain_record
// sends, so later changes to shared code cannot alter them unnoticed.
type fakeGoDaddy struct {
	mu       sync.Mutex
	requests []string
	// records is the JSON array returned for the first page of
	// GET /v1/domains/{domain}/records. Later pages are empty.
	records string
	server  *httptest.Server
}

// newFakeGoDaddy starts the fake and returns it with a client pointed at it.
// The server is closed when the test ends.
func newFakeGoDaddy(t *testing.T, recordsJSON string) (*fakeGoDaddy, *api.Client) {
	t.Helper()
	f := &fakeGoDaddy{records: recordsJSON}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)

	client, err := api.NewClient(f.server.URL, api.Credentials{Token: "tok"})
	if err != nil {
		t.Fatalf("NewClient: %s", err)
	}
	return f, client
}

func (f *fakeGoDaddy) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	line := r.Method + " " + r.URL.RequestURI()
	if len(body) > 0 {
		line += " " + string(body)
	}
	if shopper := r.Header.Get("X-Shopper-Id"); shopper != "" {
		line += " shopper=" + shopper
	}
	if auth := r.Header.Get("Authorization"); auth != "Bearer tok" {
		line += " UNEXPECTED-AUTH=" + auth
	}
	f.mu.Lock()
	f.requests = append(f.requests, line)
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/records"):
		if r.URL.Query().Get("offset") == "1" {
			io.WriteString(w, f.records)
		} else {
			io.WriteString(w, "[]")
		}
	case r.Method == http.MethodGet:
		io.WriteString(w, `{"domainId":1234,"domain":"example.org","status":"ACTIVE"}`)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// sorted returns the recorded requests in sorted order. The provider sends
// its per-type writes in Go map order, which is random, so tests compare the
// set of requests and not their sequence.
func (f *fakeGoDaddy) sorted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.requests...)
	sort.Strings(out)
	return out
}

// inOrder returns the recorded requests in the order they arrived.
func (f *fakeGoDaddy) inOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func assertRequests(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d requests, want %d\ngot:\n  %s\nwant:\n  %s",
			len(got), len(want), strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d\n got: %s\nwant: %s", i, got[i], want[i])
		}
	}
}
````
- [ ] **Step 2: Write the request tests**

Create `plugin/godaddy/resource_dns_record_requests_test.go`:

````go
package godaddy

import (
	"context"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// These tests pin the requests godaddy_domain_record sends today. The
// resource is frozen in 1.x: if one of these tests fails, the change under
// test has altered what existing users' applies do. Do not update the
// expected values to make a failure go away.

const (
	base = "/v1/domains/example.org"

	nsBody = `[{"type":"NS","name":"@","data":"ns1.example.net","priority":0,"ttl":3600,"weight":0},` +
		`{"type":"NS","name":"@","data":"ns2.example.net","priority":0,"ttl":3600,"weight":0}]`

	defaultCNAMEBody = `[{"type":"CNAME","name":"www","data":"@","priority":0,"ttl":3600,"weight":0},` +
		`{"type":"CNAME","name":"_domainconnect","data":"_domainconnect.gd.domaincontrol.com","priority":0,"ttl":3600,"weight":0}]`
)

// fullConfig uses every attribute. It includes a CAA record on purpose: the
// provider accepts it and never sends it.
func fullConfig() map[string]interface{} {
	return map[string]interface{}{
		"domain":      "example.org",
		"addresses":   []interface{}{"192.0.2.1"},
		"nameservers": []interface{}{"ns1.example.net", "ns2.example.net"},
		"record": []interface{}{
			map[string]interface{}{"name": "www", "type": "CNAME", "data": "example.github.io", "ttl": 600},
			map[string]interface{}{"name": "@", "type": "MX", "data": "mx1.example.net", "priority": 10},
			map[string]interface{}{"name": "@", "type": "TXT", "data": "v=spf1 -all"},
			map[string]interface{}{"name": "@", "type": "CAA", "data": `0 issue "letsencrypt.org"`},
		},
	}
}

// nameserversOnlyConfig is the configuration that prompted the 2026-11-26
// audit: it looks like "only change the nameservers".
func nameserversOnlyConfig() map[string]interface{} {
	return map[string]interface{}{
		"domain":      "example.org",
		"nameservers": []interface{}{"ns1.example.net", "ns2.example.net"},
	}
}

func resellerConfig() map[string]interface{} {
	return map[string]interface{}{
		"domain":   "example.org",
		"customer": "5678",
		"record": []interface{}{
			map[string]interface{}{"name": "@", "type": "TXT", "data": "hello"},
		},
	}
}

var fullWrites = []string{
	"GET " + base,
	"PUT " + base + `/records/A [{"type":"A","name":"@","data":"192.0.2.1","priority":0,"ttl":3600,"weight":0}]`,
	"PUT " + base + "/records/AAAA []",
	"PUT " + base + `/records/CNAME [{"type":"CNAME","name":"www","data":"example.github.io","priority":0,"ttl":600,"weight":0}]`,
	"PUT " + base + `/records/MX [{"type":"MX","name":"@","data":"mx1.example.net","priority":10,"ttl":3600,"weight":0}]`,
	"PUT " + base + "/records/NS " + nsBody,
	"PUT " + base + "/records/SRV []",
	"PUT " + base + `/records/TXT [{"type":"TXT","name":"@","data":"v=spf1 -all","priority":0,"ttl":3600,"weight":0}]`,
}

var nameserversOnlyWrites = []string{
	"GET " + base,
	"PUT " + base + "/records/A []",
	"PUT " + base + "/records/AAAA []",
	"PUT " + base + "/records/CNAME []",
	"PUT " + base + "/records/MX []",
	"PUT " + base + "/records/NS " + nsBody,
	"PUT " + base + "/records/SRV []",
	"PUT " + base + "/records/TXT []",
}

var resellerWrites = []string{
	"GET " + base + " shopper=5678",
	"PUT " + base + "/records/A [] shopper=5678",
	"PUT " + base + "/records/AAAA [] shopper=5678",
	"PUT " + base + "/records/CNAME [] shopper=5678",
	"PUT " + base + "/records/MX [] shopper=5678",
	"PUT " + base + "/records/SRV [] shopper=5678",
	"PUT " + base + `/records/TXT [{"type":"TXT","name":"@","data":"hello","priority":0,"ttl":3600,"weight":0}] shopper=5678`,
}

// restoreWrites is what destroy sends, whatever the configuration was.
var restoreWrites = []string{
	"GET " + base,
	"PUT " + base + "/records/A []",
	"PUT " + base + "/records/AAAA []",
	"PUT " + base + "/records/CNAME " + defaultCNAMEBody,
	"PUT " + base + "/records/MX []",
	"PUT " + base + "/records/SRV []",
	"PUT " + base + "/records/TXT []",
}

type crudFunc func(context.Context, *schema.ResourceData, interface{}) diag.Diagnostics

func TestDomainRecordRequests(t *testing.T) {
	cases := []struct {
		name   string
		op     crudFunc
		config map[string]interface{}
		want   []string
	}{
		{"create full", resourceDomainRecordCreate, fullConfig(), fullWrites},
		{"update full", resourceDomainRecordUpdate, fullConfig(), fullWrites},
		{"create nameservers only", resourceDomainRecordCreate, nameserversOnlyConfig(), nameserversOnlyWrites},
		{"update nameservers only", resourceDomainRecordUpdate, nameserversOnlyConfig(), nameserversOnlyWrites},
		{"create reseller without nameservers", resourceDomainRecordCreate, resellerConfig(), resellerWrites},
		{"delete full", resourceDomainRecordRestore, fullConfig(), restoreWrites},
		{"delete nameservers only", resourceDomainRecordRestore, nameserversOnlyConfig(), restoreWrites},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Each case has its own client, so its own one-request-per-second
			// throttle. Running in parallel keeps the suite under ten seconds.
			t.Parallel()
			fake, client := newFakeGoDaddy(t, "[]")
			d := schema.TestResourceDataRaw(t, resourceDomainRecord().Schema, tc.config)

			if diags := tc.op(context.Background(), d, client); diags.HasError() {
				t.Fatalf("unexpected error: %v", diags)
			}

			assertRequests(t, fake.sorted(), tc.want)
			if d.Id() != "1234" {
				t.Errorf("resource ID = %q, want the domainId %q", d.Id(), "1234")
			}
		})
	}
}

// zoneJSON is what the fake returns for the first page of records. It mixes
// records the provider classifies as addresses, as nameservers, and as
// plain record blocks.
const zoneJSON = `[
 {"type":"A","name":"@","data":"192.0.2.1","ttl":3600},
 {"type":"A","name":"@","data":"192.0.2.9","ttl":600},
 {"type":"NS","name":"@","data":"ns1.example.net","ttl":3600},
 {"type":"NS","name":"@","data":"ns2.example.net","ttl":3600},
 {"type":"NS","name":"sub","data":"ns1.other.net","ttl":3600},
 {"type":"SOA","name":"@","data":"Primary nameserver: ns1.example.net","ttl":3600},
 {"type":"MX","name":"@","data":"mx1.example.net","priority":10,"ttl":3600},
 {"type":"CNAME","name":"www","data":"@","ttl":3600}
]`

var readRequests = []string{
	"GET " + base + "/records?limit=500&offset=1",
	"GET " + base + "/records?limit=500&offset=2",
	"GET " + base,
}

// recordSummaries reduces the record set to sortable strings so the test
// does not depend on set hash order.
func recordSummaries(d *schema.ResourceData) map[string]bool {
	out := map[string]bool{}
	for _, raw := range d.Get("record").(*schema.Set).List() {
		r := raw.(map[string]interface{})
		key := r["type"].(string) + " " + r["name"].(string) + " " + r["data"].(string)
		out[key] = true
	}
	return out
}

func TestDomainRecordRead(t *testing.T) {
	t.Parallel()
	fake, client := newFakeGoDaddy(t, zoneJSON)
	d := schema.TestResourceDataRaw(t, resourceDomainRecord().Schema, nameserversOnlyConfig())

	if diags := resourceDomainRecordRead(context.Background(), d, client); diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	assertRequests(t, fake.inOrder(), readRequests)

	// Apex A records with TTL 3600 become addresses; the one with TTL 600
	// stays a record block.
	if got, want := d.Get("addresses"), []interface{}{"192.0.2.1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("addresses = %v, want %v", got, want)
	}
	// Apex NS records with TTL 3600 become nameservers.
	if got, want := d.Get("nameservers"), []interface{}{"ns1.example.net", "ns2.example.net"}; !reflect.DeepEqual(got, want) {
		t.Errorf("nameservers = %v, want %v", got, want)
	}
	// Everything else, including SOA and the subdomain NS record, lands in
	// state as record blocks even though none of it is in the configuration.
	want := map[string]bool{
		"A @ 192.0.2.9":                             true,
		"NS sub ns1.other.net":                      true,
		"SOA @ Primary nameserver: ns1.example.net": true,
		"MX @ mx1.example.net":                      true,
		"CNAME www @":                               true,
	}
	if got := recordSummaries(d); !reflect.DeepEqual(got, want) {
		t.Errorf("record blocks = %v, want %v", got, want)
	}
	if d.Id() != "1234" {
		t.Errorf("resource ID = %q, want %q", d.Id(), "1234")
	}
}

// Import calls read with the domain name as the ID and nothing else set.
func TestDomainRecordImport(t *testing.T) {
	t.Parallel()
	fake, client := newFakeGoDaddy(t, zoneJSON)
	d := resourceDomainRecord().TestResourceData()
	d.SetId("example.org")

	if diags := resourceDomainRecordRead(context.Background(), d, client); diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	assertRequests(t, fake.inOrder(), readRequests)
	if got := d.Get("domain"); got != "example.org" {
		t.Errorf("domain = %v, want example.org", got)
	}
	if got, want := d.Get("nameservers"), []interface{}{"ns1.example.net", "ns2.example.net"}; !reflect.DeepEqual(got, want) {
		t.Errorf("nameservers = %v, want %v", got, want)
	}
	if got := len(recordSummaries(d)); got != 5 {
		t.Errorf("imported %d record blocks, want 5", got)
	}
	if d.Id() != "1234" {
		t.Errorf("resource ID = %q, want %q", d.Id(), "1234")
	}
}
````
- [ ] **Step 3: Run the tests**

Run: `go test ./plugin/godaddy -race -count=1 -run 'TestDomainRecord(Requests|Read|Import)' -v 2>&1 | grep -E '^(=== RUN|--- |PASS|FAIL|ok)'`
Expected, in about 10 seconds:

```
--- PASS: TestDomainRecordRequests (0.00s)
--- PASS: TestDomainRecordRead (2.00s)
--- PASS: TestDomainRecordImport (2.00s)
PASS
ok  	github.com/n3integration/terraform-provider-godaddy/plugin/godaddy	10.043s
```

The `grep` hides the provider's own `log.Println` output and the indented subtest lines.

If a test fails, stop and report the failure. Do not edit expected values.

- [ ] **Step 4: Prove the tests can fail**

In `resource_dns_record_requests_test.go`, in `restoreWrites`, temporarily change the line
`"PUT " + base + "/records/MX []",` to `"PUT " + base + "/records/MX [{}]",`. The same line also appears in `nameserversOnlyWrites`; change only the one in `restoreWrites`.

Run: `go test ./plugin/godaddy -count=1 -run 'TestDomainRecordRequests/delete'`
Expected: FAIL for both delete subtests, each with the lines ` got: PUT /v1/domains/example.org/records/MX []` and `want: PUT /v1/domains/example.org/records/MX [{}]`.

Undo the change. Run the Step 3 command again. Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add plugin/godaddy/fake_godaddy_test.go plugin/godaddy/resource_dns_record_requests_test.go
git commit -m "test: pin the requests godaddy_domain_record sends"
```

---

### Task 3: Pin the old resource's plans

An upgrade of the Terraform SDK can change what a resource plans without changing the requests it sends for a given plan. These tests catch that. They call the resource's `Diff` directly with fixed state and configuration; no HTTP is involved, so they run in milliseconds.

The SDK stores a set element under a numeric hash, for example `record.2347713523.name`. The helper replaces the hash with `#` so expected values do not depend on it.

**Files:**
- Create: `plugin/godaddy/resource_dns_record_plan_test.go`

- [ ] **Step 1: Write the plan tests**

Create `plugin/godaddy/resource_dns_record_plan_test.go`. It uses `fullConfig` and `nameserversOnlyConfig` from Task 2's file, which is in the same package.

````go
package godaddy

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// These tests pin what godaddy_domain_record plans for fixed state and
// configuration. The request tests cannot see a change in planning: an SDK
// upgrade can alter the plan without altering the requests sent for a given
// plan. Do not update the expected values to make a failure go away.

// planFor returns the planned changes as sorted "attribute: old => new"
// lines, with " (forces replacement)" appended where the SDK marks it.
// Set elements are keyed by a hash in the SDK's flat form, so the hash is
// replaced by "#" and each line carries its values instead.
func planFor(t *testing.T, state *terraform.InstanceState, config map[string]interface{}) []string {
	t.Helper()
	diff, err := resourceDomainRecord().Diff(context.Background(), state,
		terraform.NewResourceConfigRaw(config), nil)
	if err != nil {
		t.Fatalf("Diff: %s", err)
	}
	if diff == nil {
		return nil
	}
	var out []string
	for key, attr := range diff.Attributes {
		line := fmt.Sprintf("%s: %q => %q", stripSetHash(key), attr.Old, attr.New)
		if attr.NewRemoved {
			line += " (removed)"
		}
		if attr.RequiresNew {
			line += " (forces replacement)"
		}
		out = append(out, line)
	}
	sort.Strings(out)
	return out
}

// stripSetHash turns "record.123456.name" into "record.#.name".
func stripSetHash(key string) string {
	const prefix = "record."
	if len(key) <= len(prefix) || key[:len(prefix)] != prefix {
		return key
	}
	rest := key[len(prefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '.' {
			return prefix + "#" + rest[i:]
		}
		if rest[i] < '0' || rest[i] > '9' {
			return key
		}
	}
	return key
}

// stateAfterCreate returns the state Terraform would hold after creating
// the resource from config.
func stateAfterCreate(t *testing.T, config map[string]interface{}) *terraform.InstanceState {
	t.Helper()
	d := schema.TestResourceDataRaw(t, resourceDomainRecord().Schema, config)
	d.SetId("1234")
	return d.State()
}

func oneRecordConfig() map[string]interface{} {
	return map[string]interface{}{
		"domain":    "example.org",
		"addresses": []interface{}{"192.0.2.1"},
		"record": []interface{}{
			map[string]interface{}{"name": "@", "type": "TXT", "data": "hello"},
		},
	}
}

func TestDomainRecordPlan(t *testing.T) {
	domainOnly := map[string]interface{}{"domain": "example.org"}

	otherDomain := nameserversOnlyConfig()
	otherDomain["domain"] = "example.com"

	withCustomer := nameserversOnlyConfig()
	withCustomer["customer"] = "5678"

	cases := []struct {
		name   string
		state  *terraform.InstanceState
		config map[string]interface{}
		want   []string
	}{
		{
			// A new resource plans only additions. Nothing in the plan
			// shows that existing records at GoDaddy will be replaced.
			name:   "create with nameservers only",
			state:  nil,
			config: nameserversOnlyConfig(),
			want: []string{
				`domain: "" => "example.org"`,
				`nameservers.#: "0" => "2"`,
				`nameservers.0: "" => "ns1.example.net"`,
				`nameservers.1: "" => "ns2.example.net"`,
			},
		},
		{
			name:   "create applies record defaults",
			state:  nil,
			config: oneRecordConfig(),
			want: []string{
				`addresses.#: "0" => "1"`,
				`addresses.0: "" => "192.0.2.1"`,
				`domain: "" => "example.org"`,
				`record.#.data: "" => "hello"`,
				`record.#.name: "" => "@"`,
				`record.#.port: "" => "0"`,
				`record.#.priority: "" => "0"`,
				`record.#.protocol: "" => ""`,
				`record.#.service: "" => ""`,
				`record.#.ttl: "" => "3600"`,
				`record.#.type: "" => "TXT"`,
				`record.#.weight: "" => "0"`,
				`record.#: "0" => "1"`,
			},
		},
		{
			name:   "state equal to configuration plans nothing",
			state:  stateAfterCreate(t, fullConfig()),
			config: fullConfig(),
			want:   nil,
		},
		{
			// Anything in state that is not in configuration is planned
			// for removal. After a refresh, state holds the whole zone.
			name:   "records and addresses absent from configuration are removed",
			state:  stateAfterCreate(t, oneRecordConfig()),
			config: domainOnly,
			want: []string{
				`addresses.#: "1" => "0"`,
				`addresses.0: "192.0.2.1" => "" (removed)`,
				`record.#.data: "hello" => "" (removed)`,
				`record.#.name: "@" => "" (removed)`,
				`record.#.port: "0" => "0" (removed)`,
				`record.#.priority: "0" => "0" (removed)`,
				`record.#.protocol: "" => "" (removed)`,
				`record.#.service: "" => "" (removed)`,
				`record.#.ttl: "3600" => "0" (removed)`,
				`record.#.type: "TXT" => "" (removed)`,
				`record.#.weight: "0" => "0" (removed)`,
				`record.#: "1" => "0"`,
			},
		},
		{
			// Audit finding F10: changing the domain is an in-place update,
			// not a replacement.
			name:   "changing domain does not force replacement",
			state:  stateAfterCreate(t, nameserversOnlyConfig()),
			config: otherDomain,
			want:   []string{`domain: "example.org" => "example.com"`},
		},
		{
			name:   "changing customer does not force replacement",
			state:  stateAfterCreate(t, nameserversOnlyConfig()),
			config: withCustomer,
			want:   []string{`customer: "" => "5678"`},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := planFor(t, tc.state, tc.config)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("plan differs\n got:\n  %s\nwant:\n  %s",
					strings.Join(got, "\n  "), strings.Join(tc.want, "\n  "))
			}
		})
	}
}
````
- [ ] **Step 2: Run the tests**

Run: `go test ./plugin/godaddy -race -count=1 -run TestDomainRecordPlan -v 2>&1 | grep -E '^(\s*--- |PASS|FAIL|ok)'`
Expected: `--- PASS: TestDomainRecordPlan`, six indented `--- PASS` subtest lines, `PASS`, then `ok`.

If a test fails, stop and report. Do not edit expected values.

- [ ] **Step 3: Prove the tests can fail**

In the case `"changing domain does not force replacement"`, temporarily add ` (forces replacement)` to the end of the expected line, inside the backquotes, so it reads:

```go
			want:   []string{`domain: "example.org" => "example.com" (forces replacement)`},
```

Run: `go test ./plugin/godaddy -count=1 -run TestDomainRecordPlan`
Expected: FAIL with `plan differs`.

Undo the change. Run the Step 2 command again. Expected: `ok`.

- [ ] **Step 4: Run the whole suite**

Run: `go build ./... && go vet ./... && go test -race -count=1 ./...`
Expected: all `ok`.

- [ ] **Step 5: Commit**

```bash
git add plugin/godaddy/resource_dns_record_plan_test.go
git commit -m "test: pin what godaddy_domain_record plans"
```

After this task, plan 2 (SDK bump and mux) can start in parallel with the rest of this plan.

---

### Task 4: Probe safety guard

The probe tool writes to a live zone. Everything that keeps it from damaging that zone is in one file of pure functions, written and tested first.

**Files:**
- Create: `scripts/probe/guard.go`
- Test: `scripts/probe/guard_test.go`

- [ ] **Step 1: Write the failing tests**

Create `scripts/probe/guard_test.go`:

````go
package main

import (
	"reflect"
	"testing"
)

func TestCheckWrite(t *testing.T) {
	const domain, prefix = "example.org", "_probe-ab12-"
	cases := []struct {
		name      string
		method    string
		path      string
		zoneWide  bool
		wantAllow bool
	}{
		{"read of anything", "GET", "/v1/domains/other.org/records", false, true},
		{"put probe group", "PUT", "/v1/domains/example.org/records/TXT/_probe-ab12-a", false, true},
		{"delete probe group", "DELETE", "/v1/domains/example.org/records/TXT/_probe-ab12-a", false, true},
		{"put real group", "PUT", "/v1/domains/example.org/records/MX/@", false, false},
		{"put real group even with zone-wide", "PUT", "/v1/domains/example.org/records/MX/@", true, false},
		{"delete www", "DELETE", "/v1/domains/example.org/records/CNAME/www", false, false},
		{"other run's prefix", "PUT", "/v1/domains/example.org/records/TXT/_probe-ffff-a", false, false},
		{"type-wide without flag", "PUT", "/v1/domains/example.org/records/AAAA", false, false},
		{"type-wide with flag", "PUT", "/v1/domains/example.org/records/AAAA", true, true},
		{"type-wide delete with flag", "DELETE", "/v1/domains/example.org/records/AAAA", true, false},
		{"whole zone put", "PUT", "/v1/domains/example.org/records", true, false},
		{"whole zone patch", "PATCH", "/v1/domains/example.org/records", true, false},
		{"nameservers without flag", "PATCH", "/v1/domains/example.org", false, false},
		{"nameservers with flag", "PATCH", "/v1/domains/example.org", true, true},
		{"delete the domain", "DELETE", "/v1/domains/example.org", true, false},
		{"another domain", "PUT", "/v1/domains/other.org/records/TXT/_probe-ab12-a", true, false},
		{"domain name as prefix of another", "PUT", "/v1/domains/example.organic/records/TXT/_probe-ab12-a", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkWrite(tc.method, tc.path, domain, prefix, tc.zoneWide)
			if allowed := err == nil; allowed != tc.wantAllow {
				t.Errorf("checkWrite(%s %s, zoneWide=%v) allowed=%v, want %v (err: %v)",
					tc.method, tc.path, tc.zoneWide, allowed, tc.wantAllow, err)
			}
		})
	}
}

func TestDiffOutside(t *testing.T) {
	const prefix = "_probe-ab12-"
	mx := zoneRecord{Type: "MX", Name: "@", Data: "mx1.example.net", TTL: 3600, Priority: 10}
	txt := zoneRecord{Type: "TXT", Name: "@", Data: "v=spf1 -all", TTL: 3600}
	probe := zoneRecord{Type: "TXT", Name: "_probe-ab12-a", Data: "x", TTL: 3600}

	cases := []struct {
		name          string
		before, after []zoneRecord
		want          []string
	}{
		{"identical", []zoneRecord{mx, txt}, []zoneRecord{txt, mx}, nil},
		{"probe record left behind is ignored", []zoneRecord{mx}, []zoneRecord{mx, probe}, nil},
		{"record lost", []zoneRecord{mx, txt}, []zoneRecord{txt},
			[]string{`- MX @ "mx1.example.net" ttl=3600 priority=10`}},
		{"record added", []zoneRecord{mx}, []zoneRecord{mx, txt},
			[]string{`+ TXT @ "v=spf1 -all" ttl=3600 priority=0`}},
		{"ttl changed shows as remove and add", []zoneRecord{txt},
			[]zoneRecord{{Type: "TXT", Name: "@", Data: "v=spf1 -all", TTL: 600}},
			[]string{`+ TXT @ "v=spf1 -all" ttl=600 priority=0`, `- TXT @ "v=spf1 -all" ttl=3600 priority=0`}},
		{"one of two duplicates lost", []zoneRecord{mx, mx}, []zoneRecord{mx},
			[]string{`- MX @ "mx1.example.net" ttl=3600 priority=10`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := diffOutside(tc.before, tc.after, prefix); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRedactDomainDetail(t *testing.T) {
	in := []byte(`{"domain":"example.org","status":"ACTIVE","nameServers":["ns1.x","ns2.x"],` +
		`"locked":true,"domainId":42,"contactRegistrant":{"email":"someone@example.org","nameFirst":"A"}}`)
	got := string(redactDomainDetail(in))
	want := `{"domain":"example.org","locked":true,"nameServers":["ns1.x","ns2.x"],"status":"ACTIVE"}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}

	for _, passthrough := range []string{``, `[]`, `{"code":"NOT_FOUND","message":"x"}`, `not json`} {
		if got := string(redactDomainDetail([]byte(passthrough))); got != passthrough {
			t.Errorf("redactDomainDetail(%q) = %q, want it unchanged", passthrough, got)
		}
	}
}
````
- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./scripts/probe -count=1`
Expected: FAIL to build, with `undefined: checkWrite`, `undefined: zoneRecord`, `undefined: diffOutside`, `undefined: redactDomainDetail`.

- [ ] **Step 3: Write the guard**

Create `scripts/probe/guard.go`:

````go
package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// checkWrite decides whether the probe tool may send a request. It is the
// only thing standing between a probe and a live zone, so it allows a short
// list of shapes and refuses everything else.
//
//   - GET is always allowed.
//   - A write must target the probe domain.
//   - A write to /records/{type}/{name} is allowed only when {name} starts
//     with the probe prefix.
//   - A write to /records/{type} (replaces every record of a type) or to the
//     domain itself (nameservers) needs allowZoneWide.
//   - A write to /records (replaces the whole zone) is never allowed.
func checkWrite(method, path, domain, prefix string, allowZoneWide bool) error {
	if method == "GET" {
		return nil
	}
	root := "/v1/domains/" + domain
	if path != root && !strings.HasPrefix(path, root+"/") {
		return fmt.Errorf("refusing %s %s: not under %s", method, path, root)
	}
	rest := strings.TrimPrefix(path, root)
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	switch {
	case rest == "":
		if method == "PATCH" && allowZoneWide {
			return nil
		}
		return fmt.Errorf("refusing %s %s: changing the domain needs -allow-zone-wide", method, path)
	case len(parts) == 3 && parts[0] == "records":
		if strings.HasPrefix(parts[2], prefix) {
			return nil
		}
		return fmt.Errorf("refusing %s %s: name %q does not start with %q", method, path, parts[2], prefix)
	case len(parts) == 2 && parts[0] == "records":
		if method == "PUT" && allowZoneWide {
			return nil
		}
		return fmt.Errorf("refusing %s %s: a type-wide write needs -allow-zone-wide", method, path)
	default:
		return fmt.Errorf("refusing %s %s: not a request shape the probe tool sends", method, path)
	}
}

// zoneRecord is one DNS record as GoDaddy returns it.
type zoneRecord struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Data     string `json:"data"`
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority"`
}

func (z zoneRecord) String() string {
	return fmt.Sprintf("%s %s %q ttl=%d priority=%d", z.Type, z.Name, z.Data, z.TTL, z.Priority)
}

// diffOutside compares two snapshots of a zone and returns one line per
// record that was added or removed, ignoring records whose name starts with
// the probe prefix. An empty result means the probe left the rest of the
// zone alone.
func diffOutside(before, after []zoneRecord, prefix string) []string {
	count := map[string]int{}
	for _, z := range before {
		if !strings.HasPrefix(z.Name, prefix) {
			count[z.String()]--
		}
	}
	for _, z := range after {
		if !strings.HasPrefix(z.Name, prefix) {
			count[z.String()]++
		}
	}
	var out []string
	for rec, n := range count {
		switch {
		case n < 0:
			out = append(out, "- "+rec)
		case n > 0:
			out = append(out, "+ "+rec)
		}
	}
	sort.Strings(out)
	return out
}

// domainDetailKeys are the only fields of GET /v1/domains/{domain} that the
// transcript keeps. The full response holds the registrant's name, address,
// phone number and email, which must not reach a file that may be committed.
var domainDetailKeys = map[string]bool{
	"domain":      true,
	"status":      true,
	"nameServers": true,
	"locked":      true,
}

// redactDomainDetail drops every field not in domainDetailKeys. Input that
// is not a JSON object (an error body, an empty body) is returned unchanged.
func redactDomainDetail(body []byte) []byte {
	var full map[string]json.RawMessage
	if err := json.Unmarshal(body, &full); err != nil {
		return body
	}
	if _, isDetail := full["domain"]; !isDetail {
		return body
	}
	kept := map[string]json.RawMessage{}
	for k, v := range full {
		if domainDetailKeys[k] {
			kept[k] = v
		}
	}
	out, err := json.Marshal(kept)
	if err != nil {
		return []byte(`{"redaction":"failed"}`)
	}
	return out
}
````
- [ ] **Step 4: Run the tests to see them pass**

Run: `go test ./scripts/probe -count=1`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add scripts/probe/guard.go scripts/probe/guard_test.go
git commit -m "feat(probe): add write guard, zone diff and redaction"
```

---

### Task 5: Probe runner

The runner sends one request at a time, at most one per second, asks the guard before every request, and writes each exchange to the transcript. The token goes only into the `Authorization` header.

**Files:**
- Create: `scripts/probe/runner.go`
- Test: `scripts/probe/runner_test.go`

- [ ] **Step 1: Write the failing tests**

Create `scripts/probe/runner_test.go`:

````go
package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAPI records the requests it receives and answers with canned bodies.
type fakeAPI struct {
	mu       sync.Mutex
	requests []string
	bodies   map[string]string // "METHOD path" -> response body
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	key := req.Method + " " + req.URL.RequestURI()
	f.mu.Lock()
	f.requests = append(f.requests, strings.TrimSpace(key+" "+string(body)))
	answer, ok := f.bodies[key]
	f.mu.Unlock()
	if !ok {
		answer = "[]"
	}
	io.WriteString(w, answer)
}

func newTestRunner(t *testing.T, api *fakeAPI) (*runner, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	out := &bytes.Buffer{}
	return &runner{
		base:   srv.URL,
		token:  "secret-token-value",
		domain: "example.org",
		prefix: "_probe-ab12-",
		out:    out,
		hc:     srv.Client(),
	}, out
}

func TestRunnerNeverWritesTheToken(t *testing.T) {
	r, out := newTestRunner(t, &fakeAPI{})
	r.put("write a group", "TXT", r.name("a"), []map[string]interface{}{{"data": "x", "ttl": 3600}})
	r.do("read it", "GET", r.group("TXT", r.name("a")), nil)

	if strings.Contains(out.String(), "secret-token-value") {
		t.Fatalf("transcript contains the token:\n%s", out)
	}
	if !strings.Contains(out.String(), "`PUT /v1/domains/example.org/records/TXT/_probe-ab12-a`") {
		t.Errorf("transcript does not show the request:\n%s", out)
	}
}

func TestRunnerRefusedWriteSendsNothing(t *testing.T) {
	api := &fakeAPI{}
	r, out := newTestRunner(t, api)

	resp := r.do("overwrite real MX", "PUT", r.group("MX", "@"), []map[string]interface{}{{"data": "x"}})

	if len(api.requests) != 0 {
		t.Fatalf("a refused write reached the server: %v", api.requests)
	}
	if resp.Status != 0 {
		t.Errorf("status = %d, want 0 for a request that was not sent", resp.Status)
	}
	if !strings.Contains(out.String(), "NOT SENT") {
		t.Errorf("transcript does not say the request was refused:\n%s", out)
	}
}

func TestRunnerRedactsDomainDetail(t *testing.T) {
	api := &fakeAPI{bodies: map[string]string{
		"GET /v1/domains/example.org": `{"domain":"example.org","status":"ACTIVE",` +
			`"nameServers":["ns1.x","ns2.x"],"contactRegistrant":{"email":"someone@example.org"}}`,
	}}
	r, out := newTestRunner(t, api)

	resp := r.do("domain detail", "GET", "/v1/domains/example.org", nil)

	if strings.Contains(out.String(), "someone@example.org") || strings.Contains(string(resp.Body), "someone@example.org") {
		t.Fatalf("contact details were not removed:\n%s", out)
	}
	if !strings.Contains(out.String(), `"nameServers":["ns1.x","ns2.x"]`) {
		t.Errorf("nameServers missing from transcript:\n%s", out)
	}
}

func TestRunnerCleanupDeletesEachCreatedGroupOnce(t *testing.T) {
	api := &fakeAPI{}
	r, _ := newTestRunner(t, api)
	r.put("first", "TXT", r.name("a"), []map[string]interface{}{{"data": "1"}})
	r.put("again", "TXT", r.name("a"), []map[string]interface{}{{"data": "2"}})
	r.put("other", "A", r.name("b"), []map[string]interface{}{{"data": "192.0.2.1"}})
	api.requests = nil

	r.cleanup()

	want := []string{
		"DELETE /v1/domains/example.org/records/TXT/_probe-ab12-a",
		"DELETE /v1/domains/example.org/records/A/_probe-ab12-b",
	}
	if strings.Join(api.requests, "\n") != strings.Join(want, "\n") {
		t.Errorf("cleanup sent\n  %s\nwant\n  %s", strings.Join(api.requests, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestSnapshotReadsEveryTypeAndFillsInType(t *testing.T) {
	api := &fakeAPI{bodies: map[string]string{
		"GET /v1/domains/example.org/records/MX":  `[{"name":"@","data":"mx1.example.net","ttl":3600,"priority":10}]`,
		"GET /v1/domains/example.org/records/TXT": `[{"type":"TXT","name":"@","data":"v=spf1 -all","ttl":3600}]`,
	}}
	r, out := newTestRunner(t, api)

	zone, err := r.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(api.requests) != len(recordTypes) {
		t.Errorf("snapshot sent %d requests, want one per record type (%d)", len(api.requests), len(recordTypes))
	}
	if len(zone) != 2 || zone[0].Type != "MX" || zone[1].Type != "TXT" {
		t.Errorf("snapshot = %v", zone)
	}
	if out.Len() != 0 {
		t.Errorf("snapshot wrote to the transcript:\n%s", out)
	}
}
````
- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./scripts/probe -count=1`
Expected: FAIL to build, with `undefined: runner` and `undefined: recordTypes`.

- [ ] **Step 3: Write the runner**

Create `scripts/probe/runner.go`:

````go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// recordTypes are the types GoDaddy's DNS API knows.
var recordTypes = []string{"A", "AAAA", "CAA", "CNAME", "MX", "NS", "SOA", "SRV", "TXT"}

// runner sends requests to GoDaddy and writes a Markdown transcript of each.
type runner struct {
	base          string        // for example https://api.godaddy.com
	token         string        // never written to the transcript
	domain        string        // the probe domain
	runID         string        // random, unique to this run
	prefix        string        // every record a probe creates starts with this
	allowZoneWide bool          // see checkWrite
	gap           time.Duration // minimum time between requests
	pollEvery     time.Duration // time between nameserver polls
	out           io.Writer     // transcript
	hc            *http.Client

	last    time.Time
	created [][2]string // {type, name} of groups to delete at the end
	quiet   bool        // send without writing to the transcript

	// onInterrupt, when set, runs if the user presses Ctrl-C. p6 uses it to
	// put the original nameservers back.
	onInterrupt func()
}

type response struct {
	Status int
	Header http.Header
	Body   []byte
}

// name returns a record name inside the probe prefix.
func (r *runner) name(suffix string) string { return r.prefix + suffix }

// group returns the path of one type+name group on the probe domain.
func (r *runner) group(recordType, name string) string {
	return "/v1/domains/" + r.domain + "/records/" + recordType + "/" + name
}

// do sends one request with the runner's token.
func (r *runner) do(label, method, path string, body interface{}) response {
	return r.doAs(label, r.token, method, path, body)
}

// doAs sends one request with the given token. It refuses any write that
// checkWrite does not allow, and in that case sends nothing.
func (r *runner) doAs(label, token, method, path string, body interface{}) response {
	pathOnly := path
	if i := strings.IndexByte(path, '?'); i >= 0 {
		pathOnly = path[:i]
	}
	if err := checkWrite(method, pathOnly, r.domain, r.prefix, r.allowZoneWide); err != nil {
		fmt.Fprintf(r.out, "\n#### %s\n\nNOT SENT: %s\n", label, err)
		return response{}
	}

	var reqBody []byte
	if body != nil {
		var err error
		if reqBody, err = json.Marshal(body); err != nil {
			panic(err)
		}
	}

	if wait := r.gap - time.Since(r.last); wait > 0 {
		time.Sleep(wait)
	}
	r.last = time.Now()

	req, err := http.NewRequest(method, r.base+path, bytes.NewReader(reqBody))
	if err != nil {
		panic(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	started := time.Now()
	resp, err := r.hc.Do(req)
	if err != nil {
		fmt.Fprintf(r.out, "\n#### %s\n\n`%s %s`\n\nTRANSPORT ERROR: %s\n", label, method, path, err)
		return response{}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if method == "GET" && pathOnly == "/v1/domains/"+r.domain {
		respBody = redactDomainDetail(respBody)
	}

	if !r.quiet {
		fmt.Fprintf(r.out, "\n#### %s\n\n`%s %s`\n\n", label, method, path)
		if reqBody != nil {
			fmt.Fprintf(r.out, "Request body:\n\n```json\n%s\n```\n\n", reqBody)
		}
		fmt.Fprintf(r.out, "Status: %d (%d ms)\n\n", resp.StatusCode, time.Since(started).Milliseconds())
		for _, h := range []string{"Retry-After", "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
			if v := resp.Header.Get(h); v != "" {
				fmt.Fprintf(r.out, "Header %s: %s\n\n", h, v)
			}
		}
		if len(respBody) == 0 {
			fmt.Fprint(r.out, "Response body: empty\n")
		} else {
			fmt.Fprintf(r.out, "Response body:\n\n```json\n%s\n```\n", respBody)
		}
	}
	return response{Status: resp.StatusCode, Header: resp.Header, Body: respBody}
}

// put writes a group and remembers it for cleanup.
func (r *runner) put(label, recordType, name string, records []map[string]interface{}) response {
	r.created = append(r.created, [2]string{recordType, name})
	return r.do(label, "PUT", r.group(recordType, name), records)
}

// cleanup deletes every group a probe wrote. NS and SOA groups cannot be
// deleted through this endpoint and the probes never create them.
func (r *runner) cleanup() {
	fmt.Fprint(r.out, "\n### Cleanup\n")
	seen := map[[2]string]bool{}
	for _, g := range r.created {
		if seen[g] {
			continue
		}
		seen[g] = true
		r.do("delete "+g[0]+" "+g[1], "DELETE", r.group(g[0], g[1]), nil)
	}
	r.created = nil
}

// snapshot reads every record in the zone, one request per record type.
// If the per-type read is not available it falls back to one read of the
// whole zone, which is only trustworthy for zones under 500 records.
func (r *runner) snapshot() ([]zoneRecord, error) {
	wasQuiet := r.quiet
	r.quiet = true
	defer func() { r.quiet = wasQuiet }()

	var all []zoneRecord
	for _, recordType := range recordTypes {
		resp := r.do("", "GET", "/v1/domains/"+r.domain+"/records/"+recordType, nil)
		if resp.Status != http.StatusOK {
			return r.snapshotWholeZone()
		}
		var page []zoneRecord
		if err := json.Unmarshal(resp.Body, &page); err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", recordType, err)
		}
		for i := range page {
			if page[i].Type == "" {
				page[i].Type = recordType
			}
		}
		all = append(all, page...)
	}
	return all, nil
}

func (r *runner) snapshotWholeZone() ([]zoneRecord, error) {
	resp := r.do("", "GET", "/v1/domains/"+r.domain+"/records?limit=500", nil)
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("snapshot: GET records returned %d: %s", resp.Status, resp.Body)
	}
	var all []zoneRecord
	if err := json.Unmarshal(resp.Body, &all); err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	if len(all) >= 500 {
		return nil, fmt.Errorf("snapshot: zone has %d or more records, too many to snapshot in one read", len(all))
	}
	return all, nil
}
````
- [ ] **Step 4: Run the tests to see them pass**

Run: `go test ./scripts/probe -race -count=1`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add scripts/probe/runner.go scripts/probe/runner_test.go
git commit -m "feat(probe): add request runner with transcript and zone snapshot"
```

---

### Task 6: The probes and the command

One function per probe. P6 moves the domain's nameservers and must put them back on every exit path, so its restore logic is tested against a stateful fake before it ever runs for real.

**Files:**
- Create: `scripts/probe/probes.go`
- Create: `scripts/probe/main.go`
- Create: `scripts/probe/README.md`
- Test: `scripts/probe/probes_test.go`

- [ ] **Step 1: Write the failing tests**

Create `scripts/probe/probes_test.go`:

````go
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDomain is a stateful stand-in for one GoDaddy domain: it holds
// nameservers and applies PATCH requests to them.
type fakeDomain struct {
	mu          sync.Mutex
	nameServers []string
	patches     [][]string
	writes      []string // every non-GET request
	detailFails bool     // answer GET on the domain with a 500
}

func (f *fakeDomain) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(req.Body)
	if req.Method != "GET" {
		f.writes = append(f.writes, req.Method+" "+req.URL.Path)
	}
	switch {
	case req.URL.Path == "/v1/domains/example.org" && req.Method == "GET":
		if f.detailFails {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"domain": "example.org", "nameServers": f.nameServers})
	case req.URL.Path == "/v1/domains/example.org" && req.Method == "PATCH":
		var in struct {
			NameServers []string `json:"nameServers"`
		}
		json.Unmarshal(body, &in)
		if len(in.NameServers) < 2 {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		// Like a registrar that validates nothing: accept any two hosts.
		f.nameServers = in.NameServers
		f.patches = append(f.patches, in.NameServers)
	default:
		io.WriteString(w, "[]")
	}
}

func newDomainRunner(t *testing.T, f *fakeDomain) *runner {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &runner{
		base: srv.URL, token: "tok", domain: "example.org", runID: "ab12", prefix: "_probe-ab12-",
		allowZoneWide: true, pollEvery: time.Millisecond, out: &bytes.Buffer{}, hc: srv.Client(),
	}
}

func TestP6RestoresOriginalNameservers(t *testing.T) {
	original := []string{"ns1.orig.example", "ns2.orig.example"}
	f := &fakeDomain{nameServers: original}
	r := newDomainRunner(t, f)

	if err := p6(r, []string{"ns1.target.example", "ns2.target.example"}); err != nil {
		t.Fatal(err)
	}

	if !sameHosts(f.nameServers, original) {
		t.Fatalf("nameservers after p6 = %v, want the originals %v", f.nameServers, original)
	}
	// The fake accepted the made-up nameservers, then the target, then the
	// restore. The restore must be the last change.
	if n := len(f.patches); n != 3 || !sameHosts(f.patches[n-1], original) {
		t.Errorf("accepted changes = %v, want three with the originals last", f.patches)
	}
}

func TestP6InterruptHandlerRestores(t *testing.T) {
	original := []string{"ns1.orig.example", "ns2.orig.example"}
	f := &fakeDomain{nameServers: original}
	r := newDomainRunner(t, f)

	if err := p6(r, []string{"ns1.target.example", "ns2.target.example"}); err != nil {
		t.Fatal(err)
	}
	if r.onInterrupt == nil {
		t.Fatal("p6 did not register an interrupt handler")
	}
	// Simulate an interrupt arriving after the domain was moved again.
	f.mu.Lock()
	f.nameServers = []string{"ns1.target.example", "ns2.target.example"}
	f.mu.Unlock()
	before := len(f.patches)
	r.onInterrupt()
	if len(f.patches) != before {
		t.Errorf("restore ran twice; it must run once per p6 call")
	}
}

func TestP6ChangesNothingWhenCurrentNameserversAreUnknown(t *testing.T) {
	f := &fakeDomain{detailFails: true}
	r := newDomainRunner(t, f)

	if err := p6(r, []string{"ns1.target.example", "ns2.target.example"}); err == nil {
		t.Fatal("want an error when the current nameservers cannot be read")
	}
	if len(f.writes) != 0 {
		t.Errorf("p6 sent writes without knowing what to restore: %v", f.writes)
	}
}

func TestP9RefusesWhenZoneHasRealAAAA(t *testing.T) {
	f := &fakeDomain{}
	r := newDomainRunner(t, f)
	zone := []zoneRecord{{Type: "AAAA", Name: "www", Data: "2001:db8::2", TTL: 3600}}

	if err := p9(r, zone); err == nil {
		t.Fatal("want an error when the zone has an AAAA record outside the probe prefix")
	}
	if len(f.writes) != 0 {
		t.Errorf("p9 sent writes: %v", f.writes)
	}
}

func TestP9SendsTypeWideEmptyArray(t *testing.T) {
	f := &fakeDomain{}
	r := newDomainRunner(t, f)

	if err := p9(r, nil); err != nil {
		t.Fatal(err)
	}
	want := "PUT /v1/domains/example.org/records/AAAA/_probe-ab12-p9\nPUT /v1/domains/example.org/records/AAAA"
	if got := strings.Join(f.writes, "\n"); got != want {
		t.Errorf("p9 writes:\n%s\nwant:\n%s", got, want)
	}
}

func TestSameHosts(t *testing.T) {
	if !sameHosts([]string{"NS2.Example.net.", "ns1.example.net"}, []string{"ns1.example.net", "ns2.example.net"}) {
		t.Error("order, case and a trailing dot must not matter")
	}
	if sameHosts([]string{"ns1.example.net"}, []string{"ns1.example.net", "ns2.example.net"}) {
		t.Error("different lists must not compare equal")
	}
}
````
- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./scripts/probe -count=1`
Expected: FAIL to build, with `undefined: p6`, `undefined: p9`, `undefined: sameHosts`.

- [ ] **Step 3: Write the probes**

Create `scripts/probe/probes.go`:

````go
package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type rec = map[string]interface{}

// one returns a single-value group body with a safe TTL.
func one(data string) []rec { return []rec{{"data": data, "ttl": 3600}} }

// p1: what does a read return for a missing group, a missing domain, a
// domain in another account, and a bad token?
func p1(r *runner, noScopeToken string) {
	missing := r.group("A", r.name("p1-missing"))
	r.do("P1 group that does not exist", "GET", missing, nil)
	r.do("P1 domain that does not exist", "GET",
		"/v1/domains/probe-"+r.runID+"-does-not-exist.org/records/A/www", nil)
	r.do("P1 domain in another account", "GET", "/v1/domains/example.com/records/A/www", nil)
	r.doAs("P1 invalid token", "not-a-real-token", "GET", missing, nil)
	if noScopeToken == "" {
		fmt.Fprint(r.out, "\n#### P1 token without DNS scope\n\nNOT RUN: GODADDY_PROBE_NOSCOPE_TOKEN is not set.\n")
		return
	}
	r.doAs("P1 token without DNS scope", noScopeToken, "GET", missing, nil)
}

// p2: do PUT and DELETE by type and name leave neighbours alone?
func p2(r *runner) {
	a, b := r.name("p2-a"), r.name("p2-b")
	r.put("P2 create TXT a", "TXT", a, one("a1"))
	r.put("P2 create TXT b (neighbour, other name)", "TXT", b, one("b1"))
	r.put("P2 create A a (neighbour, other type)", "A", a, one("192.0.2.1"))

	r.put("P2 replace TXT a with two values", "TXT", a,
		[]rec{{"data": "a2", "ttl": 3600}, {"data": "a3", "ttl": 3600}})
	r.do("P2 read TXT a, expect a2 and a3 only", "GET", r.group("TXT", a), nil)
	r.do("P2 read TXT b, expect b1 unchanged", "GET", r.group("TXT", b), nil)
	r.do("P2 read A a, expect unchanged", "GET", r.group("A", a), nil)

	r.do("P2 delete TXT a", "DELETE", r.group("TXT", a), nil)
	r.do("P2 read TXT a after delete", "GET", r.group("TXT", a), nil)
	r.do("P2 read TXT b after delete, expect b1", "GET", r.group("TXT", b), nil)
	r.do("P2 read A a after delete, expect unchanged", "GET", r.group("A", a), nil)
	r.do("P2 delete TXT a again (already gone)", "DELETE", r.group("TXT", a), nil)

	r.put("P2 create CNAME b where TXT b exists", "CNAME", b, one("target.example.net"))
	r.do("P2 read CNAME b", "GET", r.group("CNAME", b), nil)
	r.do("P2 read TXT b after CNAME attempt", "GET", r.group("TXT", b), nil)
}

// p3: what is the minimum TTL?
func p3(r *runner) {
	name := r.name("p3")
	for _, ttl := range []int{3600, 600, 599, 300, 60, 1, 0} {
		r.put(fmt.Sprintf("P3 write with ttl %d", ttl), "A", name, []rec{{"data": "192.0.2.1", "ttl": ttl}})
		r.do(fmt.Sprintf("P3 read after ttl %d", ttl), "GET", r.group("A", name), nil)
	}
}

// p4: in what form does GoDaddy return values?
func p4(r *runner) {
	check := func(label, recordType, suffix string, body []rec) {
		name := r.name(suffix)
		r.put("P4 write "+label, recordType, name, body)
		r.do("P4 read "+label, "GET", r.group(recordType, strings.ToLower(name)), nil)
	}
	check("TXT with quotes and semicolon", "TXT", "p4-quote", one(`he said "hi"; ok`))
	check("TXT of 300 characters", "TXT", "p4-300", one(strings.Repeat("a", 300)))
	check("TXT of 600 characters", "TXT", "p4-600", one(strings.Repeat("b", 600)))
	check("MX upper case with trailing dot", "MX", "p4-mx",
		[]rec{{"data": "MX1.Example.NET.", "priority": 10, "ttl": 3600}})
	check("CNAME upper case with trailing dot", "CNAME", "p4-cname", one("Target.Example.NET."))
	check("CNAME to @", "CNAME", "p4-cname-at", one("@"))
	check("MX to @", "MX", "p4-mx-at", []rec{{"data": "@", "priority": 10, "ttl": 3600}})
	check("AAAA in long upper-case form", "AAAA", "p4-aaaa", one("2001:0DB8:0000:0000:0000:0000:0000:0001"))
	check("A group with two different TTLs", "A", "p4-mixed",
		[]rec{{"data": "192.0.2.1", "ttl": 600}, {"data": "192.0.2.2", "ttl": 3600}})
	check("TXT with upper-case name", "TXT", "P4-UPPER", one("x"))
}

// p5: what do SRV and CAA records look like?
func p5(r *runner) {
	srv := r.name("p5-srv")
	r.put("P5 SRV with all fields", "SRV", srv, []rec{{
		"data": "target.example.net", "priority": 10, "weight": 5, "port": 5060,
		"service": "_sip", "protocol": "_tcp", "ttl": 3600,
	}})
	r.do("P5 read SRV by the name used to write it", "GET", r.group("SRV", srv), nil)
	r.do("P5 read all SRV records, to see how the name is returned", "GET",
		"/v1/domains/"+r.domain+"/records/SRV", nil)

	srvBare := r.name("p5-srv-bare")
	r.put("P5 SRV without service and protocol", "SRV", srvBare, []rec{{
		"data": "target.example.net", "priority": 10, "weight": 5, "port": 5060, "ttl": 3600,
	}})
	r.do("P5 read SRV without service and protocol", "GET", r.group("SRV", srvBare), nil)

	caa := r.name("p5-caa")
	for _, v := range []struct {
		label string
		body  rec
	}{
		{"CAA as one quoted string", rec{"data": `0 issue "letsencrypt.org"`, "ttl": 3600}},
		{"CAA as one unquoted string", rec{"data": "0 issue letsencrypt.org", "ttl": 3600}},
		{"CAA with separate flag and tag fields", rec{"data": "letsencrypt.org", "flag": 0, "tag": "issue", "ttl": 3600}},
	} {
		r.put("P5 write "+v.label, "CAA", caa, []rec{v.body})
		r.do("P5 read after "+v.label, "GET", r.group("CAA", caa), nil)
	}
}

// p7: how does the whole-zone list page? Read-only.
func p7(r *runner) {
	root := "/v1/domains/" + r.domain + "/records"
	for _, q := range []string{"", "?limit=2&offset=0", "?limit=2&offset=1", "?limit=2&offset=2",
		"?limit=500&offset=1", "?limit=500&offset=2"} {
		r.do("P7 list "+q, "GET", root+q, nil)
	}
}

// p8: what does a rate-limited response contain? Read-only. Uses up the
// credential's quota for about a minute.
func p8(r *runner) {
	gap := r.gap
	r.gap = 0
	defer func() { r.gap = gap; r.quiet = false }()

	path := r.group("TXT", r.name("p8-missing"))
	started := time.Now()
	r.do("P8 request 1", "GET", path, nil)
	r.quiet = true
	for i := 2; i <= 120; i++ {
		if resp := r.do("", "GET", path, nil); resp.Status == 429 {
			r.quiet = false
			fmt.Fprintf(r.out, "\nRequest %d returned 429 after %s.\n", i, time.Since(started).Round(time.Millisecond))
			r.do("P8 next request after the first 429", "GET", path, nil)
			fmt.Fprint(r.out, "\nWait at least one minute before running another probe.\n")
			return
		}
	}
	r.quiet = false
	fmt.Fprintf(r.out, "\nNo 429 after 120 requests in %s.\n", time.Since(started).Round(time.Millisecond))
}

// p9: does a type-wide replace accept an empty array? This is the request
// the old resource sends. It runs against AAAA and only when the zone has no
// AAAA records of its own.
func p9(r *runner, before []zoneRecord) error {
	for _, z := range before {
		if z.Type == "AAAA" && !strings.HasPrefix(z.Name, r.prefix) {
			return fmt.Errorf("zone has a real AAAA record (%s); P9 would delete it, so it was not run", z)
		}
	}
	name := r.name("p9")
	r.put("P9 create an AAAA probe record", "AAAA", name, one("2001:db8::1"))
	r.do("P9 replace all AAAA records with an empty array", "PUT",
		"/v1/domains/"+r.domain+"/records/AAAA", []rec{})
	r.do("P9 read the probe record afterwards", "GET", r.group("AAAA", name), nil)
	return nil
}

// p10: does a read straight after a write return the new state?
func p10(r *runner) {
	gap := r.gap
	defer func() { r.gap = gap; r.quiet = false }()

	name := r.name("p10")
	path := r.group("TXT", name)
	stale := 0
	for i := 1; i <= 20; i++ {
		want := fmt.Sprintf("value-%d", i)
		r.quiet = true
		r.gap = 2 * time.Second // keeps the average at one request per second
		r.put("", "TXT", name, one(want))
		r.gap = 0 // read with no delay
		resp := r.do("", "GET", path, nil)
		r.quiet = false
		if strings.Contains(string(resp.Body), `"`+want+`"`) {
			continue
		}
		stale++
		fmt.Fprintf(r.out, "\nWrite %d: immediate read was stale. Status %d, body: %s\n", i, resp.Status, resp.Body)
		r.gap = time.Second
		for waited := 1; waited <= 30; waited++ {
			r.quiet = true
			again := r.do("", "GET", path, nil)
			r.quiet = false
			if strings.Contains(string(again.Body), `"`+want+`"`) {
				fmt.Fprintf(r.out, "Write %d: visible after about %d s.\n", i, waited)
				break
			}
		}
	}
	fmt.Fprintf(r.out, "\nP10 writes: %d of 20 immediate reads were stale.\n", stale)

	r.gap = time.Second
	r.do("P10 delete the group", "DELETE", path, nil)
	r.gap = 0
	r.do("P10 immediate read after delete", "GET", path, nil)
}

// p12: do the type-wide and whole-zone reads work, and do they page?
// Read-only.
func p12(r *runner) {
	root := "/v1/domains/" + r.domain + "/records"
	for _, q := range []string{"/TXT", "/TXT?limit=1&offset=0", "/TXT?limit=1&offset=1", "/NS", "/SOA", ""} {
		r.do("P12 read "+root+q, "GET", root+q, nil)
	}
}

// p6 moves the domain's nameservers to target, runs P11 while they point
// away, and puts the original nameservers back. The restore runs on every
// exit path, including Ctrl-C.
func p6(r *runner, target []string) error {
	root := "/v1/domains/" + r.domain

	detail := r.do("P6 current nameservers", "GET", root, nil)
	original := nameServersOf(detail.Body)
	if len(original) < 2 {
		return fmt.Errorf("could not read the current nameservers (status %d); nothing was changed", detail.Status)
	}

	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		fmt.Fprintf(r.out, "\n### Restore the original nameservers\n")
		resp := r.do("P6 restore", "PATCH", root, rec{"nameServers": original})
		if resp.Status < 200 || resp.Status > 299 {
			fmt.Fprintf(r.out, "\n**RESTORE FAILED (status %d). Set these nameservers by hand in the GoDaddy console: %s**\n",
				resp.Status, strings.Join(original, ", "))
			return
		}
		if !r.pollNameservers("after restore", original) {
			fmt.Fprintf(r.out, "\n**Restore was accepted but is not visible yet. Check the GoDaddy console. Expected: %s**\n",
				strings.Join(original, ", "))
		}
	}
	r.onInterrupt = restore
	defer restore()

	bogus := "probe-" + r.runID + ".invalid"
	r.do("P6 one nameserver, expect a 422", "PATCH", root, rec{"nameServers": target[:1]})
	r.do("P6 nameservers that do not exist", "PATCH", root, rec{"nameServers": []string{"ns1." + bogus, "ns2." + bogus}})
	r.do("P6 nameservers after the two error cases", "GET", root, nil)

	fmt.Fprintf(r.out, "\n### Move nameservers to %v\n", target)
	r.do("P6 set target nameservers", "PATCH", root, rec{"nameServers": target})
	r.pollNameservers("after change", target)

	fmt.Fprint(r.out, "\n### P11: record endpoints while GoDaddy does not serve the zone\n")
	name := r.name("p11")
	r.do("P11 read a group that does not exist", "GET", r.group("TXT", r.name("p11-missing")), nil)
	r.put("P11 write a group", "TXT", name, one("p11"))
	r.do("P11 read it back", "GET", r.group("TXT", name), nil)
	r.do("P11 delete it", "DELETE", r.group("TXT", name), nil)
	r.do("P11 read all TXT records", "GET", root+"/records/TXT", nil)
	return nil
}

func nameServersOf(body []byte) []string {
	var d struct {
		NameServers []string `json:"nameServers"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return nil
	}
	return d.NameServers
}

// sameHosts compares two nameserver lists ignoring order, case and a
// trailing dot.
func sameHosts(a, b []string) bool {
	norm := func(in []string) string {
		out := make([]string, len(in))
		for i, h := range in {
			out[i] = strings.TrimSuffix(strings.ToLower(h), ".")
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	return norm(a) == norm(b)
}

// pollNameservers reads the domain every 10 seconds, up to 12 times, until
// its nameservers equal want. It writes one line per read.
func (r *runner) pollNameservers(label string, want []string) bool {
	wasQuiet, gap := r.quiet, r.gap
	defer func() { r.quiet, r.gap = wasQuiet, gap }()
	r.quiet = true
	r.gap = r.pollEvery

	fmt.Fprintf(r.out, "\nPolling %s, waiting for %v:\n\n", label, want)
	started := time.Now()
	for i := 1; i <= 12; i++ {
		resp := r.do("", "GET", "/v1/domains/"+r.domain, nil)
		got := nameServersOf(resp.Body)
		fmt.Fprintf(r.out, "- %3.0f s: status %d, nameServers %v\n", time.Since(started).Seconds(), resp.Status, got)
		if sameHosts(got, want) {
			return true
		}
	}
	return false
}
````
- [ ] **Step 4: Write the command**

Create `scripts/probe/main.go`:

````go
// Command probe sends a fixed set of requests to the GoDaddy Domains API and
// writes a Markdown transcript of what came back. It answers the questions
// in "Phase 0: live probes" of
// docs/superpowers/specs/2026-11-26-safe-dns-resources-design.md.
//
// It writes to a live DNS zone. Run it only against a domain that is not in
// use. See scripts/probe/README.md.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

// readOnly lists the probes that send no writes. They run without a zone
// snapshot.
var readOnly = map[string]bool{"p1": true, "p7": true, "p8": true, "p12": true}

func main() {
	probe := flag.String("probe", "", "which probe to run: p1 p2 p3 p4 p5 p6 p7 p8 p9 p10 p12 (p6 includes p11)")
	outDir := flag.String("out", "scripts/probe/out", "directory for transcripts")
	allowZoneWide := flag.Bool("allow-zone-wide", false, "permit p6 (nameservers) and p9 (type-wide replace)")
	targetNS := flag.String("target-ns", "", "p6 only: comma-separated nameservers to move the domain to")
	flag.Parse()

	if err := run(*probe, *outDir, *allowZoneWide, *targetNS); err != nil {
		fmt.Fprintln(os.Stderr, "probe:", err)
		os.Exit(1)
	}
}

func run(probe, outDir string, allowZoneWide bool, targetNS string) error {
	token := os.Getenv("GODADDY_API_TOKEN")
	domain := os.Getenv("GODADDY_PROBE_DOMAIN")
	if token == "" || domain == "" {
		return fmt.Errorf("set GODADDY_API_TOKEN and GODADDY_PROBE_DOMAIN; there is no default domain")
	}
	base := os.Getenv("GODADDY_PROBE_BASEURL")
	if base == "" {
		base = "https://api.godaddy.com"
	}

	var targets []string
	if probe == "p6" {
		for _, h := range strings.Split(targetNS, ",") {
			if h = strings.TrimSpace(h); h != "" {
				targets = append(targets, h)
			}
		}
		if len(targets) < 2 {
			return fmt.Errorf("p6 needs -target-ns with at least two nameservers")
		}
	}
	if (probe == "p6" || probe == "p9") && !allowZoneWide {
		return fmt.Errorf("%s changes more than probe records; pass -allow-zone-wide to confirm", probe)
	}

	idBytes := make([]byte, 3)
	if _, err := rand.Read(idBytes); err != nil {
		return err
	}
	runID := hex.EncodeToString(idBytes)

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	outPath := filepath.Join(outDir, probe+"-"+runID+".md")
	file, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer file.Close()

	r := &runner{
		base:          base,
		token:         token,
		domain:        domain,
		runID:         runID,
		prefix:        "_probe-" + runID + "-",
		allowZoneWide: allowZoneWide,
		gap:           time.Second,
		pollEvery:     10 * time.Second,
		out:           io.MultiWriter(os.Stdout, file),
		hc:            &http.Client{Timeout: 30 * time.Second},
	}

	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt)
	go func() {
		<-interrupted
		fmt.Fprint(r.out, "\n**Interrupted.**\n")
		if r.onInterrupt != nil {
			r.onInterrupt()
		}
		r.cleanup()
		os.Exit(130)
	}()

	fmt.Fprintf(r.out, "## Probe %s on %s\n\nDate: %s. Run ID: %s. Record prefix: `%s`.\n",
		strings.ToUpper(probe), domain, time.Now().UTC().Format("2006-01-02 15:04 MST"), runID, r.prefix)

	var before []zoneRecord
	if !readOnly[probe] {
		if before, err = r.snapshot(); err != nil {
			return fmt.Errorf("before any write: %w", err)
		}
		fmt.Fprintf(r.out, "\nZone snapshot before: %d records.\n", len(before))
	}

	var probeErr error
	switch probe {
	case "p1":
		p1(r, os.Getenv("GODADDY_PROBE_NOSCOPE_TOKEN"))
	case "p2":
		p2(r)
	case "p3":
		p3(r)
	case "p4":
		p4(r)
	case "p5":
		p5(r)
	case "p6":
		probeErr = p6(r, targets)
	case "p7":
		p7(r)
	case "p8":
		p8(r)
	case "p9":
		probeErr = p9(r, before)
	case "p10":
		p10(r)
	case "p12":
		p12(r)
	default:
		return fmt.Errorf("unknown probe %q", probe)
	}

	if readOnly[probe] {
		fmt.Fprintf(r.out, "\nTranscript: %s\n", outPath)
		return probeErr
	}

	r.cleanup()
	after, err := r.snapshot()
	if err != nil {
		return fmt.Errorf("after the probe: %w (compare the zone by hand)", err)
	}
	changes := diffOutside(before, after, r.prefix)
	fmt.Fprintf(r.out, "\n### Zone check\n\nSnapshot after: %d records.\n", len(after))
	if len(changes) > 0 {
		fmt.Fprintf(r.out, "\n**RECORDS OUTSIDE THE PROBE PREFIX CHANGED:**\n\n```\n%s\n```\n", strings.Join(changes, "\n"))
		return fmt.Errorf("%d record(s) outside the probe prefix changed; see %s", len(changes), outPath)
	}
	fmt.Fprint(r.out, "\nNo record outside the probe prefix changed.\n")
	for _, z := range after {
		if strings.HasPrefix(z.Name, r.prefix) {
			fmt.Fprintf(r.out, "\nLeft behind, delete by hand: %s\n", z)
		}
	}
	fmt.Fprintf(r.out, "\nTranscript: %s\n", outPath)
	return probeErr
}
````
- [ ] **Step 5: Run the tests to see them pass**

Run: `go vet ./scripts/probe && go test ./scripts/probe -race -count=1`
Expected: no vet output, then `ok`

- [ ] **Step 6: Check the command refuses to run unconfigured**

Run: `env -u GODADDY_API_TOKEN -u GODADDY_PROBE_DOMAIN go run ./scripts/probe -probe p1`
Expected: `probe: set GODADDY_API_TOKEN and GODADDY_PROBE_DOMAIN; there is no default domain` and exit status 1. No request is sent.

Run: `GODADDY_API_TOKEN=x GODADDY_PROBE_DOMAIN=example.org go run ./scripts/probe -probe p9`
Expected: `probe: p9 changes more than probe records; pass -allow-zone-wide to confirm` and exit status 1. No request is sent.

- [ ] **Step 7: Write the README**

Create `scripts/probe/README.md`:

````markdown
# GoDaddy API probes

`go run ./scripts/probe -probe <name>` sends a fixed set of requests to the GoDaddy Domains API and
writes a transcript to `scripts/probe/out/` (not committed). The probes answer the questions in
"Phase 0: live probes" of `docs/superpowers/specs/2026-11-26-safe-dns-resources-design.md`. Results
are written up in `docs/godaddy-api-behaviour.md`.

**These probes write to a live DNS zone. Use a domain that nothing depends on.**

## Setup

```bash
export GODADDY_API_TOKEN=...            # Personal Access Token
export GODADDY_PROBE_DOMAIN=example.org # no default; you must choose
```

Optional: `GODADDY_PROBE_NOSCOPE_TOKEN`, a second token without DNS scopes, for one case in P1.

## What keeps the zone safe

- Every record a probe creates is named `_probe-<run id>-...`. The tool refuses to send a write to
  any other name; this is enforced in `guard.go` and tested.
- Before and after each writing probe the tool reads the whole zone and fails if any record outside
  that prefix was added, removed or changed.
- A write that replaces a whole record type (P9) or changes nameservers (P6) needs
  `-allow-zone-wide`. A write that replaces the whole zone is never sent.
- P6 records the original nameservers first and restores them at the end, on error, and on Ctrl-C.
- Contact details in the domain response are removed before anything is written to the transcript.
- The token is never written anywhere.

## Probes

| Probe | Writes? | Question |
|---|---|---|
| `p1` | no | What a read returns for a missing group, a missing domain, another account's domain, a bad token |
| `p7` | no | How the whole-zone list pages |
| `p12` | no | Whether type-wide and whole-zone reads work |
| `p8` | no | What a rate-limited response contains. Uses up the quota for a minute |
| `p3` | probe records | Minimum TTL |
| `p2` | probe records | Whether a write or delete by type and name touches neighbours |
| `p4` | probe records | The form values come back in |
| `p5` | probe records | SRV and CAA shapes |
| `p10` | probe records | Whether a read straight after a write is current |
| `p9` | type-wide, `-allow-zone-wide` | Whether an empty type-wide replace is accepted |
| `p6` | nameservers, `-allow-zone-wide -target-ns a,b` | Nameserver change; also runs P11 |

Leave at least a minute between probes: GoDaddy allows 60 requests a minute.
````

- [ ] **Step 8: Run the whole suite**

Run: `go build ./... && go vet ./... && go test -race -count=1 ./...`
Expected: all `ok`.

- [ ] **Step 9: Commit**

```bash
git add scripts/probe/probes.go scripts/probe/main.go scripts/probe/probes_test.go scripts/probe/README.md
git commit -m "feat(probe): add probes P1 to P12 and the probe command"
```

---

### Task 7: Behaviour document

The document starts with what the project already knows, most of it previously recorded only in commit messages, and has one section per probe ready to receive results.

**Files:**
- Create: `docs/godaddy-api-behaviour.md`

- [ ] **Step 1: Write the document**

Create `docs/godaddy-api-behaviour.md`:

````markdown
# GoDaddy Domains API: observed behaviour

What this project knows about how GoDaddy's v1 Domains API behaves, beyond what the published API
document says. Each entry gives its source and date. Add to it whenever behaviour is confirmed or
found to have changed.

API document: `https://developer.godaddy.com/openapi/domains-v1.json`.

## Inherited knowledge

From the history of this repository and its upstream, `n3integration/terraform-provider-godaddy`.
None of it has been re-verified unless a probe section below says so.

| Behaviour | Source |
|---|---|
| The API allows 60 requests per minute per credential. The client spaces requests one second apart. | `api/client.go:33-35`; v1 overview page, read 2026-11-26 |
| A type-wide replace with an empty NS or SOA list is rejected, so the client never sends one. | `api/types.go:270-273` |
| CAA writes were disabled on purpose the day after CAA support was added. The reason was not recorded. | commit `cd8cde9`, 2020-04-02, "add caa record type to disallow list to prevent propagation" |
| Custom nameservers had to be entered in GoDaddy's console before the API would accept them. | `README.md`, example comment |
| `X-Shopper-Id` is needed only by resellers managing a domain bought outside their own account. | API document |
| Bearer tokens (Personal Access Tokens) are accepted by the v1 API. | v1.10.0 release, 2026-11-25 |
| A new domain comes with a parked apex A record and `www` and `_domainconnect` CNAME records. | `plugin/godaddy/resource_dns_record.go:42-46` |
| The whole-zone record list (`GET /v1/domains/{domain}/records`) is not in the current API document, though the client uses it. | API document, read 2026-11-26 |

## Probe results

Probes are in `scripts/probe/`. Each section below is filled in from that probe's transcript.
Format for every section: the date and run ID, each question with its answer, and the request and
response that show it, copied from the transcript.

### P1: reads of things that are not there

Not yet run.

### P2: neighbours of a write or delete by type and name

Not yet run.

### P3: minimum TTL

Not yet run.

### P4: the form of returned values

Not yet run.

### P5: SRV and CAA

Not yet run.

### P6: nameserver change

Not yet run.

### P7: paging of the whole-zone list

Not yet run.

### P8: rate-limited responses

Not yet run.

### P9: empty type-wide replace

Not yet run.

### P10: read after write

Not yet run.

### P11: record endpoints while GoDaddy does not serve the zone

Not yet run.

### P12: type-wide and whole-zone reads

Not yet run.
````

- [ ] **Step 2: Commit**

```bash
git add docs/godaddy-api-behaviour.md
git commit -m "docs: start the GoDaddy API behaviour document"
```

---

### Task 8: Run the read-only probes (maintainer)

**Stop. This task sends requests to GoDaddy with the maintainer's token.** These four probes send no writes. An agent shows the commands and waits for the maintainer.

**Files:**
- Modify: `docs/godaddy-api-behaviour.md` (sections P1, P7, P8, P12)

- [ ] **Step 1: Set the environment**

```bash
export GODADDY_PROBE_DOMAIN=elsewhere-zone.example
test -n "$GODADDY_API_TOKEN" && echo "token is set" || echo "token is NOT set"
```

Expected: `token is set`. If not, the maintainer exports `GODADDY_API_TOKEN`; the agent never asks for its value.

- [ ] **Step 2: Run P1, P7 and P12**

```bash
go run ./scripts/probe -probe p1
sleep 60
go run ./scripts/probe -probe p7
sleep 60
go run ./scripts/probe -probe p12
```

Expected: each ends with `Transcript: scripts/probe/out/<probe>-<id>.md`. A 401 or 403 on every request means the token or its scopes are wrong: stop and tell the maintainer.

- [ ] **Step 3: Checkpoint: can the tool snapshot the zone?**

Open the P12 transcript. Find the entry `P12 read /v1/domains/elsewhere-zone.example/records/TXT`.

- Status 200 with a JSON array: the per-type read works, and the snapshot in Tasks 9 and 10 uses it. Continue.
- Anything else: the snapshot falls back to the whole-zone read. Open the P7 transcript and confirm that `P7 list ` (no query) returned status 200 and every record of the zone. If it did, continue. If neither read works, **stop**: the zone cannot be checked, so the writing probes must not run. Report to the maintainer.

- [ ] **Step 4: Run P8**

```bash
sleep 60
go run ./scripts/probe -probe p8
sleep 90
```

Expected: the transcript reports either the request number that returned 429, with that response's headers and body, or `No 429 after 120 requests`.

- [ ] **Step 5: Write up the four sections**

In `docs/godaddy-api-behaviour.md`, replace `Not yet run.` under P1, P7, P8 and P12. For each, write the date and run ID from the transcript header, then one bullet per question below with the answer and the status code and body that show it, copied from the transcript.

- P1: What do status and body look like for (a) a group that does not exist, (b) a domain that does not exist, (c) a domain in another account, (d) an invalid token, (e) a token without DNS scope if it was run? Can (a) be told apart from (b) and (c)?
- P7: Does `offset` count records or pages? Does `offset=1` skip the first record? Compare the `limit=2` responses with the unpaged list.
- P8: Which request number was limited? Which header or body field gives the wait, and in what unit?
- P12: Do `GET /records/{type}` and `GET /records` return 200? Do `limit` and `offset` work on the per-type read?

- [ ] **Step 6: Commit**

```bash
git add docs/godaddy-api-behaviour.md
git commit -m "docs: record read-only probe results (P1, P7, P8, P12)"
```

---

### Task 9: Run the probes that write probe records (maintainer)

**Stop. This task writes to the live zone of `elsewhere-zone.example`.** Each probe creates records named `_probe-<id>-...`, deletes them, and checks that nothing else in the zone changed. An agent shows each command and waits for the maintainer before each one.

**Files:**
- Modify: `docs/godaddy-api-behaviour.md` (sections P2, P3, P4, P5, P9, P10)

- [ ] **Step 1: Run P3 first**

The other probes write with TTL 3600. P3 confirms that value is accepted before they rely on it.

```bash
go run ./scripts/probe -probe p3
```

Expected: the transcript ends with `No record outside the probe prefix changed.` and the command exits 0. In the transcript, `P3 write with ttl 3600` must show a 2xx status. If it does not, stop and report.

**After every probe in this task:** if the output contains `RECORDS OUTSIDE THE PROBE PREFIX CHANGED`, stop at once, do not run another probe, and give the maintainer the listed lines. If it contains `Left behind, delete by hand`, tell the maintainer which records.

- [ ] **Step 2: Run P2, P4, P5 and P10**

Wait a minute between probes.

```bash
sleep 60 && go run ./scripts/probe -probe p2
sleep 60 && go run ./scripts/probe -probe p4
sleep 60 && go run ./scripts/probe -probe p5
sleep 60 && go run ./scripts/probe -probe p10
```

Expected for each: exit 0 and `No record outside the probe prefix changed.` Individual requests inside a probe may return 4xx; that is data, not failure.

- [ ] **Step 3: Run P9**

P9 sends the request the old resource sends: a type-wide replace with an empty array, against AAAA. It refuses to run if the zone has an AAAA record of its own.

```bash
sleep 60 && go run ./scripts/probe -probe p9 -allow-zone-wide
```

Expected: exit 0 and `No record outside the probe prefix changed.`, or the refusal `zone has a real AAAA record ...; P9 would delete it, so it was not run`. If refused, write that under P9 in the behaviour document and move on.

- [ ] **Step 4: Write up the six sections**

In `docs/godaddy-api-behaviour.md`, replace `Not yet run.` under each, with date, run ID, and one bullet per question with the evidence from the transcript.

- P2: Status codes for write and delete by type and name. Were the other-name and other-type neighbours unchanged after the replace and after the delete? What does delete return for a group that is already gone? What happened when a CNAME was written where a TXT record existed, and was the TXT record still there afterwards?
- P3: The lowest TTL accepted, and the status and body for the first one refused.
- P4: For each case, the value written and the value returned: quotes in TXT; 300 and 600 character TXT; upper case and trailing dot in MX and CNAME data; `@` as CNAME and MX data; long-form IPv6; two TTLs in one group; upper-case name.
- P5: Was the SRV write accepted? Under what `name` does GoDaddy return it, and are `service` and `protocol` separate fields? Is SRV without them accepted? Which of the three CAA forms were accepted, and what came back?
- P9: Was the empty type-wide replace accepted, and was the probe AAAA record gone afterwards?
- P10: How many of 20 immediate reads were stale, and for how long? Was the read after delete current?

- [ ] **Step 5: Commit**

```bash
git add docs/godaddy-api-behaviour.md
git commit -m "docs: record write probe results (P2 to P5, P9, P10)"
```

---

### Task 10: Run the nameserver probe (maintainer)

**Stop. This task changes the nameservers of `elsewhere-zone.example` and puts them back.** While it runs, the domain resolves through another DNS host. It needs an AWS account for a temporary Route 53 hosted zone. An agent shows each command and waits for the maintainer.

**Files:**
- Modify: `docs/godaddy-api-behaviour.md` (sections P6, P11)

- [ ] **Step 1: Create a temporary hosted zone**

```bash
aws route53 create-hosted-zone --name elsewhere-zone.example \
  --caller-reference "probe-$(date +%s)" \
  --query '{id: HostedZone.Id, nameservers: DelegationSet.NameServers}'
```

Expected: JSON with an `id` like `/hostedzone/Z...` and four `nameservers`. Keep both. Route 53 does not charge for a zone deleted within 12 hours.

- [ ] **Step 2: Note the current nameservers by hand**

```bash
dig +short NS elsewhere-zone.example
```

Write the output down outside the terminal. If the probe's restore fails, these are the values to set in the GoDaddy console.

- [ ] **Step 3: Run P6**

Replace the four names with the ones from Step 1, without trailing dots.

```bash
go run ./scripts/probe -probe p6 -allow-zone-wide \
  -target-ns ns-1.awsdns-01.org,ns-2.awsdns-02.co.uk,ns-3.awsdns-03.com,ns-4.awsdns-04.net
```

Expected: the transcript shows the two error cases, the move, a poll, the P11 requests, a section `Restore the original nameservers`, and a final poll that ends on the original nameservers. Takes up to five minutes. Do not close the terminal; Ctrl-C triggers the restore.

If the first `PATCH` returns 403, the token lacks the scope for a domain update. The probe still restores (nothing changed). Record the 403 body under P6, ask the maintainer for a token with wider scope, and run this step again.

- [ ] **Step 4: Confirm the nameservers are back**

Read the end of the P6 transcript: the last poll line under `Polling after restore` must list the nameservers from Step 2. Then ask the registry directly:

```bash
dig +short NS elsewhere-zone.example @a0.org.afilias-nst.info
```

Expected: the nameservers from Step 2. The registry can lag the API by a few minutes; if it still shows the Route 53 names, wait five minutes and ask again. If the transcript contains `RESTORE FAILED` or `not visible yet`, the maintainer checks the GoDaddy console and sets the Step 2 values by hand before anything else happens.

- [ ] **Step 5: Delete the temporary hosted zone**

```bash
aws route53 delete-hosted-zone --id /hostedzone/ZXXXXXXXXXXXXX
```

Use the `id` from Step 1. Expected: a `ChangeInfo` block with `"Status": "PENDING"`.

- [ ] **Step 6: Write up P6 and P11**

In `docs/godaddy-api-behaviour.md`, replace `Not yet run.` under P6 and P11.

- P6: Which token scope was needed? Status and body for one nameserver, and for nameservers that do not exist: does GoDaddy validate them? Status of the real change. From the poll lines: was the change visible on the first read, and if not, after how long? Is `nameServers` present in the domain response? Same questions for the restore.
- P11: While the nameservers pointed away: status and body for a read of a missing group, a write, a read back, a delete, and a type-wide read. Can "zone not served here" be told apart from "group does not exist"?

- [ ] **Step 7: Commit**

```bash
git add docs/godaddy-api-behaviour.md
git commit -m "docs: record nameserver probe results (P6, P11)"
```

---

### Task 11: Compare results with the spec

The spec's rule: if a probe contradicts an assumption, the feature is removed from 1.11.0 or the spec is revised and reviewed again. This task produces the list of spec changes; it does not make them.

**Files:**
- Modify: `docs/godaddy-api-behaviour.md` (new final section)

- [ ] **Step 1: Check each probe against the spec section that depends on it**

Read `docs/superpowers/specs/2026-11-26-safe-dns-resources-design.md` and, for each row, decide: confirmed, or needs a spec change.

| Probe | Spec section | The spec assumes or leaves open |
|---|---|---|
| P1 | "Operations", Read cases 2 and 4 | A missing group can be told apart from a missing or unauthorised domain |
| P2 | "Operations", central invariant; Delete | Writes and deletes by type and name leave all other records unchanged; deleting a missing group can be treated as success |
| P3 | "Validation at plan time", `ttl` | A minimum exists; the spec needs the number |
| P4 | "Normalisation"; "Validation at plan time" | Hostnames come back lower case with no trailing dot; TXT is unchanged; the spec needs the TXT length limit, whether `@` data is used, and whether TTLs can be mixed |
| P5 | "Record types" | SRV fits the schema's fields; CAA fits in `data`. The spec needs the CAA format, or CAA and SRV are dropped |
| P6 | "Resource: godaddy_domain_nameservers" | The domain update sets nameservers; the spec needs the token scope and whether the change is immediate |
| P7 | "godaddy_domain_record in 1.11.0", pagination | Nothing; the result is documentation for the old resource only |
| P8 | "Retry" | A 429 states how long to wait; the spec needs the header or field and its unit |
| P9 | Appendix A, F1 | An empty type-wide replace deletes every record of that type |
| P10 | "Reading after a write" | Open: polling is added only if reads can be stale |
| P11 | "Operations", Read case 3 | Open: the spec needs the rule for a zone GoDaddy does not serve |
| P12 | "Shared rate limit" | Nothing in 1.11.0; informs a later bulk read |

- [ ] **Step 2: Write the comparison into the behaviour document**

Append to `docs/godaddy-api-behaviour.md` a section `## What the probes mean for the 1.11.0 design`, containing one line per probe in the form:

`- P3: confirmed | needs spec change. <one sentence: what was found and which spec sentence it confirms or contradicts>`

Every probe must have a line. For a probe that could not be run, write `not run` and why.

- [ ] **Step 3: Run the whole suite one last time**

Run: `go build ./... && go vet ./... && go test -race -count=1 ./...`
Expected: all `ok`.

- [ ] **Step 4: Commit**

```bash
git add docs/godaddy-api-behaviour.md
git commit -m "docs: compare probe results with the 1.11.0 design"
```

- [ ] **Step 5: Hand back to the maintainer**

Report: the list from Step 2, with every `needs spec change` line first. The maintainer decides how the spec is amended. Plan 3 is written only after that. Do not push or open a pull request unless asked.

---

## Done when

- `go test -race -count=1 ./...` passes, including the new tests in `plugin/godaddy` and `scripts/probe`.
- No non-test file under `api/` or `plugin/godaddy/` has changed: `git diff docs/safe-dns-resources-spec -- api plugin/godaddy ':!*_test.go'` prints nothing.
- Every probe section in `docs/godaddy-api-behaviour.md` holds a result or a stated reason it was not run.
- `elsewhere-zone.example` has its original nameservers and no `_probe-` records.
- The maintainer has the list of spec changes the results require.
