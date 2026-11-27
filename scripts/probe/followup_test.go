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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeZone is a stateful stand-in for the records of one domain. It answers
// as the live API was seen to: PUT 200, DELETE 204 or 404, a read of a
// missing group 200 [].
type fakeZone struct {
	mu       sync.Mutex
	groups   map[string]map[string][]map[string]interface{} // type -> name -> records
	writes   []string                                       // "METHOD path body" of every non-GET request
	requests int

	onRequest      func(n int)
	ignoreTypeWide bool                                     // answer a type-wide PUT with 200 and change nothing
	emptyDeletes   map[string]bool                          // types for which a type-wide PUT of [] deletes everything
	acceptCAA      func(recs []map[string]interface{}) bool // nil accepts every CAA body
	// A "restore" is a type-wide PUT that carries a record whose name does
	// not start with the probe prefix: real records being put back.
	restoreRejects int            // answer this many restores with restoreStatus; -1 means all
	restoreStatus  int            // 409 when zero
	restoreAlters  bool           // apply a restore, then change the ttl of one real record
	restores       []string       // "type body" of every restore received
	listStatus     map[string]int // type -> status for a read of the whole type

	// srvReadName, when set, turns the name in a read of one SRV group into
	// the name the record is stored under, or "" for no match.
	srvReadName func(asked string) string
}

func (f *fakeZone) hook(fn func(n int)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onRequest = fn
}

func (f *fakeZone) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes...)
}

// seed stores one record before the test starts.
func (f *fakeZone) seed(recordType, name string, fields map[string]interface{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.store(recordType, name, []map[string]interface{}{fields})
}

func (f *fakeZone) store(recordType, name string, recs []map[string]interface{}) {
	if f.groups == nil {
		f.groups = map[string]map[string][]map[string]interface{}{}
	}
	if f.groups[recordType] == nil {
		f.groups[recordType] = map[string][]map[string]interface{}{}
	}
	for _, rec := range recs {
		rec["name"], rec["type"] = name, recordType
	}
	f.groups[recordType][name] = recs
}

func (f *fakeZone) ServeHTTP(w http.ResponseWriter, req *http.Request) {
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
	recordType := parts[0]
	var recs []map[string]interface{}
	json.Unmarshal(body, &recs)
	reply := func(v []map[string]interface{}) {
		if v == nil {
			v = []map[string]interface{}{}
		}
		json.NewEncoder(w).Encode(v)
	}

	switch {
	case len(parts) == 1 && req.Method == "GET" && f.listStatus[recordType] != 0:
		w.WriteHeader(f.listStatus[recordType])
	case len(parts) == 1 && req.Method == "GET":
		var all []map[string]interface{}
		var names []string
		for name := range f.groups[recordType] {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			all = append(all, f.groups[recordType][name]...)
		}
		reply(all)
	case len(parts) == 1 && req.Method == "PUT":
		if f.ignoreTypeWide || (len(recs) == 0 && !f.emptyDeletes[recordType]) {
			return // as seen live: an empty type-wide replace changes nothing
		}
		isRestore := false
		for _, rec := range recs {
			if name, _ := rec["name"].(string); !strings.HasPrefix(name, "_probe-") {
				isRestore = true
			}
		}
		if isRestore {
			f.restores = append(f.restores, recordType+" "+string(body))
			if f.restoreRejects != 0 {
				if f.restoreRejects > 0 {
					f.restoreRejects--
				}
				status := f.restoreStatus
				if status == 0 {
					status = http.StatusConflict
				}
				w.WriteHeader(status)
				return
			}
			if f.restoreAlters {
				recs[0]["ttl"] = 12345
			}
		}
		delete(f.groups, recordType)
		for _, rec := range recs {
			name, _ := rec["name"].(string)
			f.store(recordType, name, append(f.groups[recordType][name], rec))
		}
	case len(parts) == 2 && req.Method == "GET":
		name := parts[1]
		if recordType == "SRV" && f.srvReadName != nil {
			name = f.srvReadName(name)
		}
		reply(f.groups[recordType][name])
	case len(parts) == 2 && req.Method == "PUT":
		if recordType == "CAA" && f.acceptCAA != nil && !f.acceptCAA(recs) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, `{"code":"INVALID_RECORDS","message":"Missing record information, [flags]."}`)
			return
		}
		f.store(recordType, parts[1], recs)
	case len(parts) == 2 && req.Method == "DELETE":
		if _, ok := f.groups[recordType][parts[1]]; !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"code":"RECORD_NOT_FOUND"}`)
			return
		}
		delete(f.groups[recordType], parts[1])
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// assertOnlyProbeWrites fails unless every write went to a probe-prefix
// name, apart from the writes listed in also.
func assertOnlyProbeWrites(t *testing.T, writes []string, also ...string) {
	t.Helper()
next:
	for _, w := range writes {
		path := strings.Fields(w)[1]
		for _, allowed := range also {
			if strings.Fields(w)[0]+" "+path == allowed {
				continue next
			}
		}
		parts := strings.Split(path, "/")
		if len(parts) != 7 || !strings.HasPrefix(parts[6], "_probe-ab12-") {
			t.Errorf("write outside the probe prefix: %s", w)
		}
	}
}

func assertNothingRefused(t *testing.T, transcript string) {
	t.Helper()
	if strings.Contains(transcript, "NOT SENT") {
		t.Errorf("the guard refused a request the probe is meant to send:\n%s", transcript)
	}
}

const typeWideAAAA = "PUT /v1/domains/example.org/records/AAAA"

func TestFollowUpAllowances(t *testing.T) {
	const domain, prefix = "example.org", "_probe-ab12-"
	p13 := allowanceFor("p13", true)
	if !reflect.DeepEqual(p13, allowance{typeWidePut: []string{"AAAA"}}) {
		t.Errorf("allowanceFor(p13, true) = %+v, want only the type-wide PUT to AAAA", p13)
	}
	if err := checkWrite("PUT", "/v1/domains/example.org/records/AAAA", domain, prefix, p13); err != nil {
		t.Errorf("p13 may replace AAAA: %v", err)
	}
	if err := checkWrite("PUT", "/v1/domains/example.org/records/MX", domain, prefix, p13); err == nil {
		t.Error("p13 must not replace MX")
	}
	if err := checkWrite("PATCH", "/v1/domains/example.org", domain, prefix, p13); err == nil {
		t.Error("p13 must not change the domain")
	}
	if got := allowanceFor("p13", false); !reflect.DeepEqual(got, allowance{}) {
		t.Errorf("allowanceFor(p13, false) = %+v, want nothing", got)
	}
	for _, flag := range []bool{true, false} {
		if got := allowanceFor("p14", flag); !reflect.DeepEqual(got, allowance{}) {
			t.Errorf("allowanceFor(p14, %v) = %+v, want nothing", flag, got)
		}
	}
	for _, name := range []string{"p13", "p14"} {
		if err := validateProbe(name); err != nil {
			t.Errorf("validateProbe(%s) = %v", name, err)
		}
		if readOnly[name] {
			t.Errorf("%s writes; it must not be listed as read-only", name)
		}
	}
}

func TestRunRefusesZoneWideProbesWithoutTheFlag(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&requests, 1) }))
	defer srv.Close()
	t.Setenv("GODADDY_API_TOKEN", "tok")
	t.Setenv("GODADDY_PROBE_DOMAIN", "example.org")
	t.Setenv("GODADDY_PROBE_BASEURL", srv.URL)
	outDir := filepath.Join(t.TempDir(), "out")

	for _, name := range []string{"p9", "p13", "p17"} {
		err := run(name, outDir, false, false, "")
		if err == nil || !strings.Contains(err.Error(), "-allow-zone-wide") {
			t.Errorf("run(%s) without the flag = %v, want a refusal naming -allow-zone-wide", name, err)
		}
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Errorf("the output directory was created (stat error: %v)", err)
	}
	if n := atomic.LoadInt32(&requests); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

func TestP13TypeWideReplaceDeletes(t *testing.T) {
	f := &fakeZone{}
	r, out := newDomainRunner(t, f, allowanceFor("p13", true))

	if err := p13(r, nil); err != nil {
		t.Fatal(err)
	}

	text := out.String()
	assertNothingRefused(t, text)
	writes := f.sent()
	assertOnlyProbeWrites(t, writes, typeWideAAAA)
	want := []string{
		`PUT /v1/domains/example.org/records/AAAA/_probe-ab12-p13-a [{"data":"2001:db8::a","ttl":3600}]`,
		`PUT /v1/domains/example.org/records/AAAA/_probe-ab12-p13-b [{"data":"2001:db8::b","ttl":3600}]`,
		`PUT /v1/domains/example.org/records/AAAA [{"data":"2001:db8::c","name":"_probe-ab12-p13-b","ttl":3600}]`,
	}
	if strings.Join(writes, "\n") != strings.Join(want, "\n") {
		t.Errorf("writes:\n%s\nwant:\n%s", strings.Join(writes, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(text, "p13-a was DELETED by the type-wide replace") || strings.Contains(text, "SURVIVED") {
		t.Errorf("summary must say p13-a was deleted:\n%s", text)
	}
	if !strings.Contains(text, "p13-b holds: 2001:db8::c") {
		t.Errorf("summary must say what p13-b holds:\n%s", text)
	}
	for _, label := range []string{"all AAAA before", "gone if the replace is type-wide", "expect 2001:db8::c only", "all AAAA after"} {
		if !strings.Contains(text, label) {
			t.Errorf("transcript lacks the entry %q", label)
		}
	}
	if len(r.created) != 2 {
		t.Errorf("groups registered for cleanup: %v, want both", r.created)
	}
}

func TestP13TypeWideReplaceIgnored(t *testing.T) {
	f := &fakeZone{ignoreTypeWide: true}
	r, out := newDomainRunner(t, f, allowanceFor("p13", true))

	if err := p13(r, nil); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	assertNothingRefused(t, text)
	if !strings.Contains(text, "p13-a SURVIVED the type-wide replace") || strings.Contains(text, "DELETED") {
		t.Errorf("summary must say p13-a survived:\n%s", text)
	}
	if !strings.Contains(text, "p13-b holds: 2001:db8::b") {
		t.Errorf("summary must say what p13-b holds:\n%s", text)
	}
}

func TestP13InconclusiveWhenARequestFails(t *testing.T) {
	// The fake has no allowance problem; the runner does: without the
	// allowance the type-wide PUT is refused and never sent.
	f := &fakeZone{}
	r, out := newDomainRunner(t, f, allowance{})

	if err := p13(r, nil); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "P13 result: inconclusive") || strings.Contains(text, "SURVIVED") || strings.Contains(text, "DELETED") {
		t.Errorf("summary must say the result is inconclusive:\n%s", text)
	}
	assertOnlyProbeWrites(t, f.sent())
}

func TestP13RefusesWhenZoneHasRealAAAA(t *testing.T) {
	f := &fakeZone{}
	r, _ := newDomainRunner(t, f, allowanceFor("p13", true))
	zone := []zoneRecord{
		newZoneRecord(map[string]interface{}{"name": "_probe-ab12-old", "data": "2001:db8::1"}, "AAAA"),
		newZoneRecord(map[string]interface{}{"name": "www", "data": "2001:db8::2"}, "AAAA"),
	}

	err := p13(r, zone)

	if err == nil || !strings.Contains(err.Error(), "P13") {
		t.Fatalf("p13 = %v, want an error naming P13", err)
	}
	if got := f.sent(); len(got) != 0 {
		t.Errorf("p13 sent writes: %v", got)
	}
}

func TestExecuteP13ChecksTheZoneItRead(t *testing.T) {
	f := &fakeZone{}
	f.seed("AAAA", "www", map[string]interface{}{"data": "2001:db8::2", "ttl": 3600})
	r, _ := newDomainRunner(t, f, allowanceFor("p13", true))

	err := execute(r, "p13", probeArgs{}, "transcript.md")

	if exitCode(err) != 1 {
		t.Errorf("execute = %v, want exit 1", err)
	}
	if got := f.sent(); len(got) != 0 {
		t.Errorf("writes were sent although the zone has a real AAAA record: %v", got)
	}
}

func srvForm(visibleAs string) func(string) string {
	return func(asked string) string {
		if asked == visibleAs {
			return "_probe-ab12-p14-srv"
		}
		return ""
	}
}

// caaBody tells which of p14's four CAA bodies a record belongs to.
func caaBody(rec map[string]interface{}) int {
	data, _ := rec["data"].(string)
	_, hasTag := rec["tag"]
	switch {
	case !hasTag:
		return 4
	case strings.HasPrefix(data, `"`):
		return 3
	case rec["flags"] == "0":
		return 2
	}
	return 1
}

func caaPuts(writes []string) (single, double []string) {
	for _, w := range writes {
		if !strings.HasPrefix(w, "PUT /v1/domains/example.org/records/CAA/_probe-ab12-p14-caa ") {
			continue
		}
		if strings.Count(w, `"data"`) == 1 {
			single = append(single, w)
		} else {
			double = append(double, w)
		}
	}
	return single, double
}

func TestP14FindsTheAcceptedCAAForm(t *testing.T) {
	f := &fakeZone{acceptCAA: func(recs []map[string]interface{}) bool { return caaBody(recs[0]) == 3 }}
	r, out := newDomainRunner(t, f, allowance{})

	p14(r)

	text := out.String()
	assertNothingRefused(t, text)
	writes := f.sent()
	assertOnlyProbeWrites(t, writes)
	single, double := caaPuts(writes)
	wantSingle := []string{
		`[{"data":"letsencrypt.org","flags":0,"tag":"issue","ttl":3600}]`,
		`[{"data":"letsencrypt.org","flags":"0","tag":"issue","ttl":3600}]`,
		`[{"data":"\"letsencrypt.org\"","flags":0,"tag":"issue","ttl":3600}]`,
	}
	if len(single) != 3 {
		t.Fatalf("single-record CAA PUTs: %d, want 3:\n%s", len(single), strings.Join(single, "\n"))
	}
	for i, want := range wantSingle {
		if !strings.HasSuffix(single[i], " "+want) {
			t.Errorf("CAA body %d = %s, want %s", i+1, single[i], want)
		}
	}
	wantDouble := `[{"data":"\"letsencrypt.org\"","flags":0,"tag":"issue","ttl":3600},{"data":"\";\"","flags":0,"tag":"issuewild","ttl":3600}]`
	if len(double) != 1 || !strings.HasSuffix(double[0], " "+wantDouble) {
		t.Errorf("two-record CAA PUT = %v, want one with %s", double, wantDouble)
	}
	if last := writes[len(writes)-1]; len(double) == 1 && last != double[0] {
		t.Errorf("the two-record PUT must come after the single ones; last write was %s", last)
	}
	if !strings.Contains(text, "P14 CAA result: body 3 was accepted") {
		t.Errorf("summary must name body 3:\n%s", text)
	}
	summary := text[strings.Index(text, "P14 CAA result"):]
	if !strings.Contains(summary, `"tag":"issuewild"`) {
		t.Errorf("summary must quote the JSON returned for the group:\n%s", summary)
	}
	if !strings.Contains(text, "P14 read all CAA records") {
		t.Errorf("transcript lacks the whole-type CAA read:\n%s", text)
	}
}

func TestP14EachCAAStyleKeepsItsFieldForm(t *testing.T) {
	want := map[int]string{
		1: `[{"data":"letsencrypt.org","flags":0,"tag":"issue","ttl":3600},{"data":";","flags":0,"tag":"issuewild","ttl":3600}]`,
		2: `[{"data":"letsencrypt.org","flags":"0","tag":"issue","ttl":3600},{"data":";","flags":"0","tag":"issuewild","ttl":3600}]`,
		4: `[{"data":"0 issue \"letsencrypt.org\"","flags":0,"ttl":3600},{"data":"0 issuewild \";\"","flags":0,"ttl":3600}]`,
	}
	for n, wantDouble := range want {
		n := n
		f := &fakeZone{acceptCAA: func(recs []map[string]interface{}) bool { return caaBody(recs[0]) == n }}
		r, out := newDomainRunner(t, f, allowance{})

		p14(r)

		single, double := caaPuts(f.sent())
		if len(single) != n {
			t.Errorf("body %d accepted: %d single-record PUTs were sent, want %d", n, len(single), n)
		}
		if len(double) != 1 || !strings.HasSuffix(double[0], " "+wantDouble) {
			t.Errorf("body %d accepted: two-record PUT = %v, want %s", n, double, wantDouble)
		}
		if !strings.Contains(out.String(), "P14 CAA result: body "+string(rune('0'+n))+" was accepted") {
			t.Errorf("summary must name body %d:\n%s", n, out)
		}
	}
}

func TestP14NoCAAFormAccepted(t *testing.T) {
	f := &fakeZone{acceptCAA: func([]map[string]interface{}) bool { return false }}
	r, out := newDomainRunner(t, f, allowance{})

	p14(r)

	text := out.String()
	assertNothingRefused(t, text)
	single, double := caaPuts(f.sent())
	if len(single) != 4 || len(double) != 0 {
		t.Errorf("CAA PUTs: %d single and %d two-record, want 4 and 0", len(single), len(double))
	}
	if !strings.HasSuffix(single[len(single)-1], ` [{"data":"0 issue \"letsencrypt.org\"","flags":0,"ttl":3600}]`) {
		t.Errorf("CAA body 4 = %s", single[len(single)-1])
	}
	if !strings.Contains(text, "P14 CAA result: no CAA form was accepted") {
		t.Errorf("summary must say that no form was accepted:\n%s", text)
	}
	if strings.Contains(text, "P14 read all CAA records") {
		t.Errorf("nothing was accepted; the whole-type read is not needed:\n%s", text)
	}
}

func TestP14NamesTheSRVFormThatReads(t *testing.T) {
	const name = "_probe-ab12-p14-srv"
	cases := []struct {
		visibleAs string
		want      string
	}{
		{"_sip._tcp." + name, "P14 SRV result: a read by name returned records for: _sip._tcp." + name + "\n"},
		{name, "P14 SRV result: a read by name returned records for: " + name + "\n"},
		{"nothing-matches", "P14 SRV result: a read by name returned records for: none\n"},
	}
	for _, tc := range cases {
		f := &fakeZone{srvReadName: srvForm(tc.visibleAs)}
		r, out := newDomainRunner(t, f, allowance{})

		p14(r)

		text := out.String()
		assertNothingRefused(t, text)
		if !strings.Contains(text, tc.want) {
			t.Errorf("visible as %s: transcript lacks %q:\n%s", tc.visibleAs, tc.want, text)
		}
		for _, path := range []string{
			"`GET /v1/domains/example.org/records/SRV/" + name + "`",
			"`GET /v1/domains/example.org/records/SRV/_sip._tcp." + name + "`",
			"`GET /v1/domains/example.org/records/SRV/" + name + "._sip._tcp`",
			"`GET /v1/domains/example.org/records/SRV/_sip." + name + "`",
			"`GET /v1/domains/example.org/records/SRV`",
		} {
			if !strings.Contains(text, path) {
				t.Errorf("transcript lacks the read %s", path)
			}
		}
		want := `PUT /v1/domains/example.org/records/SRV/` + name +
			` [{"data":"target.example.net","port":5060,"priority":10,"protocol":"_tcp","service":"_sip","ttl":3600,"weight":5}]`
		if writes := f.sent(); len(writes) == 0 || writes[0] != want {
			t.Errorf("first write = %v, want %s", writes, want)
		}
	}
}

func TestFollowUpProbesCleanUpAfterAnInterrupt(t *testing.T) {
	cases := []struct {
		probe   string
		deletes []string
	}{
		// Requests 1 to 9 are the snapshot; the interrupt comes with the 11th.
		{"p13", []string{
			"DELETE /v1/domains/example.org/records/AAAA/_probe-ab12-p13-a",
			"DELETE /v1/domains/example.org/records/AAAA/_probe-ab12-p13-b",
		}},
		{"p14", []string{"DELETE /v1/domains/example.org/records/SRV/_probe-ab12-p14-srv"}},
	}
	for _, tc := range cases {
		f := &fakeZone{}
		r, out := newDomainRunner(t, f, allowanceFor(tc.probe, true))
		f.hook(func(n int) {
			if n == 11 {
				r.interrupt()
			}
		})

		err := execute(r, tc.probe, probeArgs{}, "transcript.md")

		if !errors.Is(err, errInterrupted) {
			t.Errorf("%s: execute = %v, want errInterrupted\n%s", tc.probe, err, out)
		}
		var deletes []string
		for _, w := range f.sent() {
			if strings.HasPrefix(w, "DELETE ") {
				deletes = append(deletes, w)
			}
			if strings.HasPrefix(w, typeWideAAAA+" ") || strings.Contains(w, "/records/CAA/") {
				t.Errorf("%s: a write was sent after the interrupt: %s", tc.probe, w)
			}
		}
		if strings.Join(deletes, "\n") != strings.Join(tc.deletes, "\n") {
			t.Errorf("%s: cleanup sent %v, want %v", tc.probe, deletes, tc.deletes)
		}
		if n := strings.Count(out.String(), "NOT SENT: interrupted"); n > 1 {
			t.Errorf("%s wrote %d NOT SENT entries, want at most one", tc.probe, n)
		}
		if strings.Contains(out.String(), "P13 result: p13-a") {
			t.Errorf("an interrupted p13 must not draw a conclusion:\n%s", out)
		}
	}
}

func TestExecuteFollowUpProbesLeaveACleanZone(t *testing.T) {
	for _, probe := range []string{"p13", "p14"} {
		f := &fakeZone{}
		f.seed("MX", "@", map[string]interface{}{"data": "mx1.example.net", "priority": 10, "ttl": 3600})
		r, out := newDomainRunner(t, f, allowanceFor(probe, true))

		if err := execute(r, probe, probeArgs{}, "transcript.md"); err != nil {
			t.Errorf("%s: execute = %v\n%s", probe, err, out)
		}
		assertNothingRefused(t, out.String())
		f.mu.Lock()
		for recordType, groups := range f.groups {
			for name := range groups {
				if recordType != "MX" {
					t.Errorf("%s left %s %s behind", probe, recordType, name)
				}
			}
		}
		f.mu.Unlock()
	}
}
