package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// lastLine returns the last line of the transcript that is not blank.
func lastLine(transcript string) string {
	lines := strings.Split(strings.TrimSpace(transcript), "\n")
	return lines[len(lines)-1]
}

func TestExecuteCleansUpAfterAnInterrupt(t *testing.T) {
	f := &fakeDomain{}
	r, out := newDomainRunner(t, f, allowance{})
	// Requests 1 to 9 are the snapshot. Request 11 is p2's second write.
	f.hook(func(n int) {
		if n == 11 {
			r.interrupt()
		}
	})

	err := execute(r, "p2", probeArgs{}, "transcript.md")

	if !errors.Is(err, errInterrupted) {
		t.Errorf("execute = %v, want errInterrupted", err)
	}
	if code := exitCode(err); code != 130 {
		t.Errorf("exit code = %d, want 130", code)
	}
	writes := strings.Join(f.sentWrites(), "\n")
	want := strings.Join([]string{
		"PUT /v1/domains/example.org/records/TXT/_probe-ab12-p2-a",
		"PUT /v1/domains/example.org/records/TXT/_probe-ab12-p2-b",
		"DELETE /v1/domains/example.org/records/TXT/_probe-ab12-p2-a",
		"DELETE /v1/domains/example.org/records/TXT/_probe-ab12-p2-b",
	}, "\n")
	if !strings.HasPrefix(writes, want) {
		t.Errorf("writes:\n%s\nwant them to start with:\n%s", writes, want)
	}
	if strings.Count(writes, "PUT ") != 2 {
		t.Errorf("writes were sent after the interrupt:\n%s", writes)
	}
	text := out.String()
	for _, want := range []string{"### Cleanup", "### Zone check", "No record outside the probe prefix changed.", "Interrupted"} {
		if !strings.Contains(text, want) {
			t.Errorf("transcript lacks %q:\n%s", want, text)
		}
	}
	// Snapshot before (9), two writes, then cleanup and the snapshot after.
	if n, _ := f.count(); n < 9+2+2+9 {
		t.Errorf("server saw %d requests; the snapshot after the probe did not run", n)
	}
}

func TestExecuteInterruptedDuringTheFirstSnapshot(t *testing.T) {
	f := &fakeDomain{}
	r, _ := newDomainRunner(t, f, allowance{})
	f.hook(func(n int) {
		if n == 2 {
			r.interrupt()
		}
	})

	err := execute(r, "p2", probeArgs{}, "transcript.md")

	if exitCode(err) != 130 {
		t.Errorf("execute = %v (exit %d), want exit 130", err, exitCode(err))
	}
	if got := f.sentWrites(); len(got) != 0 {
		t.Errorf("writes were sent: %v", got)
	}
}

func TestExecuteP6FailureIsTheLastThingSaid(t *testing.T) {
	f := &fakeDomain{nameServers: originalNS, restoreRejects: -1}
	r, out := newDomainRunner(t, f, allowance{domainPatch: true})

	err := execute(r, "p6", probeArgs{targets: targetNS}, "transcript.md")

	if err == nil {
		t.Fatalf("execute = nil although the nameservers were not restored\n%s", out)
	}
	if code := exitCode(err); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	last := lastLine(out.String())
	for _, want := range append([]string{"FAILED", "by hand"}, originalNS...) {
		if !strings.Contains(last, want) {
			t.Errorf("last line of the transcript lacks %q: %s", want, last)
		}
		if !strings.Contains(err.Error(), want) && want != "FAILED" {
			t.Errorf("error lacks %q: %s", want, err)
		}
	}
	if !strings.Contains(out.String(), "### Zone check") {
		t.Errorf("the zone check must still run:\n%s", out)
	}
}

func TestExecuteP6FailureAndInterruptReportsBoth(t *testing.T) {
	f := &fakeDomain{nameServers: originalNS, restoreRejects: -1}
	r, out := newDomainRunner(t, f, allowance{domainPatch: true})
	f.hook(func(n int) {
		if n == 9+5 { // snapshot, then p6's fifth request: the PATCH to the target
			r.interrupt()
		}
	})

	err := execute(r, "p6", probeArgs{targets: targetNS}, "transcript.md")

	if code := exitCode(err); code != 1 {
		t.Errorf("exit code = %d, want 1: a failed restore outranks the interrupt", code)
	}
	if err == nil || !strings.Contains(err.Error(), "interrupted") || !strings.Contains(err.Error(), originalNS[0]) {
		t.Errorf("error must name the failure and the interrupt: %v", err)
	}
	if last := lastLine(out.String()); !strings.Contains(last, originalNS[0]) {
		t.Errorf("last line of the transcript must be the failure: %s", last)
	}
}

func TestExecuteReportsChangedRecordsLast(t *testing.T) {
	// The MX read answers differently the second time it is asked.
	var reads int32
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/records/MX") {
			n := atomic.AddInt32(&reads, 1)
			fmt.Fprintf(w, `[{"name":"@","data":"mx1.example.net","ttl":3600,"priority":%d}]`, n*10)
			return
		}
		w.Write([]byte("[]"))
	})
	r, out := newTestRunner(t, h)

	err := execute(r, "p3", probeArgs{}, "transcript.md")

	if err == nil || exitCode(err) != 1 {
		t.Fatalf("execute = %v, want an error with exit code 1", err)
	}
	text := out.String()
	if !strings.Contains(text, "RECORDS OUTSIDE THE PROBE PREFIX CHANGED") || !strings.Contains(text, `"priority":20`) {
		t.Errorf("transcript does not show the change:\n%s", text)
	}
	if strings.Contains(text, "No record outside the probe prefix changed.") {
		t.Errorf("transcript claims nothing changed:\n%s", text)
	}
	if last := lastLine(text); !strings.Contains(last, "FAILED") || !strings.Contains(last, "outside the probe prefix") {
		t.Errorf("last line of the transcript must be the failure: %s", last)
	}
	if !strings.Contains(text, "Zone snapshot before: 1 records (MX 1).") {
		t.Errorf("transcript lacks the per-type counts:\n%s", text)
	}
}

func TestExecuteReadOnlyProbeTakesNoSnapshot(t *testing.T) {
	api := &fakeAPI{}
	r, out := newTestRunner(t, api)

	if err := execute(r, "p12", probeArgs{}, "transcript.md"); err != nil {
		t.Fatal(err)
	}
	if got := api.got(); len(got) != 6 {
		t.Errorf("p12 sent %d requests, want 6 and no snapshot: %v", len(got), got)
	}
	if !strings.Contains(out.String(), "Transcript: transcript.md") {
		t.Errorf("transcript does not name itself:\n%s", out)
	}
}

func TestExitCode(t *testing.T) {
	if got := exitCode(nil); got != 0 {
		t.Errorf("exitCode(nil) = %d", got)
	}
	if got := exitCode(errInterrupted); got != 130 {
		t.Errorf("exitCode(errInterrupted) = %d, want 130", got)
	}
	if got := exitCode(errors.New("restore failed; the run was also interrupted")); got != 1 {
		t.Errorf("exitCode(other) = %d, want 1", got)
	}
}

func TestValidateProbe(t *testing.T) {
	for _, name := range []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8", "p9", "p10", "p12", "p13", "p14", "p15", "p16", "p17", "p18", "p19"} {
		if err := validateProbe(name); err != nil {
			t.Errorf("validateProbe(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"", "nonsense", "p11", "P1", "p1 ", "../p1", "p20"} {
		if err := validateProbe(name); err == nil || !strings.Contains(err.Error(), "unknown probe") {
			t.Errorf("validateProbe(%q) = %v, want an unknown probe error", name, err)
		}
	}
}

func TestRunWithAnUnknownProbeTouchesNothing(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&requests, 1) }))
	defer srv.Close()
	t.Setenv("GODADDY_API_TOKEN", "tok")
	t.Setenv("GODADDY_PROBE_DOMAIN", "example.org")
	t.Setenv("GODADDY_PROBE_BASEURL", srv.URL)
	outDir := filepath.Join(t.TempDir(), "out")

	for _, name := range []string{"nonsense", ""} {
		err := run(name, outDir, true, false, "a,b")
		if err == nil || !strings.Contains(err.Error(), "unknown probe") {
			t.Errorf("run(%q) = %v, want an unknown probe error", name, err)
		}
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Errorf("the output directory was created (stat error: %v)", err)
	}
	if n := atomic.LoadInt32(&requests); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

func TestRunNormalisesTheDomain(t *testing.T) {
	api := &fakeAPI{}
	srv := httptest.NewServer(api)
	defer srv.Close()
	t.Setenv("GODADDY_API_TOKEN", "tok")
	t.Setenv("GODADDY_PROBE_DOMAIN", "  Example.ORG \n")
	t.Setenv("GODADDY_PROBE_BASEURL", srv.URL)
	outDir := t.TempDir()

	// p1 is the shortest probe: four reads, one second apart.
	if err := run("p1", outDir, false, false, ""); err != nil {
		t.Fatal(err)
	}
	got := api.got()
	if len(got) != 4 || !strings.HasPrefix(got[0], "GET /v1/domains/example.org/records/A/_probe-") {
		t.Errorf("requests = %v, want four, using the trimmed, lower-case domain", got)
	}
	files, _ := filepath.Glob(filepath.Join(outDir, "p1-*.md"))
	if len(files) != 1 {
		t.Errorf("transcripts written: %v, want one", files)
	}
}

func TestHTTPClientNeverFollowsARedirect(t *testing.T) {
	var elsewhere int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&elsewhere, 1) }))
	defer other.Close()
	api := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, other.URL+"/v1/domains/evil.example/records", http.StatusTemporaryRedirect)
	})
	r, out := newTestRunner(t, api)
	r.hc = newHTTPClient()

	resp := r.put("write", "TXT", r.name("a"), one("x"))

	if resp.Status != http.StatusTemporaryRedirect {
		t.Errorf("status = %d, want the 307 itself", resp.Status)
	}
	if n := atomic.LoadInt32(&elsewhere); n != 0 {
		t.Errorf("the client followed the redirect %d time(s)", n)
	}
	if !strings.Contains(out.String(), "Header Location: ") {
		t.Errorf("transcript does not show where the redirect pointed:\n%s", out)
	}
}

// N3: a probe record that cannot be deleted fails the run.
func TestExecuteFailsWhenAProbeRecordIsLeftBehind(t *testing.T) {
	var written int32
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == "PUT":
			atomic.StoreInt32(&written, 1)
		case req.Method == "DELETE":
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasSuffix(req.URL.Path, "/records/A") && atomic.LoadInt32(&written) == 1:
			w.Write([]byte(`[{"type":"A","name":"_probe-ab12-p3","data":"192.0.2.1","ttl":600}]`))
		default:
			w.Write([]byte("[]"))
		}
	})
	r, out := newTestRunner(t, h)

	err := execute(r, "p3", probeArgs{}, "transcript.md")

	if exitCode(err) != 1 {
		t.Fatalf("execute = %v (exit %d), want exit 1", err, exitCode(err))
	}
	for _, want := range []string{"1 probe record(s) left behind; delete by hand:", "_probe-ab12-p3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %s", want, err)
		}
	}
	text := out.String()
	if last := lastLine(text); !strings.Contains(last, "FAILED") || !strings.Contains(last, "_probe-ab12-p3") {
		t.Errorf("last line of the transcript must be the failure: %s", last)
	}
	if !strings.Contains(text, "DELETE of A _probe-ab12-p3 was not confirmed (status 500)") {
		t.Errorf("cleanup did not record the failed DELETE:\n%s", text)
	}
}

func TestCleanupAcceptsA404(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "DELETE" {
			w.WriteHeader(http.StatusNotFound)
		}
	})
	r, out := newTestRunner(t, h)
	r.put("write", "TXT", r.name("a"), one("x"))

	r.cleanup()

	if strings.Contains(out.String(), "not confirmed") {
		t.Errorf("a 404 means the record is gone; cleanup must not complain:\n%s", out)
	}
}

// N4: a probe that panics still gets its records deleted and its zone
// checked.
func TestExecuteCleansUpAfterAPanic(t *testing.T) {
	probes["ptest-panic"] = func(r *runner, _ probeArgs) error {
		r.put("write before the panic", "TXT", r.name("boom"), one("x"))
		panic("something broke")
	}
	t.Cleanup(func() { delete(probes, "ptest-panic") })
	f := &fakeDomain{}
	r, out := newDomainRunner(t, f, allowance{})

	err := execute(r, "ptest-panic", probeArgs{}, "transcript.md")

	if exitCode(err) != 1 {
		t.Errorf("execute = %v (exit %d), want exit 1", err, exitCode(err))
	}
	if err == nil || !strings.Contains(err.Error(), "probe panicked: something broke") {
		t.Errorf("error must report the panic: %v", err)
	}
	want := "PUT /v1/domains/example.org/records/TXT/_probe-ab12-boom\nDELETE /v1/domains/example.org/records/TXT/_probe-ab12-boom"
	if got := strings.Join(f.sentWrites(), "\n"); got != want {
		t.Errorf("writes:\n%s\nwant:\n%s", got, want)
	}
	text := out.String()
	if !strings.Contains(text, "### Zone check") || !strings.Contains(text, "No record outside the probe prefix changed.") {
		t.Errorf("the zone check must still run:\n%s", text)
	}
	if last := lastLine(text); !strings.Contains(last, "FAILED: probe panicked: something broke") {
		t.Errorf("last line of the transcript must be the failure: %s", last)
	}
	if n, _ := f.count(); n != 9+1+1+9 {
		t.Errorf("server saw %d requests, want two snapshots, one write and one delete", n)
	}
}

func TestEveryProbeNameHasAFunction(t *testing.T) {
	for _, name := range probeNames {
		if probes[name] == nil {
			t.Errorf("probe %s is accepted by -probe but has no function", name)
		}
	}
	if len(probes) != len(probeNames) {
		t.Errorf("%d probe functions for %d names", len(probes), len(probeNames))
	}
}
