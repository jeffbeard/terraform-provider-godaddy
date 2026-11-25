package godaddy

import (
	"fmt"
	"log"
	"strings"

	"github.com/n3integration/terraform-provider-godaddy/api"
)

// Config provides the provider's configuration
type Config struct {
	Token   string
	Key     string
	Secret  string
	BaseURL string
}

// credentials returns the api.Credentials represented by this Config.
func (c *Config) credentials() api.Credentials {
	return api.Credentials{
		Token:  c.Token,
		Key:    c.Key,
		Secret: c.Secret,
	}
}

// Client returns a new client for accessing GoDaddy.
func (c *Config) Client() (*api.Client, error) {
	client, err := api.NewClient(c.BaseURL, c.credentials())

	if err != nil {
		return nil, fmt.Errorf("error setting up client: %s", err)
	}

	scheme := "api key"
	if strings.TrimSpace(c.Token) != "" {
		scheme = "personal access token"
	}
	log.Printf("GoDaddy Client configured (auth: %s)", scheme)

	return client, nil
}
