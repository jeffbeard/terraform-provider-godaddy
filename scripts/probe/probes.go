package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type rec = map[string]interface{}

// one returns a single-value group body with a safe TTL.
func one(data string) []rec { return []rec{{"data": data, "ttl": 3600}} }

// p1: what does a read return for a missing group, a missing domain, a
// domain in another account, and a bad token? What under-scoped tokens get
// is P16's question.
func p1(r *runner) {
	missing := r.group("A", r.name("p1-missing"))
	r.inOrder(
		func() { r.do("P1 group that does not exist", "GET", missing, nil) },
		func() {
			r.do("P1 domain that does not exist", "GET",
				"/v1/domains/probe-"+r.runID+"-does-not-exist.org/records/A/www", nil)
		},
		func() { r.do("P1 domain in another account", "GET", "/v1/domains/example.com/records/A/www", nil) },
		func() { r.doAs("P1 invalid token", "not-a-real-token", "GET", missing, nil) },
	)
}

// p2: do PUT and DELETE by type and name leave neighbours alone?
func p2(r *runner) {
	a, b := r.name("p2-a"), r.name("p2-b")
	r.inOrder(
		func() { r.put("P2 create TXT a", "TXT", a, one("a1")) },
		func() { r.put("P2 create TXT b (neighbour, other name)", "TXT", b, one("b1")) },
		func() { r.put("P2 create A a (neighbour, other type)", "A", a, one("192.0.2.1")) },

		func() {
			r.put("P2 replace TXT a with two values", "TXT", a,
				[]rec{{"data": "a2", "ttl": 3600}, {"data": "a3", "ttl": 3600}})
		},
		func() { r.do("P2 read TXT a, expect a2 and a3 only", "GET", r.group("TXT", a), nil) },
		func() { r.do("P2 read TXT b, expect b1 unchanged", "GET", r.group("TXT", b), nil) },
		func() { r.do("P2 read A a, expect unchanged", "GET", r.group("A", a), nil) },

		func() { r.do("P2 delete TXT a", "DELETE", r.group("TXT", a), nil) },
		func() { r.do("P2 read TXT a after delete", "GET", r.group("TXT", a), nil) },
		func() { r.do("P2 read TXT b after delete, expect b1", "GET", r.group("TXT", b), nil) },
		func() { r.do("P2 read A a after delete, expect unchanged", "GET", r.group("A", a), nil) },
		func() { r.do("P2 delete TXT a again (already gone)", "DELETE", r.group("TXT", a), nil) },

		func() { r.put("P2 create CNAME b where TXT b exists", "CNAME", b, one("target.example.net")) },
		func() { r.do("P2 read CNAME b", "GET", r.group("CNAME", b), nil) },
		func() { r.do("P2 read TXT b after CNAME attempt", "GET", r.group("TXT", b), nil) },
	)
}

// p3: what is the minimum TTL?
func p3(r *runner) {
	name := r.name("p3")
	for _, ttl := range []int{3600, 600, 599, 300, 60, 1, 0} {
		ttl := ttl
		r.inOrder(
			func() {
				r.put(fmt.Sprintf("P3 write with ttl %d", ttl), "A", name, []rec{{"data": "192.0.2.1", "ttl": ttl}})
			},
			func() { r.do(fmt.Sprintf("P3 read after ttl %d", ttl), "GET", r.group("A", name), nil) },
		)
	}
}

// p4: in what form does GoDaddy return values?
func p4(r *runner) {
	check := func(label, recordType, suffix string, body []rec) {
		name := r.name(suffix)
		r.inOrder(
			func() { r.put("P4 write "+label, recordType, name, body) },
			func() { r.do("P4 read "+label, "GET", r.group(recordType, strings.ToLower(name)), nil) },
		)
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
	srvBare := r.name("p5-srv-bare")
	r.inOrder(
		func() {
			r.put("P5 SRV with all fields", "SRV", srv, []rec{{
				"data": "target.example.net", "priority": 10, "weight": 5, "port": 5060,
				"service": "_sip", "protocol": "_tcp", "ttl": 3600,
			}})
		},
		func() { r.do("P5 read SRV by the name used to write it", "GET", r.group("SRV", srv), nil) },
		func() {
			r.do("P5 read all SRV records, to see how the name is returned", "GET",
				"/v1/domains/"+r.domain+"/records/SRV", nil)
		},
		func() {
			r.put("P5 SRV without service and protocol", "SRV", srvBare, []rec{{
				"data": "target.example.net", "priority": 10, "weight": 5, "port": 5060, "ttl": 3600,
			}})
		},
		func() { r.do("P5 read SRV without service and protocol", "GET", r.group("SRV", srvBare), nil) },
	)

	caa := r.name("p5-caa")
	for _, v := range []struct {
		label string
		body  rec
	}{
		{"CAA as one quoted string", rec{"data": `0 issue "letsencrypt.org"`, "ttl": 3600}},
		{"CAA as one unquoted string", rec{"data": "0 issue letsencrypt.org", "ttl": 3600}},
		{"CAA with separate flag and tag fields", rec{"data": "letsencrypt.org", "flag": 0, "tag": "issue", "ttl": 3600}},
	} {
		v := v
		r.inOrder(
			func() { r.put("P5 write "+v.label, "CAA", caa, []rec{v.body}) },
			func() { r.do("P5 read after "+v.label, "GET", r.group("CAA", caa), nil) },
		)
	}
}

// p7: how does the whole-zone list page? Read-only.
func p7(r *runner) {
	root := "/v1/domains/" + r.domain + "/records"
	for _, q := range []string{"", "?limit=2&offset=0", "?limit=2&offset=1", "?limit=2&offset=2",
		"?limit=500&offset=1", "?limit=500&offset=2"} {
		if r.halted() {
			return
		}
		r.do("P7 list "+q, "GET", root+q, nil)
	}
}

// p8: what does a rate-limited response contain? Read-only. Uses up the
// credential's quota for about a minute. Only the first request, the first
// 429 and the request after it are written to the transcript.
func p8(r *runner) {
	gap := r.gap
	r.gap = 0
	defer func() { r.gap = gap; r.quiet = false }()

	path := r.group("TXT", r.name("p8-missing"))
	started := time.Now()
	r.do("P8 request 1", "GET", path, nil)
	r.quiet = true
	for i := 2; i <= 120; i++ {
		if r.halted() {
			fmt.Fprintf(r.out, "\nP8 interrupted after %d requests.\n", i-1)
			return
		}
		if resp := r.do(fmt.Sprintf("P8 request %d", i), "GET", path, nil); resp.Status == 429 {
			r.quiet = false
			fmt.Fprintf(r.out, "\nRequest %d returned 429 after %s. It is the first 429:\n",
				i, time.Since(started).Round(time.Millisecond))
			fmt.Fprint(r.out, r.lastEntry)
			r.do("P8 next request after the first 429", "GET", path, nil)
			fmt.Fprint(r.out, "\nWait at least one minute before running another probe.\n")
			return
		}
	}
	if r.halted() {
		fmt.Fprint(r.out, "\nP8 interrupted.\n")
		return
	}
	r.quiet = false
	fmt.Fprintf(r.out, "\nNo 429 after 120 requests in %s.\n", time.Since(started).Round(time.Millisecond))
}

// p9: does a type-wide replace accept an empty array? This is the request
// the old resource sends. It runs against AAAA and only when the zone has no
// AAAA records of its own.
func p9(r *runner, before []zoneRecord) error {
	if err := r.noRealRecords("P9", "AAAA", before); err != nil {
		return err
	}
	name := r.name("p9")
	r.put("P9 create an AAAA probe record", "AAAA", name, one("2001:db8::1"))
	r.do("P9 replace all AAAA records with an empty array", "PUT",
		"/v1/domains/"+r.domain+"/records/AAAA", []rec{})
	r.do("P9 read the probe record afterwards", "GET", r.group("AAAA", name), nil)
	return nil
}

// noRealRecords is the precondition of the probes that replace every
// record of one type: the zone, as read before the probe, must hold no
// record of that type outside the probe prefix.
func (r *runner) noRealRecords(probe, recordType string, before []zoneRecord) error {
	for _, z := range before {
		if z.Type == recordType && !strings.HasPrefix(z.Name, r.prefix) {
			return fmt.Errorf("zone has a real %s record (%s); %s would delete it, so it was not run", recordType, z, probe)
		}
	}
	return nil
}

// p10: does a read straight after a write, or straight after a delete,
// return the new state? Twenty writes, each read back at once; after writes
// 5, 10, 15 and 20 the group is deleted and read at once. A request that
// fails is counted as failed, never as stale.
func p10(r *runner) {
	gap := r.gap
	defer func() { r.gap = gap; r.quiet = false }()
	r.quiet = true

	name := r.name("p10")
	path := r.group("TXT", name)
	var writeReads, staleWrites, deleteReads, staleDeletes, failed int

	for i := 1; i <= 20 && !r.halted(); i++ {
		want := fmt.Sprintf("value-%d", i)
		r.gap = 3 * gap // with the read, two requests every three seconds
		put := r.put(fmt.Sprintf("P10 write %d", i), "TXT", name, one(want))
		if r.halted() {
			break
		}
		if !put.ok() {
			failed++
			fmt.Fprintf(r.out, "\n- write %d failed: status %d\n", i, put.Status)
		} else {
			r.gap = 0 // read with no delay
			resp := r.do(fmt.Sprintf("P10 read after write %d", i), "GET", path, nil)
			switch {
			case r.halted():
			case resp.Status != 200:
				failed++
				fmt.Fprintf(r.out, "\n- read %d failed: status %d\n", i, resp.Status)
			case strings.Contains(string(resp.Body), `"`+want+`"`):
				writeReads++
			default:
				writeReads++
				staleWrites++
				fmt.Fprintf(r.out, "\n- write %d: read straight after was stale. Body: %s\n", i, resp.Body)
				r.gap = gap
				for waited := 1; waited <= 30 && !r.halted(); waited++ {
					again := r.do(fmt.Sprintf("P10 read %d after write %d", waited+1, i), "GET", path, nil)
					if again.Status == 200 && strings.Contains(string(again.Body), `"`+want+`"`) {
						fmt.Fprintf(r.out, "- write %d: visible after %d more read(s), one every %s.\n", i, waited, gap)
						break
					}
					if waited == 30 {
						fmt.Fprintf(r.out, "- write %d: still not visible after 30 more reads.\n", i)
					}
				}
			}
		}
		if i%5 != 0 || r.halted() {
			continue
		}

		r.gap = gap
		del := r.do(fmt.Sprintf("P10 delete after write %d", i), "DELETE", path, nil)
		if r.halted() {
			break
		}
		if !del.ok() {
			failed++
			fmt.Fprintf(r.out, "\n- delete after write %d failed: status %d\n", i, del.Status)
			continue
		}
		r.gap = 0
		resp := r.do(fmt.Sprintf("P10 read after the delete after write %d", i), "GET", path, nil)
		var left []json.RawMessage
		switch {
		case r.halted():
		case resp.Status != 200 && resp.Status != 404:
			failed++
			fmt.Fprintf(r.out, "\n- read after the delete after write %d failed: status %d\n", i, resp.Status)
		case resp.Status == 200 && json.Unmarshal(resp.Body, &left) == nil && len(left) > 0:
			deleteReads++
			staleDeletes++
			fmt.Fprintf(r.out, "\n- delete after write %d: read straight after still returned %d record(s) (stale delete). Body: %s\n",
				i, len(left), resp.Body)
		default:
			deleteReads++
			fmt.Fprintf(r.out, "\n- delete after write %d: read straight after returned status %d, no records.\n", i, resp.Status)
		}
	}

	if r.halted() {
		fmt.Fprint(r.out, "\nP10 interrupted; the counts below are for the part that ran.\n")
	}
	fmt.Fprintf(r.out, "\nP10 summary: %d of %d reads straight after a write were stale; "+
		"%d of %d reads straight after a delete were stale; %d requests failed.\n",
		staleWrites, writeReads, staleDeletes, deleteReads, failed)
}

// p12: do the type-wide and whole-zone reads work, and do they page?
// Read-only.
func p12(r *runner) {
	root := "/v1/domains/" + r.domain + "/records"
	for _, q := range []string{"/TXT", "/TXT?limit=1&offset=0", "/TXT?limit=1&offset=1", "/NS", "/SOA", ""} {
		if r.halted() {
			return
		}
		r.do("P12 read "+root+q, "GET", root+q, nil)
	}
}

// p13: does a type-wide replace with a non-empty array delete the other
// records of that type? P9 showed that an empty array deletes nothing. Like
// P9 it runs against AAAA and only when the zone has no AAAA records of its
// own: if the answer is yes, every AAAA record but the one in the body goes.
func p13(r *runner, before []zoneRecord) error {
	if err := r.noRealRecords("P13", "AAAA", before); err != nil {
		return err
	}
	a, b := r.name("p13-a"), r.name("p13-b")
	all := "/v1/domains/" + r.domain + "/records/AAAA"
	var putA, putB, replace, readA, readB response
	r.inOrder(
		func() { putA = r.put("P13 create AAAA p13-a", "AAAA", a, one("2001:db8::a")) },
		func() { putB = r.put("P13 create AAAA p13-b", "AAAA", b, one("2001:db8::b")) },
		func() { r.do("P13 all AAAA before", "GET", all, nil) },
		func() {
			replace = r.do("P13 replace all AAAA records with one record, for p13-b", "PUT", all,
				[]rec{{"name": b, "data": "2001:db8::c", "ttl": 3600}})
		},
		func() {
			readA = r.do("P13 read p13-a, gone if the replace is type-wide", "GET", r.group("AAAA", a), nil)
		},
		func() { readB = r.do("P13 read p13-b, expect 2001:db8::c only", "GET", r.group("AAAA", b), nil) },
		func() { r.do("P13 all AAAA after", "GET", all, nil) },
	)
	if r.halted() {
		fmt.Fprint(r.out, "\nP13 interrupted; no result.\n")
		return nil
	}

	var why []string
	for _, step := range []struct {
		what   string
		status int
		ok     bool
	}{
		{"the write of p13-a", putA.Status, putA.ok()},
		{"the write of p13-b", putB.Status, putB.ok()},
		{"the type-wide replace", replace.Status, replace.ok()},
		{"the read of p13-a", readA.Status, readA.Status == 200},
		{"the read of p13-b", readB.Status, readB.Status == 200},
	} {
		if !step.ok {
			why = append(why, fmt.Sprintf("%s returned status %d", step.what, step.status))
		}
	}
	leftA, errA := dataOf(readA.Body)
	leftB, errB := dataOf(readB.Body)
	if len(why) == 0 && (errA != nil || errB != nil) {
		why = append(why, "a read did not return a JSON array of records")
	}
	holds := strings.Join(leftB, ", ")
	if holds == "" {
		holds = "nothing"
	}
	switch {
	case len(why) > 0:
		fmt.Fprintf(r.out, "\nP13 result: inconclusive, because %s.\n", strings.Join(why, " and "))
	case len(leftA) == 0:
		fmt.Fprintf(r.out, "\nP13 result: p13-a was DELETED by the type-wide replace; p13-b holds: %s.\n", holds)
	default:
		fmt.Fprintf(r.out, "\nP13 result: p13-a SURVIVED the type-wide replace (it holds: %s); p13-b holds: %s.\n",
			strings.Join(leftA, ", "), holds)
	}
	return nil
}

// dataOf returns the data field of each record in a JSON array of records.
func dataOf(body []byte) ([]string, error) {
	var records []struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(body, &records); err != nil {
		return nil, err
	}
	out := make([]string, len(records))
	for i, record := range records {
		out[i] = record.Data
	}
	return out, nil
}

// p14: what forms do SRV and CAA really take? P5 showed that an SRV record
// is accepted and listed but not returned by a read of the name it was
// written to, and that CAA needs a "flags" field. P14 looks for the name an
// SRV record reads back under, and for a CAA body GoDaddy accepts.
func p14(r *runner) {
	p14SRV(r)
	if r.halted() {
		fmt.Fprint(r.out, "\nP14 interrupted; no result.\n")
		return
	}
	p14CAA(r)
	if r.halted() {
		fmt.Fprint(r.out, "\nP14 interrupted; the CAA result is not complete.\n")
	}
}

func p14SRV(r *runner) {
	name := r.name("p14-srv")
	forms := []string{name, "_sip._tcp." + name, name + "._sip._tcp", "_sip." + name}
	steps := []func(){func() {
		r.put("P14 SRV with all fields", "SRV", name, []rec{{
			"data": "target.example.net", "priority": 10, "weight": 5, "port": 5060,
			"service": "_sip", "protocol": "_tcp", "ttl": 3600,
		}})
	}}
	var found []string
	for _, form := range forms {
		form := form
		steps = append(steps, func() {
			resp := r.do("P14 read SRV as "+form, "GET", r.group("SRV", form), nil)
			if records, err := dataOf(resp.Body); resp.Status == 200 && err == nil && len(records) > 0 {
				found = append(found, form)
			}
		})
	}
	steps = append(steps, func() {
		r.do("P14 read all SRV records", "GET", "/v1/domains/"+r.domain+"/records/SRV", nil)
	})
	r.inOrder(steps...)
	if r.halted() {
		return
	}
	if len(found) == 0 {
		found = []string{"none"}
	}
	fmt.Fprintf(r.out, "\nP14 SRV result: a read by name returned records for: %s\n", strings.Join(found, ", "))
}

// caaStyles are the ways P14 tries to say "flags 0, this tag, this value".
// The number of the body in the transcript is the index plus one.
var caaStyles = []func(tag, value string) rec{
	func(tag, value string) rec { return rec{"data": value, "flags": 0, "tag": tag, "ttl": 3600} },
	func(tag, value string) rec { return rec{"data": value, "flags": "0", "tag": tag, "ttl": 3600} },
	func(tag, value string) rec {
		return rec{"data": `"` + value + `"`, "flags": 0, "tag": tag, "ttl": 3600}
	},
	func(tag, value string) rec {
		return rec{"data": `0 ` + tag + ` "` + value + `"`, "flags": 0, "ttl": 3600}
	},
}

func p14CAA(r *runner) {
	name := r.name("p14-caa")
	group := r.group("CAA", name)
	accepted := 0
	var single response
	for i, style := range caaStyles {
		if r.halted() {
			return
		}
		n := i + 1
		put := r.put(fmt.Sprintf("P14 write CAA body %d", n), "CAA", name, []rec{style("issue", "letsencrypt.org")})
		if r.halted() {
			return
		}
		single = r.do(fmt.Sprintf("P14 read after CAA body %d", n), "GET", group, nil)
		if put.ok() {
			accepted = n
			break
		}
	}
	if r.halted() {
		return
	}
	if accepted == 0 {
		fmt.Fprint(r.out, "\nP14 CAA result: no CAA form was accepted.\n")
		return
	}

	style := caaStyles[accepted-1]
	var double, both response
	r.inOrder(
		func() { r.do("P14 read all CAA records", "GET", "/v1/domains/"+r.domain+"/records/CAA", nil) },
		func() {
			double = r.put(fmt.Sprintf("P14 write two CAA records in the form of body %d", accepted), "CAA", name,
				[]rec{style("issue", "letsencrypt.org"), style("issuewild", ";")})
		},
		func() { both = r.do("P14 read the two-record CAA group", "GET", group, nil) },
	)
	if r.halted() {
		return
	}
	fmt.Fprintf(r.out, "\nP14 CAA result: body %d was accepted. The group with one record read back as (status %d): %s\n",
		accepted, single.Status, single.Body)
	fmt.Fprintf(r.out, "\nThe write of two records in that form returned status %d; the group then read back as (status %d): %s\n",
		double.Status, both.Status, both.Body)
}

// srvState is what one list of all SRV records shows at the probe's name.
type srvState struct {
	status int      // of the list
	sip    []string // data of the _sip records at the name
	xmpp   []string // data of the _xmpp-client records at the name
	total  int      // SRV records at the name, whatever their service
}

func (s srvState) ok() bool { return s.status == 200 }

func (s srvState) String() string {
	show := func(data []string) string {
		if len(data) == 0 {
			return "absent"
		}
		return strings.Join(data, ", ")
	}
	return fmt.Sprintf("sip: %s; xmpp: %s", show(s.sip), show(s.xmpp))
}

// srvStateOf reads the response to GET /records/SRV. A record belongs to
// name if it is listed under name or under a long form of it.
func srvStateOf(resp response, name string) srvState {
	state := srvState{status: resp.Status}
	var records []struct {
		Name    string `json:"name"`
		Service string `json:"service"`
		Data    string `json:"data"`
	}
	if resp.Status != 200 {
		return state
	}
	if err := json.Unmarshal(resp.Body, &records); err != nil {
		state.status = 0
		return state
	}
	for _, record := range records {
		listed := strings.ToLower(record.Name)
		if listed != name && !strings.HasSuffix(listed, "."+name) {
			continue
		}
		state.total++
		switch {
		case record.Service == "_sip" || strings.HasPrefix(listed, "_sip."):
			state.sip = append(state.sip, record.Data)
		case record.Service == "_xmpp-client" || strings.HasPrefix(listed, "_xmpp-client."):
			state.xmpp = append(state.xmpp, record.Data)
		}
	}
	return state
}

// p15: what is one SRV group? P14 showed that an SRV record written at a
// name is listed under that name with its service and protocol apart, and
// read back under "_service._protocol.name". P15 asks whether a write at
// the plain name replaces the records of other services there, and whether
// the long name can be written and deleted on its own.
//
// It is the one probe that writes to a name that does not start with the
// probe prefix: the long form of its own name, which the guard allows for
// SRV only.
func p15(r *runner) {
	name := r.name("p15")
	long := "_sip._tcp." + name
	listPath := "/v1/domains/" + r.domain + "/records/SRV"
	srv := func(service, data string, port int) rec {
		return rec{"data": data, "priority": 10, "weight": 5, "port": port,
			"service": service, "protocol": "_tcp", "ttl": 3600}
	}
	sip, xmpp := srv("_sip", "sip.example.net", 5060), srv("_xmpp-client", "xmpp.example.net", 5222)
	list := func(label string) srvState {
		return srvStateOf(r.do("P15 list all SRV records "+label, "GET", listPath, nil), name)
	}
	line := func(letter, format string, args ...interface{}) {
		fmt.Fprintf(r.out, "\nP15 %s: %s\n", letter, fmt.Sprintf(format, args...))
	}
	stopped := func() bool {
		if r.halted() {
			fmt.Fprint(r.out, "\nP15 interrupted; the results above are all there are.\n")
		}
		return r.halted()
	}

	// 1 and 2: one service, then the other, at the plain name.
	var put1, put2 response
	var after1, after2 srvState
	r.inOrder(
		func() { put1 = r.put("P15 write the sip record at the plain name", "SRV", name, []rec{sip}) },
		func() { after1 = list("after the sip record") },
		func() { put2 = r.put("P15 write only the xmpp record at the plain name", "SRV", name, []rec{xmpp}) },
		func() { r.do("P15 read the sip record by its long name", "GET", r.group("SRV", long), nil) },
		func() {
			r.do("P15 read the xmpp record by its long name", "GET", r.group("SRV", "_xmpp-client._tcp."+name), nil)
		},
		func() { after2 = list("after the xmpp record") },
	)
	if stopped() {
		return
	}
	switch {
	case !put1.ok() || !put2.ok():
		line("A", "inconclusive, because the writes returned status %d and %d.", put1.Status, put2.Status)
	case !after1.ok() || !after2.ok():
		line("A", "inconclusive, because the lists returned status %d and %d.", after1.status, after2.status)
	case len(after1.sip) == 0 || len(after2.xmpp) == 0:
		line("A", "inconclusive, because the lists do not show the records that were written (after the first write: %s; after the second: %s).", after1, after2)
	case len(after2.sip) == 0:
		line("A", "a write at the plain name for one service REPLACED the other service's record.")
	default:
		line("A", "a write at the plain name for one service KEPT the other service's record.")
	}

	// 3: both services in one body.
	var put3 response
	var after3 srvState
	r.inOrder(
		func() {
			put3 = r.put("P15 write both records at the plain name in one body", "SRV", name, []rec{sip, xmpp})
		},
		func() { after3 = list("after both records in one body") },
	)
	if stopped() {
		return
	}
	switch {
	case !put3.ok():
		line("B", "inconclusive, because the write returned status %d.", put3.Status)
	case !after3.ok():
		line("B", "inconclusive, because the list returned status %d.", after3.status)
	case len(after3.sip) > 0 && len(after3.xmpp) > 0:
		line("B", "after a write of both services in one body, both are present.")
	default:
		line("B", "after a write of both services in one body, NOT both are present (%s).", after3)
	}

	// 4: a write at the long name. The long name is remembered for cleanup
	// as well, in case a record ends up reachable only there.
	sip2 := srv("_sip", "sip2.example.net", 5060)
	bare := rec{}
	for k, v := range sip2 {
		if k != "service" && k != "protocol" {
			bare[k] = v
		}
	}
	var withFields, withoutFields response
	var after4 srvState
	r.inOrder(
		func() {
			withFields = r.put("P15 write the sip record at its long name", "SRV", long, []rec{sip2})
		},
		func() { after4 = list("after the write at the long name") },
	)
	triedBare := !r.halted() && !withFields.ok()
	if triedBare {
		r.inOrder(
			func() {
				withoutFields = r.put("P15 write the sip record at its long name, without service and protocol in the body",
					"SRV", long, []rec{bare})
			},
			func() { after4 = list("after the second write at the long name") },
		)
	}
	if stopped() {
		return
	}
	how := fmt.Sprintf("status %d, with service and protocol in the body", withFields.Status)
	if triedBare {
		how = fmt.Sprintf("status %d, without service and protocol in the body, after status %d with them",
			withoutFields.Status, withFields.Status)
	}
	switch {
	case !after4.ok():
		line("C", "inconclusive, because the list after the long-form write returned status %d (the write: %s).", after4.status, how)
	case !withFields.ok() && !withoutFields.ok():
		line("C", "a long-form write was REFUSED (status %d with service and protocol in the body, status %d without).",
			withFields.Status, withoutFields.Status)
	default:
		xmppNow := "survived"
		if len(after4.xmpp) == 0 {
			xmppNow = "was removed"
		}
		sipNow := strings.Join(after4.sip, ", ")
		if sipNow == "" {
			sipNow = "nothing (it is absent)"
		}
		line("C", "a long-form write was accepted (%s); the xmpp record %s; the sip record now holds: %s.", how, xmppNow, sipNow)
	}

	// 5: a delete at the long name.
	var del5 response
	var after5 srvState
	r.inOrder(
		func() { del5 = r.do("P15 delete the sip record at its long name", "DELETE", r.group("SRV", long), nil) },
		func() { after5 = list("after the delete at the long name") },
	)
	if stopped() {
		return
	}
	switch {
	case !after4.ok() || !after5.ok():
		line("D", "inconclusive, because the lists before and after returned status %d and %d (the delete: status %d).",
			after4.status, after5.status, del5.Status)
	case !del5.ok():
		line("D", "a long-form delete was REFUSED (status %d).", del5.Status)
	case len(after4.sip) > 0 && len(after5.sip) == 0 && len(after5.xmpp) == len(after4.xmpp) && len(after5.xmpp) > 0:
		line("D", "a long-form delete was accepted (status %d) and removed only the sip record.", del5.Status)
	default:
		line("D", "a long-form delete was accepted (status %d) but did not remove only the sip record (before: %s; after: %s).",
			del5.Status, after4, after5)
	}

	// 6: a delete at the plain name.
	var del6 response
	var after6 srvState
	r.inOrder(
		func() { del6 = r.do("P15 delete at the plain name", "DELETE", r.group("SRV", name), nil) },
		func() { after6 = list("after the delete at the plain name") },
	)
	if stopped() {
		return
	}
	switch {
	case !after5.ok() || !after6.ok():
		line("E", "inconclusive, because the lists before and after returned status %d and %d (the delete: status %d).",
			after5.status, after6.status, del6.Status)
	case after5.total == 0:
		line("E", "nothing was left at the name to delete (status %d).", del6.Status)
	case after6.total == 0:
		line("E", "a delete at the plain name (status %d) removed everything left at the name.", del6.Status)
	default:
		line("E", "a delete at the plain name (status %d) left %d record(s) at the name (%s).", del6.Status, after6.total, after6)
	}
}

// p16: what do under-scoped tokens get? One token may only read domains;
// the other may also update DNS but not the domain itself.
//
// The last step sends a nameserver PATCH with the second token. It is built
// to change nothing: its body is the list of nameservers read from the
// domain one request earlier, exactly as returned. p16 never sends any other
// list. Afterwards it reads the domain again and fails if the nameservers
// are not what they were.
func p16(r *runner, args probeArgs) error {
	root := "/v1/domains/" + r.domain
	ro, dns := args.readOnlyToken, args.dnsOnlyToken
	roGroup, dnsGroup := r.name("p16-ro"), r.name("p16-dns")

	var s1, s2, s3, s4, s5, s6, s7, s8 response
	r.inOrder(
		func() {
			s1 = r.doAs("P16 read-only token: read a missing group", ro, "GET", r.group("TXT", r.name("p16-missing")), nil)
		},
		func() { s2 = r.doAs("P16 read-only token: read all A records", ro, "GET", root+"/records/A", nil) },
		func() { s3 = r.doAs("P16 read-only token: read the domain", ro, "GET", root, nil) },
		func() { s4 = r.putAs("P16 read-only token: write a TXT group", ro, "TXT", roGroup, one("x")) },
		func() {
			s5 = r.doAs("P16 read-only token: delete that group", ro, "DELETE", r.group("TXT", roGroup), nil)
		},
		func() { s6 = r.doAs("P16 DNS-only token: read the domain", dns, "GET", root, nil) },
		func() { s7 = r.putAs("P16 DNS-only token: write a TXT group", dns, "TXT", dnsGroup, one("y")) },
		func() { s8 = r.doAs("P16 DNS-only token: read that group", dns, "GET", r.group("TXT", dnsGroup), nil) },
	)
	if r.halted() {
		fmt.Fprint(r.out, "\nP16 interrupted; no nameserver PATCH was sent.\n")
		return nil
	}

	// Steps 9 to 11. The PATCH body is the list from step 9 and nothing else.
	detail := r.do("P16 main token: read the current nameservers", "GET", root, nil)
	current := nameServersOf(detail.Body)
	var patch response
	var patchLine string
	var err error
	switch {
	case r.halted():
		fmt.Fprint(r.out, "\nP16 interrupted; no nameserver PATCH was sent.\n")
		return nil
	case detail.Status != 200 || len(current) < 2:
		patchLine = fmt.Sprintf("P16 DNS-only token: step 10, nameserver PATCH: NOT SENT, because the read of the current nameservers "+
			"returned status %d and %d nameserver(s).", detail.Status, len(current))
	default:
		patch = r.doAs("P16 DNS-only token: PATCH the nameservers with the list the domain already has (a no-op change)",
			dns, "PATCH", root, rec{"nameServers": current})
		patchLine = fmt.Sprintf("P16 DNS-only token: step 10, nameserver PATCH with the current nameservers: status %d.", patch.Status)
		if !patch.Sent {
			patchLine = "P16 DNS-only token: step 10, nameserver PATCH: NOT SENT; see the entry above."
		}
	}
	interrupted := r.halted()
	var checkLine string
	if patch.Sent {
		checkLine, err = r.p16Check(current)
	}
	if interrupted {
		fmt.Fprintf(r.out, "\nP16 interrupted.\n\n%s\n", checkLine)
		return err
	}

	for _, step := range []struct {
		token, what string
		n           int
		resp        response
	}{
		{"read-only", "read of a missing group", 1, s1},
		{"read-only", "read of all A records", 2, s2},
		{"read-only", "read of the domain", 3, s3},
		{"read-only", "write of a TXT group", 4, s4},
		{"read-only", "delete of that group", 5, s5},
		{"DNS-only", "read of the domain", 6, s6},
		{"DNS-only", "write of a TXT group", 7, s7},
		{"DNS-only", "read of that group", 8, s8},
	} {
		fmt.Fprintf(r.out, "\nP16 %s token: step %d, %s: status %d.\n", step.token, step.n, step.what, step.resp.Status)
	}
	fmt.Fprintf(r.out, "\n%s\n", patchLine)
	if checkLine != "" {
		fmt.Fprintf(r.out, "\n%s\n", checkLine)
	}
	return err
}

// p16Check reads the domain after p16's PATCH and compares its nameservers
// with want. It runs even after an interrupt. It returns the line for the
// transcript, and an error unless the nameservers are shown to be unchanged.
func (r *runner) p16Check(want []string) (string, error) {
	wasFinishing := r.finishing
	r.finishing = true
	defer func() { r.finishing = wasFinishing }()

	byHand := strings.Join(want, ", ")
	after := r.do("P16 main token: read the nameservers after the PATCH", "GET", "/v1/domains/"+r.domain, nil)
	got := nameServersOf(after.Body)
	switch {
	case after.Status != 200:
		err := fmt.Errorf("after P16's nameserver PATCH the domain could not be read (status %d); "+
			"check in the GoDaddy console that the nameservers are still: %s", after.Status, byHand)
		return "**P16 nameservers after the PATCH: NOT CHECKED. " + err.Error() + "**", err
	case !sameHosts(got, want):
		err := fmt.Errorf("P16's nameserver PATCH changed the nameservers to %v; set these nameservers by hand in the GoDaddy console: %s",
			got, byHand)
		return "**P16 nameservers after the PATCH: CHANGED. " + err.Error() + "**", err
	}
	return fmt.Sprintf("P16 nameservers after the PATCH: unchanged (%v).", got), nil
}

// typeList is what one read of all records of a type shows about the two
// probe groups of p17.
type typeList struct {
	status int
	total  int      // records of the type
	a      int      // records at name a
	b      []string // data of the records at name b
}

func (l typeList) ok() bool { return l.status == 200 }

func typeListOf(resp response, a, b string) typeList {
	list := typeList{status: resp.Status}
	var records []struct {
		Name string `json:"name"`
		Data string `json:"data"`
	}
	if resp.Status != 200 {
		return list
	}
	if err := json.Unmarshal(resp.Body, &records); err != nil {
		list.status = 0
		return list
	}
	at := func(listed, name string) bool { return listed == name || strings.HasSuffix(listed, "."+name) }
	list.total = len(records)
	for _, record := range records {
		listed := strings.ToLower(record.Name)
		switch {
		case at(listed, a):
			list.a++
		case at(listed, b):
			list.b = append(list.b, record.Data)
		}
	}
	return list
}

// p17: do P9 and P13 hold for types other than AAAA? P9 showed that a
// type-wide PUT of an empty array deletes nothing, P13 that the same PUT
// with one record deletes every other record of the type. P17 repeats both
// on TXT, MX and SRV.
//
// If P13 holds, the one-record replace deletes every other record of the
// type in the zone. So a type is skipped when the zone, as read before the
// probe, has a record of that type of its own.
func p17(r *runner, before []zoneRecord) error {
	types := []struct {
		name   string
		record func(data string) rec
		dataA  string
		dataB  string
		dataB2 string
	}{
		{"TXT", func(data string) rec { return rec{"data": data, "ttl": 3600} }, "a1", "b1", "b2"},
		{"MX", func(data string) rec { return rec{"data": data, "priority": 10, "ttl": 3600} },
			"mx-a.example.net", "mx-b.example.net", "mx-b2.example.net"},
		{"SRV", func(data string) rec {
			return rec{"data": data, "priority": 10, "weight": 5, "port": 5060,
				"service": "_sip", "protocol": "_tcp", "ttl": 3600}
		}, "a.example.net", "b.example.net", "b2.example.net"},
	}

	var nothing, deletedOther, unclear []string
	skipped := 0
	for _, t := range types {
		t := t
		if r.halted() {
			break
		}
		if err := r.noRealRecords("P17", t.name, before); err != nil {
			fmt.Fprintf(r.out, "\nP17 %s: skipped, because the %s\n", t.name, err)
			unclear = append(unclear, t.name)
			skipped++
			continue
		}
		fmt.Fprintf(r.out, "\n### P17 %s\n", t.name)

		a, b := r.name("p17-"+strings.ToLower(t.name)+"-a"), r.name("p17-"+strings.ToLower(t.name)+"-b")
		all := "/v1/domains/" + r.domain + "/records/" + t.name
		list := func(label string) typeList {
			return typeListOf(r.do("P17 all "+t.name+" "+label, "GET", all, nil), a, b)
		}
		withName := t.record(t.dataB2)
		withName["name"] = b
		var empty, one response
		var first, afterEmpty, afterOne typeList
		r.inOrder(
			func() { r.put("P17 create "+t.name+" a", t.name, a, []rec{t.record(t.dataA)}) },
			func() { r.put("P17 create "+t.name+" b", t.name, b, []rec{t.record(t.dataB)}) },
			func() { first = list("before") },
			func() { empty = r.do("P17 "+t.name+" empty type-wide replace", "PUT", all, []rec{}) },
			func() { afterEmpty = list("after the empty replace") },
			func() {
				one = r.do("P17 "+t.name+" type-wide replace with one record, for b", "PUT", all, []rec{withName})
			},
			func() { afterOne = list("after the one-record replace") },
		)
		if r.halted() {
			break
		}

		// The empty replace.
		count := func(l typeList) string {
			if !l.ok() {
				return "unknown"
			}
			return fmt.Sprint(l.total)
		}
		var verdict string
		emptyClear := false
		switch {
		case !first.ok() || !afterEmpty.ok():
			verdict = fmt.Sprintf("inconclusive because the lists before and after returned status %d and %d", first.status, afterEmpty.status)
		case !empty.ok():
			verdict = "inconclusive because the replace was not accepted"
		case first.a == 0 || len(first.b) == 0:
			verdict = "inconclusive because the list before does not show both probe records"
		case afterEmpty.total == first.total:
			verdict, emptyClear = "DELETED NOTHING", true
			nothing = append(nothing, t.name)
		case afterEmpty.total < first.total:
			verdict, emptyClear = fmt.Sprintf("DELETED %d record(s)", first.total-afterEmpty.total), true
		default:
			verdict = "inconclusive because there are more records after than before"
		}
		fmt.Fprintf(r.out, "\nP17 %s empty replace: status %d; records before %s, after %s; %s\n",
			t.name, empty.Status, count(first), count(afterEmpty), verdict)

		// The one-record replace.
		oneClear := false
		switch {
		case !one.ok():
			verdict = "inconclusive because the replace was not accepted"
		case !afterEmpty.ok() || !afterOne.ok():
			verdict = fmt.Sprintf("inconclusive because the lists before and after returned status %d and %d", afterEmpty.status, afterOne.status)
		case afterEmpty.a == 0:
			verdict = "inconclusive because a was already gone before this replace"
		default:
			oneClear = true
			holds := strings.Join(afterOne.b, ", ")
			if holds == "" {
				holds = "nothing"
			}
			if afterOne.a == 0 {
				verdict = "a was DELETED; b holds " + holds
				deletedOther = append(deletedOther, t.name)
			} else {
				verdict = "a was KEPT; b holds " + holds
			}
		}
		fmt.Fprintf(r.out, "\nP17 %s one-record replace: status %d; %s\n", t.name, one.Status, verdict)
		if !emptyClear || !oneClear {
			unclear = append(unclear, t.name)
		}
	}
	if skipped == len(types) {
		return fmt.Errorf("the zone has records of its own of every type P17 tests (TXT, MX, SRV); P17 would delete them, so it was not run")
	}
	if r.halted() {
		fmt.Fprint(r.out, "\nP17 interrupted; the lines above are all there are.\n")
		return nil
	}
	names := func(types []string) string {
		if len(types) == 0 {
			return "none"
		}
		return strings.Join(types, ", ")
	}
	fmt.Fprintf(r.out, "\nP17 summary: empty replace deleted nothing for: %s; "+
		"one-record replace deleted the other record for: %s; inconclusive or skipped: %s\n",
		names(nothing), names(deletedOther), names(unclear))
	return nil
}

// p18List is what one read of all records of a type shows p18.
type p18List struct {
	status int
	total  int          // records of the type
	a      int          // records at probe name a
	b      []string     // data of the records at probe name b
	real   []zoneRecord // records outside the probe prefix
}

func (l p18List) ok() bool { return l.status == 200 }

func (r *runner) p18ListOf(resp response, recordType, a, b string) p18List {
	list := p18List{status: resp.Status}
	if resp.Status != 200 {
		return list
	}
	records, err := decodeRecords(resp.Body, recordType)
	if err != nil {
		list.status = 0
		return list
	}
	list.total = len(records)
	for _, z := range records {
		switch {
		case !strings.HasPrefix(z.Name, r.prefix):
			list.real = append(list.real, z)
		case z.Name == a:
			list.a++
		case z.Name == b:
			data, _ := z.fields["data"].(string)
			list.b = append(list.b, data)
		}
	}
	return list
}

// p18Summary collects, per type, what p18 found and whether the real
// records are back.
type p18Summary struct {
	nothing, deletedOthers, restored, notRestored []string
}

// p18: do P9 and P13 hold for A and CNAME? These are the two types of which
// the probe zone has records of its own, so this is the one probe that
// removes real records: if P13 holds, the one-record replace deletes them.
// It is built around putting them back.
//
//   - The real records of a type come from the snapshot taken before the
//     probe, with every field. They are written to the transcript before
//     the first write for the type.
//   - Nothing is replaced unless a fresh list of the type shows exactly
//     those real records.
//   - The restore is deferred from just before the first type-wide PUT, so
//     it runs however the type's part ends, including after an interrupt,
//     in finishing mode and on this goroutine. See restoreRecords.
//   - One type is finished, restore included, before the next starts, and
//     the next does not start if a restore failed.
func p18(r *runner, before []zoneRecord) error {
	types := []p18Type{
		{"A", "192.0.2.1", "192.0.2.2", "192.0.2.3"},
		{"CNAME", "a.example.net", "b.example.net", "b2.example.net"},
	}
	var sum p18Summary
	var err error
	for _, t := range types {
		if r.halted() {
			break
		}
		if err = r.p18One(t, before, &sum); err != nil {
			break
		}
	}
	if err == nil && r.halted() {
		fmt.Fprint(r.out, "\nP18 interrupted; the lines above are all there are.\n")
		return nil
	}
	names := func(types []string) string {
		if len(types) == 0 {
			return "none"
		}
		return strings.Join(types, ", ")
	}
	fmt.Fprintf(r.out, "\nP18 summary: empty replace deleted nothing for: %s; one-record replace deleted the other records for: %s; "+
		"restored: %s; NOT restored: %s\n", names(sum.nothing), names(sum.deletedOthers), names(sum.restored), names(sum.notRestored))
	return err
}

type p18Type struct {
	name                 string
	dataA, dataB, dataB2 string
}

// p18One runs p18 for one record type, restore included.
func (r *runner) p18One(t p18Type, before []zoneRecord, sum *p18Summary) (err error) {
	var originals []zoneRecord
	var canon []string
	for _, z := range before {
		if z.Type == t.name && !strings.HasPrefix(z.Name, r.prefix) {
			originals = append(originals, z)
			canon = append(canon, z.canon)
		}
	}
	originalsJSON := "[" + strings.Join(canon, ",") + "]"
	fmt.Fprintf(r.out, "\n### P18 %s\n\nReal %s records, to recreate by hand if the restore fails:\n\n```json\n%s\n```\n",
		t.name, t.name, originalsJSON)

	a, b := r.name("p18-"+strings.ToLower(t.name)+"-a"), r.name("p18-"+strings.ToLower(t.name)+"-b")
	all := "/v1/domains/" + r.domain + "/records/" + t.name
	list := func(label string) p18List {
		return r.p18ListOf(r.do("P18 all "+t.name+" "+label, "GET", all, nil), t.name, a, b)
	}

	// The restore is armed just before the first type-wide PUT is sent and
	// from then on runs whatever happens.
	armed := false
	defer func() {
		switch {
		case !armed:
		case len(originals) == 0:
			fmt.Fprintf(r.out, "\nP18 %s: the zone had no real %s records; nothing to restore.\n", t.name, t.name)
		default:
			if restoreErr := r.restoreRecords(t.name, originals, originalsJSON); restoreErr != nil {
				err = restoreErr
				sum.notRestored = append(sum.notRestored, t.name)
			} else {
				sum.restored = append(sum.restored, t.name)
			}
		}
	}()

	var first p18List
	r.inOrder(
		func() { r.put("P18 create "+t.name+" a", t.name, a, one(t.dataA)) },
		func() { r.put("P18 create "+t.name+" b", t.name, b, one(t.dataB)) },
		func() { first = list("before") },
	)
	if r.halted() {
		return nil
	}
	// Last check before real records are at stake: the zone must still hold
	// exactly the real records that would be put back.
	if !first.ok() {
		return fmt.Errorf("P18 %s: the list of all %s records before the replace returned status %d; nothing was replaced",
			t.name, t.name, first.status)
	}
	if len(first.real) != len(originals) || missingFrom(first.real, originals) != 0 {
		return fmt.Errorf("P18 %s: the real %s records changed since the snapshot (then %d, now %d); nothing was replaced",
			t.name, t.name, len(originals), len(first.real))
	}

	var empty, single response
	var afterEmpty, afterOne p18List
	r.inOrder(
		func() {
			armed = true
			empty = r.do("P18 "+t.name+" empty type-wide replace", "PUT", all, []rec{})
		},
		func() { afterEmpty = list("after the empty replace") },
		func() {
			single = r.do("P18 "+t.name+" type-wide replace with one record, for b", "PUT", all,
				[]rec{{"name": b, "data": t.dataB2, "ttl": 3600}})
		},
		func() { afterOne = list("after the one-record replace") },
	)
	if r.halted() {
		return nil
	}

	count := func(l p18List) string {
		if !l.ok() {
			return "unknown"
		}
		return fmt.Sprint(l.total)
	}
	var verdict string
	switch {
	case !afterEmpty.ok():
		verdict = fmt.Sprintf("inconclusive because the list after returned status %d", afterEmpty.status)
	case !empty.ok():
		verdict = "inconclusive because the replace was not accepted"
	case first.a == 0 || len(first.b) == 0:
		verdict = "inconclusive because the list before does not show both probe records"
	case afterEmpty.total == first.total:
		verdict = "DELETED NOTHING"
		sum.nothing = append(sum.nothing, t.name)
	case afterEmpty.total < first.total:
		lostReal := len(first.real) - len(afterEmpty.real)
		verdict = fmt.Sprintf("DELETED %d record(s) (real: %d, probe: %d)",
			first.total-afterEmpty.total, lostReal, first.total-afterEmpty.total-lostReal)
	default:
		verdict = "inconclusive because there are more records after than before"
	}
	fmt.Fprintf(r.out, "\nP18 %s empty replace: status %d; records before %s, after %s; %s\n",
		t.name, empty.Status, count(first), count(afterEmpty), verdict)

	switch {
	case !single.ok():
		verdict = "inconclusive because the replace was not accepted"
	case !afterEmpty.ok() || !afterOne.ok():
		verdict = fmt.Sprintf("inconclusive because the lists before and after returned status %d and %d", afterEmpty.status, afterOne.status)
	case afterEmpty.a == 0:
		verdict = "inconclusive because probe a was already gone before this replace"
	default:
		probeA := "probe a was KEPT"
		if afterOne.a == 0 {
			probeA = "probe a was DELETED"
		}
		n := len(afterEmpty.real)
		lost := missingFrom(afterOne.real, afterEmpty.real)
		realPart := fmt.Sprintf("real records DELETED: %d of %d", lost, n)
		switch {
		case n == 0:
			realPart = "no real records"
		case lost == 0:
			realPart = "real records KEPT"
		}
		holds := strings.Join(afterOne.b, ", ")
		if holds == "" {
			holds = "nothing"
		}
		verdict = probeA + "; " + realPart + "; b holds " + holds
		if afterOne.a == 0 && lost == n {
			sum.deletedOthers = append(sum.deletedOthers, t.name)
		}
	}
	fmt.Fprintf(r.out, "\nP18 %s one-record replace: status %d; %s\n", t.name, single.Status, verdict)
	return nil
}

// restoreRecords puts the real records of one type back after p18 replaced
// the type. It reads the type first and sends nothing if every original is
// there. Otherwise it sends one type-wide PUT with the originals, without
// their "type" field. See restore for the rest.
func (r *runner) restoreRecords(recordType string, originals []zoneRecord, originalsJSON string) error {
	body := make([]rec, len(originals))
	for i, z := range originals {
		body[i] = z.withoutType()
	}
	return r.restore(restoreJob{
		heading:   fmt.Sprintf("Restore the real %s records", recordType),
		reads:     fmt.Sprintf("P18 all %s", recordType),
		writes:    fmt.Sprintf("P18 restore the real %s records", recordType),
		path:      "/v1/domains/" + r.domain + "/records/" + recordType,
		body:      body,
		originals: originals,
		decode:    func(b []byte) ([]zoneRecord, error) { return decodeRecords(b, recordType) },
		noWrite:   fmt.Sprintf("Every real %s record is there; no write is needed.", recordType),
		done:      fmt.Sprintf("P18 %s restored: %d real record(s) are back.", recordType, len(originals)),
		failure: fmt.Sprintf("P18: the restore of the real %s records FAILED (%%s); recreate these %s records by hand: %s",
			recordType, recordType, strings.ReplaceAll(originalsJSON, "%", "%%")),
	})
}

// restoreJob describes real records to put back with one PUT.
type restoreJob struct {
	heading   string       // of the transcript section
	reads     string       // label of the reads
	writes    string       // label of the PUT
	path      string       // read with GET, written with PUT
	body      []rec        // what to PUT
	originals []zoneRecord // what a read of path must show afterwards
	decode    func(body []byte) ([]zoneRecord, error)
	exact     bool   // a read must show the originals and nothing else
	noWrite   string // transcript line when the originals are already there
	done      string // transcript line on success
	failure   string // error text, with one %s for the reason
}

// restore puts real records back and returns an error unless it has seen
// every one of them in the zone again, field for field. It runs in
// finishing mode: an interrupt does not stop it.
//
// It reads the path first and sends nothing if the originals are there.
// Otherwise it sends the PUT, retrying a refusal up to 20 times (a 401 or
// 403 ends the retries at once), and reads again. If the originals are not
// all there it sends the PUT once more and then gives up. The error text
// repeats the originals as JSON, to recreate by hand.
func (r *runner) restore(job restoreJob) error {
	wasFinishing := r.finishing
	r.finishing = true
	defer func() { r.finishing = wasFinishing }()

	fmt.Fprintf(r.out, "\n### %s\n", job.heading)
	fail := func(why string) error {
		err := fmt.Errorf(job.failure, why)
		fmt.Fprintf(r.out, "\n**RESTORE FAILED: %s**\n", err)
		return err
	}
	done := func() error {
		fmt.Fprintf(r.out, "\n%s\n", job.done)
		return nil
	}
	// present reads the path, trying a failed read again a few times, and
	// reports whether the originals are there.
	present := func(label string) (bool, string) {
		var resp response
		for try := 0; try <= 5; try++ {
			if try > 0 {
				time.Sleep(r.restoreEvery)
			}
			resp = r.do(fmt.Sprintf("%s %s, read %d", job.reads, label, try+1), "GET", job.path, nil)
			if resp.Status == 200 {
				break
			}
		}
		if resp.Status != 200 {
			return false, fmt.Sprintf("the read %s returned status %d", label, resp.Status)
		}
		records, err := job.decode(resp.Body)
		if err != nil {
			return false, fmt.Sprintf("the read %s could not be parsed", label)
		}
		if missing := missingFrom(records, job.originals); missing > 0 {
			return false, fmt.Sprintf("%d of %d record(s) are missing or differ in the read %s", missing, len(job.originals), label)
		}
		if job.exact && len(records) != len(job.originals) {
			return false, fmt.Sprintf("the read %s shows %d record(s) where there were %d", label, len(records), len(job.originals))
		}
		return true, ""
	}

	if ok, _ := present("before the restore"); ok {
		fmt.Fprintf(r.out, "\n%s\n", job.noWrite)
		return done()
	}

	const attempts = 20
	why := ""
	for round := 1; round <= 2; round++ {
		accepted := false
	send:
		for attempt := 1; attempt <= attempts; attempt++ {
			resp := r.do(fmt.Sprintf("%s, round %d, attempt %d", job.writes, round, attempt), "PUT", job.path, job.body)
			why = fmt.Sprintf("the restoring PUT was not accepted, last status %d", resp.Status)
			switch {
			case resp.ok():
				accepted = true
				break send
			case !resp.Sent:
				break send // the guard refused it; asking again will not change that
			case resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden:
				fmt.Fprintf(r.out, "\nRestore not authorised (status %d). Trying again cannot help.\n", resp.Status)
				break send
			case attempt < attempts:
				wait := restoreWait(resp.Header, r.restoreEvery)
				fmt.Fprintf(r.out, "\nRestore not accepted (status %d). Trying again in %s.\n", resp.Status, wait)
				time.Sleep(wait)
			}
		}
		if !accepted {
			return fail(why)
		}
		ok, problem := present("after the restore")
		if ok {
			return done()
		}
		why = "the restoring PUT was accepted, but " + problem
		fmt.Fprintf(r.out, "\nRestore accepted, but %s.\n", problem)
	}
	return fail(why)
}

// groupRecords decodes the response to a read of one group. A per-name
// read may leave out the name and the type; they are filled in, so that the
// records compare with those of a snapshot.
func groupRecords(body []byte, recordType, name string) ([]zoneRecord, error) {
	var raw []map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]zoneRecord, 0, len(raw))
	for _, fields := range raw {
		if fields == nil {
			return nil, fmt.Errorf("a record is null")
		}
		if n, _ := fields["name"].(string); n == "" {
			fields["name"] = name
		}
		out = append(out, newZoneRecord(fields, recordType))
	}
	return out, nil
}

// p19Result is what p19 found, for its summary line.
type p19Result struct {
	change, del, cameBack, restored string
}

// p19: can the _domainconnect CNAME be changed or deleted through the
// per-name endpoint? P18 showed that a type-wide CNAME replace that leaves
// this record out does not remove it. Either GoDaddy protects it or GoDaddy
// puts it back; p19 changes it, deletes it and then watches for a minute.
//
// It is the one probe that writes to a real record at its own path, and the
// guard allows it that one path only. Like p18 it is built around putting
// the record back:
//
//   - The record comes from the snapshot taken before the probe, with every
//     field, and is written to the transcript before the first write.
//   - Nothing is written unless a fresh read of the record matches it.
//   - The restore is deferred from just before the first write, so it runs
//     however the probe ends, including after an interrupt, in finishing
//     mode and on this goroutine. See restore.
func p19(r *runner, before []zoneRecord) error {
	const recordType, name = "CNAME", "_domainconnect"
	var originals []zoneRecord
	var canon []string
	for _, z := range before {
		if z.Type == recordType && z.Name == name {
			originals = append(originals, z)
			canon = append(canon, z.canon)
		}
	}
	if len(originals) == 0 {
		return fmt.Errorf("the zone has no _domainconnect CNAME; nothing to probe")
	}
	originalsJSON := "[" + strings.Join(canon, ",") + "]"
	fmt.Fprintf(r.out, "\nReal _domainconnect record, to recreate by hand if the restore fails:\n\n```json\n%s\n```\n", originalsJSON)

	result := p19Result{change: "n/a", del: "n/a", cameBack: "n/a", restored: "n/a"}
	err := r.p19Steps(originals, originalsJSON, &result)
	if err == nil && r.halted() {
		fmt.Fprint(r.out, "\nP19 interrupted; the lines above are all there are.\n")
		return nil
	}
	if err == nil && result.restored == "n/a" {
		return nil // stopped before the first write; p19Steps said why
	}
	fmt.Fprintf(r.out, "\nP19 summary: per-name change %s; per-name delete %s; GoDaddy %s; restored: %s\n",
		result.change, result.del, result.cameBack, result.restored)
	return err
}

func (r *runner) p19Steps(originals []zoneRecord, originalsJSON string, result *p19Result) (err error) {
	const recordType, name, changed = "CNAME", "_domainconnect", "probe-changed.example.net"
	group := r.group(recordType, name)
	listPath := "/v1/domains/" + r.domain + "/records/" + recordType
	originalData, _ := originals[0].fields["data"].(string)

	// held reads the group and returns the data of its records.
	type reading struct {
		status  int
		data    []string
		records []zoneRecord
	}
	read := func(resp response) reading {
		got := reading{status: resp.Status}
		if resp.Status != 200 {
			return got
		}
		records, decodeErr := groupRecords(resp.Body, recordType, name)
		if decodeErr != nil {
			got.status = 0
			return got
		}
		got.records = records
		for _, z := range records {
			data, _ := z.fields["data"].(string)
			got.data = append(got.data, data)
		}
		return got
	}

	// Last check before a real record is at stake: it must still be what
	// would be put back.
	fresh := read(r.do("P19 read the record before anything is changed", "GET", group, nil))
	if r.halted() {
		return nil
	}
	if fresh.status != 200 {
		return fmt.Errorf("P19: the read of the _domainconnect CNAME before the change returned status %d; nothing was written", fresh.status)
	}
	if len(fresh.records) != len(originals) || missingFrom(fresh.records, originals) != 0 {
		return fmt.Errorf("P19: the _domainconnect CNAME changed since the snapshot (it now holds %v); nothing was written", fresh.data)
	}

	// The restore is armed just before the first write is sent and from
	// then on runs whatever happens.
	armed := false
	defer func() {
		if !armed {
			return
		}
		body := make([]rec, len(originals))
		for i, z := range originals {
			body[i] = z.withoutType()
			delete(body[i], "name") // a per-name body carries neither
		}
		restoreErr := r.restore(restoreJob{
			heading:   "Restore the _domainconnect CNAME",
			reads:     "P19 read the record",
			writes:    "P19 restore the record",
			path:      group,
			body:      body,
			originals: originals,
			decode:    func(b []byte) ([]zoneRecord, error) { return groupRecords(b, recordType, name) },
			exact:     true,
			noWrite:   "The record is as it was; no write is needed.",
			done:      "P19 restored: _domainconnect is back as it was.",
			failure: "P19: the restore of the _domainconnect CNAME record FAILED (%s); recreate this record by hand: " +
				strings.ReplaceAll(originalsJSON, "%", "%%"),
		})
		result.restored = "yes"
		if restoreErr != nil {
			err = restoreErr
			result.restored = "NO"
		}
	}()

	// 1 and 2: change it.
	var put response
	var afterPut reading
	r.inOrder(
		func() {
			armed = true
			put = r.do("P19 change the record at its own name", "PUT", group, []rec{{"data": changed, "ttl": 3600}})
		},
		func() { afterPut = read(r.do("P19 read the record after the change", "GET", group, nil)) },
		func() { r.do("P19 list all CNAME records after the change", "GET", listPath, nil) },
	)
	if r.halted() {
		return nil
	}
	holds := func(got reading) string { return strings.Join(got.data, ", ") }
	verdict := "accepted, effect unknown"
	switch {
	case afterPut.status != 200:
		fmt.Fprintf(r.out, "\nP19 change: status %d; inconclusive because the read afterwards returned status %d\n", put.Status, afterPut.status)
	case len(afterPut.data) == 0:
		fmt.Fprintf(r.out, "\nP19 change: status %d; _domainconnect now holds nothing (GONE)\n", put.Status)
	case len(afterPut.data) == 1 && afterPut.data[0] == changed:
		fmt.Fprintf(r.out, "\nP19 change: status %d; _domainconnect now holds %s (CHANGED)\n", put.Status, holds(afterPut))
		verdict = "accepted and applied"
	case len(afterPut.data) == 1 && afterPut.data[0] == originalData:
		fmt.Fprintf(r.out, "\nP19 change: status %d; _domainconnect now holds %s (UNCHANGED)\n", put.Status, holds(afterPut))
		verdict = "accepted but not applied"
	default:
		fmt.Fprintf(r.out, "\nP19 change: status %d; _domainconnect now holds %s (inconclusive because that is neither the old nor the new value)\n",
			put.Status, holds(afterPut))
	}
	if !put.ok() {
		verdict = fmt.Sprintf("refused (status %d)", put.Status)
	}
	result.change = verdict

	// 3 and 4: delete it.
	var del response
	var afterDelete reading
	var deletedAt time.Time
	r.inOrder(
		func() {
			del = r.do("P19 delete the record at its own name", "DELETE", group, nil)
			deletedAt = time.Now()
		},
		func() {
			afterDelete = read(r.doNow("P19 read the record straight after the delete", "GET", group, nil))
		},
		func() { r.do("P19 list all CNAME records after the delete", "GET", listPath, nil) },
	)
	if r.halted() {
		return nil
	}
	absent := afterDelete.status == 200 && len(afterDelete.data) == 0
	verdict = "accepted, effect unknown"
	switch {
	case afterDelete.status != 200:
		fmt.Fprintf(r.out, "\nP19 delete: status %d; inconclusive because the read afterwards returned status %d\n", del.Status, afterDelete.status)
	case absent:
		fmt.Fprintf(r.out, "\nP19 delete: status %d; straight afterwards the record is ABSENT\n", del.Status)
		verdict = "accepted and applied"
	default:
		fmt.Fprintf(r.out, "\nP19 delete: status %d; straight afterwards the record is PRESENT holding %s\n", del.Status, holds(afterDelete))
		verdict = "accepted but not applied"
	}
	if !del.ok() {
		verdict = fmt.Sprintf("refused (status %d)", del.Status)
	}
	result.del = verdict
	if !absent {
		return nil // there is nothing to watch come back
	}

	// 5: does GoDaddy put it back?
	fmt.Fprint(r.out, "\nWatching for the record to come back:\n\n")
	wasQuiet := r.quiet
	r.quiet = true
	defer func() { r.quiet = wasQuiet }()
	for _, seconds := range []int{10, 20, 30, 60} {
		if wait := time.Until(deletedAt.Add(time.Duration(seconds) * r.watchUnit)); wait > 0 && !r.pause(wait) {
			return nil
		}
		got := read(r.doNow(fmt.Sprintf("P19 read the record about %d s after the delete", seconds), "GET", group, nil))
		if r.halted() {
			return nil
		}
		line := fmt.Sprintf("- %d s: status %d, %d record(s)", seconds, got.status, len(got.data))
		if len(got.data) > 0 {
			line += ": " + holds(got)
		}
		fmt.Fprintln(r.out, line)
		if len(got.data) > 0 {
			fmt.Fprintf(r.out, "\nP19 after 60 s: the record CAME BACK holding %s after about %d s\n", holds(got), seconds)
			result.cameBack = fmt.Sprintf("put it back after about %d s", seconds)
			return nil
		}
	}
	fmt.Fprint(r.out, "\nP19 after 60 s: the record is still ABSENT\n")
	result.cameBack = "did not put it back within 60 s"
	return nil
}

// p6 asks what a nameserver change looks like, and runs P11 while the
// nameservers point away. It changes the nameservers of a live domain, so
// most of it is about putting them back:
//
//   - It reads the current nameservers first and changes nothing if it
//     cannot.
//   - The restore is deferred, so it runs however p6 ends, including after
//     an interrupt: once interrupted, the runner refuses every further
//     request, p6 returns, and the restore runs in finishing mode on this
//     same goroutine. Nothing is ever undone from a signal handler.
//   - The restore is skipped only when the domain shows its original
//     nameservers and no PATCH from this run can have taken effect.
//     Otherwise it sends the originals, retrying a refusal up to 20 times
//     (a 401 or 403 ends the retries at once), and then polls until the
//     domain shows them.
//   - If the restore is not accepted, or is accepted and never becomes
//     visible, p6 returns an error that names the original nameservers. When
//     no restore was acknowledged it looks once more, and the error says
//     whether the domain shows the originals anyway.
func p6(r *runner, target []string) (err error) {
	root := "/v1/domains/" + r.domain

	detail := r.do("P6 current nameservers", "GET", root, nil)
	if !detail.Sent && r.halted() {
		return nil // interrupted before anything was sent; nothing to put back
	}
	original := nameServersOf(detail.Body)
	if len(original) < 2 {
		return fmt.Errorf("could not read the current nameservers (status %d); nothing was changed", detail.Status)
	}

	// changed is set once any nameserver PATCH went out and was not plainly
	// refused. From then on a read that shows the original nameservers is
	// not proof: it may be stale.
	changed := false
	patch := func(label string, nameServers []string) response {
		resp := r.do(label, "PATCH", root, rec{"nameServers": nameServers})
		if resp.Sent && (resp.Status < 400 || resp.Status > 499) {
			changed = true
		}
		return resp
	}
	defer func() {
		if restoreErr := r.restoreNameservers(original, changed); restoreErr != nil {
			err = restoreErr
		}
	}()

	bogus := "probe-" + r.runID + ".invalid"
	patch("P6 one nameserver, expect a 422", target[:1])
	patch("P6 nameservers that do not exist", []string{"ns1." + bogus, "ns2." + bogus})
	r.do("P6 nameservers after the two error cases", "GET", root, nil)
	if r.halted() {
		return nil
	}

	fmt.Fprintf(r.out, "\n### Move nameservers to %v\n", target)
	patch("P6 set target nameservers", target)
	r.doNow("P6 nameservers immediately after the change", "GET", root, nil)
	confirmed := r.pollNameservers("after change", target)
	if r.halted() {
		return nil
	}
	if !confirmed {
		fmt.Fprint(r.out, "\n**Nameserver change NOT confirmed; P11 results below may not reflect a zone served elsewhere.**\n")
	}

	fmt.Fprint(r.out, "\n### P11: record endpoints while GoDaddy does not serve the zone\n")
	name := r.name("p11")
	r.do("P11 read a group that does not exist", "GET", r.group("TXT", r.name("p11-missing")), nil)
	r.put("P11 write a group", "TXT", name, one("p11"))
	r.do("P11 read it back", "GET", r.group("TXT", name), nil)
	r.do("P11 delete it", "DELETE", r.group("TXT", name), nil)
	r.do("P11 read all TXT records", "GET", root+"/records/TXT", nil)
	return nil
}

// restoreNameservers puts the domain back on its original nameservers and
// returns an error if it cannot show that it did. changed says whether a
// nameserver PATCH from this run may have taken effect. It runs in finishing
// mode: an interrupt does not stop it.
func (r *runner) restoreNameservers(original []string, changed bool) error {
	wasFinishing := r.finishing
	r.finishing = true
	defer func() { r.finishing = wasFinishing }()

	root := "/v1/domains/" + r.domain
	byHand := strings.Join(original, ", ")
	fmt.Fprint(r.out, "\n### Restore the original nameservers\n")

	// With nothing changed, one failed read is no reason to send a PATCH.
	now := r.do("P6 nameservers before the restore", "GET", root, nil)
	for retry := 1; retry <= 5 && !changed && now.Status != 200; retry++ {
		time.Sleep(r.restoreEvery)
		now = r.do(fmt.Sprintf("P6 nameservers before the restore, retry %d", retry), "GET", root, nil)
	}
	if !changed && now.Status == 200 && sameHosts(nameServersOf(now.Body), original) {
		fmt.Fprint(r.out, "\nNo nameserver change was accepted and the domain shows its original nameservers. Nothing to restore.\n")
		return nil
	}

	const attempts = 20
	accepted := false
restore:
	for attempt := 1; attempt <= attempts; attempt++ {
		resp := r.do(fmt.Sprintf("P6 restore, attempt %d", attempt), "PATCH", root, rec{"nameServers": original})
		switch {
		case resp.ok():
			accepted = true
			break restore
		case !resp.Sent:
			// The guard refused it. Asking again will not change that.
			break restore
		case resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden:
			fmt.Fprintf(r.out, "\nRestore not authorised (status %d). Trying again cannot help.\n", resp.Status)
			break restore
		case attempt < attempts:
			wait := restoreWait(resp.Header, r.restoreEvery)
			fmt.Fprintf(r.out, "\nRestore not accepted (status %d). Trying again in %s.\n", resp.Status, wait)
			time.Sleep(wait)
		}
	}
	if !accepted {
		// No restore was acknowledged. One may have been applied all the
		// same, so look before telling the user what to do. Either way it
		// is a failure: the tool cannot vouch for the nameservers.
		var err error
		if r.pollNameservers("after the failed restore", original) {
			err = fmt.Errorf("the restore of the nameservers was not acknowledged, but the domain currently shows the original nameservers (%s); check them in the GoDaddy console", byHand)
		} else {
			err = fmt.Errorf("the restore of the nameservers was not accepted; set these nameservers by hand in the GoDaddy console: %s", byHand)
		}
		fmt.Fprintf(r.out, "\n**RESTORE FAILED: %s**\n", err)
		return err
	}

	r.doNow("P6 nameservers immediately after the restore", "GET", root, nil)
	if !r.pollNameservers("after restore", original) {
		err := fmt.Errorf("the restore of the nameservers was accepted but is not visible; "+
			"check the GoDaddy console and if needed set these nameservers by hand: %s", byHand)
		fmt.Fprintf(r.out, "\n**RESTORE NOT CONFIRMED: %s**\n", err)
		return err
	}
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

// pollNameservers reads the domain every r.pollEvery, up to 12 times, until
// its nameservers equal want. It writes one line per read. It gives up early
// if the runner is interrupted, unless the runner is finishing.
func (r *runner) pollNameservers(label string, want []string) bool {
	wasQuiet, gap := r.quiet, r.gap
	defer func() { r.quiet, r.gap = wasQuiet, gap }()
	r.quiet = true
	r.gap = r.pollEvery

	fmt.Fprintf(r.out, "\nPolling %s, waiting for %v:\n\n", label, want)
	started := time.Now()
	for i := 1; i <= 12; i++ {
		if r.halted() {
			fmt.Fprint(r.out, "- interrupted, polling stopped\n")
			return false
		}
		resp := r.do(fmt.Sprintf("poll %d %s", i, label), "GET", "/v1/domains/"+r.domain, nil)
		if !resp.Sent {
			continue // interrupted while waiting; the check above ends the loop
		}
		got := nameServersOf(resp.Body)
		fmt.Fprintf(r.out, "- %3.0f s: status %d, nameServers %v\n", time.Since(started).Seconds(), resp.Status, got)
		if resp.Status == 200 && sameHosts(got, want) {
			return true
		}
	}
	return false
}
