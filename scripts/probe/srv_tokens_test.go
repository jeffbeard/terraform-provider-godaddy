package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const p15Name = "_probe-ab12-p15"

// fakeSRV holds the SRV records of one domain and can be told how a write
// at the plain name and requests at the long name behave.
type fakeSRV struct {
	mu       sync.Mutex
	records  []map[string]interface{}
	writes   []string // "METHOD path body" of every non-GET request
	requests int

	onRequest        func(n int)
	plainReplacesAll bool   // a PUT at the plain name replaces every SRV record there, not just the same service
	longForm         string // "": accepted; "refuse": 422 and 404; "bare": accepted only without service and protocol in the body
}

func (f *fakeSRV) hook(fn func(n int)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onRequest = fn
}

func (f *fakeSRV) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes...)
}

func (f *fakeSRV) left() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records)
}

// splitLong splits "_sip._tcp.name" into its parts. long is false for a
// plain name.
func splitLong(name string) (service, protocol, plain string, long bool) {
	parts := strings.SplitN(name, ".", 3)
	if len(parts) == 3 && strings.HasPrefix(parts[0], "_") && strings.HasPrefix(parts[1], "_") {
		return parts[0], parts[1], parts[2], true
	}
	return "", "", name, false
}

// drop removes the records at name for which match is true and reports how
// many it removed.
func (f *fakeSRV) drop(name string, match func(rec map[string]interface{}) bool) int {
	var kept []map[string]interface{}
	for _, rec := range f.records {
		if rec["name"] == name && match(rec) {
			continue
		}
		kept = append(kept, rec)
	}
	removed := len(f.records) - len(kept)
	f.records = kept
	return removed
}

func (f *fakeSRV) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(req.Body)
	f.requests++
	if f.onRequest != nil {
		f.onRequest(f.requests)
	}
	if req.Method != "GET" {
		f.writes = append(f.writes, strings.TrimSpace(req.Method+" "+req.URL.Path+" "+string(body)))
	}
	parts := strings.Split(strings.TrimPrefix(req.URL.Path, "/v1/domains/example.org/records/"), "/")
	reply := func(v []map[string]interface{}) {
		if v == nil {
			v = []map[string]interface{}{}
		}
		json.NewEncoder(w).Encode(v)
	}
	if parts[0] != "SRV" {
		reply(nil)
		return
	}
	if len(parts) == 1 {
		reply(f.records)
		return
	}
	var recs []map[string]interface{}
	json.Unmarshal(body, &recs)
	service, protocol, name, long := splitLong(parts[1])
	same := func(rec map[string]interface{}) bool { return rec["service"] == service && rec["protocol"] == protocol }
	all := func(map[string]interface{}) bool { return true }

	switch {
	case req.Method == "GET" && long:
		var found []map[string]interface{}
		for _, rec := range f.records {
			if rec["name"] == name && same(rec) {
				found = append(found, rec)
			}
		}
		reply(found)
	case req.Method == "GET":
		reply(nil) // as seen live: a read at the plain name finds nothing
	case req.Method == "PUT" && long:
		_, hasService := recs[0]["service"]
		if f.longForm == "refuse" || (f.longForm == "bare" && hasService) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		f.drop(name, same)
		for _, rec := range recs {
			rec["name"], rec["type"], rec["service"], rec["protocol"] = name, "SRV", service, protocol
			f.records = append(f.records, rec)
		}
	case req.Method == "PUT":
		for _, rec := range recs {
			rec := rec
			if f.plainReplacesAll {
				f.drop(name, all)
			} else {
				f.drop(name, func(old map[string]interface{}) bool {
					return old["service"] == rec["service"] && old["protocol"] == rec["protocol"]
				})
			}
		}
		for _, rec := range recs {
			rec["name"], rec["type"] = name, "SRV"
			f.records = append(f.records, rec)
		}
	case req.Method == "DELETE" && long:
		if f.longForm == "refuse" || f.drop(name, same) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case req.Method == "DELETE":
		if f.drop(name, all) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func TestCheckWriteSRVLongForm(t *testing.T) {
	const domain, prefix = "example.org", "_probe-ab12-"
	root := "/v1/domains/example.org/records/"
	cases := []struct {
		name      string
		method    string
		path      string
		wantAllow bool
	}{
		{"SRV long form put", "PUT", root + "SRV/_sip._tcp._probe-ab12-p15", true},
		{"SRV long form delete", "DELETE", root + "SRV/_sip._tcp._probe-ab12-p15", true},
		{"SRV long form with a hyphen and digits", "PUT", root + "SRV/_xmpp-client._tcp2._probe-ab12-p15", true},
		{"SRV plain probe name still allowed", "PUT", root + "SRV/_probe-ab12-p15", true},
		{"the same long form for TXT", "PUT", root + "TXT/_sip._tcp._probe-ab12-p15", false},
		{"the same long form for srv in lower case", "PUT", root + "srv/_sip._tcp._probe-ab12-p15", false},
		{"SRV long form of a real name", "PUT", root + "SRV/_sip._tcp.www", false},
		{"SRV long form of the apex", "DELETE", root + "SRV/_sip._tcp.@", false},
		{"SRV long form with nothing after it", "PUT", root + "SRV/_sip._tcp.", false},
		{"SRV long form of another run's prefix", "PUT", root + "SRV/_sip._tcp._probe-ffff-p15", false},
		{"SRV with one underscore label", "PUT", root + "SRV/_sip._probe-ab12-x", false},
		{"SRV with three underscore labels", "PUT", root + "SRV/_a._sip._tcp._probe-ab12-x", false},
		{"SRV prefix later in the name", "PUT", root + "SRV/_sip._tcp.www._probe-ab12-x", false},
		{"SRV upper-case service", "PUT", root + "SRV/_SIP._tcp._probe-ab12-x", false},
		{"SRV upper-case protocol", "PUT", root + "SRV/_sip._TCP._probe-ab12-x", false},
		{"SRV odd character in the service", "PUT", root + "SRV/_s%69p._tcp._probe-ab12-x", false},
		{"SRV space in the protocol", "PUT", root + "SRV/_sip._t cp._probe-ab12-x", false},
		{"SRV empty service", "PUT", root + "SRV/_._tcp._probe-ab12-x", false},
		{"SRV empty protocol", "PUT", root + "SRV/_sip._._probe-ab12-x", false},
		{"SRV label without its underscore", "PUT", root + "SRV/sip._tcp._probe-ab12-x", false},
		{"SRV underscore inside a label", "PUT", root + "SRV/_s_ip._tcp._probe-ab12-x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, allow := range []allowance{{}, {typeWidePut: []string{"AAAA"}, domainPatch: true}} {
				err := checkWrite(tc.method, tc.path, domain, prefix, allow)
				if allowed := err == nil; allowed != tc.wantAllow {
					t.Errorf("checkWrite(%s %s, %+v) allowed=%v, want %v (err: %v)", tc.method, tc.path, allow, allowed, tc.wantAllow, err)
				}
			}
		})
	}
	if err := checkWrite("PUT", root+"SRV/_sip._tcp._probe-ab12-x", domain, "", allowance{}); err == nil {
		t.Error("an empty prefix must still refuse the long form")
	}
}

func TestP15AndP16Allowances(t *testing.T) {
	const domain, prefix = "example.org", "_probe-ab12-"
	p16 := allowanceFor("p16", true)
	if !reflect.DeepEqual(p16, allowance{domainPatch: true}) {
		t.Errorf("allowanceFor(p16, true) = %+v, want only the domain PATCH", p16)
	}
	if err := checkWrite("PATCH", "/v1/domains/example.org", domain, prefix, p16); err != nil {
		t.Errorf("p16 may PATCH the domain: %v", err)
	}
	if err := checkWrite("PUT", "/v1/domains/example.org/records/AAAA", domain, prefix, p16); err == nil {
		t.Error("p16 must not make a type-wide write")
	}
	if got := allowanceFor("p16", false); !reflect.DeepEqual(got, allowance{}) {
		t.Errorf("allowanceFor(p16, false) = %+v, want nothing", got)
	}
	for _, flag := range []bool{true, false} {
		if got := allowanceFor("p15", flag); !reflect.DeepEqual(got, allowance{}) {
			t.Errorf("allowanceFor(p15, %v) = %+v, want nothing", flag, got)
		}
	}
	for _, name := range []string{"p15", "p16"} {
		if err := validateProbe(name); err != nil {
			t.Errorf("validateProbe(%s) = %v", name, err)
		}
		if readOnly[name] {
			t.Errorf("%s writes; it must not be listed as read-only", name)
		}
	}
}

// assertP15Writes fails unless every write went to the probe's SRV group,
// under its plain or its long name.
func assertP15Writes(t *testing.T, writes []string) {
	t.Helper()
	for _, w := range writes {
		path := strings.Fields(w)[1]
		if path != "/v1/domains/example.org/records/SRV/"+p15Name &&
			path != "/v1/domains/example.org/records/SRV/_sip._tcp."+p15Name {
			t.Errorf("write outside the probe's SRV group: %s", w)
		}
	}
}

func assertLines(t *testing.T, transcript string, want ...string) {
	t.Helper()
	for _, line := range want {
		if !strings.Contains(transcript, line+"\n") {
			t.Errorf("transcript lacks the line %q", line)
		}
	}
	if t.Failed() {
		var summary []string
		for _, line := range strings.Split(transcript, "\n") {
			if strings.HasPrefix(line, "P15 ") || strings.HasPrefix(line, "P16 ") {
				summary = append(summary, line)
			}
		}
		t.Logf("summary lines in the transcript:\n%s", strings.Join(summary, "\n"))
	}
}

const (
	sipBody  = `{"data":"sip.example.net","port":5060,"priority":10,"protocol":"_tcp","service":"_sip","ttl":3600,"weight":5}`
	xmppBody = `{"data":"xmpp.example.net","port":5222,"priority":10,"protocol":"_tcp","service":"_xmpp-client","ttl":3600,"weight":5}`
	sip2Body = `{"data":"sip2.example.net","port":5060,"priority":10,"protocol":"_tcp","service":"_sip","ttl":3600,"weight":5}`
	sip2Bare = `{"data":"sip2.example.net","port":5060,"priority":10,"ttl":3600,"weight":5}`
)

func TestP15WhenAPlainWriteReplacesTheWholeName(t *testing.T) {
	f := &fakeSRV{plainReplacesAll: true}
	r, out := newDomainRunner(t, f, allowance{})

	p15(r)

	text := out.String()
	assertNothingRefused(t, text)
	writes := f.sent()
	assertP15Writes(t, writes)
	plain, long := "/v1/domains/example.org/records/SRV/"+p15Name, "/v1/domains/example.org/records/SRV/_sip._tcp."+p15Name
	want := []string{
		"PUT " + plain + " [" + sipBody + "]",
		"PUT " + plain + " [" + xmppBody + "]",
		"PUT " + plain + " [" + sipBody + "," + xmppBody + "]",
		"PUT " + long + " [" + sip2Body + "]",
		"DELETE " + long,
		"DELETE " + plain,
	}
	if strings.Join(writes, "\n") != strings.Join(want, "\n") {
		t.Errorf("writes:\n%s\nwant:\n%s", strings.Join(writes, "\n"), strings.Join(want, "\n"))
	}
	assertLines(t, text,
		"P15 A: a write at the plain name for one service REPLACED the other service's record.",
		"P15 B: after a write of both services in one body, both are present.",
		"P15 C: a long-form write was accepted (status 200, with service and protocol in the body); "+
			"the xmpp record survived; the sip record now holds: sip2.example.net.",
		"P15 D: a long-form delete was accepted (status 204) and removed only the sip record.",
		"P15 E: a delete at the plain name (status 204) removed everything left at the name.",
	)
	for _, read := range []string{
		"`GET /v1/domains/example.org/records/SRV/_sip._tcp." + p15Name + "`",
		"`GET /v1/domains/example.org/records/SRV/_xmpp-client._tcp." + p15Name + "`",
	} {
		if !strings.Contains(text, read) {
			t.Errorf("transcript lacks the read %s", read)
		}
	}
	if n := strings.Count(text, "`GET /v1/domains/example.org/records/SRV`"); n != 6 {
		t.Errorf("the whole SRV type was listed %d times, want 6", n)
	}
	if f.left() != 0 {
		t.Errorf("%d SRV record(s) left in the fake", f.left())
	}
}

func TestP15WhenAPlainWriteReplacesOnlyItsService(t *testing.T) {
	f := &fakeSRV{}
	r, out := newDomainRunner(t, f, allowance{})

	p15(r)

	text := out.String()
	assertNothingRefused(t, text)
	assertP15Writes(t, f.sent())
	assertLines(t, text,
		"P15 A: a write at the plain name for one service KEPT the other service's record.",
		"P15 B: after a write of both services in one body, both are present.",
		"P15 D: a long-form delete was accepted (status 204) and removed only the sip record.",
		"P15 E: a delete at the plain name (status 204) removed everything left at the name.",
	)
	if strings.Contains(text, "REPLACED") {
		t.Errorf("the other service's record was kept:\n%s", text)
	}
}

func TestP15WhenTheLongFormIsRefused(t *testing.T) {
	f := &fakeSRV{plainReplacesAll: true, longForm: "refuse"}
	r, out := newDomainRunner(t, f, allowance{})

	p15(r)

	text := out.String()
	assertNothingRefused(t, text)
	writes := f.sent()
	assertP15Writes(t, writes)
	long := "PUT /v1/domains/example.org/records/SRV/_sip._tcp." + p15Name
	if got := strings.Join(writes, "\n"); !strings.Contains(got, long+" ["+sip2Body+"]\n"+long+" ["+sip2Bare+"]\n") {
		t.Errorf("after the refusal the write must be tried again without service and protocol:\n%s", got)
	}
	assertLines(t, text,
		"P15 C: a long-form write was REFUSED (status 422 with service and protocol in the body, status 422 without).",
		"P15 D: a long-form delete was REFUSED (status 404).",
		"P15 E: a delete at the plain name (status 204) removed everything left at the name.",
	)
	if n := strings.Count(text, "`GET /v1/domains/example.org/records/SRV`"); n != 7 {
		t.Errorf("the whole SRV type was listed %d times, want 7 (one more after the second long-form write)", n)
	}
}

func TestP15WhenTheLongFormWantsABareBody(t *testing.T) {
	f := &fakeSRV{plainReplacesAll: true, longForm: "bare"}
	r, out := newDomainRunner(t, f, allowance{})

	p15(r)

	text := out.String()
	assertNothingRefused(t, text)
	assertLines(t, text,
		"P15 C: a long-form write was accepted (status 200, without service and protocol in the body, after status 422 with them); "+
			"the xmpp record survived; the sip record now holds: sip2.example.net.",
		"P15 D: a long-form delete was accepted (status 204) and removed only the sip record.",
	)
}

func TestP15InconclusiveWhenTheListFails(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/records/SRV") {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		io.WriteString(w, "[]")
	})
	r, out := newTestRunner(t, h)

	p15(r)

	text := out.String()
	for _, line := range []string{"P15 A: inconclusive", "P15 B: inconclusive", "P15 C: inconclusive", "P15 D: inconclusive", "P15 E: inconclusive"} {
		if !strings.Contains(text, line) || !strings.Contains(text, "status 429") {
			t.Errorf("transcript lacks %q with the reason:\n%s", line, text)
		}
	}
	for _, verdict := range []string{"REPLACED", "KEPT", "both are present", "removed only", "removed everything"} {
		if strings.Contains(text, verdict) {
			t.Errorf("a verdict (%s) was drawn from lists that failed", verdict)
		}
	}
}

// fakeScoped is one domain seen through three tokens. It records which
// token sent what.
type fakeScoped struct {
	mu          sync.Mutex
	nameServers []string
	log         []string // "who METHOD path body"
	requests    int

	onRequest        func(n int)
	mainDomainStatus int  // status of GET domain with the main token; 200 when zero
	corruptOnPatch   bool // a PATCH changes the nameservers to something else
	patchStatus      int  // status of a PATCH with the DNS-only token; 204 when zero
}

const (
	roToken  = "ro-token-value"
	dnsToken = "dns-token-value"
)

func (f *fakeScoped) hook(fn func(n int)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onRequest = fn
}

func (f *fakeScoped) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

// only returns the logged requests that contain part.
func (f *fakeScoped) only(part string) []string {
	var out []string
	for _, line := range f.sent() {
		if strings.Contains(line, part) {
			out = append(out, line)
		}
	}
	return out
}

func (f *fakeScoped) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(req.Body)
	f.requests++
	if f.onRequest != nil {
		f.onRequest(f.requests)
	}
	who := map[string]string{
		"Bearer secret-token-value": "main", "Bearer " + roToken: "ro", "Bearer " + dnsToken: "dns",
	}[req.Header.Get("Authorization")]
	if who == "" {
		who = "unknown"
	}
	f.log = append(f.log, strings.TrimSpace(who+" "+req.Method+" "+req.URL.Path+" "+string(body)))

	isDomain := req.URL.Path == "/v1/domains/example.org"
	switch {
	case who == "ro" && req.Method != "GET":
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"code":"ACCESS_DENIED"}`)
	case isDomain && req.Method == "GET":
		if who == "main" && f.mainDomainStatus != 0 {
			w.WriteHeader(f.mainDomainStatus)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"domain": "example.org", "nameServers": f.nameServers,
			"contactRegistrant": map[string]string{"email": "someone@example.org"},
		})
	case isDomain && req.Method == "PATCH":
		if f.patchStatus != 0 {
			w.WriteHeader(f.patchStatus)
			return
		}
		if f.corruptOnPatch {
			f.nameServers = []string{"ns1.wrong.example", "ns2.wrong.example"}
		}
		w.WriteHeader(http.StatusNoContent)
	case req.Method == "DELETE":
		w.WriteHeader(http.StatusNoContent)
	case req.Method == "GET":
		io.WriteString(w, "[]")
	}
}

var p16Tokens = probeArgs{readOnlyToken: roToken, dnsOnlyToken: dnsToken}

func assertNoTokens(t *testing.T, where, text string) {
	t.Helper()
	for _, token := range []string{"secret-token-value", roToken, dnsToken} {
		if strings.Contains(text, token) {
			t.Errorf("%s contains the token %q", where, token)
		}
	}
}

func TestP16SendsEachStepWithItsToken(t *testing.T) {
	// The order and the case are GoDaddy's; the PATCH must repeat them.
	current := []string{"NS2.Orig.Example", "ns1.orig.example"}
	f := &fakeScoped{nameServers: current}
	r, out := newDomainRunner(t, f, allowanceFor("p16", true))

	if err := p16(r, p16Tokens); err != nil {
		t.Fatal(err)
	}

	text := out.String()
	assertNothingRefused(t, text)
	assertNoTokens(t, "the transcript", text)
	if strings.Contains(text, "someone@example.org") {
		t.Errorf("contact details reached the transcript:\n%s", text)
	}
	const d, g = "/v1/domains/example.org", "/v1/domains/example.org/records/"
	want := []string{
		"ro GET " + g + "TXT/_probe-ab12-p16-missing",
		"ro GET " + g + "A",
		"ro GET " + d,
		"ro PUT " + g + `TXT/_probe-ab12-p16-ro [{"data":"x","ttl":3600}]`,
		"ro DELETE " + g + "TXT/_probe-ab12-p16-ro",
		"dns GET " + d,
		"dns PUT " + g + `TXT/_probe-ab12-p16-dns [{"data":"y","ttl":3600}]`,
		"dns GET " + g + "TXT/_probe-ab12-p16-dns",
		"main GET " + d,
		"dns PATCH " + d + ` {"nameServers":["NS2.Orig.Example","ns1.orig.example"]}`,
		"main GET " + d,
	}
	if got := f.sent(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	assertLines(t, text,
		"P16 read-only token: step 1, read of a missing group: status 200.",
		"P16 read-only token: step 2, read of all A records: status 200.",
		"P16 read-only token: step 3, read of the domain: status 200.",
		"P16 read-only token: step 4, write of a TXT group: status 403.",
		"P16 read-only token: step 5, delete of that group: status 403.",
		"P16 DNS-only token: step 7, write of a TXT group: status 200.",
		"P16 DNS-only token: step 8, read of that group: status 200.",
		"P16 DNS-only token: step 10, nameserver PATCH with the current nameservers: status 204.",
		"P16 nameservers after the PATCH: unchanged ([NS2.Orig.Example ns1.orig.example]).",
	)
	if !strings.Contains(text, "no-op") {
		t.Errorf("the PATCH must be labelled as a no-op:\n%s", text)
	}
	if len(r.created) != 2 {
		t.Errorf("groups registered for cleanup: %v, want both TXT groups", r.created)
	}
	if r.finishing {
		t.Error("p16 left the runner in finishing mode")
	}
}

func TestP16SendsNoPatchWhenItCannotReadTheNameservers(t *testing.T) {
	cases := map[string]*fakeScoped{
		"read fails":     {nameServers: originalNS, mainDomainStatus: 500},
		"one nameserver": {nameServers: originalNS[:1]},
		"no nameservers": {},
	}
	for name, f := range cases {
		r, out := newDomainRunner(t, f, allowanceFor("p16", true))

		if err := p16(r, p16Tokens); err != nil {
			t.Errorf("%s: p16 = %v, want nil", name, err)
		}
		if got := f.only(" PATCH "); len(got) != 0 {
			t.Errorf("%s: a PATCH was sent without knowing the nameservers: %v", name, got)
		}
		if !strings.Contains(out.String(), "P16 DNS-only token: step 10, nameserver PATCH: NOT SENT") {
			t.Errorf("%s: transcript must say that step 10 was skipped:\n%s", name, out)
		}
	}
}

func TestP16FailsIfTheNameserversChanged(t *testing.T) {
	f := &fakeScoped{nameServers: originalNS, corruptOnPatch: true}
	r, out := newDomainRunner(t, f, allowanceFor("p16", true))

	err := p16(r, p16Tokens)

	if err == nil {
		t.Fatalf("p16 = nil although the nameservers changed\n%s", out)
	}
	for _, want := range append([]string{"by hand"}, originalNS...) {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %s", want, err)
		}
	}
	assertNoTokens(t, "the error", err.Error())
}

func TestP16FailsIfItCannotCheckTheNameservers(t *testing.T) {
	f := &fakeScoped{nameServers: originalNS}
	r, _ := newDomainRunner(t, f, allowanceFor("p16", true))
	f.hook(func(n int) {
		if n == 10 { // the PATCH: from here on the main token cannot read the domain
			f.mainDomainStatus = 503
		}
	})

	err := p16(r, p16Tokens)

	if err == nil || !strings.Contains(err.Error(), originalNS[0]) || !strings.Contains(err.Error(), "status 503") {
		t.Errorf("p16 = %v, want an error naming the nameservers and the failed read", err)
	}
}

func TestP16ARefusedPatchIsAResultNotAFailure(t *testing.T) {
	f := &fakeScoped{nameServers: originalNS, patchStatus: 403}
	r, out := newDomainRunner(t, f, allowanceFor("p16", true))

	if err := p16(r, p16Tokens); err != nil {
		t.Fatal(err)
	}
	assertLines(t, out.String(),
		"P16 DNS-only token: step 10, nameserver PATCH with the current nameservers: status 403.",
		"P16 nameservers after the PATCH: unchanged ([ns1.orig.example ns2.orig.example]).",
	)
}

func TestP16WithoutTheAllowanceSendsNoPatch(t *testing.T) {
	f := &fakeScoped{nameServers: originalNS}
	r, _ := newDomainRunner(t, f, allowance{})

	if err := p16(r, p16Tokens); err != nil {
		t.Fatal(err)
	}
	if got := f.only(" PATCH "); len(got) != 0 {
		t.Errorf("the guard let a PATCH through: %v", got)
	}
}

func TestP16ChecksTheNameserversEvenWhenInterrupted(t *testing.T) {
	// Requests 1 to 8 use the extra tokens, 9 reads the nameservers, 10 is
	// the PATCH, 11 the check.
	for n := 1; n <= 11; n++ {
		f := &fakeScoped{nameServers: originalNS}
		r, out := newDomainRunner(t, f, allowanceFor("p16", true))
		at := n
		f.hook(func(i int) {
			if i == at {
				r.interrupt()
			}
		})

		if err := p16(r, p16Tokens); err != nil {
			t.Errorf("interrupt at %d: p16 = %v", n, err)
		}
		got := f.sent()
		wantRequests := n
		if n == 10 {
			wantRequests = 11 // the PATCH went out, so the check must follow
		}
		if len(got) != wantRequests {
			t.Errorf("interrupt at %d: %d requests were sent, want %d:\n%s", n, len(got), wantRequests, strings.Join(got, "\n"))
		}
		if patched := len(f.only(" PATCH ")) > 0; patched && !strings.Contains(out.String(), "P16 nameservers after the PATCH: unchanged") {
			t.Errorf("interrupt at %d: a PATCH was sent but the nameservers were not checked:\n%s", n, out)
		}
		if n := strings.Count(out.String(), "NOT SENT: interrupted"); n > 1 {
			t.Errorf("interrupt at %d: %d NOT SENT entries, want at most one", at, n)
		}
	}
}

func TestExecuteP16CleansUpWithTheMainToken(t *testing.T) {
	f := &fakeScoped{nameServers: originalNS}
	r, out := newDomainRunner(t, f, allowanceFor("p16", true))

	if err := execute(r, "p16", p16Tokens, "transcript.md"); err != nil {
		t.Fatalf("execute = %v\n%s", err, out)
	}
	assertNothingRefused(t, out.String())
	assertNoTokens(t, "the transcript", out.String())
	want := []string{
		"main DELETE /v1/domains/example.org/records/TXT/_probe-ab12-p16-ro",
		"main DELETE /v1/domains/example.org/records/TXT/_probe-ab12-p16-dns",
	}
	if got := f.only("main DELETE"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("cleanup sent %v, want %v", got, want)
	}
	if got := f.only(" PATCH "); len(got) != 1 || !strings.HasSuffix(got[0], ` {"nameServers":["ns1.orig.example","ns2.orig.example"]}`) {
		t.Errorf("PATCHes = %v, want one, with exactly the current nameservers", got)
	}
}

func TestSRVAndTokenProbesCleanUpAfterAnInterrupt(t *testing.T) {
	// Requests 1 to 9 are the snapshot.
	srv := &fakeSRV{}
	r, out := newDomainRunner(t, srv, allowance{})
	srv.hook(func(n int) {
		if n == 11 { // the list after p15's first write
			r.interrupt()
		}
	})
	err := execute(r, "p15", probeArgs{}, "transcript.md")
	if !errors.Is(err, errInterrupted) {
		t.Errorf("p15: execute = %v, want errInterrupted\n%s", err, out)
	}
	want := "PUT /v1/domains/example.org/records/SRV/" + p15Name + " [" + sipBody + "]\n" +
		"DELETE /v1/domains/example.org/records/SRV/" + p15Name
	if got := strings.Join(srv.sent(), "\n"); got != want {
		t.Errorf("p15 writes:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(out.String(), "P15 A:") {
		t.Errorf("an interrupted p15 must not draw conclusions:\n%s", out)
	}

	scoped := &fakeScoped{nameServers: originalNS}
	r, out = newDomainRunner(t, scoped, allowanceFor("p16", true))
	scoped.hook(func(n int) {
		if n == 9+4 { // the write with the read-only token
			r.interrupt()
		}
	})
	err = execute(r, "p16", p16Tokens, "transcript.md")
	if !errors.Is(err, errInterrupted) {
		t.Errorf("p16: execute = %v, want errInterrupted\n%s", err, out)
	}
	if got := scoped.only("DELETE"); len(got) != 1 || got[0] != "main DELETE /v1/domains/example.org/records/TXT/_probe-ab12-p16-ro" {
		t.Errorf("p16 cleanup sent %v, want one DELETE of the read-only token's group with the main token", got)
	}
	if got := scoped.only(" PATCH "); len(got) != 0 {
		t.Errorf("p16 sent a PATCH after the interrupt: %v", got)
	}
}

func TestExecuteP15LeavesACleanZone(t *testing.T) {
	for _, f := range []*fakeSRV{{}, {plainReplacesAll: true}, {longForm: "refuse"}, {longForm: "bare"}} {
		r, out := newDomainRunner(t, f, allowance{})
		if err := execute(r, "p15", probeArgs{}, "transcript.md"); err != nil {
			t.Errorf("%+v: execute = %v\n%s", f.longForm, err, out)
		}
		assertNothingRefused(t, out.String())
		if f.left() != 0 {
			t.Errorf("p15 left %d SRV record(s) behind", f.left())
		}
	}
}

func TestRunRefusesP16WithoutItsFlagOrTokens(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&requests, 1) }))
	defer srv.Close()
	t.Setenv("GODADDY_API_TOKEN", "secret-token-value")
	t.Setenv("GODADDY_PROBE_DOMAIN", "example.org")
	t.Setenv("GODADDY_PROBE_BASEURL", srv.URL)
	outDir := filepath.Join(t.TempDir(), "out")

	cases := []struct {
		name     string
		ro, dns  string
		zoneWide bool
		want     string
	}{
		{"no flag", roToken, dnsToken, false, "-allow-zone-wide"},
		{"no tokens", "", "", true, "GODADDY_PROBE_READONLY_TOKEN"},
		{"no read-only token", "", dnsToken, true, "GODADDY_PROBE_READONLY_TOKEN"},
		{"no DNS-only token", roToken, "", true, "GODADDY_PROBE_DNSONLY_TOKEN"},
		{"blank DNS-only token", roToken, "  ", true, "GODADDY_PROBE_DNSONLY_TOKEN"},
	}
	for _, tc := range cases {
		t.Setenv("GODADDY_PROBE_READONLY_TOKEN", tc.ro)
		t.Setenv("GODADDY_PROBE_DNSONLY_TOKEN", tc.dns)
		err := run("p16", outDir, tc.zoneWide, false, "")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: run = %v, want a refusal naming %s", tc.name, err, tc.want)
			continue
		}
		assertNoTokens(t, "the error", err.Error())
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Errorf("the output directory was created (stat error: %v)", err)
	}
	if n := atomic.LoadInt32(&requests); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

func TestP1HasFourCases(t *testing.T) {
	api := &fakeAPI{}
	r, out := newTestRunner(t, api)

	p1(r)

	if got := api.got(); len(got) != 4 {
		t.Errorf("p1 sent %d requests, want 4: %v", len(got), got)
	}
	if strings.Contains(out.String(), "NOSCOPE") || strings.Contains(out.String(), "without DNS scope") {
		t.Errorf("p1 still talks about the token without DNS scope:\n%s", out)
	}
}
