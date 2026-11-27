package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAPI records the requests it receives and answers with canned bodies.
type fakeAPI struct {
	mu       sync.Mutex
	requests []string
	bodies   map[string]string      // "METHOD path" -> response body
	statuses map[string]int         // "METHOD path" -> status, default 200
	headers  map[string]http.Header // "METHOD path" -> extra response headers
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	key := req.Method + " " + req.URL.RequestURI()
	f.mu.Lock()
	f.requests = append(f.requests, strings.TrimSpace(key+" "+string(body)))
	answer, ok := f.bodies[key]
	status := f.statuses[key]
	extra := f.headers[key]
	f.mu.Unlock()
	if !ok {
		answer = "[]"
	}
	for name, values := range extra {
		for _, v := range values {
			w.Header().Add(name, v)
		}
	}
	if status != 0 {
		w.WriteHeader(status)
	}
	io.WriteString(w, answer)
}

// got returns the requests received so far.
func (f *fakeAPI) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeAPI) forget() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = nil
}

func newTestRunner(t *testing.T, api http.Handler) (*runner, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	out := &bytes.Buffer{}
	return &runner{
		base:         srv.URL,
		token:        "secret-token-value",
		domain:       "example.org",
		runID:        "ab12",
		prefix:       "_probe-ab12-",
		pollEvery:    time.Millisecond,
		watchUnit:    10 * time.Microsecond,
		restoreEvery: time.Millisecond,
		out:          out,
		hc:           srv.Client(),
		stop:         make(chan struct{}),
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

	if got := api.got(); len(got) != 0 {
		t.Fatalf("a refused write reached the server: %v", got)
	}
	if resp.Status != 0 || resp.Sent {
		t.Errorf("response = %+v, want the zero response for a request that was not sent", resp)
	}
	if !strings.Contains(out.String(), "NOT SENT") {
		t.Errorf("transcript does not say the request was refused:\n%s", out)
	}
}

func TestRunnerUsesItsAllowance(t *testing.T) {
	api := &fakeAPI{}
	r, _ := newTestRunner(t, api)
	r.allow = allowance{typeWidePut: []string{"AAAA"}}

	r.do("allowed", "PUT", "/v1/domains/example.org/records/AAAA", []rec{})
	r.do("other type", "PUT", "/v1/domains/example.org/records/MX", []rec{})
	r.do("nameservers", "PATCH", "/v1/domains/example.org", rec{"nameServers": []string{"a", "b"}})

	want := "PUT /v1/domains/example.org/records/AAAA []"
	if got := strings.Join(api.got(), "\n"); got != want {
		t.Errorf("server received:\n%s\nwant only:\n%s", got, want)
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

func TestRunnerWithholdsDomainDetailItCannotParse(t *testing.T) {
	for _, body := range []string{
		`{"contactRegistrant":{"email":"someone@example.org"}}`,
		`[{"domain":"example.org","contactRegistrant":{"email":"someone@example.org"}}]`,
		`<html>someone@example.org</html>`,
	} {
		api := &fakeAPI{bodies: map[string]string{"GET /v1/domains/example.org": body}}
		r, out := newTestRunner(t, api)

		resp := r.do("domain detail", "GET", "/v1/domains/example.org", nil)

		if strings.Contains(out.String(), "someone@example.org") || strings.Contains(string(resp.Body), "someone@example.org") {
			t.Errorf("body %s: contact details reached the transcript or the caller:\n%s", body, out)
		}
		if want := `{"redaction":"unparseable body withheld"}`; string(resp.Body) != want || !strings.Contains(out.String(), want) {
			t.Errorf("body %s: got %s, want %s in the response and the transcript:\n%s", body, resp.Body, want, out)
		}
		if resp.Status != 200 {
			t.Errorf("status = %d, want 200", resp.Status)
		}
	}
}

func TestRunnerPassesDomainErrorBodiesThrough(t *testing.T) {
	const body = `{"code":"NOT_FOUND","message":"domain not found"}`
	api := &fakeAPI{
		bodies:   map[string]string{"GET /v1/domains/example.org": body},
		statuses: map[string]int{"GET /v1/domains/example.org": 404},
	}
	r, out := newTestRunner(t, api)

	resp := r.do("domain detail", "GET", "/v1/domains/example.org", nil)

	if string(resp.Body) != body || !strings.Contains(out.String(), body) {
		t.Errorf("a non-2xx body must pass unchanged; got %s\n%s", resp.Body, out)
	}
}

func TestRunnerReportsABodyItCannotRead(t *testing.T) {
	// The server promises 100 bytes, sends 5 and closes the connection.
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Length", "100")
		io.WriteString(w, "short")
	})
	r, out := newTestRunner(t, h)

	resp := r.do("cut off", "GET", r.group("TXT", r.name("a")), nil)

	if resp.Status != 0 || len(resp.Body) != 0 {
		t.Errorf("response = %+v, want status 0 and no body", resp)
	}
	if !resp.Sent {
		t.Error("the request was sent; the response must say so")
	}
	if !strings.Contains(out.String(), "TRANSPORT ERROR") {
		t.Errorf("transcript does not report the failure:\n%s", out)
	}
}

func TestRunnerPrintsAllHeadersButCookies(t *testing.T) {
	key := "GET /v1/domains/example.org/records/TXT/_probe-ab12-a"
	api := &fakeAPI{headers: map[string]http.Header{key: {
		"X-Request-Id": {"abc123"},
		"Retry-After":  {"30"},
		"Set-Cookie":   {"session=do-not-print"},
		"Vary":         {"Accept", "Origin"},
	}}}
	r, out := newTestRunner(t, api)

	r.do("read", "GET", r.group("TXT", r.name("a")), nil)

	text := out.String()
	for _, want := range []string{"Header Retry-After: 30\n", "Header X-Request-Id: abc123\n", "Header Vary: Accept, Origin\n", "Header Content-Length: 2\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("transcript lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "do-not-print") || strings.Contains(text, "Set-Cookie") {
		t.Errorf("transcript contains a cookie:\n%s", text)
	}
	if a, b := strings.Index(text, "Header Retry-After"), strings.Index(text, "Header X-Request-Id"); a > b {
		t.Errorf("headers are not sorted by name:\n%s", text)
	}
}

func TestRunnerQuietKeepsTheLastEntry(t *testing.T) {
	r, out := newTestRunner(t, &fakeAPI{})
	r.quiet = true

	r.do("quiet read", "GET", r.group("TXT", r.name("a")), nil)

	if out.Len() != 0 {
		t.Errorf("a quiet request wrote to the transcript:\n%s", out)
	}
	for _, want := range []string{"#### quiet read", "Status: 200", "Header Content-Length: 2"} {
		if !strings.Contains(r.lastEntry, want) {
			t.Errorf("lastEntry lacks %q:\n%s", want, r.lastEntry)
		}
	}
}

func TestRunnerRefusesToSendOnceInterrupted(t *testing.T) {
	api := &fakeAPI{}
	r, out := newTestRunner(t, api)
	r.interrupt()
	r.interrupt() // a second signal must be harmless

	resp := r.do("read", "GET", r.group("TXT", r.name("a")), nil)
	put := r.put("write", "TXT", r.name("a"), one("x"))

	if got := api.got(); len(got) != 0 {
		t.Fatalf("requests were sent after an interrupt: %v", got)
	}
	if resp.Status != 0 || resp.Sent || put.Status != 0 || put.Sent {
		t.Errorf("responses = %+v, %+v; want zero responses", resp, put)
	}
	if n := strings.Count(out.String(), "NOT SENT: interrupted"); n != 2 {
		t.Errorf("transcript says NOT SENT: interrupted %d times, want 2:\n%s", n, out)
	}
}

func TestRunnerStillSendsWhenFinishing(t *testing.T) {
	api := &fakeAPI{}
	r, _ := newTestRunner(t, api)
	r.put("write", "TXT", r.name("a"), one("x"))
	r.interrupt()
	api.forget()

	r.finishing = true
	r.gap = 20 * time.Millisecond // the pacing wait must not be cut short either
	r.last = time.Now()
	started := time.Now()
	r.cleanup()

	want := "DELETE /v1/domains/example.org/records/TXT/_probe-ab12-a"
	if got := strings.Join(api.got(), "\n"); got != want {
		t.Errorf("cleanup while finishing sent:\n%s\nwant:\n%s", got, want)
	}
	if waited := time.Since(started); waited < 20*time.Millisecond {
		t.Errorf("finishing request went out after %s, before the pacing gap had passed", waited)
	}
}

func TestRunnerPacingWaitWakesOnInterrupt(t *testing.T) {
	api := &fakeAPI{}
	r, out := newTestRunner(t, api)
	r.gap = time.Hour
	r.last = time.Now()
	go func() {
		time.Sleep(20 * time.Millisecond)
		r.interrupt()
	}()

	done := make(chan response, 1)
	go func() { done <- r.do("read", "GET", r.group("TXT", r.name("a")), nil) }()

	select {
	case resp := <-done:
		if resp.Status != 0 || resp.Sent {
			t.Errorf("response = %+v, want the zero response", resp)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the pacing wait did not wake when the runner was interrupted")
	}
	if got := api.got(); len(got) != 0 {
		t.Errorf("request was sent after the interrupt: %v", got)
	}
	if !strings.Contains(out.String(), "NOT SENT: interrupted") {
		t.Errorf("transcript does not say why nothing was sent:\n%s", out)
	}
}

func TestRunnerCleanupDeletesEachCreatedGroupOnce(t *testing.T) {
	api := &fakeAPI{}
	r, _ := newTestRunner(t, api)
	r.put("first", "TXT", r.name("a"), []map[string]interface{}{{"data": "1"}})
	r.put("again", "TXT", r.name("a"), []map[string]interface{}{{"data": "2"}})
	r.put("other", "A", r.name("b"), []map[string]interface{}{{"data": "192.0.2.1"}})
	api.forget()

	r.cleanup()

	want := []string{
		"DELETE /v1/domains/example.org/records/TXT/_probe-ab12-a",
		"DELETE /v1/domains/example.org/records/A/_probe-ab12-b",
	}
	if got := api.got(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("cleanup sent\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestSnapshotReadsEveryTypeAndKeepsWholeRecords(t *testing.T) {
	api := &fakeAPI{bodies: map[string]string{
		"GET /v1/domains/example.org/records/MX":  `[{"name":"@","data":"mx1.example.net","ttl":3600,"priority":10}]`,
		"GET /v1/domains/example.org/records/TXT": `[{"type":"TXT","name":"@","data":"v=spf1 -all","ttl":3600}]`,
		"GET /v1/domains/example.org/records/SRV": `[{"type":"SRV","name":"@","data":"sip.example.net","ttl":3600,` +
			`"priority":10,"weight":5,"port":5060,"service":"_sip","protocol":"_tcp"}]`,
	}}
	r, out := newTestRunner(t, api)

	zone, err := r.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got := api.got(); len(got) != len(recordTypes) {
		t.Errorf("snapshot sent %d requests, want one per record type (%d)", len(got), len(recordTypes))
	}
	if len(zone) != 3 || zone[0].Type != "MX" || zone[1].Type != "SRV" || zone[2].Type != "TXT" {
		t.Fatalf("snapshot = %v", zone)
	}
	if want := `{"data":"mx1.example.net","name":"@","priority":10,"ttl":3600,"type":"MX"}`; zone[0].String() != want {
		t.Errorf("MX record = %s, want %s", zone[0], want)
	}
	if want := `{"data":"sip.example.net","name":"@","port":5060,"priority":10,"protocol":"_tcp",` +
		`"service":"_sip","ttl":3600,"type":"SRV","weight":5}`; zone[1].String() != want {
		t.Errorf("SRV record = %s, want %s", zone[1], want)
	}
	if out.Len() != 0 {
		t.Errorf("snapshot wrote to the transcript:\n%s", out)
	}
}

func TestSnapshotSeesAChangedSRVPort(t *testing.T) {
	const key = "GET /v1/domains/example.org/records/SRV"
	srv := func(port string) string {
		return `[{"type":"SRV","name":"@","data":"sip.example.net","ttl":3600,"priority":10,"weight":5,"port":` +
			port + `,"service":"_sip","protocol":"_tcp"}]`
	}
	api := &fakeAPI{bodies: map[string]string{key: srv("5060")}}
	r, _ := newTestRunner(t, api)

	before, err := r.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	api.bodies[key] = srv("5061")
	api.mu.Unlock()
	after, err := r.snapshot()
	if err != nil {
		t.Fatal(err)
	}

	changes := diffOutside(before, after, r.prefix)
	if len(changes) != 2 || !strings.Contains(changes[0], `"port":5061`) || !strings.Contains(changes[1], `"port":5060`) {
		t.Errorf("diff = %q, want the old record removed and the new one added", changes)
	}
}

func TestRestoreWait(t *testing.T) {
	const fallback = 15 * time.Second
	cases := []struct {
		retryAfter string
		want       time.Duration
	}{
		{"", fallback},
		{"5", 5 * time.Second},
		{"60", 60 * time.Second},
		{"3600", 60 * time.Second},
		{"0", fallback},
		{"-3", fallback},
		{"soon", fallback},
		{"Wed, 21 Oct 2026 07:28:00 GMT", fallback},
	}
	for _, tc := range cases {
		h := http.Header{}
		if tc.retryAfter != "" {
			h.Set("Retry-After", tc.retryAfter)
		}
		if got := restoreWait(h, fallback); got != tc.want {
			t.Errorf("restoreWait(Retry-After %q) = %s, want %s", tc.retryAfter, got, tc.want)
		}
	}
	if got := restoreWait(nil, fallback); got != fallback {
		t.Errorf("restoreWait(nil) = %s, want %s", got, fallback)
	}
}
