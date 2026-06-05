package handler

import (
	"context"
	"testing"
	"time"

	stockpb "github.com/exbanka/contract/stockpb"
	"github.com/exbanka/stock-service/internal/model"
	"github.com/shopspring/decimal"
)

// fakeOTCOfferReader is a stub OTCOfferReader returning a fixed slice.
type fakeOTCOfferReader struct {
	rows []model.OTCOffer
	err  error
}

func (f *fakeOTCOfferReader) ListOpenForCache(limit int) ([]model.OTCOffer, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// TestComposePeerSellerID covers every branch of composePeerSellerID, the
// outbound SI-TX wire-identity composer. The literal "bank" must NEVER be
// emitted: a bank offer with an acting employee maps to "employee-<N>", a
// legacy/seed bank offer (no acting employee) maps to "" (skipped), and a
// client offer maps to "client-<owner_id>".
func TestComposePeerSellerID(t *testing.T) {
	tests := []struct {
		name string
		o    *model.OTCOffer
		want string
	}{
		{
			name: "bank offer with acting employee -> employee-<N>",
			o:    &model.OTCOffer{InitiatorOwnerType: model.OwnerBank, ActingEmployeeID: u64(17)},
			want: "employee-17",
		},
		{
			name: "bank offer without acting employee -> empty (not exposable)",
			o:    &model.OTCOffer{InitiatorOwnerType: model.OwnerBank, ActingEmployeeID: nil},
			want: "",
		},
		{
			name: "client offer -> client-<owner_id>",
			o:    &model.OTCOffer{InitiatorOwnerType: model.OwnerClient, InitiatorOwnerID: u64(9)},
			want: "client-9",
		},
		{
			name: "client offer with nil owner id -> empty",
			o:    &model.OTCOffer{InitiatorOwnerType: model.OwnerClient, InitiatorOwnerID: nil},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := composePeerSellerID(tc.o); got != tc.want {
				t.Errorf("composePeerSellerID() = %q, want %q", got, tc.want)
			}
			if got := composePeerSellerID(tc.o); got == "bank" {
				t.Errorf("composePeerSellerID must NEVER return the literal \"bank\"")
			}
		})
	}
}

// TestGetPublicOptionOffers_SellerIDComposition asserts the published list:
//   - a bank offer with ActingEmployeeID=17 surfaces with sellerId.id == "employee-17"
//   - a bank offer with ActingEmployeeID==nil is ABSENT (filtered out)
//   - a client offer (owner 9) surfaces with sellerId.id == "client-9"
//   - the literal "bank" never appears as any row's sellerId.id
func TestGetPublicOptionOffers_SellerIDComposition(t *testing.T) {
	now := time.Now().UTC()
	emp := uint64(17)
	owner := uint64(9)
	reader := &fakeOTCOfferReader{rows: []model.OTCOffer{
		{
			ID:                 1,
			InitiatorOwnerType: model.OwnerBank,
			ActingEmployeeID:   &emp,
			Direction:          model.OTCDirectionSellInitiated,
			Ticker:             "AAPL",
			Quantity:           decimal.NewFromInt(10),
			StrikePrice:        decimal.NewFromInt(150),
			Premium:            decimal.NewFromInt(5),
			SettlementDate:     now,
			CreatedAt:          now,
			Status:             model.OTCOfferStatusOpen,
		},
		{
			ID:                 2,
			InitiatorOwnerType: model.OwnerBank,
			ActingEmployeeID:   nil, // legacy/seed bank offer — must be skipped
			Direction:          model.OTCDirectionSellInitiated,
			Ticker:             "MSFT",
			Quantity:           decimal.NewFromInt(3),
			StrikePrice:        decimal.NewFromInt(300),
			Premium:            decimal.NewFromInt(8),
			SettlementDate:     now,
			CreatedAt:          now,
			Status:             model.OTCOfferStatusOpen,
		},
		{
			ID:                 3,
			InitiatorOwnerType: model.OwnerClient,
			InitiatorOwnerID:   &owner,
			Direction:          model.OTCDirectionSellInitiated,
			Ticker:             "TSLA",
			Quantity:           decimal.NewFromInt(2),
			StrikePrice:        decimal.NewFromInt(700),
			Premium:            decimal.NewFromInt(12),
			SettlementDate:     now,
			CreatedAt:          now,
			Status:             model.OTCOfferStatusOpen,
		},
	}}

	h := (&PeerOTCGRPCHandler{ownRouting: 111}).WithOTCOfferReader(reader, nil)

	resp, err := h.GetPublicOptionOffers(context.Background(), &stockpb.GetPublicOptionOffersRequest{})
	if err != nil {
		t.Fatalf("GetPublicOptionOffers: %v", err)
	}

	// Offer 2 (no acting employee) is skipped → 2 rows remain.
	if got := len(resp.GetOffers()); got != 2 {
		t.Fatalf("expected 2 published offers (offer 2 skipped), got %d", got)
	}

	byOfferID := map[string]*stockpb.PeerPublicOptionOffer{}
	for _, row := range resp.GetOffers() {
		byOfferID[row.GetOfferId().GetId()] = row
		// Hard invariant: "bank" must never reach the wire as a seller id.
		if row.GetSellerId().GetId() == "bank" {
			t.Errorf("offer %s emitted literal \"bank\" as sellerId.id", row.GetOfferId().GetId())
		}
		if row.GetLastModifiedBy().GetId() == "bank" {
			t.Errorf("offer %s emitted literal \"bank\" as lastModifiedBy.id", row.GetOfferId().GetId())
		}
	}

	if row, ok := byOfferID["1"]; !ok {
		t.Errorf("bank offer (id 1) with acting employee missing from list")
	} else if row.GetSellerId().GetId() != "employee-17" {
		t.Errorf("offer 1 sellerId.id = %q, want employee-17", row.GetSellerId().GetId())
	}

	if _, ok := byOfferID["2"]; ok {
		t.Errorf("bank offer (id 2) with nil acting employee must be ABSENT from the published list")
	}

	if row, ok := byOfferID["3"]; !ok {
		t.Errorf("client offer (id 3) missing from list")
	} else if row.GetSellerId().GetId() != "client-9" {
		t.Errorf("offer 3 sellerId.id = %q, want client-9", row.GetSellerId().GetId())
	}
}
