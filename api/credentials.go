package api

import (
	"errors"
	"fmt"
	"strings"
)

const errNoCredentials = "no credentials: set token (GODADDY_API_TOKEN) or key and secret (GODADDY_API_KEY, GODADDY_API_SECRET)"

// Credentials holds the values used to authenticate against the GoDaddy API.
// Token is a Personal Access Token and takes precedence when set. Key and
// Secret are the deprecated API key pair and are only used when Token is empty.
type Credentials struct {
	Token  string
	Key    string
	Secret string
}

// Validate returns an error when neither a token nor a complete key/secret
// pair is present. Surrounding whitespace is ignored.
func (c Credentials) Validate() error {
	if c.token() != "" {
		return nil
	}
	if c.key() != "" && c.secret() != "" {
		return nil
	}
	return errors.New(errNoCredentials)
}

// authorization returns the value for the Authorization header. Callers
// must call Validate first.
func (c Credentials) authorization() string {
	if tok := c.token(); tok != "" {
		return fmt.Sprintf("Bearer %s", tok)
	}
	return fmt.Sprintf("sso-key %s:%s", c.key(), c.secret())
}

func (c Credentials) token() string  { return strings.TrimSpace(c.Token) }
func (c Credentials) key() string    { return strings.TrimSpace(c.Key) }
func (c Credentials) secret() string { return strings.TrimSpace(c.Secret) }
