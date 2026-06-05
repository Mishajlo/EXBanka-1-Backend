package handler

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	stockpb "github.com/exbanka/contract/stockpb"
	"github.com/exbanka/stock-service/internal/model"
	"github.com/exbanka/stock-service/internal/repository"
)

// newUnifiedContractFixture builds an OTCOptionsHandler over the standard
// sqlite fixture, additionally wiring the peer-contracts repo (so remote
// contracts merge into ListMyContracts / resolve in GetContract) and own
// routing / bank-code provenance the same way main.go does.
func newUnifiedContractFixture(t *testing.T, ownRouting int64, ownBankCode string) (*OTCOptionsHandler, *otcOptionsHandlerFixture) {
	t.Helper()
	fx := newOTCOptionsHandlerFixture(t)
	peerRepo := repository.NewPeerOptionContractRepository(fx.db)
	h := fx.h.
		WithPeerContracts(peerRepo, ownRouting).
		WithRemoteOffers(nil, ownBankCode)
	return h, fx
}

// seedLocalContract inserts a LOCAL OptionContract with the given buyer/seller
// owners. A nil owner id means the bank side.
func seedLocalContract(t *testing.T, fx *otcOptionsHandlerFixture, buyerID, sellerID *uint64) *model.OptionContract {
	t.Helper()
	buyerType := model.OwnerClient
	if buyerID == nil {
		buyerType = model.OwnerBank
	}
	sellerType := model.OwnerClient
	if sellerID == nil {
		sellerType = model.OwnerBank
	}
	c := &model.OptionContract{
		StockID:         42,
		Ticker:          "ACME",
		Quantity:        decimal.NewFromInt(10),
		StrikePrice:     decimal.NewFromInt(150),
		PremiumPaid:     decimal.NewFromInt(20),
		PremiumCurrency: "USD",
		StrikeCurrency:  "USD",
		SettlementDate:  time.Now().Add(30 * 24 * time.Hour),
		Status:          model.OptionContractStatusActive,
		BuyerOwnerType:  buyerType, BuyerOwnerID: buyerID,
		SellerOwnerType: sellerType, SellerOwnerID: sellerID,
		PremiumPaidAt: time.Now(),
	}
	if err := fx.contracts.Create(c); err != nil {
		t.Fatalf("seed local contract: %v", err)
	}
	return c
}

// seedPeerContract inserts a cross-bank PeerOptionContract row.
func seedPeerContract(t *testing.T, fx *otcOptionsHandlerFixture, p *model.PeerOptionContract) {
	t.Helper()
	if err := fx.db.Create(p).Error; err != nil {
		t.Fatalf("seed peer contract: %v", err)
	}
}

// ---------------- ListMyContracts: local me_owner ----------------

// Caller is the contract's BUYER/HOLDER → me_owner=true (the formed option is
// the buyer's owned asset).
func TestListMyContracts_LocalBuyerIsOwner(t *testing.T) {
	const ownRouting int64 = 111
	h, fx := newUnifiedContractFixture(t, ownRouting, "111")
	buyer := uint64(7)
	seedLocalContract(t, fx, &buyer, nil) // seller = bank

	resp, err := h.ListMyContracts(context.Background(), &stockpb.ListMyContractsRequest{
		ActorUserId: 7, ActorSystemType: "client", Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(resp.GetContracts()) != 1 {
		t.Fatalf("want 1 contract, got %d", len(resp.GetContracts()))
	}
	got := resp.GetContracts()[0]
	if got.GetKind() != "local" {
		t.Errorf("kind = %q want local", got.GetKind())
	}
	if got.GetRoutingNumber() != ownRouting {
		t.Errorf("routing_number = %d want %d", got.GetRoutingNumber(), ownRouting)
	}
	if got.GetBankCode() != "111" {
		t.Errorf("bank_code = %q want 111", got.GetBankCode())
	}
	if !got.GetMeOwner() {
		t.Errorf("me_owner = false; caller is the BUYER/HOLDER, must be true")
	}
}

// Caller is the contract's SELLER/WRITER → me_owner=false.
func TestListMyContracts_LocalSellerNotOwner(t *testing.T) {
	const ownRouting int64 = 111
	h, fx := newUnifiedContractFixture(t, ownRouting, "111")
	seller := uint64(9)
	buyer := uint64(7)
	seedLocalContract(t, fx, &buyer, &seller)

	resp, err := h.ListMyContracts(context.Background(), &stockpb.ListMyContractsRequest{
		ActorUserId: 9, ActorSystemType: "client", Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(resp.GetContracts()) != 1 {
		t.Fatalf("want 1 contract, got %d", len(resp.GetContracts()))
	}
	got := resp.GetContracts()[0]
	if got.GetKind() != "local" {
		t.Errorf("kind = %q want local", got.GetKind())
	}
	if got.GetMeOwner() {
		t.Errorf("me_owner = true; caller is the SELLER/WRITER, must be false")
	}
}

// ---------------- ListMyContracts: remote me_owner ----------------

// A remote CREDIT row → this bank holds the BUYER side → kind=remote, surrogate
// id, me_owner=true, counterparty = seller's bank.
func TestListMyContracts_RemoteCreditWeHoldBuyer(t *testing.T) {
	const ownRouting int64 = 111
	const peerSellerRouting int64 = 222
	h, fx := newUnifiedContractFixture(t, ownRouting, "111")
	seedPeerContract(t, fx, &model.PeerOptionContract{
		ID:                 55,
		CrossbankTxID:      "tx-1",
		PostingIndex:       0,
		NegotiationID:      "neg-1",
		BuyerRoutingNumber: ownRouting, BuyerID: "client-7",
		SellerRoutingNumber: peerSellerRouting, SellerID: "client-3",
		Ticker:         "ACME",
		Quantity:       5,
		StrikePrice:    decimal.NewFromInt(200),
		Currency:       "USD",
		SettlementDate: "2030-01-01",
		Direction:      "CREDIT",
		Status:         "active",
		CreatedAt:      time.Now(),
	})

	resp, err := h.ListMyContracts(context.Background(), &stockpb.ListMyContractsRequest{
		ActorUserId: 7, ActorSystemType: "client", Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(resp.GetContracts()) != 1 {
		t.Fatalf("want 1 contract, got %d", len(resp.GetContracts()))
	}
	got := resp.GetContracts()[0]
	if got.GetKind() != "remote" {
		t.Errorf("kind = %q want remote", got.GetKind())
	}
	if got.GetId() != 55 {
		t.Errorf("id = %d want 55 (peer surrogate id)", got.GetId())
	}
	if !got.GetMeOwner() {
		t.Errorf("me_owner = false; CREDIT row holds the BUYER, must be true")
	}
	if got.GetRoutingNumber() != peerSellerRouting {
		t.Errorf("routing_number = %d want %d (counterparty seller bank)", got.GetRoutingNumber(), peerSellerRouting)
	}
	if got.GetBankCode() != "222" {
		t.Errorf("bank_code = %q want 222 (counterparty routing as code)", got.GetBankCode())
	}
	if got.GetQuantity() != "5" {
		t.Errorf("quantity = %q want 5", got.GetQuantity())
	}
	if got.GetStrikePrice() != "200" {
		t.Errorf("strike_price = %q want 200", got.GetStrikePrice())
	}
	if got.GetStockTicker() != "ACME" {
		t.Errorf("stock_ticker = %q want ACME", got.GetStockTicker())
	}
	// PeerContracts/PeerTotal are no longer populated (SP-1 double-listing fix).
	// Remote contracts appear only in the unified Contracts[] with kind=remote.
	if resp.GetPeerTotal() != 0 || len(resp.GetPeerContracts()) != 0 {
		t.Errorf("peer_contracts must be empty (remote rows already in contracts[]): total=%d len=%d", resp.GetPeerTotal(), len(resp.GetPeerContracts()))
	}
}

// A remote DEBIT row → this bank holds the SELLER side → me_owner=false,
// counterparty = buyer's bank.
func TestListMyContracts_RemoteDebitWeHoldSeller(t *testing.T) {
	const ownRouting int64 = 111
	const peerBuyerRouting int64 = 333
	h, fx := newUnifiedContractFixture(t, ownRouting, "111")
	seedPeerContract(t, fx, &model.PeerOptionContract{
		ID:                 77,
		CrossbankTxID:      "tx-2",
		PostingIndex:       0,
		NegotiationID:      "neg-2",
		BuyerRoutingNumber: peerBuyerRouting, BuyerID: "client-9",
		SellerRoutingNumber: ownRouting, SellerID: "client-7",
		Ticker:         "ACME",
		Quantity:       5,
		StrikePrice:    decimal.NewFromInt(200),
		Currency:       "USD",
		SettlementDate: "2030-01-01",
		Direction:      "DEBIT",
		Status:         "active",
		CreatedAt:      time.Now(),
	})

	resp, err := h.ListMyContracts(context.Background(), &stockpb.ListMyContractsRequest{
		ActorUserId: 7, ActorSystemType: "client", Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(resp.GetContracts()) != 1 {
		t.Fatalf("want 1 contract, got %d", len(resp.GetContracts()))
	}
	got := resp.GetContracts()[0]
	if got.GetKind() != "remote" {
		t.Errorf("kind = %q want remote", got.GetKind())
	}
	if got.GetMeOwner() {
		t.Errorf("me_owner = true; DEBIT row holds the SELLER, must be false")
	}
	if got.GetRoutingNumber() != peerBuyerRouting {
		t.Errorf("routing_number = %d want %d (counterparty buyer bank)", got.GetRoutingNumber(), peerBuyerRouting)
	}
	if got.GetBankCode() != "333" {
		t.Errorf("bank_code = %q want 333 (counterparty routing as code)", got.GetBankCode())
	}
}

// A bank caller (employee acting as the bank) has no cross-bank client
// identity → no remote rows merged.
func TestListMyContracts_BankCallerSkipsRemote(t *testing.T) {
	const ownRouting int64 = 111
	h, fx := newUnifiedContractFixture(t, ownRouting, "111")
	seedPeerContract(t, fx, &model.PeerOptionContract{
		ID:                 55,
		CrossbankTxID:      "tx-1",
		PostingIndex:       0,
		NegotiationID:      "neg-1",
		BuyerRoutingNumber: ownRouting, BuyerID: "client-7",
		SellerRoutingNumber: 222, SellerID: "client-3",
		Ticker:         "ACME",
		Quantity:       5,
		StrikePrice:    decimal.NewFromInt(200),
		Currency:       "USD",
		SettlementDate: "2030-01-01",
		Direction:      "CREDIT",
		Status:         "active",
		CreatedAt:      time.Now(),
	})

	resp, err := h.ListMyContracts(context.Background(), &stockpb.ListMyContractsRequest{
		ActorUserId: 0, ActorSystemType: "bank", Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, c := range resp.GetContracts() {
		if c.GetKind() == "remote" {
			t.Errorf("bank caller must not get remote contracts; saw %+v", c)
		}
	}
	if resp.GetPeerTotal() != 0 {
		t.Errorf("peer_total = %d want 0 (bank caller skips remote)", resp.GetPeerTotal())
	}
}

// ---------------- GetContract: remote resolve / miss / error ----------------

// A non-local id resolves to a remote peer contract (CREDIT → me_owner=true).
func TestGetContract_RemoteResolve_Credit(t *testing.T) {
	const ownRouting int64 = 111
	const peerSellerRouting int64 = 222
	h, fx := newUnifiedContractFixture(t, ownRouting, "111")
	seedPeerContract(t, fx, &model.PeerOptionContract{
		ID:                 900,
		CrossbankTxID:      "tx-9",
		PostingIndex:       0,
		NegotiationID:      "neg-9",
		BuyerRoutingNumber: ownRouting, BuyerID: "client-7",
		SellerRoutingNumber: peerSellerRouting, SellerID: "client-3",
		Ticker:         "ACME",
		Quantity:       5,
		StrikePrice:    decimal.NewFromInt(200),
		Currency:       "USD",
		SettlementDate: "2030-01-01",
		Direction:      "CREDIT",
		Status:         "active",
		CreatedAt:      time.Now(),
	})

	resp, err := h.GetContract(context.Background(), &stockpb.GetContractRequest{
		ContractId: 900, ActorUserId: 7, ActorSystemType: "client",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.GetKind() != "remote" {
		t.Errorf("kind = %q want remote", resp.GetKind())
	}
	if resp.GetId() != 900 {
		t.Errorf("id = %d want 900", resp.GetId())
	}
	if !resp.GetMeOwner() {
		t.Errorf("me_owner = false; CREDIT row, must be true")
	}
	if resp.GetRoutingNumber() != peerSellerRouting {
		t.Errorf("routing_number = %d want %d (counterparty seller)", resp.GetRoutingNumber(), peerSellerRouting)
	}
}

// Fix 1 (privacy): a remote contract whose local buyer is client 9, requested
// by client 5 → NotFound (existence must not leak to non-parties).
// Requested by client 9 (the actual local buyer) → returned successfully.
func TestGetContract_Remote_ParticipantCheck(t *testing.T) {
	const ownRouting int64 = 111
	const peerSellerRouting int64 = 222
	h, fx := newUnifiedContractFixture(t, ownRouting, "111")
	// CREDIT row → this bank holds the BUYER side (client-9).
	seedPeerContract(t, fx, &model.PeerOptionContract{
		ID:                 800,
		CrossbankTxID:      "tx-800",
		PostingIndex:       0,
		NegotiationID:      "neg-800",
		BuyerRoutingNumber: ownRouting, BuyerID: "client-9",
		SellerRoutingNumber: peerSellerRouting, SellerID: "client-3",
		Ticker:         "ACME",
		Quantity:       5,
		StrikePrice:    decimal.NewFromInt(200),
		Currency:       "USD",
		SettlementDate: "2030-01-01",
		Direction:      "CREDIT",
		Status:         "active",
		CreatedAt:      time.Now(),
	})

	// Non-participant (client 5) → NotFound (existence must not leak).
	_, err := h.GetContract(context.Background(), &stockpb.GetContractRequest{
		ContractId: 800, ActorUserId: 5, ActorSystemType: "client",
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("non-participant: expected NotFound, got %v", err)
	}

	// Actual local buyer (client 9) → success.
	resp, err := h.GetContract(context.Background(), &stockpb.GetContractRequest{
		ContractId: 800, ActorUserId: 9, ActorSystemType: "client",
	})
	if err != nil {
		t.Fatalf("participant: unexpected err: %v", err)
	}
	if resp.GetId() != 800 {
		t.Errorf("id = %d, want 800", resp.GetId())
	}
	if resp.GetKind() != "remote" {
		t.Errorf("kind = %q, want remote", resp.GetKind())
	}
	if !resp.GetMeOwner() {
		t.Errorf("me_owner = false; CREDIT row holds the BUYER, must be true")
	}
}

// Neither a local nor a remote contract exists → NotFound.
func TestGetContract_RemoteMiss_NotFound(t *testing.T) {
	const ownRouting int64 = 111
	h, _ := newUnifiedContractFixture(t, ownRouting, "111")

	_, err := h.GetContract(context.Background(), &stockpb.GetContractRequest{
		ContractId: 4242, ActorUserId: 7, ActorSystemType: "client",
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
}

// A non-NotFound error from the remote lookup surfaces as Internal — it is
// NEVER masked as a 404 (the GetOffer bug regression guard).
func TestGetContract_RemoteError_SurfacesInternal(t *testing.T) {
	const ownRouting int64 = 111
	// Build a fixture whose peer-contracts repo points at a DB that has NOT
	// migrated peer_option_contracts, so GetByID returns a non-NotFound DB
	// error ("no such table") rather than gorm.ErrRecordNotFound.
	fx := newOTCOptionsHandlerFixture(t)
	emptyDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open empty db: %v", err)
	}
	brokenPeerRepo := repository.NewPeerOptionContractRepository(emptyDB)
	h := fx.h.WithPeerContracts(brokenPeerRepo, ownRouting).WithRemoteOffers(nil, "111")

	_, gerr := h.GetContract(context.Background(), &stockpb.GetContractRequest{
		ContractId: 4242, ActorUserId: 7, ActorSystemType: "client",
	})
	if status.Code(gerr) != codes.Internal {
		t.Errorf("expected Internal (remote DB error must NOT be masked as NotFound), got %v", gerr)
	}
}
