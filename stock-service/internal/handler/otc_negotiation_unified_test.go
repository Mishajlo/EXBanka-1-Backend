package handler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	contractsitx "github.com/exbanka/contract/sitx"
	stockpb "github.com/exbanka/contract/stockpb"
	"github.com/exbanka/stock-service/internal/model"
	"github.com/exbanka/stock-service/internal/repository"
	"github.com/exbanka/stock-service/internal/service"
)

// fakePeerNegLister is an in-memory PeerNegotiationLister for the unified
// ListMyNegotiations merge tests. It returns whatever rows it was given,
// ignoring the role filter (the handler always passes "" today).
type fakePeerNegLister struct {
	rows []model.PeerOtcNegotiation
	err  error
}

func (f *fakePeerNegLister) ListByClient(_ int64, _ string, _ string) ([]model.PeerOtcNegotiation, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// newUnifiedNegFixture builds an OTCOptionsHandler backed by a sqlite
// negotiation repo (for LOCAL chains) plus a fake peer lister (for REMOTE
// chains), with own routing/bank-code wired the same way main.go does.
func newUnifiedNegFixture(t *testing.T, ownRouting int64, ownBankCode string, peer PeerNegotiationLister) (*OTCOptionsHandler, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.OTCOffer{}, &model.OTCNegotiation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	offerRepo := repository.NewOTCOfferRepository(db)
	negRepo := repository.NewOTCNegotiationRepository(db)
	negSvc := service.NewOTCNegotiationService(db, offerRepo, negRepo)

	h := NewOTCOptionsHandler(nil, nil).
		WithNegotiations(negSvc).
		// WithPeerContracts threads ownRouting; pass a nil peer-contracts
		// repo (unused on this path) just to set the routing number.
		WithPeerContracts(nil, ownRouting).
		WithRemoteOffers(nil, ownBankCode).
		WithPeerNegotiations(peer)
	return h, db
}

// seedBidderChain inserts a LOCAL negotiation row where the given client is
// the bidder (so ListMyNegotiations surfaces it as a bidder chain).
func seedBidderChain(t *testing.T, db *gorm.DB, bidderID uint64, parentOfferID uint64) uint64 {
	t.Helper()
	bid := bidderID
	neg := &model.OTCNegotiation{
		ParentOfferID:             parentOfferID,
		BidderOwnerType:           model.OwnerClient,
		BidderOwnerID:             &bid,
		BidderAccountID:           9001,
		Quantity:                  decimal.NewFromInt(10),
		StrikePrice:               decimal.NewFromInt(150),
		Premium:                   decimal.NewFromInt(20),
		SettlementDate:            time.Now().AddDate(0, 0, 30),
		Status:                    model.OTCNegotiationStatusOpen,
		LastActionByPrincipalType: "client",
		LastActionByPrincipalID:   bidderID,
		LastActionByOwnerType:     "client",
		LastActionByOwnerID:       &bid,
		LastActionAt:              time.Now(),
		CreatedAt:                 time.Now(),
		UpdatedAt:                 time.Now(),
	}
	if err := db.Create(neg).Error; err != nil {
		t.Fatalf("seed bidder chain: %v", err)
	}
	return neg.ID
}

func peerRow(id uint64, buyerRouting int64, buyerID string, sellerRouting int64, sellerID, status string) model.PeerOtcNegotiation {
	offer := contractsitx.OtcOffer{
		Ticker:         "ACME",
		Amount:         5,
		PricePerStock:  decimal.NewFromInt(200),
		Premium:        decimal.NewFromInt(15),
		SettlementDate: "2030-01-01",
	}
	js, _ := json.Marshal(offer)
	return model.PeerOtcNegotiation{
		ID:                  id,
		PeerBankCode:        "222",
		ForeignID:           "neg-uuid",
		BuyerRoutingNumber:  buyerRouting,
		BuyerID:             buyerID,
		SellerRoutingNumber: sellerRouting,
		SellerID:            sellerID,
		OfferJSON:           string(js),
		Status:              status,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}
}

// TestListMyNegotiations_LocalBidderChain: a local chain the caller opened
// as the bidder → kind="local", me_owner=false, own provenance stamped.
func TestListMyNegotiations_LocalBidderChain(t *testing.T) {
	const ownRouting int64 = 111
	h, db := newUnifiedNegFixture(t, ownRouting, "111", &fakePeerNegLister{})
	seedBidderChain(t, db, 7, 100)

	resp, err := h.ListMyNegotiations(context.Background(), &stockpb.ListMyNegotiationsRequest{
		OwnerType: "client", OwnerId: 7,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(resp.GetNegotiations()) != 1 {
		t.Fatalf("want 1 negotiation, got %d", len(resp.GetNegotiations()))
	}
	got := resp.GetNegotiations()[0]
	if got.GetKind() != "local" {
		t.Errorf("kind = %q want local", got.GetKind())
	}
	if got.GetRoutingNumber() != ownRouting {
		t.Errorf("routing_number = %d want %d", got.GetRoutingNumber(), ownRouting)
	}
	if got.GetBankCode() != "111" {
		t.Errorf("bank_code = %q want 111", got.GetBankCode())
	}
	if got.GetMeOwner() {
		t.Errorf("me_owner = true; a bidder is NOT an owner")
	}
}

// TestListMyNegotiations_RemoteWeHostBuyer: a remote peer negotiation where
// WE host the buyer (our routing == buyer routing) → kind="remote",
// me_owner=false, surrogate id = peer row id, counterparty = seller bank.
func TestListMyNegotiations_RemoteWeHostBuyer(t *testing.T) {
	const ownRouting int64 = 111
	const peerSellerRouting int64 = 222
	peer := &fakePeerNegLister{rows: []model.PeerOtcNegotiation{
		peerRow(55, ownRouting, "client-7", peerSellerRouting, "client-3", "ongoing"),
	}}
	h, _ := newUnifiedNegFixture(t, ownRouting, "111", peer)

	resp, err := h.ListMyNegotiations(context.Background(), &stockpb.ListMyNegotiationsRequest{
		OwnerType: "client", OwnerId: 7,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(resp.GetNegotiations()) != 1 {
		t.Fatalf("want 1 negotiation, got %d", len(resp.GetNegotiations()))
	}
	got := resp.GetNegotiations()[0]
	if got.GetKind() != "remote" {
		t.Errorf("kind = %q want remote", got.GetKind())
	}
	if got.GetId() != 55 {
		t.Errorf("id = %d want 55 (peer surrogate id)", got.GetId())
	}
	if got.GetMeOwner() {
		t.Errorf("me_owner = true; we host the BUYER, not the seller")
	}
	if got.GetRoutingNumber() != peerSellerRouting {
		t.Errorf("routing_number = %d want %d (counterparty seller bank)", got.GetRoutingNumber(), peerSellerRouting)
	}
	// Terms mapped from OfferJSON.
	if got.GetQuantity() != "5" {
		t.Errorf("quantity = %q want 5", got.GetQuantity())
	}
	if got.GetStrikePrice() != "200" {
		t.Errorf("strike_price = %q want 200", got.GetStrikePrice())
	}
	if got.GetStatus() != "ongoing" {
		t.Errorf("status = %q want ongoing", got.GetStatus())
	}
}

// TestListMyNegotiations_RemoteWeHostSeller: a remote peer negotiation where
// WE host the seller/poster (our routing == seller routing) → me_owner=true,
// counterparty = buyer bank.
func TestListMyNegotiations_RemoteWeHostSeller(t *testing.T) {
	const ownRouting int64 = 111
	const peerBuyerRouting int64 = 333
	peer := &fakePeerNegLister{rows: []model.PeerOtcNegotiation{
		peerRow(77, peerBuyerRouting, "client-9", ownRouting, "client-7", "ongoing"),
	}}
	h, _ := newUnifiedNegFixture(t, ownRouting, "111", peer)

	resp, err := h.ListMyNegotiations(context.Background(), &stockpb.ListMyNegotiationsRequest{
		OwnerType: "client", OwnerId: 7,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(resp.GetNegotiations()) != 1 {
		t.Fatalf("want 1 negotiation, got %d", len(resp.GetNegotiations()))
	}
	got := resp.GetNegotiations()[0]
	if got.GetKind() != "remote" {
		t.Errorf("kind = %q want remote", got.GetKind())
	}
	if !got.GetMeOwner() {
		t.Errorf("me_owner = false; we host the SELLER/poster, so it must be true")
	}
	if got.GetRoutingNumber() != peerBuyerRouting {
		t.Errorf("routing_number = %d want %d (counterparty buyer bank)", got.GetRoutingNumber(), peerBuyerRouting)
	}
}

// TestListMyNegotiations_MergesLocalAndRemote: both a local bidder chain and
// a remote peer chain are returned in one list, each with its own kind.
func TestListMyNegotiations_MergesLocalAndRemote(t *testing.T) {
	const ownRouting int64 = 111
	peer := &fakePeerNegLister{rows: []model.PeerOtcNegotiation{
		peerRow(55, ownRouting, "client-7", 222, "client-3", "ongoing"),
	}}
	h, db := newUnifiedNegFixture(t, ownRouting, "111", peer)
	seedBidderChain(t, db, 7, 100)

	resp, err := h.ListMyNegotiations(context.Background(), &stockpb.ListMyNegotiationsRequest{
		OwnerType: "client", OwnerId: 7,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(resp.GetNegotiations()) != 2 {
		t.Fatalf("want 2 merged negotiations, got %d", len(resp.GetNegotiations()))
	}
	var sawLocal, sawRemote bool
	for _, n := range resp.GetNegotiations() {
		switch n.GetKind() {
		case "local":
			sawLocal = true
		case "remote":
			sawRemote = true
		}
	}
	if !sawLocal || !sawRemote {
		t.Errorf("merged list missing a kind: local=%v remote=%v", sawLocal, sawRemote)
	}
}

// TestListMyNegotiations_RemoteStatusFilter: a status filter that excludes
// the remote row's status drops it from the merged list (remote rows honor
// the same status filter as local ones).
func TestListMyNegotiations_RemoteStatusFilter(t *testing.T) {
	const ownRouting int64 = 111
	peer := &fakePeerNegLister{rows: []model.PeerOtcNegotiation{
		peerRow(55, ownRouting, "client-7", 222, "client-3", "ongoing"),
	}}
	h, _ := newUnifiedNegFixture(t, ownRouting, "111", peer)

	resp, err := h.ListMyNegotiations(context.Background(), &stockpb.ListMyNegotiationsRequest{
		OwnerType: "client", OwnerId: 7,
		Statuses: []string{"accepted"}, // excludes "ongoing"
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(resp.GetNegotiations()) != 0 {
		t.Fatalf("want 0 (ongoing filtered out), got %d", len(resp.GetNegotiations()))
	}
}

// TestListMyNegotiations_BankCallerSkipsRemote: an employee acting as the
// bank has no cross-bank client identity, so no remote rows are merged.
func TestListMyNegotiations_BankCallerSkipsRemote(t *testing.T) {
	const ownRouting int64 = 111
	peer := &fakePeerNegLister{rows: []model.PeerOtcNegotiation{
		peerRow(55, ownRouting, "client-7", 222, "client-3", "ongoing"),
	}}
	h, _ := newUnifiedNegFixture(t, ownRouting, "111", peer)

	resp, err := h.ListMyNegotiations(context.Background(), &stockpb.ListMyNegotiationsRequest{
		OwnerType: "bank",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(resp.GetNegotiations()) != 0 {
		t.Fatalf("want 0 (bank caller has no cross-bank identity), got %d", len(resp.GetNegotiations()))
	}
}
