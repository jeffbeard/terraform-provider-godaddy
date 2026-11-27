package godaddy

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// These tests pin the requests godaddy_domain_record sends today. The
// resource is frozen in 1.x: if one of these tests fails, the change under
// test has altered what existing users' applies do. Do not update the
// expected values to make a failure go away.

const (
	base = "/v1/domains/example.org"

	nsBody = `[{"type":"NS","name":"@","data":"ns1.example.net","priority":0,"ttl":3600,"weight":0},` +
		`{"type":"NS","name":"@","data":"ns2.example.net","priority":0,"ttl":3600,"weight":0}]`

	defaultCNAMEBody = `[{"type":"CNAME","name":"www","data":"@","priority":0,"ttl":3600,"weight":0},` +
		`{"type":"CNAME","name":"_domainconnect","data":"_domainconnect.gd.domaincontrol.com","priority":0,"ttl":3600,"weight":0}]`
)

// fullConfig uses every attribute. It includes a CAA record on purpose: the
// provider accepts it and never sends it.
func fullConfig() map[string]interface{} {
	return map[string]interface{}{
		"domain":      "example.org",
		"addresses":   []interface{}{"192.0.2.1"},
		"nameservers": []interface{}{"ns1.example.net", "ns2.example.net"},
		"record": []interface{}{
			map[string]interface{}{"name": "www", "type": "CNAME", "data": "example.github.io", "ttl": 600},
			map[string]interface{}{"name": "@", "type": "MX", "data": "mx1.example.net", "priority": 10},
			map[string]interface{}{"name": "@", "type": "TXT", "data": "v=spf1 -all"},
			map[string]interface{}{"name": "@", "type": "CAA", "data": `0 issue "letsencrypt.org"`},
		},
	}
}

// nameserversOnlyConfig is the configuration that prompted the 2026-11-26
// audit: it looks like "only change the nameservers".
func nameserversOnlyConfig() map[string]interface{} {
	return map[string]interface{}{
		"domain":      "example.org",
		"nameservers": []interface{}{"ns1.example.net", "ns2.example.net"},
	}
}

func resellerConfig() map[string]interface{} {
	return map[string]interface{}{
		"domain":   "example.org",
		"customer": "5678",
		"record": []interface{}{
			map[string]interface{}{"name": "@", "type": "TXT", "data": "hello"},
		},
	}
}

var fullWrites = []string{
	"GET " + base,
	"PUT " + base + `/records/A [{"type":"A","name":"@","data":"192.0.2.1","priority":0,"ttl":3600,"weight":0}]`,
	"PUT " + base + "/records/AAAA []",
	"PUT " + base + `/records/CNAME [{"type":"CNAME","name":"www","data":"example.github.io","priority":0,"ttl":600,"weight":0}]`,
	"PUT " + base + `/records/MX [{"type":"MX","name":"@","data":"mx1.example.net","priority":10,"ttl":3600,"weight":0}]`,
	"PUT " + base + "/records/NS " + nsBody,
	"PUT " + base + "/records/SRV []",
	"PUT " + base + `/records/TXT [{"type":"TXT","name":"@","data":"v=spf1 -all","priority":0,"ttl":3600,"weight":0}]`,
}

var nameserversOnlyWrites = []string{
	"GET " + base,
	"PUT " + base + "/records/A []",
	"PUT " + base + "/records/AAAA []",
	"PUT " + base + "/records/CNAME []",
	"PUT " + base + "/records/MX []",
	"PUT " + base + "/records/NS " + nsBody,
	"PUT " + base + "/records/SRV []",
	"PUT " + base + "/records/TXT []",
}

var resellerWrites = []string{
	"GET " + base + " shopper=5678",
	"PUT " + base + "/records/A [] shopper=5678",
	"PUT " + base + "/records/AAAA [] shopper=5678",
	"PUT " + base + "/records/CNAME [] shopper=5678",
	"PUT " + base + "/records/MX [] shopper=5678",
	"PUT " + base + "/records/SRV [] shopper=5678",
	"PUT " + base + `/records/TXT [{"type":"TXT","name":"@","data":"hello","priority":0,"ttl":3600,"weight":0}] shopper=5678`,
}

// restoreWrites is what destroy sends, whatever the configuration was.
var restoreWrites = []string{
	"GET " + base,
	"PUT " + base + "/records/A []",
	"PUT " + base + "/records/AAAA []",
	"PUT " + base + "/records/CNAME " + defaultCNAMEBody,
	"PUT " + base + "/records/MX []",
	"PUT " + base + "/records/SRV []",
	"PUT " + base + "/records/TXT []",
}

type crudFunc func(context.Context, *schema.ResourceData, interface{}) diag.Diagnostics

func TestDomainRecordRequests(t *testing.T) {
	cases := []struct {
		name   string
		op     crudFunc
		config map[string]interface{}
		want   []string
	}{
		{"create full", resourceDomainRecordCreate, fullConfig(), fullWrites},
		{"update full", resourceDomainRecordUpdate, fullConfig(), fullWrites},
		{"create nameservers only", resourceDomainRecordCreate, nameserversOnlyConfig(), nameserversOnlyWrites},
		{"update nameservers only", resourceDomainRecordUpdate, nameserversOnlyConfig(), nameserversOnlyWrites},
		{"create reseller without nameservers", resourceDomainRecordCreate, resellerConfig(), resellerWrites},
		{"delete full", resourceDomainRecordRestore, fullConfig(), restoreWrites},
		{"delete nameservers only", resourceDomainRecordRestore, nameserversOnlyConfig(), restoreWrites},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Each case has its own client, so its own one-request-per-second
			// throttle. Running in parallel keeps the suite under ten seconds.
			t.Parallel()
			fake, client := newFakeGoDaddy(t, "[]")
			d := schema.TestResourceDataRaw(t, resourceDomainRecord().Schema, tc.config)

			if diags := tc.op(context.Background(), d, client); diags.HasError() {
				t.Fatalf("unexpected error: %v", diags)
			}

			assertRequests(t, fake.sorted(), tc.want)
			// The writes go out in map order, but the read of the domain
			// always comes first. tc.want is sorted, and its first entry
			// is that read.
			if first := fake.inOrder()[0]; first != tc.want[0] || !strings.HasPrefix(first, "GET "+base) {
				t.Errorf("first request = %q, want the domain read %q", first, tc.want[0])
			}
			if d.Id() != "1234" {
				t.Errorf("resource ID = %q, want the domainId %q", d.Id(), "1234")
			}
		})
	}
}

// zoneJSON is what the fake returns for the first page of records. It mixes
// records the provider classifies as addresses, as nameservers, and as
// plain record blocks.
const zoneJSON = `[
 {"type":"A","name":"@","data":"192.0.2.1","ttl":3600},
 {"type":"A","name":"@","data":"192.0.2.9","ttl":600},
 {"type":"NS","name":"@","data":"ns1.example.net","ttl":3600},
 {"type":"NS","name":"@","data":"ns2.example.net","ttl":3600},
 {"type":"NS","name":"sub","data":"ns1.other.net","ttl":3600},
 {"type":"SOA","name":"@","data":"Primary nameserver: ns1.example.net","ttl":3600},
 {"type":"MX","name":"@","data":"mx1.example.net","priority":10,"ttl":3600},
 {"type":"CNAME","name":"www","data":"@","ttl":3600}
]`

// zoneRecordBlocks is what a read of zoneJSON leaves in state as record
// blocks, in the form recordSummaries returns: everything that is not an
// apex A or NS record with TTL 3600.
var zoneRecordBlocks = map[string]bool{
	"A @ 192.0.2.9":                             true,
	"NS sub ns1.other.net":                      true,
	"SOA @ Primary nameserver: ns1.example.net": true,
	"MX @ mx1.example.net":                      true,
	"CNAME www @":                               true,
}

var readRequests = []string{
	"GET " + base + "/records?limit=500&offset=1",
	"GET " + base + "/records?limit=500&offset=2",
	"GET " + base,
}

// recordSummaries reduces the record set to sortable strings so the test
// does not depend on set hash order.
func recordSummaries(d *schema.ResourceData) map[string]bool {
	out := map[string]bool{}
	for _, raw := range d.Get("record").(*schema.Set).List() {
		r := raw.(map[string]interface{})
		key := r["type"].(string) + " " + r["name"].(string) + " " + r["data"].(string)
		out[key] = true
	}
	return out
}

func TestDomainRecordRead(t *testing.T) {
	t.Parallel()
	fake, client := newFakeGoDaddy(t, zoneJSON)
	d := schema.TestResourceDataRaw(t, resourceDomainRecord().Schema, nameserversOnlyConfig())

	if diags := resourceDomainRecordRead(context.Background(), d, client); diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	assertRequests(t, fake.inOrder(), readRequests)

	// Apex A records with TTL 3600 become addresses; the one with TTL 600
	// stays a record block.
	if got, want := d.Get("addresses"), []interface{}{"192.0.2.1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("addresses = %v, want %v", got, want)
	}
	// Apex NS records with TTL 3600 become nameservers.
	if got, want := d.Get("nameservers"), []interface{}{"ns1.example.net", "ns2.example.net"}; !reflect.DeepEqual(got, want) {
		t.Errorf("nameservers = %v, want %v", got, want)
	}
	// Everything else, including SOA and the subdomain NS record, lands in
	// state as record blocks even though none of it is in the configuration.
	if got := recordSummaries(d); !reflect.DeepEqual(got, zoneRecordBlocks) {
		t.Errorf("record blocks = %v, want %v", got, zoneRecordBlocks)
	}
	if d.Id() != "1234" {
		t.Errorf("resource ID = %q, want %q", d.Id(), "1234")
	}
}

// Import calls read with the domain name as the ID and nothing else set.
func TestDomainRecordImport(t *testing.T) {
	t.Parallel()
	fake, client := newFakeGoDaddy(t, zoneJSON)
	d := resourceDomainRecord().TestResourceData()
	d.SetId("example.org")

	if diags := resourceDomainRecordRead(context.Background(), d, client); diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	assertRequests(t, fake.inOrder(), readRequests)
	if got := d.Get("domain"); got != "example.org" {
		t.Errorf("domain = %v, want example.org", got)
	}
	if got, want := d.Get("nameservers"), []interface{}{"ns1.example.net", "ns2.example.net"}; !reflect.DeepEqual(got, want) {
		t.Errorf("nameservers = %v, want %v", got, want)
	}
	if got, want := d.Get("addresses"), []interface{}{"192.0.2.1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("addresses = %v, want %v", got, want)
	}
	if got := recordSummaries(d); !reflect.DeepEqual(got, zoneRecordBlocks) {
		t.Errorf("record blocks = %v, want %v", got, zoneRecordBlocks)
	}
	if d.Id() != "1234" {
		t.Errorf("resource ID = %q, want %q", d.Id(), "1234")
	}
}
