package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	dcData     = "_domainconnect.gd.domaincontrol.com"
	dcOriginal = `[{"data":"_domainconnect.gd.domaincontrol.com","name":"_domainconnect","ttl":3600,"type":"CNAME"}]`
	dcRestore  = `CNAME/_domainconnect [{"data":"_domainconnect.gd.domaincontrol.com","ttl":3600}]`
	dcPath     = "/v1/domains/example.org/records/CNAME/_domainconnect"
)

// domainConnectNow returns the fake's _domainconnect CNAME group as JSON.
func (f *fakeZone) domainConnectNow() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	recs := f.groups["CNAME"]["_domainconnect"]
	if recs == nil {
		return "[]"
	}
	b, _ := json.Marshal(recs)
	return string(b)
}

func assertDomainConnectIntact(t *testing.T, f *fakeZone) {
	t.Helper()
	if got := f.domainConnectNow(); got != dcOriginal {
		t.Errorf("_domainconnect = %s, want %s", got, dcOriginal)
	}
	assertRealRecordsIntact(t, f) // imap, www, the A record and the rest
}

func TestCheckWriteRealGroup(t *testing.T) {
	const domain, prefix = "example.org", "_probe-ab12-"
	const root = "/v1/domains/example.org/records/"
	p19 := allowanceFor("p19", true)
	cases := []struct {
		name      string
		method    string
		path      string
		wantAllow bool
	}{
		{"put the record", "PUT", root + "CNAME/_domainconnect", true},
		{"delete the record", "DELETE", root + "CNAME/_domainconnect", true},
		{"patch the record", "PATCH", root + "CNAME/_domainconnect", false},
		{"post to the record", "POST", root + "CNAME/_domainconnect", false},
		{"another real CNAME", "PUT", root + "CNAME/www", false},
		{"delete another real CNAME", "DELETE", root + "CNAME/imap", false},
		{"the same name, another type", "PUT", root + "TXT/_domainconnect", false},
		{"a longer name", "PUT", root + "CNAME/_domainconnect2", false},
		{"a shorter name", "PUT", root + "CNAME/_domainconnec", false},
		{"a name below it", "PUT", root + "CNAME/x._domainconnect", false},
		{"upper-case name", "PUT", root + "CNAME/_DOMAINCONNECT", false},
		{"lower-case type", "PUT", root + "cname/_domainconnect", false},
		{"trailing slash", "PUT", root + "CNAME/_domainconnect/", false},
		{"type-wide CNAME", "PUT", root + "CNAME", false},
		{"type-wide A", "PUT", root + "A", false},
		{"the domain", "PATCH", "/v1/domains/example.org", false},
		{"another domain", "PUT", "/v1/domains/other.org/records/CNAME/_domainconnect", false},
		{"a probe record is still allowed", "PUT", root + "TXT/_probe-ab12-x", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkWrite(tc.method, tc.path, domain, prefix, p19)
			if allowed := err == nil; allowed != tc.wantAllow {
				t.Errorf("checkWrite(%s %s) allowed=%v, want %v (err: %v)", tc.method, tc.path, allowed, tc.wantAllow, err)
			}
		})
	}
	for _, other := range probeNames {
		if other == "p19" {
			continue
		}
		for _, method := range []string{"PUT", "DELETE"} {
			if err := checkWrite(method, root+"CNAME/_domainconnect", domain, prefix, allowanceFor(other, true)); err == nil {
				t.Errorf("%s must not %s the _domainconnect record", other, method)
			}
		}
	}
	if err := checkWrite("PUT", root+"CNAME/_domainconnect", domain, prefix, allowanceFor("p19", false)); err == nil {
		t.Error("p19 without -allow-zone-wide must not write the record")
	}
	// A half-filled pair allows nothing.
	for _, half := range []allowance{{realGroup: [2]string{"CNAME", ""}}, {realGroup: [2]string{"", "_domainconnect"}}} {
		for _, path := range []string{root + "CNAME/_domainconnect", root + "CNAME/www"} {
			if err := checkWrite("PUT", path, domain, prefix, half); err == nil {
				t.Errorf("allowance %+v let PUT %s through", half, path)
			}
		}
	}
	if readOnly["p19"] {
		t.Error("p19 writes; it must not be listed as read-only")
	}
}

func TestRunRefusesP19WithoutBothFlags(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&requests, 1) }))
	defer srv.Close()
	t.Setenv("GODADDY_API_TOKEN", "tok")
	t.Setenv("GODADDY_PROBE_DOMAIN", "example.org")
	t.Setenv("GODADDY_PROBE_BASEURL", srv.URL)
	outDir := filepath.Join(t.TempDir(), "out")

	for _, flags := range [][2]bool{{false, false}, {true, false}, {false, true}} {
		err := run("p19", outDir, flags[0], flags[1], "")
		if err == nil {
			t.Fatalf("run(p19, allow-zone-wide=%v, replace-real-records=%v) = nil, want a refusal", flags[0], flags[1])
		}
		for _, want := range []string{"-allow-zone-wide", "-replace-real-records", "_domainconnect"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("flags %v: refusal lacks %q: %s", flags, want, err)
			}
		}
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Errorf("the output directory was created (stat error: %v)", err)
	}
	if n := atomic.LoadInt32(&requests); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

func TestExecuteP19OnAnOrdinaryRecord(t *testing.T) {
	f := newRealZone()
	r, out := newDomainRunner(t, f, allowanceFor("p19", true))

	if err := execute(r, "p19", probeArgs{}, "transcript.md"); err != nil {
		t.Fatalf("execute = %v\n%s", err, out)
	}

	text := out.String()
	assertNothingRefused(t, text)
	assertDomainConnectIntact(t, f)
	want := []string{
		"PUT " + dcPath + ` [{"data":"probe-changed.example.net","ttl":3600}]`,
		"DELETE " + dcPath,
		"PUT " + dcPath + ` [{"data":"_domainconnect.gd.domaincontrol.com","ttl":3600}]`,
	}
	if got := f.sent(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("writes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	block := strings.Index(text, "Real _domainconnect record, to recreate by hand if the restore fails:")
	firstWrite := strings.Index(text, "`PUT "+dcPath+"`")
	if block < 0 || firstWrite < 0 || block > firstWrite || !strings.Contains(text[block:firstWrite], dcOriginal) {
		t.Errorf("the original (at %d) must be in the transcript, with its JSON, before the first write (at %d):\n%s", block, firstWrite, text)
	}
	assertLines(t, text,
		"P19 change: status 200; _domainconnect now holds probe-changed.example.net (CHANGED)",
		"P19 delete: status 204; straight afterwards the record is ABSENT",
		"- 10 s: status 200, 0 record(s)",
		"- 20 s: status 200, 0 record(s)",
		"- 30 s: status 200, 0 record(s)",
		"- 60 s: status 200, 0 record(s)",
		"P19 after 60 s: the record is still ABSENT",
		"P19 restored: _domainconnect is back as it was.",
		"P19 summary: per-name change accepted and applied; per-name delete accepted and applied; "+
			"GoDaddy did not put it back within 60 s; restored: yes",
	)
	if n := strings.Count(text, "`GET /v1/domains/example.org/records/CNAME`"); n != 2 {
		t.Errorf("all CNAME records were listed %d times in the transcript, want 2", n)
	}
	if len(r.created) != 0 {
		t.Errorf("p19 registered groups for cleanup: %v", r.created)
	}
}

func TestP19OnAProtectedRecord(t *testing.T) {
	f := newRealZone()
	f.domainConnect = "protected"
	r, out := newDomainRunner(t, f, allowanceFor("p19", true))

	if err := p19(r, snapshotFor(t, r)); err != nil {
		t.Fatalf("p19 = %v\n%s", err, out)
	}
	assertDomainConnectIntact(t, f)
	if got := f.restoresSent(); len(got) != 0 {
		t.Errorf("the record never changed, yet a restore was sent: %v", got)
	}
	assertLines(t, out.String(),
		"P19 change: status 200; _domainconnect now holds "+dcData+" (UNCHANGED)",
		"P19 delete: status 204; straight afterwards the record is PRESENT holding "+dcData,
		"P19 restored: _domainconnect is back as it was.",
		"P19 summary: per-name change accepted but not applied; per-name delete accepted but not applied; GoDaddy n/a; restored: yes",
	)
	if strings.Contains(out.String(), "- 10 s:") {
		t.Errorf("the record was never absent; there is nothing to watch for:\n%s", out)
	}
}

func TestP19WhenTheWritesAreRefused(t *testing.T) {
	f := newRealZone()
	f.domainConnect = "refused"
	r, out := newDomainRunner(t, f, allowanceFor("p19", true))

	if err := p19(r, snapshotFor(t, r)); err != nil {
		t.Fatalf("p19 = %v\n%s", err, out)
	}
	assertDomainConnectIntact(t, f)
	if got := f.restoresSent(); len(got) != 0 {
		t.Errorf("the record never changed, yet a restore was sent: %v", got)
	}
	assertLines(t, out.String(),
		"P19 change: status 422; _domainconnect now holds "+dcData+" (UNCHANGED)",
		"P19 delete: status 409; straight afterwards the record is PRESENT holding "+dcData,
		"P19 summary: per-name change refused (status 422); per-name delete refused (status 409); GoDaddy n/a; restored: yes",
	)
}

func TestP19WhenGoDaddyPutsTheRecordBack(t *testing.T) {
	f := newRealZone()
	f.comesBackAfter = 3 // the read straight after the delete, the one at 10 s, then the one at 20 s
	r, out := newDomainRunner(t, f, allowanceFor("p19", true))

	if err := p19(r, snapshotFor(t, r)); err != nil {
		t.Fatalf("p19 = %v\n%s", err, out)
	}
	assertDomainConnectIntact(t, f)
	if got := f.restoresSent(); len(got) != 0 {
		t.Errorf("GoDaddy put the record back, yet a restore was sent: %v", got)
	}
	text := out.String()
	assertLines(t, text,
		"P19 delete: status 204; straight afterwards the record is ABSENT",
		"- 10 s: status 200, 0 record(s)",
		"- 20 s: status 200, 1 record(s): "+dcData,
		"P19 after 60 s: the record CAME BACK holding "+dcData+" after about 20 s",
		"P19 summary: per-name change accepted and applied; per-name delete accepted and applied; "+
			"GoDaddy put it back after about 20 s; restored: yes",
	)
	if strings.Contains(text, "- 30 s:") {
		t.Errorf("the record was back at 20 s; the later reads are not needed:\n%s", text)
	}
}

func TestP19RestoreRetriesUntilAccepted(t *testing.T) {
	f := newRealZone()
	f.restoreRejects = 2
	r, out := newDomainRunner(t, f, allowanceFor("p19", true))

	if err := p19(r, snapshotFor(t, r)); err != nil {
		t.Fatalf("p19 = %v\n%s", err, out)
	}
	assertDomainConnectIntact(t, f)
	if got := f.restoresSent(); len(got) != 3 || got[0] != dcRestore || got[2] != dcRestore {
		t.Errorf("restores sent: %v, want three of %s", got, dcRestore)
	}
}

func TestExecuteP19RestoreNeverAccepted(t *testing.T) {
	f := newRealZone()
	f.restoreRejects = -1
	r, out := newDomainRunner(t, f, allowanceFor("p19", true))

	err := execute(r, "p19", probeArgs{}, "transcript.md")

	if exitCode(err) != 1 {
		t.Fatalf("execute = %v (exit %d), want exit 1\n%s", err, exitCode(err), out)
	}
	text := out.String()
	last := lastLine(text)
	for _, want := range []string{"_domainconnect", "restore", "FAILED", dcOriginal} {
		if !strings.Contains(err.Error(), want) && want != "FAILED" {
			t.Errorf("error lacks %q: %s", want, err)
		}
		if !strings.Contains(last, want) {
			t.Errorf("last line of the transcript lacks %q: %s", want, last)
		}
	}
	if got := f.restoresSent(); len(got) != 20 {
		t.Errorf("the restore was sent %d times, want 20", len(got))
	}
	if !strings.Contains(text, "restored: NO") {
		t.Errorf("the summary must say that the record was not restored:\n%s", text)
	}
}

func TestP19RestoreStopsAtOnceWhenNotAuthorised(t *testing.T) {
	f := newRealZone()
	f.restoreRejects, f.restoreStatus = -1, 401
	r, _ := newDomainRunner(t, f, allowanceFor("p19", true))

	err := p19(r, snapshotFor(t, r))

	if err == nil || !strings.Contains(err.Error(), dcOriginal) {
		t.Errorf("p19 = %v, want an error with the original", err)
	}
	if got := f.restoresSent(); len(got) != 1 {
		t.Errorf("the restore was sent %d times, want 1", len(got))
	}
}

func TestP19RestoreThatComesBackAltered(t *testing.T) {
	f := newRealZone()
	f.restoreAlters = true
	r, out := newDomainRunner(t, f, allowanceFor("p19", true))

	err := p19(r, snapshotFor(t, r))

	if err == nil {
		t.Fatalf("p19 = nil although the restored record differs from the original\n%s", out)
	}
	for _, want := range []string{"_domainconnect", "restore", dcOriginal} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %s", want, err)
		}
	}
	if got := f.restoresSent(); len(got) != 2 {
		t.Errorf("restores sent: %v, want two (once more after the check failed)", got)
	}
}

func TestP19WithoutTheRecord(t *testing.T) {
	f := newRealZone()
	f.mu.Lock()
	delete(f.groups["CNAME"], "_domainconnect")
	f.mu.Unlock()
	r, _ := newDomainRunner(t, f, allowanceFor("p19", true))

	err := p19(r, snapshotFor(t, r))

	if err == nil || !strings.Contains(err.Error(), "no _domainconnect CNAME") {
		t.Fatalf("p19 = %v, want an error saying the zone has no such record", err)
	}
	if got := f.sent(); len(got) != 0 {
		t.Errorf("p19 sent writes: %v", got)
	}
}

func TestP19RefusesWhenTheRecordChangedSinceTheSnapshot(t *testing.T) {
	f := newRealZone()
	r, _ := newDomainRunner(t, f, allowanceFor("p19", true))
	before := snapshotFor(t, r)
	f.seed("CNAME", "_domainconnect", map[string]interface{}{"data": "elsewhere.example.net", "ttl": 3600})

	err := p19(r, before)

	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("p19 = %v, want an error saying the record changed", err)
	}
	if got := f.sent(); len(got) != 0 {
		t.Errorf("p19 sent writes: %v", got)
	}
}

// An interrupt may arrive while any request is in flight. Whichever one it
// is, the record must be back as it was at the end.
func TestP19InterruptedAtAnyRequestRestores(t *testing.T) {
	clean := newRealZone()
	r, _ := newDomainRunner(t, clean, allowanceFor("p19", true))
	before := snapshotFor(t, r)
	base := clean.requestCount()
	if err := p19(r, before); err != nil {
		t.Fatal(err)
	}
	total := clean.requestCount() - base
	if total < 12 {
		t.Fatalf("an uninterrupted p19 sent %d requests; expected at least 12", total)
	}

	for n := 1; n <= total; n++ {
		n := n
		t.Run(fmt.Sprintf("interrupt during request %d of %d", n, total), func(t *testing.T) {
			f := newRealZone()
			r, out := newDomainRunner(t, f, allowanceFor("p19", true))
			before := snapshotFor(t, r)
			base := f.requestCount()
			f.hook(func(i int) {
				if i == base+n {
					r.interrupt()
				}
			})

			err := p19(r, before)

			if err != nil {
				t.Errorf("p19 = %v, want nil: the fake accepts every restore", err)
			}
			assertDomainConnectIntact(t, f)
			if t.Failed() {
				t.Logf("transcript:\n%s", out)
			}
			if !r.interrupted() {
				t.Error("the fake never interrupted the runner")
			}
			if r.finishing {
				t.Error("p19 left the runner in finishing mode")
			}
			for _, w := range f.sent() {
				if strings.Fields(w)[1] != dcPath {
					t.Errorf("a write went somewhere else: %s", w)
				}
			}
		})
	}
}

func TestExecuteP19InterruptedExits130(t *testing.T) {
	f := newRealZone()
	r, out := newDomainRunner(t, f, allowanceFor("p19", true))
	f.hook(func(i int) {
		if i == 9+5 { // snapshot, then the DELETE
			r.interrupt()
		}
	})

	err := execute(r, "p19", probeArgs{}, "transcript.md")

	if exitCode(err) != 130 {
		t.Errorf("execute = %v (exit %d), want exit 130\n%s", err, exitCode(err), out)
	}
	assertDomainConnectIntact(t, f)
	if !strings.Contains(out.String(), "P19 restored: _domainconnect is back as it was.") {
		t.Errorf("the restore must run and say so:\n%s", out)
	}
}
