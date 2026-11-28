package api

import (
	"context"
	"errors"
	"net/url"
)

// GetNameservers returns the nameservers a domain is delegated to, as
// GoDaddy returns them: case and order are not stable.
func (c *Client) GetNameservers(ctx context.Context, customerID, domain string) ([]string, error) {
	var detail struct {
		NameServers []string `json:"nameServers"`
	}
	if err := c.do(ctx, customerID, "GET", "/v1/domains/"+url.PathEscape(domain), nil, &detail); err != nil {
		return nil, err
	}
	return detail.NameServers, nil
}

// SetNameservers changes the nameservers a domain is delegated to. GoDaddy
// accepts the change with a 204 and reads show it 10 to 20 seconds later, so
// a read straight afterwards still returns the old nameservers.
func (c *Client) SetNameservers(ctx context.Context, customerID, domain string, nameservers []string) error {
	if len(nameservers) < 2 {
		return errors.New("a domain needs at least two nameservers")
	}
	body := map[string][]string{"nameServers": nameservers}
	return c.do(ctx, customerID, "PATCH", "/v1/domains/"+url.PathEscape(domain), body, nil)
}
