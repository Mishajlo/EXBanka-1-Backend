package handler

import (
	"testing"

	"github.com/exbanka/api-gateway/internal/middleware"
)

func TestOtcOfferMeOwner(t *testing.T) {
	emp := &middleware.ResolvedIdentity{PrincipalType: "employee", OwnerType: "bank"}
	cli := &middleware.ResolvedIdentity{PrincipalType: "client", OwnerType: "client", OwnerID: u64ptr(5)}

	cases := []struct {
		name     string
		id       *middleware.ResolvedIdentity
		kind     string
		sellerID string
		want     bool
	}{
		{"employee owns bank-local", emp, "local", "bank", true},
		{"employee not owner of client-local", emp, "local", "client-5", false},
		{"employee never owns remote", emp, "remote", "bank", false},
		{"client owns own local", cli, "local", "client-5", true},
		{"client not owner of other", cli, "local", "client-9", false},
		{"client never owns remote", cli, "remote", "client-5", false},
		{"nil identity", nil, "local", "bank", false},
	}
	for _, c := range cases {
		if got := otcOfferMeOwner(c.id, c.kind, c.sellerID); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestMeOwnerForOwner(t *testing.T) {
	emp := &middleware.ResolvedIdentity{OwnerType: "bank"}
	cli := &middleware.ResolvedIdentity{OwnerType: "client", OwnerID: u64ptr(5)}
	if !meOwnerForOwner(emp, "bank", nil) {
		t.Error("employee should own bank resource")
	}
	if meOwnerForOwner(emp, "client", u64ptr(5)) {
		t.Error("employee should not own client resource")
	}
	if !meOwnerForOwner(cli, "client", u64ptr(5)) {
		t.Error("client should own own resource")
	}
	if meOwnerForOwner(cli, "client", u64ptr(9)) {
		t.Error("client should not own another client's resource")
	}
	if meOwnerForOwner(nil, "bank", nil) {
		t.Error("nil identity owns nothing")
	}
	if meOwnerForOwner(cli, "client", nil) {
		t.Error("client resource with nil owner id is unowned")
	}
}
