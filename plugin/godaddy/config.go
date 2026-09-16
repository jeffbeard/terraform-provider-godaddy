package godaddy

import (
	"fmt"
	"log"

	"github.com/n3integration/terraform-provider-godaddy/api"
)

// Config provides the provider's configuration
type Config struct {
	Token   string
	Key     string
	Secret  string
	BaseURL string
}

// Client returns a new client for accessing GoDaddy.
func (c *Config) Client() (*api.Client, error) {
	client, err := api.NewClient(c.BaseURL, api.Credentials{
		Token:  c.Token,
		Key:    c.Key,
		Secret: c.Secret,
	})

	if err != nil {
		return nil, fmt.Errorf("error setting up client: %s", err)
	}

	log.Print("GoDaddy Client configured")

	return client, nil
}
