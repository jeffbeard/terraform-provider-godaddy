package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCredentialsValidate(t *testing.T) {
	tests := []struct {
		name    string
		creds   Credentials
		wantErr bool
	}{
		{name: "token only", creds: Credentials{Token: "tok"}, wantErr: false},
		{name: "key and secret", creds: Credentials{Key: "k", Secret: "s"}, wantErr: false},
		{name: "token plus key and secret", creds: Credentials{Token: "tok", Key: "k", Secret: "s"}, wantErr: false},
		{name: "token plus key only", creds: Credentials{Token: "tok", Key: "k"}, wantErr: false},
		{name: "key only", creds: Credentials{Key: "k"}, wantErr: true},
		{name: "secret only", creds: Credentials{Secret: "s"}, wantErr: true},
		{name: "all empty", creds: Credentials{}, wantErr: true},
		{name: "whitespace token", creds: Credentials{Token: "   "}, wantErr: true},
		{name: "whitespace key and secret", creds: Credentials{Key: " ", Secret: "\t"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.creds.Validate()
			if tt.wantErr {
				assert.ErrorIs(t, err, errNoCredentials)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCredentialsValidateMessage(t *testing.T) {
	assert.EqualError(t, Credentials{}.Validate(),
		"no credentials: set token (GODADDY_API_TOKEN) or key and secret (GODADDY_API_KEY, GODADDY_API_SECRET)")
}

func TestCredentialsAuthorization(t *testing.T) {
	tests := []struct {
		name    string
		creds   Credentials
		want    string
		wantErr bool
	}{
		{name: "token", creds: Credentials{Token: "tok"}, want: "Bearer tok"},
		{name: "token trimmed", creds: Credentials{Token: "  tok\n"}, want: "Bearer tok"},
		{name: "token wins over key and secret", creds: Credentials{Token: "tok", Key: "k", Secret: "s"}, want: "Bearer tok"},
		{name: "token wins over whitespace key", creds: Credentials{Token: "tok", Key: " "}, want: "Bearer tok"},
		{name: "whitespace token falls back to key and secret", creds: Credentials{Token: "  ", Key: "k", Secret: "s"}, want: "sso-key k:s"},
		{name: "key and secret", creds: Credentials{Key: "k", Secret: "s"}, want: "sso-key k:s"},
		{name: "key and secret trimmed", creds: Credentials{Key: " k ", Secret: " s "}, want: "sso-key k:s"},
		{name: "key only", creds: Credentials{Key: "k"}, wantErr: true},
		{name: "all empty", creds: Credentials{}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.creds.authorization()
			if tt.wantErr {
				assert.ErrorIs(t, err, errNoCredentials)
				assert.Empty(t, got)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestCredentialsRejectControlCharacters(t *testing.T) {
	for _, c := range []Credentials{
		{Token: "to\nk"},
		{Token: "to\rk"},
		{Key: "k\x00", Secret: "s"},
		{Key: "k", Secret: "s\nx"},
	} {
		got, err := c.authorization()
		assert.ErrorIs(t, err, errInvalidCredential)
		assert.Empty(t, got)
		assert.ErrorIs(t, c.Validate(), errInvalidCredential)
	}
}
