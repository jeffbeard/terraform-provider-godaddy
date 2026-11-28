package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func get(t *testing.T, c *Client, url string) {
	t.Helper()
	resp, err := c.client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// Two clients built for the same base URL and credential share one throttle,
// so two requests, one from each, are a second apart. A client for another
// credential is not held back by them.
func TestClientsForOneCredentialShareTheThrottle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	first, err := NewClient(srv.URL, Credentials{Token: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewClient(srv.URL, Credentials{Token: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewClient(srv.URL, Credentials{Token: "another"})
	if err != nil {
		t.Fatal(err)
	}
	if first.client.Transport != second.client.Transport {
		t.Fatal("clients for one credential have different transports")
	}
	if first.client.Transport == other.client.Transport {
		t.Fatal("clients for different credentials share a transport")
	}

	start := time.Now()
	get(t, first, srv.URL)
	get(t, other, srv.URL)
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("a client for another credential waited %s", d)
	}
	get(t, second, srv.URL)
	if d := time.Since(start); d < 900*time.Millisecond {
		t.Errorf("the second client for the same credential sent after only %s, want about 1s", d)
	}
}
