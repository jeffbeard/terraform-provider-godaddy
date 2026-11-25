package api

import (
	"errors"
	"strings"
)

var (
	errNoCredentials     = errors.New("no credentials: set token (GODADDY_API_TOKEN) or key and secret (GODADDY_API_KEY, GODADDY_API_SECRET)")
	errInvalidCredential = errors.New("invalid credential: contains characters that cannot be sent in an Authorization header")
)

// Credentials holds the values used to authenticate against the GoDaddy API.
// Token is a Personal Access Token and takes precedence when set. Key and
// Secret are the deprecated API key pair and are only used when Token is empty.
type Credentials struct {
	Token  string
	Key    string
	Secret string
}

// authorization returns the value for the Authorization header. It returns
// errNoCredentials when neither a token nor a complete key/secret pair is set,
// and errInvalidCredential when the value contains control characters that
// net/http would reject in a header field value.
func (c Credentials) authorization() (string, error) {
	var value string
	switch {
	case c.token() != "":
		value = "Bearer " + c.token()
	case c.key() != "" && c.secret() != "":
		value = "sso-key " + c.key() + ":" + c.secret()
	default:
		return "", errNoCredentials
	}
	if !validHeaderValue(value) {
		return "", errInvalidCredential
	}
	return value, nil
}

// validHeaderValue mirrors what net/http accepts in a header field value:
// no control characters other than horizontal tab, and no DEL.
func validHeaderValue(s string) bool {
	for i := 0; i < len(s); i++ {
		if b := s[i]; (b < 0x20 && b != '\t') || b == 0x7f {
			return false
		}
	}
	return true
}

// Validate returns an error unless the credentials are usable. Token takes
// precedence; otherwise both Key and Secret are required. Surrounding
// whitespace is ignored.
func (c Credentials) Validate() error {
	_, err := c.authorization()
	return err
}

func (c Credentials) token() string  { return strings.TrimSpace(c.Token) }
func (c Credentials) key() string    { return strings.TrimSpace(c.Key) }
func (c Credentials) secret() string { return strings.TrimSpace(c.Secret) }
