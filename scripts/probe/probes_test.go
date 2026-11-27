package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

var (
	originalNS = []string{"ns1.orig.example", "ns2.orig.example"}
	targetNS   = []string{"ns1.target.example", "ns2.target.example"}
)

// fakeDomain is a stateful stand-in for one GoDaddy domain: it holds
// nameservers and applies PATCH requests to them.
type fakeDomain struct {
	mu          sync.Mutex
	nameServers []string
	patches     [][]string // accepted nameserver changes
	writes      []string   // every non-GET request
	requests    int        // every request
	detailFails bool       // answer GET on the domain with a 500

	// onRequest, when set, is called with the number of each request as it
	// arrives, before the request is applied.
	onRequest func(n int)

	rejectAllPatches bool     // answer every PATCH with a 422
	restoreRejects   int      // answer this many restore PATCHes with a 409; -1 means all
	restoreAttempts  int      // PATCHes received that carry the original nameservers
	restoreHeader    string   // Retry-After sent with a rejected restore
	dropChanges      bool     // accept a change to the target with a 200 but do not apply it
	dropRestore      bool     // accept the restore with a 200 but do not apply it
	staleReads       int      // after an accepted change, this many GETs still show the old nameservers
	staleNS          []string // what those stale reads show

	patchStatus      int         // status for rejectAllPatches; 422 when zero
	restoreStatus    int         // status for a rejected restore; 409 when zero
	restoreApplied5x bool        // apply the restore but answer it with a 500
	domainGets       int         // GETs of the domain received
	failDomainGets   map[int]int // number of the domain GET -> status to answer with
}

// hook sets onRequest.
func (f *fakeDomain) hook(fn func(n int)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onRequest = fn
}

func (f *fakeDomain) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(req.Body)
	f.requests++
	if f.onRequest != nil {
		f.onRequest(f.requests)
	}
	if req.Method != "GET" {
		f.writes = append(f.writes, req.Method+" "+req.URL.Path)
	}
	switch {
	case req.URL.Path == "/v1/domains/example.org" && req.Method == "GET":
		f.domainGets++
		if status := f.failDomainGets[f.domainGets]; status != 0 {
			w.WriteHeader(status)
			return
		}
		if f.detailFails {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		shown := f.nameServers
		if f.staleReads > 0 {
			f.staleReads--
			shown = f.staleNS
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"domain": "example.org", "nameServers": shown})
	case req.URL.Path == "/v1/domains/example.org" && req.Method == "PATCH":
		var in struct {
			NameServers []string `json:"nameServers"`
		}
		json.Unmarshal(body, &in)
		isRestore := sameHosts(in.NameServers, originalNS)
		if isRestore {
			f.restoreAttempts++
		}
		switch {
		case f.rejectAllPatches && f.patchStatus != 0:
			w.WriteHeader(f.patchStatus)
		case len(in.NameServers) < 2, f.rejectAllPatches:
			w.WriteHeader(http.StatusUnprocessableEntity)
		case isRestore && f.restoreApplied5x:
			f.nameServers = in.NameServers
			w.WriteHeader(http.StatusInternalServerError)
		case isRestore && f.restoreRejects != 0:
			if f.restoreRejects > 0 {
				f.restoreRejects--
			}
			if f.restoreHeader != "" {
				w.Header().Set("Retry-After", f.restoreHeader)
			}
			if f.restoreStatus != 0 {
				w.WriteHeader(f.restoreStatus)
				return
			}
			w.WriteHeader(http.StatusConflict)
		case isRestore && f.dropRestore, !isRestore && f.dropChanges:
			// accepted, never applied
		default:
			// Like a registrar that validates nothing: accept any two hosts.
			f.nameServers = in.NameServers
			f.patches = append(f.patches, in.NameServers)
		}
	default:
		io.WriteString(w, "[]")
	}
}

func (f *fakeDomain) current() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.nameServers...)
}

func (f *fakeDomain) accepted() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.patches...)
}

func (f *fakeDomain) sentWrites() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes...)
}

func (f *fakeDomain) count() (requests, restoreAttempts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests, f.restoreAttempts
}

func newDomainRunner(t *testing.T, f http.Handler, allow allowance) (*runner, *bytes.Buffer) {
	t.Helper()
	r, out := newTestRunner(t, f)
	r.allow = allow
	return r, out
}

func TestP6RestoresOriginalNameservers(t *testing.T) {
	f := &fakeDomain{nameServers: originalNS}
	r, _ := newDomainRunner(t, f, allowance{domainPatch: true})

	if err := p6(r, targetNS); err != nil {
		t.Fatal(err)
	}

	if got := f.current(); !sameHosts(got, originalNS) {
		t.Fatalf("nameservers after p6 = %v, want the originals %v", got, originalNS)
	}
	// The fake accepted the made-up nameservers, then the target, then the
	// restore. The restore must be the last change.
	if got := f.accepted(); len(got) != 3 || !sameHosts(got[1], targetNS) || !sameHosts(got[2], originalNS) {
		t.Errorf("accepted changes = %v, want three: made-up, target, originals", got)
	}
}

// An interrupt may arrive while any request is in flight. Whichever one it
// is, the domain must end on its original nameservers.
func TestP6InterruptedAtAnyRequestRestores(t *testing.T) {
	clean := &fakeDomain{nameServers: originalNS}
	r, _ := newDomainRunner(t, clean, allowance{domainPatch: true})
	if err := p6(r, targetNS); err != nil {
		t.Fatal(err)
	}
	total, _ := clean.count()
	if total < 12 {
		t.Fatalf("an uninterrupted p6 sent %d requests; expected at least 12", total)
	}

	for n := 1; n <= total; n++ {
		n := n
		t.Run(fmt.Sprintf("interrupt during request %d of %d", n, total), func(t *testing.T) {
			f := &fakeDomain{nameServers: originalNS}
			r, out := newDomainRunner(t, f, allowance{domainPatch: true})
			f.hook(func(i int) {
				if i == n {
					r.interrupt() // on the server's goroutine, as a signal would be on another
				}
			})

			err := p6(r, targetNS)

			if got := f.current(); !sameHosts(got, originalNS) {
				t.Fatalf("nameservers after p6 = %v, want the originals %v\n%s", got, originalNS, out)
			}
			if err != nil {
				t.Errorf("p6 = %v, want nil: the restore worked", err)
			}
			if !r.interrupted() {
				t.Error("the fake never interrupted the runner")
			}
			if got := f.accepted(); len(got) > 0 && !sameHosts(got[len(got)-1], originalNS) {
				t.Errorf("the last accepted change was %v, want the originals", got[len(got)-1])
			}
			if r.finishing {
				t.Error("p6 left the runner in finishing mode")
			}
		})
	}
}

func TestP6InterruptedBeforeAnyChangeSendsNoPatch(t *testing.T) {
	f := &fakeDomain{nameServers: originalNS}
	r, _ := newDomainRunner(t, f, allowance{domainPatch: true})
	f.hook(func(i int) {
		if i == 1 {
			r.interrupt()
		}
	})

	if err := p6(r, targetNS); err != nil {
		t.Fatal(err)
	}
	if got := f.sentWrites(); len(got) != 0 {
		t.Errorf("p6 sent writes although nothing had changed: %v", got)
	}
}

func TestP6DoesNotRestoreWhenEveryChangeWasRefused(t *testing.T) {
	f := &fakeDomain{nameServers: originalNS, rejectAllPatches: true}
	r, out := newDomainRunner(t, f, allowance{domainPatch: true})

	if err := p6(r, targetNS); err != nil {
		t.Fatal(err)
	}
	if _, attempts := f.count(); attempts != 0 {
		t.Errorf("p6 sent %d restore PATCHes; the nameservers were already the originals", attempts)
	}
	if !strings.Contains(out.String(), "Nameserver change NOT confirmed") {
		t.Errorf("transcript does not say the change was not confirmed:\n%s", out)
	}
}

// A read of the domain straight after an accepted change may still show the
// old nameservers. The restore must not take that as "nothing to do".
func TestP6RestoreDoesNotTrustAReadAfterAnAcceptedChange(t *testing.T) {
	f := &fakeDomain{nameServers: originalNS, staleNS: originalNS}
	r, out := newDomainRunner(t, f, allowance{domainPatch: true})
	f.hook(func(i int) {
		if i == 5 { // the PATCH to the target
			r.interrupt()
			f.staleReads = 1 // the hook runs under f.mu
		}
	})

	if err := p6(r, targetNS); err != nil {
		t.Fatal(err)
	}
	if got := f.current(); !sameHosts(got, originalNS) {
		t.Fatalf("nameservers after p6 = %v, want the originals %v\n%s", got, originalNS, out)
	}
	if got := f.accepted(); len(got) != 3 || !sameHosts(got[1], targetNS) {
		t.Fatalf("accepted changes = %v; the test expects request 5 to be the target PATCH", got)
	}
}

func TestP6RestoreRetriesUntilAccepted(t *testing.T) {
	f := &fakeDomain{nameServers: originalNS, restoreRejects: 2}
	r, out := newDomainRunner(t, f, allowance{domainPatch: true})

	if err := p6(r, targetNS); err != nil {
		t.Fatalf("p6 = %v\n%s", err, out)
	}
	if got := f.current(); !sameHosts(got, originalNS) {
		t.Fatalf("nameservers after p6 = %v, want the originals %v", got, originalNS)
	}
	if _, attempts := f.count(); attempts != 3 {
		t.Errorf("restore PATCH was sent %d times, want 3 (two refused, one accepted)", attempts)
	}
	if n := strings.Count(out.String(), "Status: 409"); n != 2 {
		t.Errorf("transcript shows %d refused restores, want 2:\n%s", n, out)
	}
}

func TestP6RestoreNeverAcceptedIsAnError(t *testing.T) {
	f := &fakeDomain{nameServers: originalNS, restoreRejects: -1, restoreHeader: "0"}
	r, out := newDomainRunner(t, f, allowance{domainPatch: true})

	err := p6(r, targetNS)

	if err == nil {
		t.Fatalf("p6 = nil although the restore was never accepted\n%s", out)
	}
	for _, ns := range originalNS {
		if !strings.Contains(err.Error(), ns) {
			t.Errorf("error %q does not name the original nameserver %s", err, ns)
		}
	}
	if _, attempts := f.count(); attempts != 20 {
		t.Errorf("restore PATCH was sent %d times, want 20", attempts)
	}
	if !strings.Contains(out.String(), "RESTORE FAILED") {
		t.Errorf("transcript does not say the restore failed:\n%s", out)
	}
}

// N1: nothing from this run can have taken effect, and the read before the
// restore fails once. The read is tried again; no PATCH is needed.
func TestP6RestoreRetriesTheReadWhenNothingChanged(t *testing.T) {
	// Domain GETs: 1 current, 2 after the error cases, 3 straight after the
	// target PATCH, 4 to 15 the poll, 16 before the restore.
	f := &fakeDomain{nameServers: originalNS, rejectAllPatches: true, patchStatus: 403,
		failDomainGets: map[int]int{16: 429}}
	r, out := newDomainRunner(t, f, allowance{domainPatch: true})

	if err := p6(r, targetNS); err != nil {
		t.Fatalf("p6 = %v, want nil\n%s", err, out)
	}
	if _, attempts := f.count(); attempts != 0 {
		t.Errorf("p6 sent %d restore PATCHes; a second read would have shown the originals", attempts)
	}
	text := out.String()
	restore := text[strings.Index(text, "### Restore the original nameservers"):]
	if !strings.Contains(restore, "Status: 429") || !strings.Contains(restore, "Nothing to restore") {
		t.Errorf("the restore section must show the failed read and then that nothing was needed:\n%s", restore)
	}
}

func TestP6RestoreGivesUpOnTheReadAfterFiveRetries(t *testing.T) {
	fails := map[int]int{}
	for n := 16; n < 100; n++ {
		fails[n] = 503
	}
	f := &fakeDomain{nameServers: originalNS, rejectAllPatches: true, patchStatus: 403, failDomainGets: fails}
	r, _ := newDomainRunner(t, f, allowance{domainPatch: true})

	err := p6(r, targetNS)

	// Six reads (one and five retries), then the PATCH, refused with 403.
	if _, attempts := f.count(); attempts != 1 {
		t.Errorf("p6 sent %d restore PATCHes, want 1 after the reads kept failing", attempts)
	}
	if err == nil || !strings.Contains(err.Error(), originalNS[0]) {
		t.Errorf("p6 = %v, want an error naming the original nameservers", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := f.domainGets - 15 - 12; got != 6 { // less the reads before the restore and the last poll
		t.Errorf("the read before the restore was sent %d times, want 6", got)
	}
}

func TestP6RestoreStopsAtOnceWhenNotAuthorised(t *testing.T) {
	for _, status := range []int{401, 403} {
		f := &fakeDomain{nameServers: originalNS, restoreRejects: -1, restoreStatus: status}
		r, out := newDomainRunner(t, f, allowance{domainPatch: true})

		err := p6(r, targetNS)

		if _, attempts := f.count(); attempts != 1 {
			t.Errorf("status %d: restore PATCH was sent %d times, want 1", status, attempts)
		}
		if err == nil || !strings.Contains(err.Error(), "by hand") ||
			!strings.Contains(err.Error(), originalNS[0]) || !strings.Contains(err.Error(), originalNS[1]) {
			t.Errorf("status %d: p6 = %v, want an error naming the nameservers to set by hand\n%s", status, err, out)
		}
	}
}

// N2: the restore is applied but never acknowledged.
func TestP6RestoreAppliedButNeverAcknowledged(t *testing.T) {
	f := &fakeDomain{nameServers: originalNS, restoreApplied5x: true}
	r, out := newDomainRunner(t, f, allowance{domainPatch: true})

	err := p6(r, targetNS)

	if err == nil {
		t.Fatalf("p6 = nil although no restore was acknowledged\n%s", out)
	}
	for _, want := range []string{"not acknowledged", "currently shows the original nameservers", "console", originalNS[0], originalNS[1]} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %s", want, err)
		}
	}
	if strings.Contains(err.Error(), "set these nameservers by hand") {
		t.Errorf("the domain shows the originals; the error must not say to set them: %s", err)
	}
	if _, attempts := f.count(); attempts != 20 {
		t.Errorf("restore PATCH was sent %d times, want 20", attempts)
	}
	if !strings.Contains(out.String(), "Polling after the failed restore") {
		t.Errorf("transcript lacks the poll after the failed restore:\n%s", out)
	}
}

// N5b: interrupted before anything was sent is not a failure of p6.
func TestP6InterruptedBeforeItsFirstRequest(t *testing.T) {
	f := &fakeDomain{nameServers: originalNS}
	r, _ := newDomainRunner(t, f, allowance{domainPatch: true})
	r.interrupt()

	if err := p6(r, targetNS); err != nil {
		t.Errorf("p6 = %v, want nil: nothing was sent, nothing can be wrong", err)
	}
	if n, _ := f.count(); n != 0 {
		t.Errorf("p6 sent %d requests after the interrupt", n)
	}
}

// N5a: an interrupted simple probe stops; it does not list every request it
// did not send.
func TestSimpleProbesReturnEarlyWhenInterrupted(t *testing.T) {
	simple := map[string]func(*runner){
		"p1":  p1,
		"p2":  p2,
		"p3":  p3,
		"p4":  p4,
		"p5":  p5,
		"p7":  p7,
		"p12": p12,
		"p13": func(r *runner) { p13(r, nil) },
		"p14": p14,
		"p17": func(r *runner) { p17(r, nil) },
		"p18": func(r *runner) { p18(r, nil) },
	}
	for name, probe := range simple {
		f := &rateLimited{}
		r, out := newTestRunner(t, f)
		f.mu.Lock()
		f.onRequest = func(n int) {
			if n == 2 {
				r.interrupt()
			}
		}
		f.mu.Unlock()

		probe(r)

		if n := f.count(); n != 2 {
			t.Errorf("%s sent %d requests, want it to stop after the 2nd", name, n)
		}
		if n := strings.Count(out.String(), "NOT SENT: interrupted"); n > 1 {
			t.Errorf("%s wrote %d NOT SENT entries, want at most one:\n%s", name, n, out)
		}
	}
}

func TestP6RestoreAcceptedButNotVisibleIsAnError(t *testing.T) {
	f := &fakeDomain{nameServers: originalNS, dropRestore: true}
	r, out := newDomainRunner(t, f, allowance{domainPatch: true})

	err := p6(r, targetNS)

	if err == nil {
		t.Fatalf("p6 = nil although the original nameservers never came back\n%s", out)
	}
	for _, ns := range originalNS {
		if !strings.Contains(err.Error(), ns) {
			t.Errorf("error %q does not name the original nameserver %s", err, ns)
		}
	}
	if !strings.Contains(err.Error(), "not visible") {
		t.Errorf("error %q does not say the restore is not visible", err)
	}
}

func TestP6ReadsTheDomainStraightAfterEachChange(t *testing.T) {
	f := &fakeDomain{nameServers: originalNS}
	r, out := newDomainRunner(t, f, allowance{domainPatch: true})

	if err := p6(r, targetNS); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	order := []string{
		"#### P6 set target nameservers",
		"#### P6 nameservers immediately after the change",
		"Polling after change",
		"### P11:",
		"#### P6 restore, attempt 1",
		"#### P6 nameservers immediately after the restore",
		"Polling after restore",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(text[at:], want)
		if i < 0 {
			t.Fatalf("transcript lacks %q after offset %d:\n%s", want, at, text)
		}
		at += i
	}
	if strings.Contains(text, "NOT confirmed") {
		t.Errorf("the change was confirmed, but the transcript says otherwise:\n%s", text)
	}
}

func TestP6SaysWhenTheChangeWasNotConfirmed(t *testing.T) {
	f := &fakeDomain{nameServers: originalNS, dropChanges: true}
	r, out := newDomainRunner(t, f, allowance{domainPatch: true})

	if err := p6(r, targetNS); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	const warning = "**Nameserver change NOT confirmed; P11 results below may not reflect a zone served elsewhere.**"
	w, p11 := strings.Index(text, warning), strings.Index(text, "### P11:")
	if w < 0 || p11 < 0 || w > p11 {
		t.Errorf("transcript must carry the warning before the P11 section (warning at %d, P11 at %d):\n%s", w, p11, text)
	}
}

func TestP6ChangesNothingWhenCurrentNameserversAreUnknown(t *testing.T) {
	f := &fakeDomain{detailFails: true}
	r, _ := newDomainRunner(t, f, allowance{domainPatch: true})

	if err := p6(r, targetNS); err == nil {
		t.Fatal("want an error when the current nameservers cannot be read")
	}
	if got := f.sentWrites(); len(got) != 0 {
		t.Errorf("p6 sent writes without knowing what to restore: %v", got)
	}
}

func TestPollNameserversStopsWhenInterrupted(t *testing.T) {
	f := &fakeDomain{nameServers: originalNS}
	r, _ := newDomainRunner(t, f, allowance{})
	f.hook(func(i int) {
		if i == 3 {
			r.interrupt()
		}
	})

	if r.pollNameservers("never", targetNS) {
		t.Fatal("poll reported nameservers that were never set")
	}
	if n, _ := f.count(); n != 3 {
		t.Errorf("poll sent %d requests, want it to stop after the 3rd", n)
	}

	f2 := &fakeDomain{nameServers: originalNS}
	r2, _ := newDomainRunner(t, f2, allowance{})
	r2.interrupt()
	r2.finishing = true
	r2.pollNameservers("finishing", targetNS)
	if n, _ := f2.count(); n != 12 {
		t.Errorf("poll while finishing sent %d requests, want all 12", n)
	}
}

func TestP9RefusesWhenZoneHasRealAAAA(t *testing.T) {
	f := &fakeDomain{}
	r, _ := newDomainRunner(t, f, allowance{typeWidePut: []string{"AAAA"}})
	zone := []zoneRecord{newZoneRecord(map[string]interface{}{"name": "www", "data": "2001:db8::2", "ttl": 3600}, "AAAA")}

	if err := p9(r, zone); err == nil {
		t.Fatal("want an error when the zone has an AAAA record outside the probe prefix")
	}
	if got := f.sentWrites(); len(got) != 0 {
		t.Errorf("p9 sent writes: %v", got)
	}
}

func TestP9SendsTypeWideEmptyArray(t *testing.T) {
	f := &fakeDomain{}
	r, _ := newDomainRunner(t, f, allowance{typeWidePut: []string{"AAAA"}})
	// A leftover AAAA probe record of this run does not stop P9.
	zone := []zoneRecord{newZoneRecord(map[string]interface{}{"name": "_probe-ab12-old", "data": "2001:db8::2"}, "AAAA")}

	if err := p9(r, zone); err != nil {
		t.Fatal(err)
	}
	want := "PUT /v1/domains/example.org/records/AAAA/_probe-ab12-p9\nPUT /v1/domains/example.org/records/AAAA"
	if got := strings.Join(f.sentWrites(), "\n"); got != want {
		t.Errorf("p9 writes:\n%s\nwant:\n%s", got, want)
	}
}

// rateLimited answers 429 from its limit-th request on.
type rateLimited struct {
	mu        sync.Mutex
	requests  int
	limit     int
	onRequest func(n int)
}

func (f *rateLimited) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if f.onRequest != nil {
		f.onRequest(f.requests)
	}
	if f.limit > 0 && f.requests >= f.limit {
		w.Header().Set("Retry-After", "37")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"code":"TOO_MANY_REQUESTS","message":"slow down"}`)
		return
	}
	io.WriteString(w, "[]")
}

func (f *rateLimited) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func TestP8ShowsTheFirst429(t *testing.T) {
	f := &rateLimited{limit: 7}
	r, out := newTestRunner(t, f)

	p8(r)

	text := out.String()
	first := strings.Index(text, "#### P8 request 7\n")
	next := strings.Index(text, "#### P8 next request after the first 429")
	if first < 0 || next < 0 || first > next {
		t.Fatalf("transcript must hold request 7 itself, then the next request:\n%s", text)
	}
	entry := text[first:next]
	for _, want := range []string{"Status: 429", "Header Retry-After: 37", "TOO_MANY_REQUESTS"} {
		if !strings.Contains(entry, want) {
			t.Errorf("the entry for the first 429 lacks %q:\n%s", want, entry)
		}
	}
	if strings.Contains(text, "#### P8 request 6") {
		t.Errorf("requests before the 429 should stay out of the transcript:\n%s", text)
	}
	if n := f.count(); n != 8 {
		t.Errorf("p8 sent %d requests, want 8", n)
	}
	if r.quiet {
		t.Error("p8 left the runner quiet")
	}
}

func TestP8StopsWhenInterrupted(t *testing.T) {
	f := &rateLimited{}
	r, out := newTestRunner(t, f)
	f.mu.Lock()
	f.onRequest = func(n int) {
		if n == 5 {
			r.interrupt()
		}
	}
	f.mu.Unlock()

	p8(r)

	if n := f.count(); n != 5 {
		t.Errorf("p8 sent %d requests, want it to stop after the 5th", n)
	}
	if strings.Contains(out.String(), "No 429 after 120 requests") {
		t.Errorf("an interrupted p8 must not claim it sent 120 requests:\n%s", out)
	}
}

// fakeGroup holds one TXT group and can be told to misbehave.
type fakeGroup struct {
	mu      sync.Mutex
	value   string // "" when the group does not exist
	old     string // what the group held before the last write or delete
	puts    int
	deletes int
	stale   bool // the next read shows old
	failGet int  // the next read answers with this status

	failPut     map[int]int  // number of the PUT -> status to answer with
	stalePut    map[int]bool // the first read after this PUT shows the previous state
	failRead    map[int]int  // number of the PUT -> status of the first read after it
	staleDelete map[int]bool // the first read after this DELETE still shows the record
	failDelete  map[int]int  // number of the DELETE -> status to answer with
	onRequest   func()
}

func (f *fakeGroup) counts() (puts, deletes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.puts, f.deletes
}

func (f *fakeGroup) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(req.Body)
	if f.onRequest != nil {
		f.onRequest()
	}
	if !strings.Contains(req.URL.Path, "/records/TXT/_probe-ab12-p10") {
		io.WriteString(w, "[]")
		return
	}
	switch req.Method {
	case "PUT":
		f.puts++
		if status := f.failPut[f.puts]; status != 0 {
			w.WriteHeader(status)
			return
		}
		var in []struct {
			Data string `json:"data"`
		}
		json.Unmarshal(body, &in)
		f.old, f.value = f.value, in[0].Data
		f.stale = f.stalePut[f.puts]
		f.failGet = f.failRead[f.puts]
	case "DELETE":
		f.deletes++
		if status := f.failDelete[f.deletes]; status != 0 {
			w.WriteHeader(status)
			return
		}
		f.old, f.value = f.value, ""
		f.stale = f.staleDelete[f.deletes]
		f.failGet = 0
		w.WriteHeader(http.StatusNoContent)
	case "GET":
		if f.failGet != 0 {
			w.WriteHeader(f.failGet)
			f.failGet = 0
			return
		}
		shown := f.value
		if f.stale {
			f.stale = false
			shown = f.old
		}
		if shown == "" {
			io.WriteString(w, "[]")
			return
		}
		fmt.Fprintf(w, `[{"data":%q,"name":"_probe-ab12-p10","ttl":3600,"type":"TXT"}]`, shown)
	}
}

func TestP10AllCurrent(t *testing.T) {
	f := &fakeGroup{}
	r, out := newTestRunner(t, f)

	p10(r)

	text := out.String()
	if puts, deletes := f.counts(); puts != 20 || deletes != 4 {
		t.Errorf("p10 sent %d writes and %d deletes, want 20 and 4", puts, deletes)
	}
	want := "P10 summary: 0 of 20 reads straight after a write were stale; " +
		"0 of 4 reads straight after a delete were stale; 0 requests failed."
	if !strings.Contains(text, want) {
		t.Errorf("transcript lacks the summary %q:\n%s", want, text)
	}
	if strings.Contains(text, "failed: status") || strings.Contains(text, "was stale") {
		t.Errorf("nothing went wrong, but the transcript says otherwise:\n%s", text)
	}
	if r.quiet || r.gap != 0 {
		t.Errorf("p10 left quiet=%v gap=%s", r.quiet, r.gap)
	}
}

func TestP10TellsFailuresFromStaleReads(t *testing.T) {
	f := &fakeGroup{
		failPut:     map[int]int{3: 500},
		stalePut:    map[int]bool{7: true},
		failRead:    map[int]int{9: 429},
		staleDelete: map[int]bool{2: true},
		failDelete:  map[int]int{3: 503},
	}
	r, out := newTestRunner(t, f)

	p10(r)

	text := out.String()
	for _, want := range []string{
		"write 3 failed: status 500",
		"read 9 failed: status 429",
		"write 7: read straight after was stale",
		"write 7: visible after",
		"delete after write 10: read straight after still returned 1 record(s) (stale delete)",
		"delete after write 15 failed: status 503",
		"delete after write 5: read straight after returned status 200, no records",
		"P10 summary: 1 of 18 reads straight after a write were stale; " +
			"1 of 3 reads straight after a delete were stale; 3 requests failed.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("transcript lacks %q", want)
		}
	}
	for _, wrong := range []string{"write 3: read straight after was stale", "write 9: read straight after was stale", "write 1 failed"} {
		if strings.Contains(text, wrong) {
			t.Errorf("transcript wrongly says %q", wrong)
		}
	}
	if t.Failed() {
		t.Logf("transcript:\n%s", text)
	}
}

func TestP10StopsWhenInterrupted(t *testing.T) {
	f := &fakeGroup{}
	r, out := newTestRunner(t, f)
	n := 0
	f.mu.Lock()
	f.onRequest = func() {
		if n++; n == 6 {
			r.interrupt()
		}
	}
	f.mu.Unlock()

	p10(r)

	f.mu.Lock()
	defer f.mu.Unlock()
	if n != 6 {
		t.Errorf("p10 sent %d requests, want it to stop after the 6th", n)
	}
	text := out.String()
	if !strings.Contains(text, "interrupted") || strings.Contains(text, "failed: status 0") {
		t.Errorf("an interrupted p10 must say so and must not count unsent requests as failures:\n%s", text)
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
