package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCheckWrite(t *testing.T) {
	const domain, prefix = "example.org", "_probe-ab12-"
	var (
		none = allowance{}
		p9   = allowance{typeWidePut: []string{"AAAA"}}
		p6   = allowance{domainPatch: true}
		p17  = allowanceFor("p17", true)
		both = allowance{typeWidePut: []string{"AAAA"}, domainPatch: true}
	)
	cases := []struct {
		name      string
		method    string
		path      string
		prefix    string
		allow     allowance
		wantAllow bool
	}{
		{"read of anything", "GET", "/v1/domains/other.org/records", prefix, none, true},
		{"put probe group", "PUT", "/v1/domains/example.org/records/TXT/_probe-ab12-a", prefix, none, true},
		{"delete probe group", "DELETE", "/v1/domains/example.org/records/TXT/_probe-ab12-a", prefix, none, true},
		{"put real group", "PUT", "/v1/domains/example.org/records/MX/@", prefix, none, false},
		{"put real group even with every allowance", "PUT", "/v1/domains/example.org/records/MX/@", prefix, both, false},
		{"delete www", "DELETE", "/v1/domains/example.org/records/CNAME/www", prefix, none, false},
		{"other run's prefix", "PUT", "/v1/domains/example.org/records/TXT/_probe-ffff-a", prefix, none, false},
		{"type-wide without allowance", "PUT", "/v1/domains/example.org/records/AAAA", prefix, none, false},
		{"type-wide AAAA with p9's allowance", "PUT", "/v1/domains/example.org/records/AAAA", prefix, p9, true},
		{"type-wide MX when only AAAA is allowed", "PUT", "/v1/domains/example.org/records/MX", prefix, p9, false},
		{"type-wide delete with p9's allowance", "DELETE", "/v1/domains/example.org/records/AAAA", prefix, p9, false},
		{"type-wide put with p6's allowance", "PUT", "/v1/domains/example.org/records/AAAA", prefix, p6, false},
		{"whole zone put", "PUT", "/v1/domains/example.org/records", prefix, both, false},
		{"whole zone patch", "PATCH", "/v1/domains/example.org/records", prefix, both, false},
		{"nameservers without allowance", "PATCH", "/v1/domains/example.org", prefix, none, false},
		{"nameservers with p6's allowance", "PATCH", "/v1/domains/example.org", prefix, p6, true},
		{"nameservers with p9's allowance", "PATCH", "/v1/domains/example.org", prefix, p9, false},
		{"delete the domain", "DELETE", "/v1/domains/example.org", prefix, both, false},
		{"another domain", "PUT", "/v1/domains/other.org/records/TXT/_probe-ab12-a", prefix, both, false},
		{"domain name as prefix of another", "PUT", "/v1/domains/example.organic/records/TXT/_probe-ab12-a", prefix, both, false},
		{"empty prefix, any name", "PUT", "/v1/domains/example.org/records/MX/@", "", none, false},
		{"empty prefix, probe-looking name", "PUT", "/v1/domains/example.org/records/TXT/_probe-ab12-a", "", none, false},
		{"empty prefix, nameservers", "PATCH", "/v1/domains/example.org", "", p6, false},
		{"dot-dot back into the domain", "PUT", "/v1/domains/example.org/records/TXT/../_probe-ab12-a", prefix, both, false},
		{"dot-dot out of the domain", "PATCH", "/v1/domains/example.org/../example.org", prefix, both, false},
		{"dot-dot as the type", "PUT", "/v1/domains/example.org/records/../_probe-ab12-a", prefix, both, false},
		{"trailing slash on a type-wide put", "PUT", "/v1/domains/example.org/records/AAAA/", prefix, p9, false},
		{"trailing slash on a probe group", "PUT", "/v1/domains/example.org/records/TXT/_probe-ab12-a/", prefix, both, false},
		{"trailing slash on the domain", "PATCH", "/v1/domains/example.org/", prefix, p6, false},
		{"empty segment before records", "PUT", "/v1/domains/example.org//records/TXT/_probe-ab12-a", prefix, both, false},
		{"empty segment as the type", "PUT", "/v1/domains/example.org/records//_probe-ab12-a", prefix, both, false},
		{"no leading slash", "PATCH", "v1/domains/example.org", prefix, p6, false},
		{"p17: type-wide TXT", "PUT", "/v1/domains/example.org/records/TXT", prefix, p17, true},
		{"p17: type-wide MX", "PUT", "/v1/domains/example.org/records/MX", prefix, p17, true},
		{"p17: type-wide SRV", "PUT", "/v1/domains/example.org/records/SRV", prefix, p17, true},
		{"p17: type-wide AAAA", "PUT", "/v1/domains/example.org/records/AAAA", prefix, p17, false},
		{"p17: type-wide A", "PUT", "/v1/domains/example.org/records/A", prefix, p17, false},
		{"p17: type-wide CNAME", "PUT", "/v1/domains/example.org/records/CNAME", prefix, p17, false},
		{"p17: type-wide NS", "PUT", "/v1/domains/example.org/records/NS", prefix, p17, false},
		{"p17: type-wide txt in lower case", "PUT", "/v1/domains/example.org/records/txt", prefix, p17, false},
		{"p17: type-wide delete of TXT", "DELETE", "/v1/domains/example.org/records/TXT", prefix, p17, false},
		{"p17: type-wide patch of MX", "PATCH", "/v1/domains/example.org/records/MX", prefix, p17, false},
		{"p17: whole zone put", "PUT", "/v1/domains/example.org/records", prefix, p17, false},
		{"p17: nameservers", "PATCH", "/v1/domains/example.org", prefix, p17, false},
		{"p17: real MX group", "PUT", "/v1/domains/example.org/records/MX/@", prefix, p17, false},
		{"p13: type-wide TXT", "PUT", "/v1/domains/example.org/records/TXT", prefix, allowanceFor("p13", true), false},
		{"p9: type-wide SRV", "PUT", "/v1/domains/example.org/records/SRV", prefix, p9, false},
		{"empty type name in the set does not match", "PUT", "/v1/domains/example.org/records/MX", prefix, allowance{typeWidePut: []string{""}}, false},
		{"single dot segment", "PUT", "/v1/domains/example.org/records/./_probe-ab12-a", prefix, both, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkWrite(tc.method, tc.path, domain, tc.prefix, tc.allow)
			if allowed := err == nil; allowed != tc.wantAllow {
				t.Errorf("checkWrite(%s %s, prefix=%q, allow=%+v) allowed=%v, want %v (err: %v)",
					tc.method, tc.path, tc.prefix, tc.allow, allowed, tc.wantAllow, err)
			}
		})
	}
}

func TestAllowanceFor(t *testing.T) {
	cases := []struct {
		probe    string
		zoneWide bool
		want     allowance
	}{
		{"p6", true, allowance{domainPatch: true}},
		{"p9", true, allowance{typeWidePut: []string{"AAAA"}}},
		{"p6", false, allowance{}},
		{"p9", false, allowance{}},
		{"p13", true, allowance{typeWidePut: []string{"AAAA"}}},
		{"p17", true, allowance{typeWidePut: []string{"TXT", "MX", "SRV"}}},
		{"p17", false, allowance{}},
		{"p18", true, allowance{typeWidePut: []string{"A", "CNAME"}}},
		{"p18", false, allowance{}},
		{"p19", true, allowance{realGroup: [2]string{"CNAME", "_domainconnect"}}},
		{"p19", false, allowance{}},
		{"p2", true, allowance{}},
		{"p10", true, allowance{}},
	}
	for _, tc := range cases {
		if got := allowanceFor(tc.probe, tc.zoneWide); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("allowanceFor(%s, %v) = %+v, want %+v", tc.probe, tc.zoneWide, got, tc.want)
		}
	}
}

// zr builds a zoneRecord from the JSON GoDaddy would return for it.
func zr(t *testing.T, recordJSON string) zoneRecord {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(recordJSON), &m); err != nil {
		t.Fatalf("bad test record %s: %s", recordJSON, err)
	}
	return newZoneRecord(m, "")
}

func TestDiffOutside(t *testing.T) {
	const prefix = "_probe-ab12-"
	const (
		mxJSON  = `{"data":"mx1.example.net","name":"@","priority":10,"ttl":3600,"type":"MX"}`
		txtJSON = `{"data":"v=spf1 -all","name":"@","ttl":3600,"type":"TXT"}`
		txt600  = `{"data":"v=spf1 -all","name":"@","ttl":600,"type":"TXT"}`
		srvJSON = `{"data":"sip.example.net","name":"@","port":5060,"priority":10,"protocol":"_tcp","service":"_sip","ttl":3600,"type":"SRV","weight":5}`
		srv5061 = `{"data":"sip.example.net","name":"@","port":5061,"priority":10,"protocol":"_tcp","service":"_sip","ttl":3600,"type":"SRV","weight":5}`
	)
	mx, txt, srv := zr(t, mxJSON), zr(t, txtJSON), zr(t, srvJSON)
	probe := zr(t, `{"type":"TXT","name":"_probe-ab12-a","data":"x","ttl":3600}`)
	// The same record with its keys in another order is the same record.
	mxReordered := zr(t, `{"type":"MX","ttl":3600,"priority":10,"name":"@","data":"mx1.example.net"}`)

	cases := []struct {
		name          string
		before, after []zoneRecord
		want          []string
	}{
		{"identical", []zoneRecord{mx, txt}, []zoneRecord{txt, mxReordered}, nil},
		{"probe record left behind is ignored", []zoneRecord{mx}, []zoneRecord{mx, probe}, nil},
		{"record lost", []zoneRecord{mx, txt}, []zoneRecord{txt}, []string{"- " + mxJSON}},
		{"record added", []zoneRecord{mx}, []zoneRecord{mx, txt}, []string{"+ " + txtJSON}},
		{"ttl changed shows as remove and add", []zoneRecord{txt}, []zoneRecord{zr(t, txt600)},
			[]string{"+ " + txt600, "- " + txtJSON}},
		{"one of two duplicates lost", []zoneRecord{mx, mx}, []zoneRecord{mx}, []string{"- " + mxJSON}},
		{"only an SRV port differs", []zoneRecord{srv, mx}, []zoneRecord{zr(t, srv5061), mx},
			[]string{"+ " + srv5061, "- " + srvJSON}},
		{"a field appears", []zoneRecord{txt},
			[]zoneRecord{zr(t, `{"data":"v=spf1 -all","name":"@","ttl":3600,"type":"TXT","weight":1}`)},
			[]string{`+ {"data":"v=spf1 -all","name":"@","ttl":3600,"type":"TXT","weight":1}`, "- " + txtJSON}},
		{"record without a name is outside the prefix", nil, []zoneRecord{zr(t, `{"type":"TXT","data":"x"}`)},
			[]string{`+ {"data":"x","type":"TXT"}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := diffOutside(tc.before, tc.after, prefix); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewZoneRecordFillsInType(t *testing.T) {
	z := newZoneRecord(map[string]interface{}{"name": "www", "data": "2001:db8::2"}, "AAAA")
	if z.Type != "AAAA" || z.Name != "www" {
		t.Errorf("got type %q name %q, want AAAA www", z.Type, z.Name)
	}
	if want := `{"data":"2001:db8::2","name":"www","type":"AAAA"}`; z.String() != want {
		t.Errorf("canonical form = %s, want %s", z, want)
	}
	if z := newZoneRecord(map[string]interface{}{"name": "www", "type": "A"}, "AAAA"); z.Type != "A" {
		t.Errorf("a type that is present must be kept, got %q", z.Type)
	}
}

func TestCountByType(t *testing.T) {
	zone := []zoneRecord{
		zr(t, `{"type":"TXT","name":"@"}`), zr(t, `{"type":"A","name":"@"}`), zr(t, `{"type":"TXT","name":"a"}`),
	}
	if got, want := countByType(zone), "A 1, TXT 2"; got != want {
		t.Errorf("countByType = %q, want %q", got, want)
	}
	if got, want := countByType(nil), "none"; got != want {
		t.Errorf("countByType(nil) = %q, want %q", got, want)
	}
}

func TestRedactDomainDetail(t *testing.T) {
	in := []byte(`{"domain":"example.org","status":"ACTIVE","nameServers":["ns1.x","ns2.x"],` +
		`"locked":true,"domainId":42,"contactRegistrant":{"email":"someone@example.org","nameFirst":"A"}}`)
	got := string(redactDomainDetail(200, in))
	want := `{"domain":"example.org","locked":true,"nameServers":["ns1.x","ns2.x"],"status":"ACTIVE"}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}

	// A 2xx body that is not a domain detail object might still hold contact
	// details in a shape this code does not know. It is withheld.
	for _, unknown := range []string{``, `[]`, `{"contactRegistrant":{"email":"someone@example.org"}}`,
		`{"domain":"example.org","contactRegistrant":{"email":"someone@example.org"`, `not json someone@example.org`} {
		got := string(redactDomainDetail(200, []byte(unknown)))
		if got != `{"redaction":"unparseable body withheld"}` {
			t.Errorf("redactDomainDetail(200, %q) = %q, want it withheld", unknown, got)
		}
		if strings.Contains(got, "someone") {
			t.Errorf("redactDomainDetail(200, %q) leaked contact details: %s", unknown, got)
		}
	}

	// Error responses are the thing the probes are looking for. They pass.
	for _, passthrough := range []string{``, `{"code":"NOT_FOUND","message":"x"}`, `not json`} {
		for _, status := range []int{401, 404, 422, 500} {
			if got := string(redactDomainDetail(status, []byte(passthrough))); got != passthrough {
				t.Errorf("redactDomainDetail(%d, %q) = %q, want it unchanged", status, passthrough, got)
			}
		}
	}
}
