// Package handler — cross-bank (REMOTE) bid dispatch tests for OpenNegotiation
// (Unified OTC SP-2b). The bid route dispatches local vs cross-bank in
// stock-service based on whether the parent :id is a local or a folded-in
// remote OTCOffer. These tests exercise the REMOTE branch:
//   - a client bid on a remote listing dispatches to the (fake) peer and
//     records a remote OTCNegotiation mirror row,
//   - an account-currency mismatch is rejected,
//   - a bank caller is rejected (SP-3 deferral).
package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	accountpb "github.com/exbanka/contract/accountpb"
	stockpb "github.com/exbanka/contract/stockpb"
	"github.com/exbanka/stock-service/internal/model"
	"github.com/exbanka/stock-service/internal/repository"
	"github.com/exbanka/stock-service/internal/service"
)

// fakeOTCAccountClient implements OTCAccountClient: returns a single canned
// account for any GetAccount call.
type fakeOTCAccountClient struct {
	acct *accountpb.AccountResponse
	err  error
}

func (f *fakeOTCAccountClient) GetAccount(_ context.Context, _ *accountpb.GetAccountRequest, _ ...grpc.CallOption) (*accountpb.AccountResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.acct, nil
}

// fakePeerDispatcher implements PeerNegotiationDispatcher: records the args it
// was called with and returns a canned (routing, foreignID) or an error.
type fakePeerDispatcher struct {
	gotPeerBankCode string
	gotOffer        map[string]any
	calls           int
	routing         int64
	foreignID       string
	err             error
}

func (f *fakePeerDispatcher) CreateNegotiation(_ context.Context, peerBankCode string, offer map[string]any) (int64, string, error) {
	f.calls++
	f.gotPeerBankCode = peerBankCode
	f.gotOffer = offer
	if f.err != nil {
		return 0, "", f.err
	}
	return f.routing, f.foreignID, nil
}

// newRemoteBidFixture builds an OTCOptionsHandler wired for SP-2b remote bid
// dispatch: a sqlite-backed negotiation service (so the LOCAL path reports
// NotFound for a remote parent), the remote-offer getter, the peer dispatcher,
// the remote-neg writer, and the account client. OwnRouting is set to 111;
// remote rows use peer routing 222.
func newRemoteBidFixture(t *testing.T, dispatcher PeerNegotiationDispatcher, accounts OTCAccountClient) (*OTCOptionsHandler, *gorm.DB) {
	t.Helper()
	model.SetOwnRouting("111")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.OTCOffer{}, &model.OTCNegotiation{}, &model.OTCNegotiationRevision{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	offerRepo := repository.NewOTCOfferRepository(db)
	negRepo := repository.NewOTCNegotiationRepository(db)
	negSvc := service.NewOTCNegotiationService(db, offerRepo, negRepo)

	h := NewOTCOptionsHandler(nil, nil).
		WithNegotiations(negSvc).
		WithPeerContracts(nil, 111). // sets ownRouting=111
		WithRemoteOffers(offerRepo, "111").
		WithPeerOTCDispatch(dispatcher, negRepo, accounts)
	return h, db
}

// seedRemoteOffer inserts a folded-in remote OTCOffer row (peer routing 222)
// and returns its surrogate id.
func seedRemoteOffer(t *testing.T, db *gorm.DB) uint64 {
	t.Helper()
	nid := "peer-offer-1"
	bankCode := "222"
	sellerID := "client-9"
	strikeCcy := "USD"
	premiumCcy := "USD"
	o := &model.OTCOffer{
		RoutingNumber:               222,
		NativeID:                    &nid,
		InitiatorBankCode:           &bankCode,
		RemoteSellerID:              &sellerID,
		InitiatorOwnerType:          model.OwnerBank,
		Direction:                   model.OTCDirectionSellInitiated,
		Ticker:                      "AAPL",
		Quantity:                    decimal.NewFromInt(10),
		StrikePrice:                 decimal.RequireFromString("150"),
		Premium:                     decimal.RequireFromString("20"),
		StrikeCurrency:              &strikeCcy,
		PremiumCurrency:             &premiumCcy,
		SettlementDate:              time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		Status:                      model.OTCOfferStatusOpen,
		LastModifiedByPrincipalType: "system",
		LastModifiedByPrincipalID:   0,
	}
	if err := db.Create(o).Error; err != nil {
		t.Fatalf("seed remote offer: %v", err)
	}
	return o.ID
}

func usdAccount(ownerID uint64) *accountpb.AccountResponse {
	return &accountpb.AccountResponse{
		Id:            5001,
		OwnerId:       ownerID,
		AccountNumber: "111-0000000001-22",
		CurrencyCode:  "USD",
		Status:        "active",
	}
}

func openReq(parentID, bidderID uint64, ownerType string) *stockpb.OpenNegotiationRequest {
	return &stockpb.OpenNegotiationRequest{
		ParentOfferId:       parentID,
		BidderOwnerType:     ownerType,
		BidderOwnerId:       bidderID,
		BidderAccountId:     5001,
		Quantity:            "10",
		StrikePrice:         "150",
		Premium:             "20",
		SettlementDate:      "2026-07-01",
		ActingPrincipalType: "client",
		ActingPrincipalId:   bidderID,
	}
}

func TestOpenNegotiation_RemoteClientBid_DispatchesAndRecordsMirror(t *testing.T) {
	dispatcher := &fakePeerDispatcher{routing: 222, foreignID: "neg-xyz"}
	accounts := &fakeOTCAccountClient{acct: usdAccount(9 /* matches bidder client-9 */)}
	h, db := newRemoteBidFixture(t, dispatcher, accounts)
	parentID := seedRemoteOffer(t, db)

	resp, err := h.OpenNegotiation(context.Background(), openReq(parentID, 9, "client"))
	if err != nil {
		t.Fatalf("OpenNegotiation: %v", err)
	}

	// Dispatched to peer 222 with the composed offer.
	if dispatcher.calls != 1 {
		t.Fatalf("dispatcher called %d times, want 1", dispatcher.calls)
	}
	if dispatcher.gotPeerBankCode != "222" {
		t.Errorf("peer bank code: got %q, want 222", dispatcher.gotPeerBankCode)
	}
	// Composed offer carries buyer/seller ids, account number, and the
	// parentOfferId lot key.
	stock, _ := dispatcher.gotOffer["stock"].(map[string]any)
	if stock == nil || stock["ticker"] != "AAPL" {
		t.Errorf("offer stock.ticker missing/wrong: %v", dispatcher.gotOffer["stock"])
	}
	if dispatcher.gotOffer["buyerAccountNumber"] != "111-0000000001-22" {
		t.Errorf("offer buyerAccountNumber: got %v", dispatcher.gotOffer["buyerAccountNumber"])
	}
	buyer, _ := dispatcher.gotOffer["buyerId"].(map[string]any)
	if buyer == nil || buyer["id"] != "client-9" {
		t.Errorf("offer buyerId.id: got %v", dispatcher.gotOffer["buyerId"])
	}
	seller, _ := dispatcher.gotOffer["sellerId"].(map[string]any)
	if seller == nil || seller["id"] != "client-9" /* remote seller id from seed */ {
		// seed used RemoteSellerID="client-9"
		t.Errorf("offer sellerId.id: got %v", dispatcher.gotOffer["sellerId"])
	}
	parent, _ := dispatcher.gotOffer["parentOfferId"].(map[string]any)
	if parent == nil || parent["id"] != "peer-offer-1" {
		t.Errorf("offer parentOfferId.id: got %v", dispatcher.gotOffer["parentOfferId"])
	}

	// A remote OTCNegotiation mirror row was recorded (routing 222, native_id neg-xyz).
	mirror, gerr := repository.NewOTCNegotiationRepository(db).GetRemoteNegByRoutingAndNative(222, "neg-xyz")
	if gerr != nil {
		t.Fatalf("expected remote mirror row: %v", gerr)
	}
	if mirror.Status != "ongoing" {
		t.Errorf("mirror status: got %q, want ongoing", mirror.Status)
	}
	if mirror.RemoteBuyerID == nil || *mirror.RemoteBuyerID != "client-9" {
		t.Errorf("mirror RemoteBuyerID: got %v", mirror.RemoteBuyerID)
	}
	if mirror.RemoteParentNativeID == nil || *mirror.RemoteParentNativeID != "peer-offer-1" {
		t.Errorf("mirror RemoteParentNativeID: got %v", mirror.RemoteParentNativeID)
	}

	// The unified response is kind=remote and carries the mirror surrogate id.
	if resp.GetKind() != "remote" {
		t.Errorf("response kind: got %q, want remote", resp.GetKind())
	}
	if resp.GetId() != mirror.ID {
		t.Errorf("response id: got %d, want mirror id %d", resp.GetId(), mirror.ID)
	}
}

func TestOpenNegotiation_RemoteBid_BadAccountCurrency(t *testing.T) {
	dispatcher := &fakePeerDispatcher{routing: 222, foreignID: "neg-xyz"}
	rsd := usdAccount(9)
	rsd.CurrencyCode = "RSD" // mismatch vs the listing's USD premium
	accounts := &fakeOTCAccountClient{acct: rsd}
	h, db := newRemoteBidFixture(t, dispatcher, accounts)
	parentID := seedRemoteOffer(t, db)

	_, err := h.OpenNegotiation(context.Background(), openReq(parentID, 9, "client"))
	if err == nil {
		t.Fatal("expected currency-mismatch error, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code: got %v, want InvalidArgument", status.Code(err))
	}
	if dispatcher.calls != 0 {
		t.Errorf("dispatcher should NOT be called on a bad account: %d", dispatcher.calls)
	}
}

func TestOpenNegotiation_RemoteBid_BankCallerUnsupported(t *testing.T) {
	dispatcher := &fakePeerDispatcher{routing: 222, foreignID: "neg-xyz"}
	accounts := &fakeOTCAccountClient{acct: usdAccount(9)}
	h, db := newRemoteBidFixture(t, dispatcher, accounts)
	parentID := seedRemoteOffer(t, db)

	// owner_type=bank → bidder_owner_id resolves to nil → SP-3 deferral.
	req := openReq(parentID, 0, "bank")
	_, err := h.OpenNegotiation(context.Background(), req)
	if err == nil {
		t.Fatal("expected FailedPrecondition for bank caller, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("code: got %v, want FailedPrecondition", status.Code(err))
	}
	if dispatcher.calls != 0 {
		t.Errorf("dispatcher should NOT be called for a bank bidder: %d", dispatcher.calls)
	}
}

func TestOpenNegotiation_NonexistentParent_StillNotFound(t *testing.T) {
	dispatcher := &fakePeerDispatcher{routing: 222, foreignID: "neg-xyz"}
	accounts := &fakeOTCAccountClient{acct: usdAccount(9)}
	h, _ := newRemoteBidFixture(t, dispatcher, accounts)

	// parent id 999 is neither local nor remote → surface the original NotFound.
	_, err := h.OpenNegotiation(context.Background(), openReq(999, 9, "client"))
	if err == nil {
		t.Fatal("expected NotFound for an unknown parent, got nil")
	}
	if dispatcher.calls != 0 {
		t.Errorf("dispatcher should NOT be called for an unknown parent: %d", dispatcher.calls)
	}
}

// TestOfferComposition_AmountsAreJSONNumbers verifies that the SI-TX OtcOffer
// composed by openRemoteNegotiation serialises pricePerUnit.amount and
// premium.amount as bare JSON numbers (e.g. 150.5, not "150.5").
// SI-TX §2.5 / §2.8.1 require MonetaryValue.amount to be a number token; a
// strict cohort peer will reject a quoted string amount.
func TestOfferComposition_AmountsAreJSONNumbers(t *testing.T) {
	dispatcher := &fakePeerDispatcher{routing: 222, foreignID: "neg-wire"}
	accounts := &fakeOTCAccountClient{acct: usdAccount(9)}
	h, db := newRemoteBidFixture(t, dispatcher, accounts)
	parentID := seedRemoteOffer(t, db)

	if _, err := h.OpenNegotiation(context.Background(), openReq(parentID, 9, "client")); err != nil {
		t.Fatalf("OpenNegotiation: %v", err)
	}
	if dispatcher.calls != 1 {
		t.Fatalf("expected 1 dispatch call, got %d", dispatcher.calls)
	}

	// Marshal the offer the dispatcher received — this is exactly what would
	// be sent over the wire to the peer bank.
	raw, err := json.Marshal(dispatcher.gotOffer)
	if err != nil {
		t.Fatalf("marshal dispatched offer: %v", err)
	}
	wire := string(raw)

	// The wire MUST contain bare numbers (e.g. "amount":150), NOT quoted
	// strings (e.g. "amount":"150"). Check for both fields.
	if strings.Contains(wire, `"amount":"`) {
		t.Errorf("wire contains quoted amount (string), want bare number; wire: %s", wire)
	}

	// Unmarshal and verify the numeric values are correct.
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	ppu, _ := parsed["pricePerUnit"].(map[string]any)
	if ppu == nil {
		t.Fatalf("pricePerUnit missing from wire")
	}
	// json.Unmarshal produces float64 for JSON numbers.
	ppuAmt, ok := ppu["amount"].(float64)
	if !ok {
		t.Errorf("pricePerUnit.amount: want float64 (bare number), got %T = %v", ppu["amount"], ppu["amount"])
	} else if ppuAmt != 150 {
		t.Errorf("pricePerUnit.amount: got %v, want 150", ppuAmt)
	}

	prem, _ := parsed["premium"].(map[string]any)
	if prem == nil {
		t.Fatalf("premium missing from wire")
	}
	premAmt, ok := prem["amount"].(float64)
	if !ok {
		t.Errorf("premium.amount: want float64 (bare number), got %T = %v", prem["amount"], prem["amount"])
	} else if premAmt != 20 {
		t.Errorf("premium.amount: got %v, want 20", premAmt)
	}
}
