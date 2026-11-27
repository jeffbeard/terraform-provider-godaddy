package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

const p17Summary = "P17 summary: empty replace deleted nothing for: %s; " +
	"one-record replace deleted the other record for: %s; inconclusive or skipped: %s"

// typeWidePuts returns the type-wide PUTs among writes, as "PUT path body".
func typeWidePuts(writes []string) []string {
	var out []string
	for _, w := range writes {
		fields := strings.Fields(w)
		if fields[0] == "PUT" && len(strings.Split(fields[1], "/")) == 6 {
			out = append(out, w)
		}
	}
	return out
}

func real(recordType, name string) zoneRecord {
	return newZoneRecord(map[string]interface{}{"name": name, "data": "x", "ttl": 3600}, recordType)
}

func TestP17OnAnAPIThatBehavesLikeAAAA(t *testing.T) {
	f := &fakeZone{}
	r, out := newDomainRunner(t, f, allowanceFor("p17", true))

	if err := p17(r, nil); err != nil {
		t.Fatal(err)
	}

	text := out.String()
	assertNothingRefused(t, text)
	const root = "/v1/domains/example.org/records/"
	wantTypeWide := []string{
		"PUT " + root + "TXT []",
		"PUT " + root + `TXT [{"data":"b2","name":"_probe-ab12-p17-txt-b","ttl":3600}]`,
		"PUT " + root + "MX []",
		"PUT " + root + `MX [{"data":"mx-b2.example.net","name":"_probe-ab12-p17-mx-b","priority":10,"ttl":3600}]`,
		"PUT " + root + "SRV []",
		"PUT " + root + `SRV [{"data":"b2.example.net","name":"_probe-ab12-p17-srv-b","port":5060,"priority":10,"protocol":"_tcp","service":"_sip","ttl":3600,"weight":5}]`,
	}
	writes := f.sent()
	if got := typeWidePuts(writes); strings.Join(got, "\n") != strings.Join(wantTypeWide, "\n") {
		t.Errorf("type-wide writes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantTypeWide, "\n"))
	}
	assertOnlyProbeWrites(t, writes, "PUT "+root+"TXT", "PUT "+root+"MX", "PUT "+root+"SRV")
	wantGroups := []string{
		"PUT " + root + `TXT/_probe-ab12-p17-txt-a [{"data":"a1","ttl":3600}]`,
		"PUT " + root + `TXT/_probe-ab12-p17-txt-b [{"data":"b1","ttl":3600}]`,
		"PUT " + root + `MX/_probe-ab12-p17-mx-a [{"data":"mx-a.example.net","priority":10,"ttl":3600}]`,
		"PUT " + root + `MX/_probe-ab12-p17-mx-b [{"data":"mx-b.example.net","priority":10,"ttl":3600}]`,
		"PUT " + root + `SRV/_probe-ab12-p17-srv-a [{"data":"a.example.net","port":5060,"priority":10,"protocol":"_tcp","service":"_sip","ttl":3600,"weight":5}]`,
		"PUT " + root + `SRV/_probe-ab12-p17-srv-b [{"data":"b.example.net","port":5060,"priority":10,"protocol":"_tcp","service":"_sip","ttl":3600,"weight":5}]`,
	}
	all := strings.Join(writes, "\n") + "\n"
	for _, want := range wantGroups {
		if !strings.Contains(all, want+"\n") {
			t.Errorf("writes lack %s", want)
		}
	}
	if len(writes) != 12 {
		t.Errorf("%d writes were sent, want 12:\n%s", len(writes), all)
	}
	var lines []string
	for _, recordType := range []string{"TXT", "MX", "SRV"} {
		lines = append(lines, "P17 "+recordType+" empty replace: status 200; records before 2, after 2; DELETED NOTHING")
	}
	lines = append(lines,
		"P17 TXT one-record replace: status 200; a was DELETED; b holds b2",
		"P17 MX one-record replace: status 200; a was DELETED; b holds mx-b2.example.net",
		"P17 SRV one-record replace: status 200; a was DELETED; b holds b2.example.net",
		fmt.Sprintf(p17Summary, "TXT, MX, SRV", "TXT, MX, SRV", "none"),
	)
	assertLines(t, text, lines...)
	for _, label := range []string{"all TXT before", "empty type-wide replace", "all MX after the empty replace", "all SRV after the one-record replace"} {
		if !strings.Contains(text, label) {
			t.Errorf("transcript lacks an entry labelled %q", label)
		}
	}
	if len(r.created) != 6 {
		t.Errorf("%d groups registered for cleanup, want 6", len(r.created))
	}
}

func TestP17WhenAnEmptyReplaceDeletesOneType(t *testing.T) {
	f := &fakeZone{emptyDeletes: map[string]bool{"MX": true}}
	r, out := newDomainRunner(t, f, allowanceFor("p17", true))

	if err := p17(r, nil); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	assertNothingRefused(t, text)
	assertLines(t, text,
		"P17 TXT empty replace: status 200; records before 2, after 2; DELETED NOTHING",
		"P17 MX empty replace: status 200; records before 2, after 0; DELETED 2 record(s)",
		"P17 SRV empty replace: status 200; records before 2, after 2; DELETED NOTHING",
		"P17 summary: empty replace deleted nothing for: TXT, SRV; "+
			"one-record replace deleted the other record for: TXT, SRV; inconclusive or skipped: MX",
	)
	if !strings.Contains(text, "P17 MX one-record replace: status 200; inconclusive because") {
		t.Errorf("a was gone before the one-record replace of MX; that line must be inconclusive:\n%s", text)
	}
	if got := typeWidePuts(f.sent()); len(got) != 6 {
		t.Errorf("%d type-wide PUTs, want 6: the one-record replace still runs for MX", len(got))
	}
}

func TestP17WhenAOneRecordReplaceKeepsTheOther(t *testing.T) {
	f := &fakeZone{ignoreTypeWide: true}
	r, out := newDomainRunner(t, f, allowanceFor("p17", true))

	if err := p17(r, nil); err != nil {
		t.Fatal(err)
	}
	assertLines(t, out.String(),
		"P17 TXT one-record replace: status 200; a was KEPT; b holds b1",
		"P17 summary: empty replace deleted nothing for: TXT, MX, SRV; "+
			"one-record replace deleted the other record for: none; inconclusive or skipped: none",
	)
}

func TestP17RefusedReplacesAreInconclusive(t *testing.T) {
	// Without the allowance every type-wide PUT is refused by the guard, so
	// its status is 0. Steps 6 and 7 are still attempted.
	f := &fakeZone{}
	r, out := newDomainRunner(t, f, allowance{})

	if err := p17(r, nil); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	assertLines(t, text,
		"P17 summary: empty replace deleted nothing for: none; "+
			"one-record replace deleted the other record for: none; inconclusive or skipped: TXT, MX, SRV",
	)
	if n := strings.Count(text, "NOT SENT"); n != 6 {
		t.Errorf("%d type-wide PUTs were refused, want 6", n)
	}
	for _, verdict := range []string{"DELETED NOTHING", "a was DELETED", "a was KEPT"} {
		if strings.Contains(text, verdict) {
			t.Errorf("a verdict (%s) was drawn from a replace that was not accepted:\n%s", verdict, text)
		}
	}
	assertOnlyProbeWrites(t, f.sent())
}

func TestP17SkipsATypeTheZoneUses(t *testing.T) {
	f := &fakeZone{}
	r, out := newDomainRunner(t, f, allowanceFor("p17", true))
	before := []zoneRecord{real("MX", "@"), real("TXT", "_probe-ab12-old"), real("AAAA", "www")}

	if err := p17(r, before); err != nil {
		t.Fatal(err)
	}

	text := out.String()
	for _, w := range f.sent() {
		if strings.Contains(w, "/records/MX") {
			t.Errorf("an MX write was sent although the zone has a real MX record: %s", w)
		}
	}
	if got := typeWidePuts(f.sent()); len(got) != 4 {
		t.Errorf("%d type-wide PUTs, want 4 (TXT and SRV)", len(got))
	}
	if !strings.Contains(text, "P17 MX: skipped, because the zone has a real MX record") {
		t.Errorf("transcript does not say MX was skipped:\n%s", text)
	}
	assertLines(t, text,
		"P17 summary: empty replace deleted nothing for: TXT, SRV; "+
			"one-record replace deleted the other record for: TXT, SRV; inconclusive or skipped: MX",
	)
}

func TestP17RefusesWhenTheZoneUsesAllThreeTypes(t *testing.T) {
	f := &fakeZone{}
	r, _ := newDomainRunner(t, f, allowanceFor("p17", true))
	before := []zoneRecord{real("MX", "@"), real("TXT", "@"), real("SRV", "www")}

	err := p17(r, before)

	if err == nil || !strings.Contains(err.Error(), "P17") {
		t.Fatalf("p17 = %v, want an error naming P17", err)
	}
	if got := f.sent(); len(got) != 0 {
		t.Errorf("p17 sent writes: %v", got)
	}
}

func TestExecuteP17ReadsThePreconditionFromTheSnapshot(t *testing.T) {
	f := &fakeZone{}
	f.seed("MX", "@", map[string]interface{}{"data": "mx1.example.net", "priority": 10, "ttl": 3600})
	f.seed("A", "@", map[string]interface{}{"data": "192.0.2.1", "ttl": 3600})
	r, out := newDomainRunner(t, f, allowanceFor("p17", true))

	if err := execute(r, "p17", probeArgs{}, "transcript.md"); err != nil {
		t.Fatalf("execute = %v\n%s", err, out)
	}
	assertNothingRefused(t, out.String())
	for _, w := range f.sent() {
		if strings.Contains(w, "/records/MX") {
			t.Errorf("an MX write was sent although the zone has a real MX record: %s", w)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.groups["MX"]["@"]) != 1 || len(f.groups["A"]["@"]) != 1 {
		t.Errorf("the zone's own records changed: %v", f.groups)
	}
	if len(f.groups["TXT"])+len(f.groups["SRV"]) != 0 {
		t.Errorf("probe records were left behind: %v", f.groups)
	}
}

func TestP17CleansUpAfterAnInterrupt(t *testing.T) {
	f := &fakeZone{}
	r, out := newDomainRunner(t, f, allowanceFor("p17", true))
	// Requests 1 to 9 are the snapshot, 10 and 11 the two TXT groups, 12 the
	// list before the first type-wide PUT.
	f.hook(func(n int) {
		if n == 12 {
			r.interrupt()
		}
	})

	err := execute(r, "p17", probeArgs{}, "transcript.md")

	if !errors.Is(err, errInterrupted) {
		t.Errorf("execute = %v, want errInterrupted\n%s", err, out)
	}
	writes := f.sent()
	if got := typeWidePuts(writes); len(got) != 0 {
		t.Errorf("a type-wide PUT was sent after the interrupt: %v", got)
	}
	want := []string{
		"DELETE /v1/domains/example.org/records/TXT/_probe-ab12-p17-txt-a",
		"DELETE /v1/domains/example.org/records/TXT/_probe-ab12-p17-txt-b",
	}
	if len(writes) != 4 || strings.Join(writes[2:], "\n") != strings.Join(want, "\n") {
		t.Errorf("writes:\n%s\nwant two TXT writes and then:\n%s", strings.Join(writes, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(out.String(), "P17 summary") || strings.Contains(out.String(), "P17 MX") {
		t.Errorf("an interrupted p17 must stop and draw no conclusions:\n%s", out)
	}
}
