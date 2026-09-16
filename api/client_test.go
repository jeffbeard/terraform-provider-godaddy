package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// newRecordingServer returns a test server that captures the headers of the
// last request and replies with an empty JSON array. A second request within
// one test through the same client pays a real 1s throttle sleep.
func newRecordingServer(t *testing.T) (*httptest.Server, *http.Header) {
	captured := &http.Header{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*captured = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	return srv, captured
}

func TestNewClientRejectsMissingCredentials(t *testing.T) {
	client, err := NewClient("https://api.godaddy.com", Credentials{})
	assert.Nil(t, client)
	assert.ErrorIs(t, err, errNoCredentials)
}

func TestNewClientRejectsInvalidURL(t *testing.T) {
	client, err := NewClient("api.godaddy.com", Credentials{Token: "tok"})
	assert.Nil(t, client)
	assert.Error(t, err)
}

func TestNewClientRejectsControlCharacters(t *testing.T) {
	client, err := NewClient("https://api.godaddy.com", Credentials{Token: "to\nk"})
	assert.Nil(t, client)
	assert.Error(t, err)
}

func TestNewClientChecksCredentialsBeforeURL(t *testing.T) {
	_, err := NewClient("api.godaddy.com", Credentials{})
	assert.ErrorIs(t, err, errNoCredentials)
}

func TestExecuteSendsBearerToken(t *testing.T) {
	srv, headers := newRecordingServer(t)

	client, err := NewClient(srv.URL, Credentials{Token: "tok"})
	assert.NoError(t, err)

	_, err = client.GetDomains("")
	assert.NoError(t, err)
	assert.Equal(t, "Bearer tok", headers.Get("Authorization"))
	assert.Empty(t, headers.Get("X-Shopper-Id"))
}

func TestExecuteSendsLegacyKey(t *testing.T) {
	srv, headers := newRecordingServer(t)

	client, err := NewClient(srv.URL, Credentials{Key: "k", Secret: "s"})
	assert.NoError(t, err)

	_, err = client.GetDomains("")
	assert.NoError(t, err)
	assert.Equal(t, "sso-key k:s", headers.Get("Authorization"))
}

func TestExecuteSendsShopperID(t *testing.T) {
	srv, headers := newRecordingServer(t)

	client, err := NewClient(srv.URL, Credentials{Token: "tok"})
	assert.NoError(t, err)

	_, err = client.GetDomains("1234")
	assert.NoError(t, err)
	assert.Equal(t, "Bearer tok", headers.Get("Authorization"))
	assert.Equal(t, "1234", headers.Get("X-Shopper-Id"))
}
