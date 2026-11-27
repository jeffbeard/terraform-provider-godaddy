package godaddy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/n3integration/terraform-provider-godaddy/api"
)

// fakeGoDaddy is a stand-in for the GoDaddy API that records every request
// it receives. It exists to pin the exact requests godaddy_domain_record
// sends, so later changes to shared code cannot alter them unnoticed.
type fakeGoDaddy struct {
	mu       sync.Mutex
	requests []string
	// records is the JSON array returned for the first page of
	// GET /v1/domains/{domain}/records. Later pages are empty.
	records string
	server  *httptest.Server
}

// newFakeGoDaddy starts the fake and returns it with a client pointed at it.
// The server is closed when the test ends.
func newFakeGoDaddy(t *testing.T, recordsJSON string) (*fakeGoDaddy, *api.Client) {
	t.Helper()
	f := &fakeGoDaddy{records: recordsJSON}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)

	client, err := api.NewClient(f.server.URL, api.Credentials{Token: "tok"})
	if err != nil {
		t.Fatalf("NewClient: %s", err)
	}
	return f, client
}

func (f *fakeGoDaddy) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	line := r.Method + " " + r.URL.RequestURI()
	if len(body) > 0 {
		line += " " + string(body)
	}
	if shopper := r.Header.Get("X-Shopper-Id"); shopper != "" {
		line += " shopper=" + shopper
	}
	if auth := r.Header.Get("Authorization"); auth != "Bearer tok" {
		line += " UNEXPECTED-AUTH=" + auth
	}
	f.mu.Lock()
	f.requests = append(f.requests, line)
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/records"):
		if r.URL.Query().Get("offset") == "1" {
			io.WriteString(w, f.records)
		} else {
			io.WriteString(w, "[]")
		}
	case r.Method == http.MethodGet:
		io.WriteString(w, `{"domainId":1234,"domain":"example.org","status":"ACTIVE"}`)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// sorted returns the recorded requests in sorted order. The provider sends
// its per-type writes in Go map order, which is random, so tests compare the
// set of requests and not their sequence.
func (f *fakeGoDaddy) sorted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.requests...)
	sort.Strings(out)
	return out
}

// inOrder returns the recorded requests in the order they arrived.
func (f *fakeGoDaddy) inOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func assertRequests(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d requests, want %d\ngot:\n  %s\nwant:\n  %s",
			len(got), len(want), strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d\n got: %s\nwant: %s", i, got[i], want[i])
		}
	}
}
