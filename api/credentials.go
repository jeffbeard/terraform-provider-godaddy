package api

import (
	"errors"
	"strings"
)

var errNoCredentials = errors.New("no credentials: set token (GODADDY_API_TOKEN) or key and secret (GODADDY_API_KEY, GODADDY_API_SECRET)")

// Credentials holds the values used to authenticate against the GoDaddy API.
// Token is a Personal Access Token and takes precedence when set. Key and
// Secret are the deprecated API key pair and are only used when Token is empty.
type Credentials struct {
	Token  string
	Key    string
	Secret string
}

// authorization returns the value for the Authorization header, or
// errNoCredentials when neither a token nor a complete key/secret pair is set.
func (c Credentials) authorization() (string, error) {
	if tok := c.token(); tok != "" {
		return "Bearer " + tok, nil
	}
	if k, s := c.key(), c.secret(); k != "" && s != "" {
		return "sso-key " + k + ":" + s, nil
	}
	return "", errNoCredentials
}

// Validate reports whether the credentials are usable. Token takes precedence;
// otherwise both Key and Secret are required. Surrounding whitespace is ignored.
func (c Credentials) Validate() error {
	_, err := c.authorization()
	return err
}

func (c Credentials) token() string  { return strings.TrimSpace(c.Token) }
func (c Credentials) key() string    { return strings.TrimSpace(c.Key) }
func (c Credentials) secret() string { return strings.TrimSpace(c.Secret) }
