//go:build integration
// +build integration

package api

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

var creds Credentials
var baseURL string

func init() {
	creds = Credentials{
		Token:  os.Getenv("GODADDY_API_TOKEN"),
		Key:    os.Getenv("GODADDY_API_KEY"),
		Secret: os.Getenv("GODADDY_API_SECRET"),
	}
	baseURL = "https://api.godaddy.com"
}

func TestInvalidUrl(t *testing.T) {
	_, err := NewClient("api.godaddy.com", Credentials{Token: "tok"})
	assert.NotNil(t, err)
}

func TestAuthFailure(t *testing.T) {
	client, err := NewClient(baseURL, Credentials{Token: "bogus"})
	assert.Nil(t, err)
	assert.NotNil(t, client)

	_, err = client.GetDomainRecords("", "bogus.com")
	assert.NotNil(t, err)
}

func TestGetRecords(t *testing.T) {
	client, err := NewClient(baseURL, creds)
	assert.Nil(t, err)
	assert.NotNil(t, client)

	getRecords(t, client, "n3integration.com")
}

func TestGetTooManyRecords(t *testing.T) {
	client, err := NewClient(baseURL, creds)
	assert.Nil(t, err)
	assert.NotNil(t, client)

	for i := 0; i < 75; i++ {
		_, err := getRecords(t, client, "n3integration.com")
		if err != nil {
			fmt.Println("Requests failing at", i+1)
		}
	}
}

func getRecords(t *testing.T, client *Client, domain string) ([]*DomainRecord, error) {
	records, err := client.GetDomainRecords("", domain)
	assert.Nil(t, err)
	assert.NotNil(t, records)

	for _, rec := range records {
		assert.NotNil(t, rec.Name)
		assert.NotNil(t, rec.Type)
		assert.NotNil(t, rec.Data)

		fmt.Println("REC -->", rec)
	}

	return records, err
}
