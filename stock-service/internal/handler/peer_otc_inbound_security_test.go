package handler_test

import (
	"context"
	"encoding/json"
	"testing"

	contractsitx "github.com/exbanka/contract/sitx"
	stockpb "github.com/exbanka/contract/stockpb"
	"github.com/exbanka/stock-service/internal/model"
	"github.com/exbanka/stock-service/internal/repository"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeParentChecker is the LocalParentChecker stub for inbound orphan-accept
// tests: it reports whether a given local parent offer id is "open" from a
// fixed allow-set.
type fakeParentChecker struct {
	open  map[uint64]bool
	calls []uint64
}

func (f *fakeParentChecker) LocalParentIsOpen(offerID uint64) bool {
	f.calls = append(f.calls, offerID)
	return f.open[offerID]
}

// peerLastModifiedOffer builds an inbound PeerOtcOffer carrying the supplied
// lastModifiedBy routing/id (the field a malicious peer would forge).
func peerLastModifiedOffer(lmRouting int64, lmID string) *stockpb.PeerOtcOffer {
	return &stockpb.PeerOtcOffer{
		Ticker: "AAPL", Amount: 10,
		PricePerStock: "150", Currency: "USD",
		Premium: "20", PremiumCurrency: "USD",
		SettlementDate: "2026-12-31",
		LastModifiedBy: &stockpb.PeerForeignBankId{RoutingNumber: lmRouting, Id: lmID},
	}
}

// --- HOLE 1: forge-proof lastModifiedBy + authoritative accept guard ---

// TestInbound_CreateNegotiation_ForgedLastModifiedBy_Rejected: the inbound
// CreateNegotiation must reject an offer whose lastModifiedBy.routingNumber is
// not the authenticated peer's. A peer may only ever mark ITSELF as the last
// actor — otherwise it could forge lastModifiedBy={ownRouting,...} so the later
// accept guard treats the forged proposal as if WE proposed it.
func TestInbound_CreateNegotiation_ForgedLastModifiedBy_Rejected(t *testing.T) {
	h, db, _, _ := newPeerOtcHandler(t) // ownRouting 111

	_, err := h.CreateNegotiation(context.Background(), &stockpb.CreateNegotiationRequest{
		PeerBankCode: "222",
		BuyerId:      &stockpb.PeerForeignBankId{RoutingNumber: 222, Id: "client-7"},
		SellerId:     &stockpb.PeerForeignBankId{RoutingNumber: 111, Id: "client-9"},
		// FORGED: claims WE (111) last modified, though the peer (222) is posting.
		Offer: peerLastModifiedOffer(111, "client-9"),
	})
	if err == nil {
		t.Fatal("expected forged lastModifiedBy to be rejected on create, got nil")
	}
	if c := status.Code(err); c != codes.PermissionDenied && c != codes.InvalidArgument {
		t.Errorf("expected PermissionDenied/InvalidArgument, got %v", err)
	}
	var n int64
	db.Table("otc_negotiations").Count(&n)
	if n != 0 {
		t.Errorf("forged create must persist no row, got %d", n)
	}
}

// TestInbound_CreateNegotiation_HonestLastModifiedBy_Accepted: lastModifiedBy
// pointing at the authenticated peer (or absent — zero value) is accepted.
func TestInbound_CreateNegotiation_HonestLastModifiedBy_Accepted(t *testing.T) {
	h, db, _, _ := newPeerOtcHandler(t)

	// peer marks itself as last actor
	if _, err := h.CreateNegotiation(context.Background(), &stockpb.CreateNegotiationRequest{
		PeerBankCode: "222",
		BuyerId:      &stockpb.PeerForeignBankId{RoutingNumber: 222, Id: "client-7"},
		SellerId:     &stockpb.PeerForeignBankId{RoutingNumber: 111, Id: "client-9"},
		Offer:        peerLastModifiedOffer(222, "client-7"),
	}); err != nil {
		t.Fatalf("honest peer lastModifiedBy: %v", err)
	}
	// absent lastModifiedBy (zero value) is tolerated
	if _, err := h.CreateNegotiation(context.Background(), &stockpb.CreateNegotiationRequest{
		PeerBankCode: "222",
		BuyerId:      &stockpb.PeerForeignBankId{RoutingNumber: 222, Id: "client-7"},
		SellerId:     &stockpb.PeerForeignBankId{RoutingNumber: 111, Id: "client-9"},
		Offer: &stockpb.PeerOtcOffer{
			Ticker: "AAPL", Amount: 10, PricePerStock: "150", Currency: "USD",
			Premium: "20", PremiumCurrency: "USD", SettlementDate: "2026-12-31",
		},
	}); err != nil {
		t.Fatalf("absent lastModifiedBy: %v", err)
	}
	var n int64
	db.Table("otc_negotiations").Count(&n)
	if n != 2 {
		t.Errorf("expected 2 persisted rows, got %d", n)
	}
}

// TestInbound_UpdateNegotiation_ForgedLastModifiedBy_Rejected: the inbound
// counter (UpdateNegotiation) must likewise reject a forged lastModifiedBy.
func TestInbound_UpdateNegotiation_ForgedLastModifiedBy_Rejected(t *testing.T) {
	h, _, _, _ := newPeerOtcHandler(t)
	ctx := context.Background()

	createResp, err := h.CreateNegotiation(ctx, &stockpb.CreateNegotiationRequest{
		PeerBankCode: "222",
		BuyerId:      &stockpb.PeerForeignBankId{RoutingNumber: 222, Id: "client-7"},
		SellerId:     &stockpb.PeerForeignBankId{RoutingNumber: 111, Id: "client-9"},
		Offer:        peerLastModifiedOffer(222, "client-7"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err = h.UpdateNegotiation(ctx, &stockpb.UpdateNegotiationRequest{
		PeerBankCode:  "222",
		NegotiationId: createResp.GetNegotiationId(),
		// FORGED counter: claims WE (111) last modified.
		Offer: peerLastModifiedOffer(111, "client-9"),
	})
	if err == nil {
		t.Fatal("expected forged lastModifiedBy to be rejected on counter, got nil")
	}
	if c := status.Code(err); c != codes.PermissionDenied && c != codes.InvalidArgument {
		t.Errorf("expected PermissionDenied/InvalidArgument, got %v", err)
	}
}

// TestInbound_AcceptNegotiation_PeerAcceptsOwnTerms_Rejected is the core
// forged-self-accept repro: even if a forged counter slipped through, the
// authoritative accept guard rejects unless the LOCAL side last proposed.
// Here the stored lastModifiedBy is the peer (222) — the peer cannot accept its
// own terms; NO settlement SI-TX may be dispatched.
func TestInbound_AcceptNegotiation_PeerAcceptsOwnTerms_Rejected(t *testing.T) {
	h, db, peerTx, _ := newPeerOtcHandler(t) // ownRouting 111

	// Seed a remote chain directly (peer is the last proposer).
	offer := contractsitx.OtcOffer{
		Ticker: "AAPL", Amount: 10,
		PricePerStock:   decimal.RequireFromString("150"),
		Currency:        "USD",
		Premium:         decimal.RequireFromString("20"),
		PremiumCurrency: "USD",
		SettlementDate:  "2026-12-31",
		LastModifiedBy:  contractsitx.ForeignBankId{RoutingNumber: 222, ID: "client-7"},
	}
	offerJSON, _ := json.Marshal(offer)
	repo := repository.NewOTCNegotiationRepository(db)
	row := buildRemoteNegForTest(222, "neg-peer-self", offer, string(offerJSON),
		222, "client-7", 111, "client-9")
	if err := repo.UpsertRemoteNeg(row); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := h.AcceptNegotiation(context.Background(), &stockpb.AcceptNegotiationRequest{
		PeerBankCode:  "222",
		NegotiationId: &stockpb.PeerForeignBankId{RoutingNumber: 111, Id: "neg-peer-self"},
	})
	if err == nil {
		t.Fatal("expected peer-accepts-own-terms to be rejected, got nil (self-accept loophole)")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", err)
	}
	if peerTx.gotReq != nil {
		t.Errorf("self-accept must NOT dispatch a settlement SI-TX; got %+v", peerTx.gotReq)
	}
	after, _ := repo.GetRemoteNegByRoutingAndNative(222, "neg-peer-self")
	if after.Status != "ongoing" {
		t.Errorf("status after rejected self-accept: got %q, want ongoing", after.Status)
	}
}

// TestInbound_AcceptNegotiation_LegitOppositePartyAccept_Succeeds: the legit
// flow — peer bids, WE (local) counter (lastModifiedBy=ownRouting), peer
// accepts → succeeds and dispatches the 4-posting settlement.
func TestInbound_AcceptNegotiation_LegitOppositePartyAccept_Succeeds(t *testing.T) {
	h, db, peerTx, _ := newPeerOtcHandler(t) // ownRouting 111

	// WE (111) last proposed → peer (222) accepting is the legit counterparty.
	offer := contractsitx.OtcOffer{
		Ticker: "AAPL", Amount: 10,
		PricePerStock:   decimal.RequireFromString("150"),
		Currency:        "USD",
		Premium:         decimal.RequireFromString("20"),
		PremiumCurrency: "USD",
		SettlementDate:  "2026-12-31",
		LastModifiedBy:  contractsitx.ForeignBankId{RoutingNumber: 111, ID: "client-9"},
	}
	offerJSON, _ := json.Marshal(offer)
	repo := repository.NewOTCNegotiationRepository(db)
	row := buildRemoteNegForTest(222, "neg-legit", offer, string(offerJSON),
		222, "client-7", 111, "client-9")
	if err := repo.UpsertRemoteNeg(row); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, err := h.AcceptNegotiation(context.Background(), &stockpb.AcceptNegotiationRequest{
		PeerBankCode:  "222",
		NegotiationId: &stockpb.PeerForeignBankId{RoutingNumber: 111, Id: "neg-legit"},
	})
	if err != nil {
		t.Fatalf("legit opposite-party accept: %v", err)
	}
	if peerTx.gotReq == nil {
		t.Fatal("legit accept must dispatch the settlement SI-TX")
	}
	if len(peerTx.gotReq.GetPostings()) != 4 {
		t.Errorf("expected 4 postings, got %d", len(peerTx.gotReq.GetPostings()))
	}
	if resp.GetTransactionId() == "" {
		t.Error("expected a transaction id on success")
	}
	after, _ := repo.GetRemoteNegByRoutingAndNative(222, "neg-legit")
	if after.Status != "accepted" {
		t.Errorf("status after legit accept: got %q, want accepted", after.Status)
	}
}

// --- HOLE 2: inbound orphan-accept (cancelled local parent listing) ---

// TestInbound_AcceptNegotiation_OrphanCancelledParent_Rejected: WE host the
// listing (remote_parent_routing == ownRouting). An inbound accept against a
// child of a CANCELLED local listing must be rejected (FailedPrecondition),
// authoritatively (regardless of cascade timing), with NO settlement dispatched.
func TestInbound_AcceptNegotiation_OrphanCancelledParent_Rejected(t *testing.T) {
	h, db, peerTx, _ := newPeerOtcHandler(t) // ownRouting 111
	parentChecker := &fakeParentChecker{open: map[uint64]bool{}} // parent 42 NOT open
	h = h.WithParentChecker(parentChecker)

	// Remote child chain: WE host the seller (client-9@111); buyer on peer 222.
	// lastModifiedBy = the LOCAL side (111) so the self-accept guard PASSES (the
	// peer is the legit counterparty) — the ONLY thing that should block is the
	// cancelled local parent listing.
	offer := contractsitx.OtcOffer{
		Ticker: "AAPL", Amount: 10,
		PricePerStock:   decimal.RequireFromString("150"),
		Currency:        "USD",
		Premium:         decimal.RequireFromString("20"),
		PremiumCurrency: "USD",
		SettlementDate:  "2026-12-31",
		LastModifiedBy:  contractsitx.ForeignBankId{RoutingNumber: 111, ID: "client-9"},
	}
	offerJSON, _ := json.Marshal(offer)
	repo := repository.NewOTCNegotiationRepository(db)
	pr := int64(111)
	pn := "42"
	row := buildRemoteNegForTest(222, "neg-orphan-in", offer, string(offerJSON),
		222, "client-3", 111, "client-9")
	row.RemoteParentRouting = &pr
	row.RemoteParentNativeID = &pn
	if err := repo.UpsertRemoteNeg(row); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := h.AcceptNegotiation(context.Background(), &stockpb.AcceptNegotiationRequest{
		PeerBankCode:  "222",
		NegotiationId: &stockpb.PeerForeignBankId{RoutingNumber: 111, Id: "neg-orphan-in"},
	})
	if err == nil {
		t.Fatal("expected inbound accept on a cancelled-parent chain to be rejected, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("expected FailedPrecondition, got %v", err)
	}
	if peerTx.gotReq != nil {
		t.Errorf("orphan accept must NOT dispatch settlement; got %+v", peerTx.gotReq)
	}
	after, _ := repo.GetRemoteNegByRoutingAndNative(222, "neg-orphan-in")
	if after.Status != "ongoing" {
		t.Errorf("status after rejected orphan accept: got %q, want ongoing", after.Status)
	}
}

// TestInbound_AcceptNegotiation_OpenParent_Succeeds: when the local parent
// listing is still open, the inbound accept proceeds.
func TestInbound_AcceptNegotiation_OpenParent_Succeeds(t *testing.T) {
	h, db, peerTx, _ := newPeerOtcHandler(t) // ownRouting 111
	parentChecker := &fakeParentChecker{open: map[uint64]bool{42: true}}
	h = h.WithParentChecker(parentChecker)

	offer := contractsitx.OtcOffer{
		Ticker: "AAPL", Amount: 10,
		PricePerStock:   decimal.RequireFromString("150"),
		Currency:        "USD",
		Premium:         decimal.RequireFromString("20"),
		PremiumCurrency: "USD",
		SettlementDate:  "2026-12-31",
		LastModifiedBy:  contractsitx.ForeignBankId{RoutingNumber: 111, ID: "client-9"},
	}
	offerJSON, _ := json.Marshal(offer)
	repo := repository.NewOTCNegotiationRepository(db)
	pr := int64(111)
	pn := "42"
	row := buildRemoteNegForTest(222, "neg-open-parent", offer, string(offerJSON),
		222, "client-3", 111, "client-9")
	row.RemoteParentRouting = &pr
	row.RemoteParentNativeID = &pn
	if err := repo.UpsertRemoteNeg(row); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := h.AcceptNegotiation(context.Background(), &stockpb.AcceptNegotiationRequest{
		PeerBankCode:  "222",
		NegotiationId: &stockpb.PeerForeignBankId{RoutingNumber: 111, Id: "neg-open-parent"},
	}); err != nil {
		t.Fatalf("open-parent accept: %v", err)
	}
	if peerTx.gotReq == nil {
		t.Error("open-parent accept must dispatch settlement")
	}
}

// --- HOLE 3: well-formed local seller id ---

// TestInbound_CreateNegotiation_BogusEmployeeSeller_Rejected: an employee-<garbage>
// seller (not ^employee-\d+$) must be rejected — the seller is OURS and must be a
// resolvable local participant. No row may persist.
func TestInbound_CreateNegotiation_BogusEmployeeSeller_Rejected(t *testing.T) {
	for _, bogus := range []string{"employee-abc", "employee-", "employee-1x", "garbage", ""} {
		h, db, _, _ := newPeerOtcHandler(t)
		val := &fakeLocalSellerValidator{exists: map[string]bool{"client-9": true}}
		h = h.WithSellerValidator(val)

		_, err := h.CreateNegotiation(context.Background(), &stockpb.CreateNegotiationRequest{
			PeerBankCode: "222",
			BuyerId:      &stockpb.PeerForeignBankId{RoutingNumber: 222, Id: "client-7"},
			SellerId:     &stockpb.PeerForeignBankId{RoutingNumber: 111, Id: bogus},
			Offer:        peerLastModifiedOffer(222, "client-7"),
		})
		if err == nil {
			t.Fatalf("seller %q: expected rejection, got nil (junk row vector)", bogus)
		}
		if c := status.Code(err); c != codes.InvalidArgument && c != codes.NotFound {
			t.Errorf("seller %q: expected InvalidArgument/NotFound, got %v", bogus, err)
		}
		var n int64
		db.Table("otc_negotiations").Count(&n)
		if n != 0 {
			t.Errorf("seller %q: expected 0 rows, got %d", bogus, n)
		}
	}
}

// TestInbound_CreateNegotiation_WellFormedSellers_Accepted: well-formed sellers
// (employee-<digits>, bank, real client-<n>) are accepted. The BUYER opaque id
// (UUID / acc-42) stays verbatim and is not format-checked.
func TestInbound_CreateNegotiation_WellFormedSellers_Accepted(t *testing.T) {
	cases := []struct {
		seller string
		buyer  string // opaque buyer id — stays verbatim
	}{
		{"employee-42", "550e8400-e29b-41d4-a716-446655440000"},
		{"bank", "acc-42"},
		{"client-9", "client-7"},
	}
	for _, c := range cases {
		h, db, _, _ := newPeerOtcHandler(t)
		val := &fakeLocalSellerValidator{exists: map[string]bool{"client-9": true}}
		h = h.WithSellerValidator(val)

		resp, err := h.CreateNegotiation(context.Background(), &stockpb.CreateNegotiationRequest{
			PeerBankCode: "222",
			BuyerId:      &stockpb.PeerForeignBankId{RoutingNumber: 222, Id: c.buyer},
			SellerId:     &stockpb.PeerForeignBankId{RoutingNumber: 111, Id: c.seller},
			Offer:        peerLastModifiedOffer(222, c.buyer),
		})
		if err != nil {
			t.Fatalf("seller %q buyer %q: %v", c.seller, c.buyer, err)
		}
		if resp.GetNegotiationId().GetId() == "" {
			t.Errorf("seller %q: expected negotiation id", c.seller)
		}
		var n int64
		db.Table("otc_negotiations").Count(&n)
		if n != 1 {
			t.Errorf("seller %q: expected 1 row, got %d", c.seller, n)
		}
		// Buyer opaque id stored verbatim. Remote rows are keyed by the PEER
		// routing (222), not our own — the returned id carries our routing only as
		// the response envelope.
		seeded, gerr := repository.NewOTCNegotiationRepository(db).GetRemoteNegByRoutingAndNative(222, resp.GetNegotiationId().GetId())
		if gerr != nil {
			t.Fatalf("read seeded: %v", gerr)
		}
		if seeded.RemoteBuyerID == nil || *seeded.RemoteBuyerID != c.buyer {
			t.Errorf("buyer opaque id not stored verbatim: got %v, want %q", seeded.RemoteBuyerID, c.buyer)
		}
	}
}

// TestInbound_CreateNegotiation_PhantomClientSeller_Rejected: a client-<n> that
// does not resolve locally is still NotFound (existing guard preserved).
func TestInbound_CreateNegotiation_PhantomClientSeller_Rejected(t *testing.T) {
	h, db, _, _ := newPeerOtcHandler(t)
	val := &fakeLocalSellerValidator{exists: map[string]bool{"client-9": true}}
	h = h.WithSellerValidator(val)

	_, err := h.CreateNegotiation(context.Background(), &stockpb.CreateNegotiationRequest{
		PeerBankCode: "222",
		BuyerId:      &stockpb.PeerForeignBankId{RoutingNumber: 222, Id: "client-7"},
		SellerId:     &stockpb.PeerForeignBankId{RoutingNumber: 111, Id: "client-888888"},
		Offer:        peerLastModifiedOffer(222, "client-7"),
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected NotFound for phantom client seller, got %v", err)
	}
	var n int64
	db.Table("otc_negotiations").Count(&n)
	if n != 0 {
		t.Errorf("expected 0 rows, got %d", n)
	}
}

// buildRemoteNegForTest is an external-package-friendly wrapper that builds a
// remote OTCNegotiation row with the buyer/seller wired, mirroring the inbound
// handler's buildRemoteNeg via the model fields.
func buildRemoteNegForTest(
	peerRouting int64, foreignID string, offer contractsitx.OtcOffer, offerJSON string,
	buyerRouting int64, buyerID string, sellerRouting int64, sellerID string,
) *model.OTCNegotiation {
	bID := buyerID
	sID := sellerID
	bR := buyerRouting
	sR := sellerRouting
	oj := offerJSON
	fid := foreignID
	return &model.OTCNegotiation{
		RoutingNumber:             peerRouting,
		NativeID:                  &fid,
		BidderOwnerType:           model.OwnerBank,
		Quantity:                  decimal.NewFromInt(offer.Amount),
		StrikePrice:               offer.PricePerStock,
		Premium:                   offer.Premium,
		Status:                    "ongoing",
		LastActionByPrincipalType: "system",
		LastActionByOwnerType:     string(model.OwnerBank),
		RemoteOfferJSON:           &oj,
		RemoteBuyerRouting:        &bR,
		RemoteBuyerID:             &bID,
		RemoteSellerRouting:       &sR,
		RemoteSellerID:            &sID,
	}
}
