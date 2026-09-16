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
				assert.EqualError(t, err, errNoCredentials)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCredentialsAuthorization(t *testing.T) {
	tests := []struct {
		name  string
		creds Credentials
		want  string
	}{
		{name: "token", creds: Credentials{Token: "tok"}, want: "Bearer tok"},
		{name: "token trimmed", creds: Credentials{Token: "  tok\n"}, want: "Bearer tok"},
		{name: "token wins over key and secret", creds: Credentials{Token: "tok", Key: "k", Secret: "s"}, want: "Bearer tok"},
		{name: "key and secret", creds: Credentials{Key: "k", Secret: "s"}, want: "sso-key k:s"},
		{name: "key and secret trimmed", creds: Credentials{Key: " k ", Secret: " s "}, want: "sso-key k:s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.creds.authorization())
		})
	}
}
