package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// allowance is what one probe may write beyond its own probe records. The
// zero value permits nothing extra.
type allowance struct {
	typeWidePut []string // record types whose type-wide PUT is allowed; usually none
	domainPatch bool     // PATCH of the domain itself (nameservers)
	// realGroup is one {type, name} group outside the probe prefix that the
	// probe may PUT and DELETE at its own path. The match is exact.
	realGroup [2]string
}

// mayWriteGroup reports whether the allowance names exactly this group.
func (a allowance) mayWriteGroup(recordType, name string) bool {
	return a.realGroup[0] != "" && a.realGroup[1] != "" && a.realGroup[0] == recordType && a.realGroup[1] == name
}

// mayReplace reports whether the allowance permits a type-wide PUT to
// recordType. The match is exact.
func (a allowance) mayReplace(recordType string) bool {
	for _, allowed := range a.typeWidePut {
		if allowed != "" && allowed == recordType {
			return true
		}
	}
	return false
}

// zoneWide lists the probes that write more than probe records, with what
// each may write. They do not run without the -allow-zone-wide flag.
var zoneWide = map[string]allowance{
	"p6":  {domainPatch: true},
	"p9":  {typeWidePut: []string{"AAAA"}},
	"p13": {typeWidePut: []string{"AAAA"}},
	"p17": {typeWidePut: []string{"TXT", "MX", "SRV"}},
	"p18": {typeWidePut: []string{"A", "CNAME"}},             // also needs -replace-real-records; see run
	"p16": {domainPatch: true},                               // one PATCH that repeats the current nameservers
	"p19": {realGroup: [2]string{"CNAME", "_domainconnect"}}, // also needs -replace-real-records
}

// allowanceFor returns what the named probe may write beyond probe records.
// Only the probes in zoneWide get anything, and only with the
// -allow-zone-wide flag.
func allowanceFor(probe string, allowZoneWide bool) allowance {
	if !allowZoneWide {
		return allowance{}
	}
	return zoneWide[probe]
}

// checkWrite decides whether the probe tool may send a request. It is the
// only thing standing between a probe and a live zone, so it allows a short
// list of shapes and refuses everything else.
//
//   - GET is always allowed.
//   - A write needs a non-empty probe prefix and a path that starts with a
//     slash and has no empty, "." or ".." segment. That rules out a trailing
//     slash and a doubled one.
//   - A write must target the probe domain.
//   - A write to /records/{type}/{name} is allowed only when {name} starts
//     with the probe prefix, or when {type} is SRV and {name} is the long
//     form of such a name; see srvLongForm. One probe may also PUT and
//     DELETE the one group named in allow.realGroup.
//   - A PUT to /records/{type} (replaces every record of a type) is allowed
//     only for the types named in allow.typeWidePut.
//   - A PATCH of the domain itself (nameservers) needs allow.domainPatch.
//   - A write to /records (replaces the whole zone) is never allowed.
func checkWrite(method, path, domain, prefix string, allow allowance) error {
	if method == "GET" {
		return nil
	}
	if prefix == "" {
		return fmt.Errorf("refusing %s %s: the probe prefix is empty", method, path)
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("refusing %s %s: path does not start with a slash", method, path)
	}
	for _, segment := range strings.Split(path[1:], "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("refusing %s %s: path has a %q segment", method, path, segment)
		}
	}
	root := "/v1/domains/" + domain
	if path != root && !strings.HasPrefix(path, root+"/") {
		return fmt.Errorf("refusing %s %s: not under %s", method, path, root)
	}
	rest := strings.TrimPrefix(path, root)
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	switch {
	case rest == "":
		if method == "PATCH" && allow.domainPatch {
			return nil
		}
		return fmt.Errorf("refusing %s %s: this probe may not change the domain", method, path)
	case len(parts) == 3 && parts[0] == "records":
		if strings.HasPrefix(parts[2], prefix) || (parts[1] == "SRV" && srvLongForm(parts[2], prefix)) {
			return nil
		}
		if (method == "PUT" || method == "DELETE") && allow.mayWriteGroup(parts[1], parts[2]) {
			return nil
		}
		return fmt.Errorf("refusing %s %s: name %q does not start with %q", method, path, parts[2], prefix)
	case len(parts) == 2 && parts[0] == "records":
		if method == "PUT" && allow.mayReplace(parts[1]) {
			return nil
		}
		return fmt.Errorf("refusing %s %s: this probe may not make this type-wide write", method, path)
	default:
		return fmt.Errorf("refusing %s %s: not a request shape the probe tool sends", method, path)
	}
}

// srvLongForm reports whether name is "_<service>._<protocol>.<rest>" where
// the service and the protocol are non-empty and made of a-z, 0-9 and "-"
// only, and <rest> starts with the probe prefix. GoDaddy reads an SRV record
// written at <rest> back under that name.
func srvLongForm(name, prefix string) bool {
	parts := strings.SplitN(name, ".", 3)
	if prefix == "" || len(parts) != 3 {
		return false
	}
	for _, label := range parts[:2] {
		if len(label) < 2 || label[0] != '_' {
			return false
		}
		for _, c := range label[1:] {
			if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
				return false
			}
		}
	}
	return strings.HasPrefix(parts[2], prefix)
}

// zoneRecord is one DNS record as GoDaddy returns it. canon holds every
// field of the record, so that a change in any of them shows in a diff.
type zoneRecord struct {
	Type   string
	Name   string
	canon  string                 // the whole record as JSON with sorted keys
	fields map[string]interface{} // the record as decoded, with its type filled in
}

// newZoneRecord builds a zoneRecord from a decoded JSON object. When the
// object has no type (a per-type read may leave it out) it gets
// fallbackType.
func newZoneRecord(fields map[string]interface{}, fallbackType string) zoneRecord {
	if t, _ := fields["type"].(string); t == "" && fallbackType != "" {
		fields["type"] = fallbackType
	}
	z := zoneRecord{}
	z.Type, _ = fields["type"].(string)
	z.Name, _ = fields["name"].(string)
	canon, err := json.Marshal(fields) // sorts the keys
	if err != nil {
		canon = []byte(fmt.Sprint(fields))
	}
	z.canon = string(canon)
	z.fields = fields
	return z
}

// withoutType returns a copy of the record's fields without "type": the
// form a record takes in the body of a type-wide PUT.
func (z zoneRecord) withoutType() map[string]interface{} {
	out := make(map[string]interface{}, len(z.fields))
	for k, v := range z.fields {
		if k != "type" {
			out[k] = v
		}
	}
	return out
}

// missingFrom returns how many of the wanted records are not among present,
// comparing whole records and counting duplicates.
func missingFrom(present, wanted []zoneRecord) int {
	have := map[string]int{}
	for _, z := range present {
		have[z.canon]++
	}
	missing := 0
	for _, z := range wanted {
		if have[z.canon] == 0 {
			missing++
			continue
		}
		have[z.canon]--
	}
	return missing
}

func (z zoneRecord) String() string { return z.canon }

// countByType summarises a snapshot as "A 2, MX 1, TXT 4".
func countByType(zone []zoneRecord) string {
	count := map[string]int{}
	for _, z := range zone {
		count[z.Type]++
	}
	if len(count) == 0 {
		return "none"
	}
	var parts []string
	for recordType, n := range count {
		parts = append(parts, fmt.Sprintf("%s %d", recordType, n))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// diffOutside compares two snapshots of a zone and returns one line per
// record that was added or removed, ignoring records whose name starts with
// the probe prefix. Records are compared whole, so a change in any field
// shows as one removal and one addition. An empty result means the probe
// left the rest of the zone alone.
func diffOutside(before, after []zoneRecord, prefix string) []string {
	count := map[string]int{}
	for _, z := range before {
		if !strings.HasPrefix(z.Name, prefix) {
			count[z.canon]--
		}
	}
	for _, z := range after {
		if !strings.HasPrefix(z.Name, prefix) {
			count[z.canon]++
		}
	}
	var out []string
	for rec, n := range count {
		switch {
		case n < 0:
			out = append(out, "- "+rec)
		case n > 0:
			out = append(out, "+ "+rec)
		}
	}
	sort.Strings(out)
	return out
}

// domainDetailKeys are the only fields of GET /v1/domains/{domain} that the
// transcript keeps. The full response holds the registrant's name, address,
// phone number and email, which must not reach a file that may be committed.
var domainDetailKeys = map[string]bool{
	"domain":      true,
	"status":      true,
	"nameServers": true,
	"locked":      true,
}

// withheld replaces a domain response that could not be redacted.
const withheld = `{"redaction":"unparseable body withheld"}`

// redactDomainDetail takes the response to GET /v1/domains/{domain} and
// returns what may be written to the transcript. From a 2xx response it
// keeps only the fields in domainDetailKeys; a 2xx body that is not a JSON
// object with a "domain" key is withheld altogether, because contact details
// cannot be told apart in it. Any other status is an error body and is
// returned unchanged.
func redactDomainDetail(status int, body []byte) []byte {
	if status < 200 || status > 299 {
		return body
	}
	var full map[string]json.RawMessage
	if err := json.Unmarshal(body, &full); err != nil {
		return []byte(withheld)
	}
	if _, isDetail := full["domain"]; !isDetail {
		return []byte(withheld)
	}
	kept := map[string]json.RawMessage{}
	for k, v := range full {
		if domainDetailKeys[k] {
			kept[k] = v
		}
	}
	out, err := json.Marshal(kept)
	if err != nil {
		return []byte(withheld)
	}
	return out
}
