package service

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/exbanka/stock-service/internal/model"
)

func seedConsumedParent(t *testing.T, f *otcCRUDFixture, ownerID uint64, qty int64) *model.OTCOffer {
	t.Helper()
	oid := ownerID
	parent := &model.OTCOffer{
		InitiatorOwnerType:          model.OwnerClient,
		InitiatorOwnerID:            &oid,
		Direction:                   model.OTCDirectionSellInitiated,
		StockID:                     5,
		Ticker:                      "AAPL",
		Quantity:                    decimal.NewFromInt(qty),
		Status:                      model.OTCOfferStatusConsumed, // accept already consumed the listing
		Local:                       true,
		InitiatorAccountID:          9,
		LastModifiedByPrincipalType: "client",
		LastModifiedByPrincipalID:   ownerID,
	}
	if err := f.offers.Create(parent); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	return parent
}

// TestRelistAcceptRemainder_PartialAccept asserts that after a partial accept
// (bid 10 of a 35 listing) the unsold remainder (25) is re-advertised as a
// brand-new OPEN listing — a fresh id, NOT the consumed parent — so it keeps
// trading as a new negotiation surface.
func TestRelistAcceptRemainder_PartialAccept(t *testing.T) {
	f := newOTCCRUDFixture(t)
	ownerID := uint64(1)
	parent := seedConsumedParent(t, f, ownerID, 35)

	f.svc.relistAcceptRemainder(context.Background(), parent, decimal.NewFromInt(10))

	open, err := f.offers.ListOpenForCache(100)
	if err != nil {
		t.Fatalf("list open: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("want exactly 1 fresh open remainder listing, got %d", len(open))
	}
	r := open[0]
	if r.ID == parent.ID {
		t.Errorf("remainder must be a NEW listing, not the consumed parent (id %d)", parent.ID)
	}
	if !r.Quantity.Equal(decimal.NewFromInt(25)) {
		t.Errorf("remainder quantity = %s, want 25", r.Quantity)
	}
	if r.Ticker != "AAPL" || r.Direction != model.OTCDirectionSellInitiated || r.InitiatorAccountID != 9 {
		t.Errorf("remainder offer fields mismatch: %+v", r)
	}
	if r.InitiatorOwnerID == nil || *r.InitiatorOwnerID != ownerID {
		t.Errorf("remainder owner mismatch: %+v", r.InitiatorOwnerID)
	}
	// The consumed parent must stay consumed (not reopened).
	got, _ := f.offers.GetByID(parent.ID)
	if got.Status != model.OTCOfferStatusConsumed {
		t.Errorf("parent status = %q, want consumed", got.Status)
	}
}

// TestRelistAcceptRemainder_FullTake_NoRelist asserts that when the accept takes
// the WHOLE listing (bid == listing quantity) nothing is re-listed.
func TestRelistAcceptRemainder_FullTake_NoRelist(t *testing.T) {
	f := newOTCCRUDFixture(t)
	parent := seedConsumedParent(t, f, 1, 35)

	f.svc.relistAcceptRemainder(context.Background(), parent, decimal.NewFromInt(35))

	open, err := f.offers.ListOpenForCache(100)
	if err != nil {
		t.Fatalf("list open: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("full take must not re-list; got %d open offers", len(open))
	}
}
