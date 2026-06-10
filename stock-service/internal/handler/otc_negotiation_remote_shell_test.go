// Package handler — freshness guard tests for /public-stock shell offers.
// Shell offers (HasPresetTerms=false) are synthesized from a peer's /public-stock
// endpoint. Because the mirror can lag a refresh cycle, openRemoteNegotiation
// re-fetches the LIVE /public-stock before dispatching to avoid a doomed bid.
// These tests exercise that guard:
//   - stale listing (seller/ticker absent) → FailedPrecondition, no dispatch,
//   - live listing (seller/ticker present)  → guard passes, dispatch fires once.
package handler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	contractsitx "github.com/exbanka/contract/sitx"
	"github.com/exbanka/stock-service/internal/model"
)

// seedShellRemoteOffer inserts a shell OTCOffer row (HasPresetTerms=false,
// ticker AAPL, seller {222,"client-5"}). Used by the shell freshness tests.
func seedShellRemoteOffer(t *testing.T, db *gorm.DB) uint64 {
	t.Helper()
	nid := "shell-offer-1"
	bankCode := "222"
	sellerID := "client-5"
	strikeCcy := "USD"
	premiumCcy := "USD"
	o := &model.OTCOffer{
		RoutingNumber:               222,
		NativeID:                    &nid,
		InitiatorBankCode:           &bankCode,
		RemoteSellerID:              &sellerID,
		HasPresetTerms:              false, // shell: synthesized from /public-stock, no preset terms
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
		t.Fatalf("seed shell remote offer: %v", err)
	}
	// GORM skips false (zero-value bool) during Create when the field has a
	// `default:true` GORM tag, letting the DB default TRUE be applied. We must
	// explicitly patch it to 0 after the insert so the freshness guard fires.
	if err := db.Model(o).UpdateColumn("has_preset_terms", false).Error; err != nil {
		t.Fatalf("seed shell remote offer: patch has_preset_terms: %v", err)
	}
	return o.ID
}

// TestOpenNegotiation_ShellFreshness_BlocksWhenSellerGone asserts that a bid on a
// shell offer is rejected with FailedPrecondition (and no CreateNegotiation
// dispatch) when the peer's live /public-stock no longer lists the seller+ticker.
func TestOpenNegotiation_ShellFreshness_BlocksWhenSellerGone(t *testing.T) {
	dispatcher := &fakePeerDispatcher{
		routing:   222,
		foreignID: "neg-shell",
		proxyByKey: map[string]proxyResult{
			"GET /public-stock": {resp: []byte("[]"), status: 200},
		},
	}
	accounts := &fakeOTCAccountClient{acct: usdAccount(7)}
	h, db := newRemoteBidFixture(t, dispatcher, accounts)
	parentID := seedShellRemoteOffer(t, db)

	_, err := h.OpenNegotiation(context.Background(), openReq(parentID, 7, "client"))
	if err == nil {
		t.Fatal("expected FailedPrecondition from stale shell guard, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("code: got %v, want FailedPrecondition", status.Code(err))
	}
	// CreateNegotiation must NOT have fired (bid was blocked before dispatch).
	if dispatcher.calls != 0 {
		t.Errorf("CreateNegotiation calls: got %d, want 0", dispatcher.calls)
	}
}

// TestOpenNegotiation_ShellFreshness_PassesWhenSellerLive asserts that a bid on a
// shell offer proceeds (guard is a no-op) when the peer's live /public-stock still
// lists the seller+ticker, and that CreateNegotiation is dispatched exactly once.
func TestOpenNegotiation_ShellFreshness_PassesWhenSellerLive(t *testing.T) {
	// Build a /public-stock response that lists AAPL with seller "client-5" at routing 222.
	liveResp, _ := json.Marshal(contractsitx.PublicStocksResponse{
		{
			Stock: contractsitx.StockDescription{Ticker: "AAPL"},
			Sellers: []contractsitx.PublicSeller{
				{Seller: contractsitx.ForeignBankId{RoutingNumber: 222, ID: "client-5"}, Amount: 10},
			},
		},
	})
	dispatcher := &fakePeerDispatcher{
		routing:   222,
		foreignID: "neg-shell-live",
		proxyByKey: map[string]proxyResult{
			"GET /public-stock": {resp: liveResp, status: 200},
		},
	}
	// Bidder is client-7 with an active USD account (matches shell's premium currency).
	accounts := &fakeOTCAccountClient{acct: usdAccount(7)}
	h, db := newRemoteBidFixture(t, dispatcher, accounts)
	parentID := seedShellRemoteOffer(t, db)

	_, err := h.OpenNegotiation(context.Background(), openReq(parentID, 7, "client"))
	if err != nil {
		t.Fatalf("expected success with live seller, got: %v", err)
	}
	// Freshness guard passed → CreateNegotiation dispatched exactly once.
	if dispatcher.calls != 1 {
		t.Errorf("CreateNegotiation calls: got %d, want 1", dispatcher.calls)
	}
}
