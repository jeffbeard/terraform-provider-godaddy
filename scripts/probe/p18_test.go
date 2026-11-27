package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	realA     = `[{"data":"192.0.2.10","name":"@","ttl":600,"type":"A"}]`
	realCNAME = `[{"data":"_domainconnect.gd.domaincontrol.com","name":"_domainconnect","ttl":3600,"type":"CNAME"},` +
		`{"data":"imap.example.net","name":"imap","ttl":3600,"type":"CNAME"},` +
		`{"data":"@","name":"www","ttl":1800,"type":"CNAME"}]`
	// What the restore sends: the same records without their type.
	restoreA     = `A [{"data":"192.0.2.10","name":"@","ttl":600}]`
	restoreCNAME = `CNAME [{"data":"_domainconnect.gd.domaincontrol.com","name":"_domainconnect","ttl":3600},` +
		`{"data":"imap.example.net","name":"imap","ttl":3600},{"data":"@","name":"www","ttl":1800}]`
)

// newRealZone returns a fake zone that looks like the probe domain: one
// apex A record, three CNAMEs, and records of other types.
func newRealZone() *fakeZone {
	f := &fakeZone{}
	f.seed("A", "@", map[string]interface{}{"data": "192.0.2.10", "ttl": 600})
	f.seed("CNAME", "imap", map[string]interface{}{"data": "imap.example.net", "ttl": 3600})
	f.seed("CNAME", "www", map[string]interface{}{"data": "@", "ttl": 1800})
	f.seed("CNAME", "_domainconnect", map[string]interface{}{"data": "_domainconnect.gd.domaincontrol.com", "ttl": 3600})
	f.seed("MX", "@", map[string]interface{}{"data": "mx1.example.net", "priority": 10, "ttl": 3600})
	f.seed("TXT", "@", map[string]interface{}{"data": "v=spf1 -all", "ttl": 3600})
	return f
}

// realRecords returns the fake's records of one type whose names are not
// probe names, as a JSON array in a fixed order, with every field.
func (f *fakeZone) realRecords(recordType string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names, out []string
	for name := range f.groups[recordType] {
		if !strings.HasPrefix(name, "_probe-") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		for _, rec := range f.groups[recordType][name] {
			b, _ := json.Marshal(rec)
			out = append(out, string(b))
		}
	}
	return "[" + strings.Join(out, ",") + "]"
}

func (f *fakeZone) probeRecords() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for recordType, groups := range f.groups {
		for name := range groups {
			if strings.HasPrefix(name, "_probe-") {
				out = append(out, recordType+" "+name)
			}
		}
	}
	return out
}

func (f *fakeZone) restoresSent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.restores...)
}

func (f *fakeZone) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func assertRealRecordsIntact(t *testing.T, f *fakeZone) {
	t.Helper()
	if got := f.realRecords("A"); got != realA {
		t.Errorf("real A records = %s, want %s", got, realA)
	}
	if got := f.realRecords("CNAME"); got != realCNAME {
		t.Errorf("real CNAME records = %s, want %s", got, realCNAME)
	}
	if got := f.realRecords("MX") + f.realRecords("TXT"); !strings.Contains(got, "mx1.example.net") || !strings.Contains(got, "v=spf1 -all") {
		t.Errorf("records of other types changed: %s", got)
	}
}

// snapshotFor reads the zone as execute would before the probe.
func snapshotFor(t *testing.T, r *runner) []zoneRecord {
	t.Helper()
	before, err := r.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return before
}

func assertP18Writes(t *testing.T, writes []string) {
	t.Helper()
	const root = "/v1/domains/example.org/records/"
	assertOnlyProbeWrites(t, writes, "PUT "+root+"A", "PUT "+root+"CNAME")
}

func TestP18FlagsAndAllowance(t *testing.T) {
	const domain, prefix = "example.org", "_probe-ab12-"
	p18 := allowanceFor("p18", true)
	for _, recordType := range []string{"A", "CNAME"} {
		if err := checkWrite("PUT", "/v1/domains/example.org/records/"+recordType, domain, prefix, p18); err != nil {
			t.Errorf("p18 may replace %s: %v", recordType, err)
		}
	}
	for _, recordType := range []string{"TXT", "MX", "AAAA", "SRV", "NS"} {
		if err := checkWrite("PUT", "/v1/domains/example.org/records/"+recordType, domain, prefix, p18); err == nil {
			t.Errorf("p18 must not replace %s", recordType)
		}
	}
	if err := checkWrite("PATCH", "/v1/domains/example.org", domain, prefix, p18); err == nil {
		t.Error("p18 must not change the domain")
	}
	for _, path := range []string{"/v1/domains/example.org/records/A/@", "/v1/domains/example.org/records/CNAME/www"} {
		for _, method := range []string{"PUT", "DELETE"} {
			if err := checkWrite(method, path, domain, prefix, p18); err == nil {
				t.Errorf("p18 must not %s %s: real records only go through the type-wide PUT", method, path)
			}
		}
	}
	for _, other := range []string{"p9", "p13", "p17"} {
		if allowanceFor(other, true).mayReplace("A") || allowanceFor(other, true).mayReplace("CNAME") {
			t.Errorf("%s must not be able to replace A or CNAME", other)
		}
	}
	if readOnly["p18"] {
		t.Error("p18 writes; it must not be listed as read-only")
	}
}

func TestRunRefusesP18WithoutBothFlags(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&requests, 1) }))
	defer srv.Close()
	t.Setenv("GODADDY_API_TOKEN", "tok")
	t.Setenv("GODADDY_PROBE_DOMAIN", "example.org")
	t.Setenv("GODADDY_PROBE_BASEURL", srv.URL)
	outDir := filepath.Join(t.TempDir(), "out")

	for _, flags := range [][2]bool{{false, false}, {true, false}, {false, true}} {
		err := run("p18", outDir, flags[0], flags[1], "")
		if err == nil {
			t.Fatalf("run(p18, allow-zone-wide=%v, replace-real-records=%v) = nil, want a refusal", flags[0], flags[1])
		}
		for _, want := range []string{"-allow-zone-wide", "-replace-real-records", "removes", "restores", "real A and CNAME records"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("flags %v: refusal lacks %q: %s", flags, want, err)
			}
		}
	}
	// The second flag alone unlocks nothing for the other probes.
	if err := run("p9", outDir, false, true, ""); err == nil {
		t.Error("run(p9) with only -replace-real-records = nil, want a refusal")
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Errorf("the output directory was created (stat error: %v)", err)
	}
	if n := atomic.LoadInt32(&requests); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

func TestExecuteP18RemovesAndRestoresRealRecords(t *testing.T) {
	f := newRealZone()
	r, out := newDomainRunner(t, f, allowanceFor("p18", true))

	if err := execute(r, "p18", probeArgs{}, "transcript.md"); err != nil {
		t.Fatalf("execute = %v\n%s", err, out)
	}

	text := out.String()
	assertNothingRefused(t, text)
	assertRealRecordsIntact(t, f)
	if left := f.probeRecords(); len(left) != 0 {
		t.Errorf("probe records left behind: %v", left)
	}
	writes := f.sent()
	assertP18Writes(t, writes)
	if got := f.restoresSent(); len(got) != 2 || got[0] != restoreA || got[1] != restoreCNAME {
		t.Errorf("restores sent:\n%s\nwant:\n%s\n%s", strings.Join(got, "\n"), restoreA, restoreCNAME)
	}
	for _, tc := range []struct{ recordType, originals string }{{"A", realA}, {"CNAME", realCNAME}} {
		block := "Real " + tc.recordType + " records, to recreate by hand if the restore fails:"
		at := strings.Index(text, block)
		firstWrite := strings.Index(text, "`PUT /v1/domains/example.org/records/"+tc.recordType)
		if at < 0 || firstWrite < 0 || at > firstWrite {
			t.Errorf("the %s originals (at %d) must be in the transcript before the first %s write (at %d)", tc.recordType, at, tc.recordType, firstWrite)
			continue
		}
		if !strings.Contains(text[at:firstWrite], tc.originals) {
			t.Errorf("the block of %s originals lacks %s:\n%s", tc.recordType, tc.originals, text[at:firstWrite])
		}
	}
	assertLines(t, text,
		"P18 A empty replace: status 200; records before 3, after 3; DELETED NOTHING",
		"P18 A one-record replace: status 200; probe a was DELETED; real records DELETED: 1 of 1; b holds 192.0.2.3",
		"P18 A restored: 1 real record(s) are back.",
		"P18 CNAME empty replace: status 200; records before 5, after 5; DELETED NOTHING",
		"P18 CNAME one-record replace: status 200; probe a was DELETED; real records DELETED: 3 of 3; b holds b2.example.net",
		"P18 CNAME restored: 3 real record(s) are back.",
		"P18 summary: empty replace deleted nothing for: A, CNAME; one-record replace deleted the other records for: A, CNAME; "+
			"restored: A, CNAME; NOT restored: none",
	)
	// A is finished, restore included, before CNAME starts.
	if a, c := strings.Index(text, "P18 A restored"), strings.Index(text, "Real CNAME records"); a < 0 || c < 0 || a > c {
		t.Errorf("A must be restored (at %d) before CNAME starts (at %d)", a, c)
	}
	const root = "PUT /v1/domains/example.org/records/"
	want := []string{
		root + `A/_probe-ab12-p18-a-a [{"data":"192.0.2.1","ttl":3600}]`,
		root + `A/_probe-ab12-p18-a-b [{"data":"192.0.2.2","ttl":3600}]`,
		root + `A []`,
		root + `A [{"data":"192.0.2.3","name":"_probe-ab12-p18-a-b","ttl":3600}]`,
		root + restoreA,
		root + `CNAME/_probe-ab12-p18-cname-a [{"data":"a.example.net","ttl":3600}]`,
		root + `CNAME/_probe-ab12-p18-cname-b [{"data":"b.example.net","ttl":3600}]`,
		root + `CNAME []`,
		root + `CNAME [{"data":"b2.example.net","name":"_probe-ab12-p18-cname-b","ttl":3600}]`,
		root + restoreCNAME,
	}
	if got := strings.Join(writes[:len(want)], "\n"); got != strings.Join(want, "\n") {
		t.Errorf("writes:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	for _, w := range writes[len(want):] {
		if !strings.HasPrefix(w, "DELETE ") {
			t.Errorf("after the probe only cleanup deletes are expected, got %s", w)
		}
	}
}

func TestP18WhenAnEmptyReplaceDeletesEverything(t *testing.T) {
	f := newRealZone()
	f.emptyDeletes = map[string]bool{"A": true, "CNAME": true}
	r, out := newDomainRunner(t, f, allowanceFor("p18", true))

	if err := p18(r, snapshotFor(t, r)); err != nil {
		t.Fatalf("p18 = %v\n%s", err, out)
	}
	assertRealRecordsIntact(t, f)
	text := out.String()
	assertLines(t, text,
		"P18 A empty replace: status 200; records before 3, after 0; DELETED 3 record(s) (real: 1, probe: 2)",
		"P18 CNAME empty replace: status 200; records before 5, after 0; DELETED 5 record(s) (real: 3, probe: 2)",
		"P18 A restored: 1 real record(s) are back.",
		"P18 CNAME restored: 3 real record(s) are back.",
		"P18 summary: empty replace deleted nothing for: none; one-record replace deleted the other records for: none; "+
			"restored: A, CNAME; NOT restored: none",
	)
	if !strings.Contains(text, "P18 A one-record replace: status 200; inconclusive because") {
		t.Errorf("everything was gone before the one-record replace; that line must be inconclusive:\n%s", text)
	}
}

func TestP18WhenNothingIsDeletedNoRestoreIsSent(t *testing.T) {
	f := newRealZone()
	f.ignoreTypeWide = true
	r, out := newDomainRunner(t, f, allowanceFor("p18", true))

	if err := p18(r, snapshotFor(t, r)); err != nil {
		t.Fatalf("p18 = %v\n%s", err, out)
	}
	assertRealRecordsIntact(t, f)
	if got := f.restoresSent(); len(got) != 0 {
		t.Errorf("every original was still there, yet a restore was sent: %v", got)
	}
	assertLines(t, out.String(),
		"P18 A one-record replace: status 200; probe a was KEPT; real records KEPT; b holds 192.0.2.2",
		"P18 A restored: 1 real record(s) are back.",
	)
}

func TestP18RestoreRetriesUntilAccepted(t *testing.T) {
	f := newRealZone()
	f.restoreRejects = 2
	r, out := newDomainRunner(t, f, allowanceFor("p18", true))

	if err := p18(r, snapshotFor(t, r)); err != nil {
		t.Fatalf("p18 = %v\n%s", err, out)
	}
	assertRealRecordsIntact(t, f)
	got := f.restoresSent()
	if len(got) != 4 || got[0] != restoreA || got[1] != restoreA || got[2] != restoreA || got[3] != restoreCNAME {
		t.Errorf("restores sent: %v, want A three times (two refused), then CNAME", got)
	}
}

func TestExecuteP18RestoreNeverAccepted(t *testing.T) {
	f := newRealZone()
	f.restoreRejects = -1
	r, out := newDomainRunner(t, f, allowanceFor("p18", true))

	err := execute(r, "p18", probeArgs{}, "transcript.md")

	if exitCode(err) != 1 {
		t.Fatalf("execute = %v (exit %d), want exit 1\n%s", err, exitCode(err), out)
	}
	text := out.String()
	last := lastLine(text)
	for _, want := range []string{"A", "restore", "FAILED", realA} {
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
	for _, w := range f.sent() {
		if strings.Contains(w, "/records/CNAME") {
			t.Errorf("CNAME was started although the restore of A failed: %s", w)
		}
	}
	if got := f.realRecords("CNAME"); got != realCNAME {
		t.Errorf("real CNAME records = %s, want them untouched", got)
	}
	if !strings.Contains(text, "NOT restored: A") {
		t.Errorf("the summary must say that A was not restored:\n%s", text)
	}
	if !strings.Contains(text, "RECORDS OUTSIDE THE PROBE PREFIX CHANGED") {
		t.Errorf("the zone check must report the missing record as well:\n%s", text)
	}
}

func TestP18RestoreStopsAtOnceWhenNotAuthorised(t *testing.T) {
	f := newRealZone()
	f.restoreRejects, f.restoreStatus = -1, 403
	r, _ := newDomainRunner(t, f, allowanceFor("p18", true))

	err := p18(r, snapshotFor(t, r))

	if err == nil || !strings.Contains(err.Error(), realA) {
		t.Errorf("p18 = %v, want an error with the originals", err)
	}
	if got := f.restoresSent(); len(got) != 1 {
		t.Errorf("the restore was sent %d times, want 1", len(got))
	}
}

func TestP18RestoreThatComesBackAltered(t *testing.T) {
	f := newRealZone()
	f.restoreAlters = true
	r, out := newDomainRunner(t, f, allowanceFor("p18", true))

	err := p18(r, snapshotFor(t, r))

	if err == nil {
		t.Fatalf("p18 = nil although the restored record differs from the original\n%s", out)
	}
	for _, want := range []string{"A", "restore", realA} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %s", want, err)
		}
	}
	if got := f.restoresSent(); len(got) != 2 || got[0] != restoreA || got[1] != restoreA {
		t.Errorf("restores sent: %v, want A twice (once more after the check failed)", got)
	}
	for _, w := range f.sent() {
		if strings.Contains(w, "/records/CNAME") {
			t.Errorf("CNAME was started although the restore of A failed: %s", w)
		}
	}
}

// An interrupt may arrive while any request is in flight. Whichever one it
// is, the real records must be back at the end.
func TestP18InterruptedAtAnyRequestRestores(t *testing.T) {
	clean := newRealZone()
	r, _ := newDomainRunner(t, clean, allowanceFor("p18", true))
	before := snapshotFor(t, r)
	base := clean.requestCount()
	if err := p18(r, before); err != nil {
		t.Fatal(err)
	}
	total := clean.requestCount() - base
	if total < 18 {
		t.Fatalf("an uninterrupted p18 sent %d requests; expected at least 18", total)
	}

	for n := 1; n <= total; n++ {
		n := n
		t.Run(fmt.Sprintf("interrupt during request %d of %d", n, total), func(t *testing.T) {
			f := newRealZone()
			r, out := newDomainRunner(t, f, allowanceFor("p18", true))
			before := snapshotFor(t, r)
			base := f.requestCount()
			f.hook(func(i int) {
				if i == base+n {
					r.interrupt()
				}
			})

			err := p18(r, before)

			if err != nil {
				t.Errorf("p18 = %v, want nil: the fake accepts every restore", err)
			}
			assertRealRecordsIntact(t, f)
			if t.Failed() {
				t.Logf("transcript:\n%s", out)
			}
			if !r.interrupted() {
				t.Error("the fake never interrupted the runner")
			}
			if r.finishing {
				t.Error("p18 left the runner in finishing mode")
			}
			assertP18Writes(t, f.sent())
		})
	}
}

func TestExecuteP18InterruptedStillRestoresAndCleansUp(t *testing.T) {
	f := newRealZone()
	r, out := newDomainRunner(t, f, allowanceFor("p18", true))
	f.hook(func(i int) {
		if i == 9+6 { // snapshot, then A's one-record replace
			r.interrupt()
		}
	})

	err := execute(r, "p18", probeArgs{}, "transcript.md")

	if exitCode(err) != 130 {
		t.Errorf("execute = %v (exit %d), want exit 130\n%s", err, exitCode(err), out)
	}
	assertRealRecordsIntact(t, f)
	if left := f.probeRecords(); len(left) != 0 {
		t.Errorf("probe records left behind: %v", left)
	}
	for _, w := range f.sent() {
		if strings.Contains(w, "/records/CNAME") {
			t.Errorf("CNAME was started after the interrupt: %s", w)
		}
	}
}

func TestP18WithoutRealARecords(t *testing.T) {
	f := newRealZone()
	f.mu.Lock()
	delete(f.groups, "A")
	f.mu.Unlock()
	r, out := newDomainRunner(t, f, allowanceFor("p18", true))

	if err := p18(r, snapshotFor(t, r)); err != nil {
		t.Fatalf("p18 = %v\n%s", err, out)
	}
	if got := f.restoresSent(); len(got) != 1 || got[0] != restoreCNAME {
		t.Errorf("restores sent: %v, want only CNAME", got)
	}
	text := out.String()
	assertLines(t, text,
		"P18 A empty replace: status 200; records before 2, after 2; DELETED NOTHING",
		"P18 A one-record replace: status 200; probe a was DELETED; no real records; b holds 192.0.2.3",
		"P18 summary: empty replace deleted nothing for: A, CNAME; one-record replace deleted the other records for: A, CNAME; "+
			"restored: CNAME; NOT restored: none",
	)
	if !strings.Contains(text, "Real A records, to recreate by hand if the restore fails:\n\n```json\n[]\n```") {
		t.Errorf("the block of A originals must be there and empty:\n%s", text)
	}
}

// The originals are read once, before the probe. If the zone no longer
// matches them when the type's turn comes, nothing is replaced.
func TestP18RefusesWhenTheZoneChangedSinceTheSnapshot(t *testing.T) {
	f := newRealZone()
	r, out := newDomainRunner(t, f, allowanceFor("p18", true))
	before := snapshotFor(t, r)
	f.seed("A", "new", map[string]interface{}{"data": "192.0.2.77", "ttl": 3600})

	err := p18(r, before)

	if err == nil || !strings.Contains(err.Error(), "A") || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("p18 = %v, want an error saying the A records changed\n%s", err, out)
	}
	for _, w := range f.sent() {
		if len(strings.Split(strings.Fields(w)[1], "/")) == 6 {
			t.Errorf("a type-wide PUT was sent: %s", w)
		}
	}
}

func TestP18RefusesWhenItCannotListTheTypeFirst(t *testing.T) {
	f := newRealZone()
	r, _ := newDomainRunner(t, f, allowanceFor("p18", true))
	before := snapshotFor(t, r)
	f.mu.Lock()
	f.listStatus = map[string]int{"A": 503}
	f.mu.Unlock()

	err := p18(r, before)

	if err == nil || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("p18 = %v, want an error naming the failed read", err)
	}
	for _, w := range f.sent() {
		if len(strings.Split(strings.Fields(w)[1], "/")) == 6 {
			t.Errorf("a type-wide PUT was sent: %s", w)
		}
	}
}

func TestZoneRecordKeepsItsFields(t *testing.T) {
	z := newZoneRecord(map[string]interface{}{"name": "@", "data": "192.0.2.10", "ttl": float64(600)}, "A")
	body, err := json.Marshal(z.withoutType())
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"data":"192.0.2.10","name":"@","ttl":600}`; string(body) != want {
		t.Errorf("withoutType = %s, want %s", body, want)
	}
	if want := `{"data":"192.0.2.10","name":"@","ttl":600,"type":"A"}`; z.String() != want {
		t.Errorf("the record itself must keep its type: %s", z)
	}
}
