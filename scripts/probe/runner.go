package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// recordTypes are the types GoDaddy's DNS API knows.
var recordTypes = []string{"A", "AAAA", "CAA", "CNAME", "MX", "NS", "SOA", "SRV", "TXT"}

// runner sends requests to GoDaddy and writes a Markdown transcript of each.
//
// Everything in it belongs to the goroutine that runs the probe, except the
// interrupt flag and the stop channel: those are the only way another
// goroutine (the signal handler) may reach a runner, through interrupt.
type runner struct {
	base         string        // for example https://api.godaddy.com
	token        string        // never written to the transcript
	domain       string        // the probe domain
	runID        string        // random, unique to this run
	prefix       string        // every record a probe creates starts with this
	allow        allowance     // what this probe may write beyond probe records; see checkWrite
	gap          time.Duration // minimum time between requests
	pollEvery    time.Duration // time between nameserver polls
	restoreEvery time.Duration // time between attempts to restore the nameservers
	watchUnit    time.Duration // p19 counts its 10, 20, 30 and 60 "seconds" in this; a second, except in tests
	out          io.Writer     // transcript
	hc           *http.Client

	last      time.Time
	created   [][2]string // {type, name} of groups to delete at the end
	quiet     bool        // send without writing to the transcript
	lastEntry string      // transcript entry of the last request, written or not

	// finishing is set while the tool puts things back: p6's restore, the
	// cleanup and the snapshot after the probe. Those requests are sent even
	// after an interrupt.
	finishing bool

	stop     chan struct{} // closed by the first interrupt
	stopOnce sync.Once
	stopped  atomic.Bool
}

// response is what came back from one request. The zero value means that
// nothing was sent. Sent with Status 0 means the request went out and no
// usable response came back, so it may or may not have taken effect.
type response struct {
	Sent   bool
	Status int
	Header http.Header
	Body   []byte
}

func (resp response) ok() bool { return resp.Status >= 200 && resp.Status <= 299 }

// interrupt records that the user asked the tool to stop. It is the only
// runner method that may be called from another goroutine. It sends nothing
// and undoes nothing: the probe's own goroutine sees the flag, stops sending
// new work and unwinds.
func (r *runner) interrupt() {
	r.stopped.Store(true)
	r.stopOnce.Do(func() {
		if r.stop != nil {
			close(r.stop)
		}
	})
}

func (r *runner) interrupted() bool { return r.stopped.Load() }

// halted reports whether the runner must not send: it was interrupted and
// is not putting things back.
func (r *runner) halted() bool { return r.interrupted() && !r.finishing }

// pause waits for d. It returns false, early, if the runner is interrupted
// while it waits, unless the runner is finishing.
func (r *runner) pause(d time.Duration) bool {
	if r.finishing {
		time.Sleep(d)
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return !r.halted()
	case <-r.stop:
		return false
	}
}

// name returns a record name inside the probe prefix.
func (r *runner) name(suffix string) string { return r.prefix + suffix }

// group returns the path of one type+name group on the probe domain.
func (r *runner) group(recordType, name string) string {
	return "/v1/domains/" + r.domain + "/records/" + recordType + "/" + name
}

// do sends one request with the runner's token.
func (r *runner) do(label, method, path string, body interface{}) response {
	return r.doAs(label, r.token, method, path, body)
}

// doNow is do without the pacing wait.
func (r *runner) doNow(label, method, path string, body interface{}) response {
	gap := r.gap
	r.gap = 0
	defer func() { r.gap = gap }()
	return r.do(label, method, path, body)
}

// doAs sends one request with the given token. It sends nothing, and
// returns the zero response, for a write that checkWrite does not allow and
// for any request once the runner is interrupted and not finishing.
//
// The transcript entry is always built and kept in r.lastEntry. It is
// written to the transcript unless the runner is quiet; a request that was
// not sent or got no response is always written.
func (r *runner) doAs(label, token, method, path string, body interface{}) response {
	heading := fmt.Sprintf("\n#### %s\n\n`%s %s`\n\n", label, method, path)
	unsent := func(why string) response {
		r.lastEntry = heading + why + "\n"
		fmt.Fprint(r.out, r.lastEntry)
		return response{}
	}

	pathOnly := path
	if i := strings.IndexByte(path, '?'); i >= 0 {
		pathOnly = path[:i]
	}
	if err := checkWrite(method, pathOnly, r.domain, r.prefix, r.allow); err != nil {
		return unsent("NOT SENT: " + err.Error())
	}
	if r.halted() {
		return unsent("NOT SENT: interrupted")
	}

	var reqBody []byte
	if body != nil {
		var err error
		if reqBody, err = json.Marshal(body); err != nil {
			panic(err)
		}
	}

	if wait := r.gap - time.Since(r.last); wait > 0 {
		if !r.pause(wait) {
			return unsent("NOT SENT: interrupted")
		}
	}
	r.last = time.Now()

	req, err := http.NewRequest(method, r.base+path, bytes.NewReader(reqBody))
	if err != nil {
		panic(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	started := time.Now()
	resp, err := r.hc.Do(req)
	if err != nil {
		r.lastEntry = heading + "TRANSPORT ERROR: " + err.Error() + "\n"
		fmt.Fprint(r.out, r.lastEntry)
		return response{Sent: true}
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		r.lastEntry = fmt.Sprintf("%sTRANSPORT ERROR: status %d, then the body could not be read: %s\n",
			heading, resp.StatusCode, err)
		fmt.Fprint(r.out, r.lastEntry)
		return response{Sent: true}
	}

	if method == "GET" && pathOnly == "/v1/domains/"+r.domain {
		respBody = redactDomainDetail(resp.StatusCode, respBody)
	}

	var entry strings.Builder
	entry.WriteString(heading)
	if reqBody != nil {
		fmt.Fprintf(&entry, "Request body:\n\n```json\n%s\n```\n\n", reqBody)
	}
	fmt.Fprintf(&entry, "Status: %d (%d ms)\n\n", resp.StatusCode, time.Since(started).Milliseconds())
	var names []string
	for name := range resp.Header {
		if !strings.EqualFold(name, "Set-Cookie") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&entry, "Header %s: %s\n", name, strings.Join(resp.Header[name], ", "))
	}
	if len(names) > 0 {
		entry.WriteString("\n")
	}
	if len(respBody) == 0 {
		entry.WriteString("Response body: empty\n")
	} else {
		fmt.Fprintf(&entry, "Response body:\n\n```json\n%s\n```\n", respBody)
	}
	r.lastEntry = entry.String()
	if !r.quiet {
		fmt.Fprint(r.out, r.lastEntry)
	}
	return response{Sent: true, Status: resp.StatusCode, Header: resp.Header, Body: respBody}
}

// restoreWait returns how long to wait before trying a refused request
// again: the Retry-After header when it is a positive number of seconds, at
// most a minute, and fallback otherwise.
func restoreWait(h http.Header, fallback time.Duration) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After")))
	if err != nil || seconds <= 0 {
		return fallback
	}
	if seconds > 60 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

// put writes a group and remembers it for cleanup. The group is remembered
// even if the write is refused or fails, so that cleanup errs on the side of
// deleting.
func (r *runner) put(label, recordType, name string, records []map[string]interface{}) response {
	r.created = append(r.created, [2]string{recordType, name})
	return r.do(label, "PUT", r.group(recordType, name), records)
}

// inOrder runs the steps one after another and stops before the first one
// that would start after an interrupt. Probes that are a plain list of
// requests use it so that an interrupted run ends at once.
func (r *runner) inOrder(steps ...func()) {
	for _, step := range steps {
		if r.halted() {
			return
		}
		step()
	}
}

// putAs is put with another token. Cleanup still deletes the group with the
// runner's own token.
func (r *runner) putAs(label, token, recordType, name string, records []map[string]interface{}) response {
	r.created = append(r.created, [2]string{recordType, name})
	return r.doAs(label, token, "PUT", r.group(recordType, name), records)
}

// cleanup deletes every group a probe wrote and says so in the transcript
// when a delete is not confirmed (anything but a 2xx, or a 404 for a group
// that is already gone). NS and SOA groups cannot be deleted through this
// endpoint and the probes never create them.
func (r *runner) cleanup() {
	fmt.Fprint(r.out, "\n### Cleanup\n")
	seen := map[[2]string]bool{}
	for _, g := range r.created {
		if seen[g] {
			continue
		}
		seen[g] = true
		resp := r.do("delete "+g[0]+" "+g[1], "DELETE", r.group(g[0], g[1]), nil)
		if !resp.ok() && resp.Status != http.StatusNotFound {
			fmt.Fprintf(r.out, "\n**Cleanup: DELETE of %s %s was not confirmed (status %d). The zone check below shows whether it is still there.**\n",
				g[0], g[1], resp.Status)
		}
	}
	r.created = nil
}

// snapshot reads every record in the zone, one request per record type.
// If the per-type read is not available it falls back to one read of the
// whole zone, which is only trustworthy for zones under 500 records.
func (r *runner) snapshot() ([]zoneRecord, error) {
	wasQuiet := r.quiet
	r.quiet = true
	defer func() { r.quiet = wasQuiet }()

	var all []zoneRecord
	for _, recordType := range recordTypes {
		resp := r.do("snapshot "+recordType, "GET", "/v1/domains/"+r.domain+"/records/"+recordType, nil)
		if r.halted() {
			return nil, fmt.Errorf("snapshot: interrupted")
		}
		if resp.Status != http.StatusOK {
			return r.snapshotWholeZone()
		}
		page, err := decodeRecords(resp.Body, recordType)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", recordType, err)
		}
		all = append(all, page...)
	}
	return all, nil
}

func (r *runner) snapshotWholeZone() ([]zoneRecord, error) {
	resp := r.do("snapshot of the whole zone", "GET", "/v1/domains/"+r.domain+"/records?limit=500", nil)
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("snapshot: GET records returned %d: %s", resp.Status, resp.Body)
	}
	all, err := decodeRecords(resp.Body, "")
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	if len(all) >= 500 {
		return nil, fmt.Errorf("snapshot: zone has %d or more records, too many to snapshot in one read", len(all))
	}
	return all, nil
}

// decodeRecords turns a JSON array of records into zoneRecords, keeping
// every field of each.
func decodeRecords(body []byte, fallbackType string) ([]zoneRecord, error) {
	var raw []map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]zoneRecord, 0, len(raw))
	for _, fields := range raw {
		if fields == nil {
			return nil, fmt.Errorf("a record is null")
		}
		out = append(out, newZoneRecord(fields, fallbackType))
	}
	return out, nil
}
