package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// reply is one canned response.
type reply struct {
	status int
	header map[string]string
	body   string
}

// fakeAPI answers each request with the next reply queued for its
// "METHOD path?query" key, or with 200 and an empty body when none is
// queued. It records what it received.
type fakeAPI struct {
	mu       sync.Mutex
	requests []string           // "METHOD path?query body"
	headers  []http.Header      // parallel to requests
	queue    map[string][]reply // consumed in order
}

func (f *fakeAPI) on(key string, replies ...reply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.queue == nil {
		f.queue = map[string][]reply{}
	}
	f.queue[key] = append(f.queue[key], replies...)
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.RequestURI()

	f.mu.Lock()
	line := key
	if len(body) > 0 {
		line += " " + string(body)
	}
	f.requests = append(f.requests, line)
	f.headers = append(f.headers, r.Header.Clone())
	var next reply
	if q := f.queue[key]; len(q) > 0 {
		next, f.queue[key] = q[0], q[1:]
	} else {
		next = reply{status: http.StatusOK}
	}
	f.mu.Unlock()

	for k, v := range next.header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(next.status)
	io.WriteString(w, next.body)
}

// testClient returns a client pointed at the fake, with the one-second
// throttle removed and retry waits recorded instead of slept.
func testClient(t *testing.T, f *fakeAPI) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	c, err := NewClient(srv.URL, Credentials{Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	c.client.Transport = http.DefaultTransport

	var waits []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	return c, &waits
}

func intPtr(n int) *int { return &n }
