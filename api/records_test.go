package api

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestGetRecordGroupDecodesEveryField(t *testing.T) {
	f := &fakeAPI{}
	f.on("GET /v1/domains/example.org/records/CAA/@", reply{status: 200,
		body: `[{"data":"letsencrypt.org","flags":0,"name":"@","tag":"issue","ttl":3600,"type":"CAA"}]`})
	f.on("GET /v1/domains/example.org/records/MX/@", reply{status: 200,
		body: `[{"data":"mx1.example.net","name":"@","priority":0,"ttl":600,"type":"MX"}]`})
	c, _ := testClient(t, f)

	caa, err := c.GetRecordGroup(context.Background(), "", "example.org", "CAA", "@")
	if err != nil {
		t.Fatal(err)
	}
	want := []Record{{Name: "@", Type: "CAA", Data: "letsencrypt.org", TTL: 3600, Flags: intPtr(0), Tag: "issue"}}
	if !reflect.DeepEqual(caa, want) {
		t.Errorf("CAA = %+v", caa)
	}

	mx, err := c.GetRecordGroup(context.Background(), "", "example.org", "MX", "@")
	if err != nil {
		t.Fatal(err)
	}
	if len(mx) != 1 || mx[0].Priority == nil || *mx[0].Priority != 0 {
		t.Errorf("priority 0 must decode as a pointer to 0, not nil: %+v", mx)
	}
}

func TestGetRecordGroupOfMissingGroupIsEmptyNotAnError(t *testing.T) {
	f := &fakeAPI{}
	f.on("GET /v1/domains/example.org/records/TXT/gone", reply{status: 200, body: `[]`})
	c, _ := testClient(t, f)

	records, err := c.GetRecordGroup(context.Background(), "", "example.org", "TXT", "gone")
	if err != nil || len(records) != 0 {
		t.Errorf("records %v, err %v", records, err)
	}
}

func TestGroupPathEscaping(t *testing.T) {
	cases := map[string]string{
		"@":              "/v1/domains/example.org/records/TXT/@",
		"*":              "/v1/domains/example.org/records/TXT/%2A",
		"*.sub":          "/v1/domains/example.org/records/TXT/%2A.sub",
		"_dmarc":         "/v1/domains/example.org/records/TXT/_dmarc",
		"sel._domainkey": "/v1/domains/example.org/records/TXT/sel._domainkey",
		"a/b":            "/v1/domains/example.org/records/TXT/a%2Fb",
		"a?limit=1":      "/v1/domains/example.org/records/TXT/a%3Flimit=1",
	}
	for name, want := range cases {
		if got := groupPath("example.org", "TXT", name); got != want {
			t.Errorf("groupPath(%q) = %s, want %s", name, got, want)
		}
	}
}

// Fields a type uses must reach the wire even when zero; fields it does not
// use must be absent; name and type are never sent.
func TestReplaceRecordGroupBodies(t *testing.T) {
	cases := []struct {
		name       string
		recordType string
		records    []Record
		want       string
	}{
		{"TXT", "TXT", []Record{{Name: "www", Type: "TXT", Data: "x", TTL: 600}},
			`[{"data":"x","ttl":600}]`},
		{"MX with priority 0", "MX", []Record{{Data: "mx1.example.net", TTL: 3600, Priority: intPtr(0)}},
			`[{"data":"mx1.example.net","ttl":3600,"priority":0}]`},
		{"CAA with flags 0", "CAA", []Record{{Data: "letsencrypt.org", TTL: 3600, Flags: intPtr(0), Tag: "issue"}},
			`[{"data":"letsencrypt.org","ttl":3600,"flags":0,"tag":"issue"}]`},
		{"SRV with weight 0", "SRV", []Record{{Data: "sip.example.net", TTL: 3600, Priority: intPtr(10), Weight: intPtr(0),
			Port: intPtr(5060), Service: "_sip", Protocol: "_tcp"}},
			`[{"data":"sip.example.net","ttl":3600,"priority":10,"weight":0,"port":5060,"service":"_sip","protocol":"_tcp"}]`},
		{"two values", "A", []Record{{Data: "192.0.2.1", TTL: 600}, {Data: "192.0.2.2", TTL: 600}},
			`[{"data":"192.0.2.1","ttl":600},{"data":"192.0.2.2","ttl":600}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAPI{}
			c, _ := testClient(t, f)

			if err := c.ReplaceRecordGroup(context.Background(), "", "example.org", tc.recordType, "www", tc.records); err != nil {
				t.Fatal(err)
			}
			want := "PUT /v1/domains/example.org/records/" + tc.recordType + "/www " + tc.want
			if len(f.requests) != 1 || f.requests[0] != want {
				t.Errorf("sent\n  %v\nwant\n  %s", f.requests, want)
			}
		})
	}
}

func TestReplaceRecordGroupDoesNotChangeItsArgument(t *testing.T) {
	f := &fakeAPI{}
	c, _ := testClient(t, f)
	records := []Record{{Name: "www", Type: "TXT", Data: "x", TTL: 600}}

	if err := c.ReplaceRecordGroup(context.Background(), "", "example.org", "TXT", "www", records); err != nil {
		t.Fatal(err)
	}
	if records[0].Name != "www" || records[0].Type != "TXT" {
		t.Errorf("the caller's slice was modified: %+v", records)
	}
}

// An empty replace is what the old resource sends. The new client must
// never send one.
func TestReplaceRecordGroupRefusesAnEmptySet(t *testing.T) {
	f := &fakeAPI{}
	c, _ := testClient(t, f)

	for _, records := range [][]Record{nil, {}} {
		if err := c.ReplaceRecordGroup(context.Background(), "", "example.org", "TXT", "www", records); err == nil {
			t.Error("want an error for an empty set")
		}
	}
	if len(f.requests) != 0 {
		t.Errorf("an empty replace reached the server: %v", f.requests)
	}
}

func TestDeleteRecordGroup(t *testing.T) {
	f := &fakeAPI{}
	f.on("DELETE /v1/domains/example.org/records/TXT/www", reply{status: 204})
	c, _ := testClient(t, f)

	if err := c.DeleteRecordGroup(context.Background(), "", "example.org", "TXT", "www"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"DELETE /v1/domains/example.org/records/TXT/www"}; !reflect.DeepEqual(f.requests, want) {
		t.Errorf("sent %v", f.requests)
	}
}

// An SRV group is read from the whole-zone list: GoDaddy returns nothing for
// a per-name read of SRV. Paging is one-based and ends at an empty page.
func TestGetRecordGroupForSRVPagesTheWholeZone(t *testing.T) {
	f := &fakeAPI{}
	f.on("GET /v1/domains/example.org/records?limit=500&offset=1", reply{status: 200, body: `[
	  {"data":"192.0.2.1","name":"@","ttl":3600,"type":"A"},
	  {"data":"sip.example.net","name":"voip","port":5060,"priority":10,"protocol":"_tcp","service":"_sip","ttl":3600,"type":"SRV","weight":5},
	  {"data":"other.example.net","name":"elsewhere","port":1,"priority":1,"protocol":"_tcp","service":"_sip","ttl":3600,"type":"SRV","weight":1},
	  {"data":"voip-target.example.net","name":"voip","ttl":3600,"type":"CNAME"}
	]`})
	f.on("GET /v1/domains/example.org/records?limit=500&offset=2", reply{status: 200, body: `[
	  {"data":"xmpp.example.net","name":"VOIP","port":5222,"priority":10,"protocol":"_tcp","service":"_xmpp-client","ttl":3600,"type":"SRV","weight":5}
	]`})
	f.on("GET /v1/domains/example.org/records?limit=500&offset=3", reply{status: 200, body: `[]`})
	c, _ := testClient(t, f)

	records, err := c.GetRecordGroup(context.Background(), "", "example.org", "SRV", "voip")
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, r := range records {
		got = append(got, r.Service+" "+r.Data)
	}
	if want := []string{"_sip sip.example.net", "_xmpp-client xmpp.example.net"}; !reflect.DeepEqual(got, want) {
		t.Errorf("group = %v, want %v", got, want)
	}
	if len(f.requests) != 3 {
		t.Errorf("sent %d requests, want three pages: %v", len(f.requests), f.requests)
	}
	for _, r := range f.requests {
		if strings.Contains(r, "/records/SRV") {
			t.Errorf("the SRV read must not use a per-type or per-name SRV path: %s", r)
		}
	}
}

func TestGetRecordGroupForSRVStopsOnError(t *testing.T) {
	f := &fakeAPI{}
	f.on("GET /v1/domains/example.org/records?limit=500&offset=1", reply{status: 404,
		body: `{"code":"UNKNOWN_DOMAIN","message":"The given domain is not registered, or does not have a zone file"}`})
	c, _ := testClient(t, f)

	records, err := c.GetRecordGroup(context.Background(), "", "example.org", "SRV", "voip")
	if !IsUnknownDomain(err) || records != nil {
		t.Errorf("records %v, err %v", records, err)
	}
}
