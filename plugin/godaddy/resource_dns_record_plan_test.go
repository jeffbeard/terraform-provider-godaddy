package godaddy

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// These tests pin what godaddy_domain_record plans for fixed state and
// configuration. The request tests cannot see a change in planning: an SDK
// upgrade can alter the plan without altering the requests sent for a given
// plan. Do not update the expected values to make a failure go away.

// planFor returns the planned changes as sorted "attribute: old => new"
// lines, with " (forces replacement)" appended where the SDK marks it.
// Set elements are keyed by a hash in the SDK's flat form, so the hash is
// replaced by "#" and each line carries its values instead.
func planFor(t *testing.T, state *terraform.InstanceState, config map[string]interface{}) []string {
	t.Helper()
	diff, err := resourceDomainRecord().Diff(context.Background(), state,
		terraform.NewResourceConfigRaw(config), nil)
	if err != nil {
		t.Fatalf("Diff: %s", err)
	}
	if diff == nil {
		return nil
	}
	var out []string
	for key, attr := range diff.Attributes {
		line := fmt.Sprintf("%s: %q => %q", stripSetHash(key), attr.Old, attr.New)
		if attr.NewRemoved {
			line += " (removed)"
		}
		if attr.RequiresNew {
			line += " (forces replacement)"
		}
		out = append(out, line)
	}
	sort.Strings(out)
	return out
}

// stripSetHash turns "record.123456.name" into "record.#.name".
func stripSetHash(key string) string {
	const prefix = "record."
	if len(key) <= len(prefix) || key[:len(prefix)] != prefix {
		return key
	}
	rest := key[len(prefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '.' {
			return prefix + "#" + rest[i:]
		}
		if rest[i] < '0' || rest[i] > '9' {
			return key
		}
	}
	return key
}

// stateAfterCreate returns the state Terraform would hold after creating
// the resource from config.
func stateAfterCreate(t *testing.T, config map[string]interface{}) *terraform.InstanceState {
	t.Helper()
	d := schema.TestResourceDataRaw(t, resourceDomainRecord().Schema, config)
	d.SetId("1234")
	return d.State()
}

func oneRecordConfig() map[string]interface{} {
	return map[string]interface{}{
		"domain":    "example.org",
		"addresses": []interface{}{"192.0.2.1"},
		"record": []interface{}{
			map[string]interface{}{"name": "@", "type": "TXT", "data": "hello"},
		},
	}
}

func TestDomainRecordPlan(t *testing.T) {
	domainOnly := map[string]interface{}{"domain": "example.org"}

	otherDomain := nameserversOnlyConfig()
	otherDomain["domain"] = "example.com"

	withCustomer := nameserversOnlyConfig()
	withCustomer["customer"] = "5678"

	cases := []struct {
		name   string
		state  *terraform.InstanceState
		config map[string]interface{}
		want   []string
	}{
		{
			// A new resource plans only additions. Nothing in the plan
			// shows that existing records at GoDaddy will be replaced.
			name:   "create with nameservers only",
			state:  nil,
			config: nameserversOnlyConfig(),
			want: []string{
				`domain: "" => "example.org"`,
				`nameservers.#: "0" => "2"`,
				`nameservers.0: "" => "ns1.example.net"`,
				`nameservers.1: "" => "ns2.example.net"`,
			},
		},
		{
			name:   "create applies record defaults",
			state:  nil,
			config: oneRecordConfig(),
			want: []string{
				`addresses.#: "0" => "1"`,
				`addresses.0: "" => "192.0.2.1"`,
				`domain: "" => "example.org"`,
				`record.#.data: "" => "hello"`,
				`record.#.name: "" => "@"`,
				`record.#.port: "" => "0"`,
				`record.#.priority: "" => "0"`,
				`record.#.protocol: "" => ""`,
				`record.#.service: "" => ""`,
				`record.#.ttl: "" => "3600"`,
				`record.#.type: "" => "TXT"`,
				`record.#.weight: "" => "0"`,
				`record.#: "0" => "1"`,
			},
		},
		{
			name:   "state equal to configuration plans nothing",
			state:  stateAfterCreate(t, fullConfig()),
			config: fullConfig(),
			want:   nil,
		},
		{
			// Anything in state that is not in configuration is planned
			// for removal. After a refresh, state holds the whole zone.
			name:   "records and addresses absent from configuration are removed",
			state:  stateAfterCreate(t, oneRecordConfig()),
			config: domainOnly,
			want: []string{
				`addresses.#: "1" => "0"`,
				`addresses.0: "192.0.2.1" => "" (removed)`,
				`record.#.data: "hello" => "" (removed)`,
				`record.#.name: "@" => "" (removed)`,
				`record.#.port: "0" => "0" (removed)`,
				`record.#.priority: "0" => "0" (removed)`,
				`record.#.protocol: "" => "" (removed)`,
				`record.#.service: "" => "" (removed)`,
				`record.#.ttl: "3600" => "0" (removed)`,
				`record.#.type: "TXT" => "" (removed)`,
				`record.#.weight: "0" => "0" (removed)`,
				`record.#: "1" => "0"`,
			},
		},
		{
			// Audit finding F10: changing the domain is an in-place update,
			// not a replacement.
			name:   "changing domain does not force replacement",
			state:  stateAfterCreate(t, nameserversOnlyConfig()),
			config: otherDomain,
			want:   []string{`domain: "example.org" => "example.com"`},
		},
		{
			name:   "changing customer does not force replacement",
			state:  stateAfterCreate(t, nameserversOnlyConfig()),
			config: withCustomer,
			want:   []string{`customer: "" => "5678"`},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := planFor(t, tc.state, tc.config)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("plan differs\n got:\n  %s\nwant:\n  %s",
					strings.Join(got, "\n  "), strings.Join(tc.want, "\n  "))
			}
		})
	}
}
