package handler

import (
	"strconv"

	"github.com/exbanka/api-gateway/internal/middleware"
)

// otcOfferMeOwner reports whether the acting identity owns this OTC offer
// (its seller/poster). A remote listing is hosted by a peer and is never
// owned by us, so it is always false. For local listings: an employee
// (acting for the bank) owns bank listings; a client owns listings whose
// seller_id is "client-<their owner id>".
func otcOfferMeOwner(identity *middleware.ResolvedIdentity, kind, sellerID string) bool {
	if identity == nil || kind != "local" {
		return false
	}
	if identity.OwnerType == "bank" {
		return sellerID == "bank"
	}
	if identity.OwnerID != nil {
		return sellerID == "client-"+strconv.FormatUint(*identity.OwnerID, 10)
	}
	return false
}

// meOwnerForOwner reports whether the acting identity owns a resource with
// the given owner_type/owner_id — the same rule the Resource Ownership
// Verification middleware enforces server-side. Used to decorate
// negotiation and contract read responses.
func meOwnerForOwner(identity *middleware.ResolvedIdentity, ownerType string, ownerID *uint64) bool {
	if identity == nil {
		return false
	}
	switch identity.OwnerType {
	case "bank":
		return ownerType == "bank"
	case "client":
		return ownerType == "client" && ownerID != nil && identity.OwnerID != nil && *ownerID == *identity.OwnerID
	}
	return false
}
