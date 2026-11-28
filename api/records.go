package api

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Record is one DNS record as the per-name endpoints send and return it.
//
// A field that a record type does not use is left out of a request; a field
// it does use is sent even when its value is zero. GoDaddy refuses a CAA
// record without "flags", so flags 0 has to reach the wire. That is why the
// numeric fields are pointers: nil means "not used", a pointer to 0 means 0.
type Record struct {
	// Name and Type are set on records GoDaddy returns. They are not sent:
	// the path of a per-name request already carries both.
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`

	Data string `json:"data"`
	TTL  int    `json:"ttl"`

	Priority *int `json:"priority,omitempty"` // MX, SRV

	Weight   *int   `json:"weight,omitempty"`   // SRV
	Port     *int   `json:"port,omitempty"`     // SRV
	Service  string `json:"service,omitempty"`  // SRV
	Protocol string `json:"protocol,omitempty"` // SRV

	Flags *int   `json:"flags,omitempty"` // CAA
	Tag   string `json:"tag,omitempty"`   // CAA
}

// zonePageSize is the page size for the whole-zone list.
const zonePageSize = 500

func groupPath(domain, recordType, name string) string {
	return "/v1/domains/" + url.PathEscape(domain) + "/records/" + url.PathEscape(recordType) + "/" + url.PathEscape(name)
}

// GetRecordGroup returns every record of one type at one name. An empty
// result with a nil error means the group does not exist.
//
// SRV is read differently. GoDaddy answers a per-name read of an SRV group
// with an empty array even when records exist, so an SRV group is read from
// the whole-zone list and filtered.
func (c *Client) GetRecordGroup(ctx context.Context, customerID, domain, recordType, name string) ([]Record, error) {
	if recordType == SRVType {
		return c.getSRVGroup(ctx, customerID, domain, name)
	}
	var records []Record
	if err := c.do(ctx, customerID, "GET", groupPath(domain, recordType, name), nil, &records); err != nil {
		return nil, err
	}
	return records, nil
}

// getSRVGroup pages through the whole zone. GoDaddy's offset is a page
// number that starts at 1, and the list ends at the first empty page.
func (c *Client) getSRVGroup(ctx context.Context, customerID, domain, name string) ([]Record, error) {
	var group []Record
	for page := 1; ; page++ {
		path := fmt.Sprintf("/v1/domains/%s/records?limit=%d&offset=%d", url.PathEscape(domain), zonePageSize, page)
		var records []Record
		if err := c.do(ctx, customerID, "GET", path, nil, &records); err != nil {
			return nil, err
		}
		if len(records) == 0 {
			return group, nil
		}
		for _, r := range records {
			if r.Type == SRVType && strings.EqualFold(r.Name, name) {
				group = append(group, r)
			}
		}
	}
}

// ReplaceRecordGroup makes records the complete set of records of one type
// at one name. It touches no other record. It refuses an empty set before
// sending anything: removing a group is DeleteRecordGroup's job.
func (c *Client) ReplaceRecordGroup(ctx context.Context, customerID, domain, recordType, name string, records []Record) error {
	if len(records) == 0 {
		return errors.New("refusing to replace a record group with nothing; delete the group instead")
	}
	body := make([]Record, len(records))
	for i, r := range records {
		r.Name, r.Type = "", ""
		body[i] = r
	}
	return c.do(ctx, customerID, "PUT", groupPath(domain, recordType, name), body, nil)
}

// DeleteRecordGroup removes every record of one type at one name. When the
// group does not exist GoDaddy answers 404 RECORD_NOT_FOUND, which this
// returns as an error; see IsRecordNotFound.
func (c *Client) DeleteRecordGroup(ctx context.Context, customerID, domain, recordType, name string) error {
	return c.do(ctx, customerID, "DELETE", groupPath(domain, recordType, name), nil, nil)
}
