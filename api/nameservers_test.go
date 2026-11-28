package api

import (
	"context"
	"reflect"
	"testing"
)

func TestGetNameserversReturnsThemAsGiven(t *testing.T) {
	f := &fakeAPI{}
	f.on("GET /v1/domains/example.org", reply{status: 200,
		body: `{"domain":"example.org","nameServers":["NS10.DOMAINCONTROL.COM","NS09.DOMAINCONTROL.COM"],"status":"ACTIVE"}`})
	c, _ := testClient(t, f)

	got, err := c.GetNameservers(context.Background(), "", "example.org")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"NS10.DOMAINCONTROL.COM", "NS09.DOMAINCONTROL.COM"}; !reflect.DeepEqual(got, want) {
		t.Errorf("nameservers = %v, want %v", got, want)
	}
}

func TestSetNameserversBodyAndPath(t *testing.T) {
	f := &fakeAPI{}
	f.on("PATCH /v1/domains/example.org", reply{status: 204})
	c, _ := testClient(t, f)

	err := c.SetNameservers(context.Background(), "", "example.org", []string{"ns1.example.net", "ns2.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`PATCH /v1/domains/example.org {"nameServers":["ns1.example.net","ns2.example.net"]}`}
	if !reflect.DeepEqual(f.requests, want) {
		t.Errorf("sent %v, want %v", f.requests, want)
	}
}

func TestSetNameserversRefusesFewerThanTwo(t *testing.T) {
	f := &fakeAPI{}
	c, _ := testClient(t, f)

	for _, ns := range [][]string{nil, {"ns1.example.net"}} {
		if err := c.SetNameservers(context.Background(), "", "example.org", ns); err == nil {
			t.Errorf("want an error for %v", ns)
		}
	}
	if len(f.requests) != 0 {
		t.Errorf("a request was sent: %v", f.requests)
	}
}
