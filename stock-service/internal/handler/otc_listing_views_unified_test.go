package handler

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	stockpb "github.com/exbanka/contract/stockpb"
	"github.com/exbanka/stock-service/internal/model"
)

// seedLocalListing inserts a local OTCOffer with the given id, owned (posted)
// by the given client, so the per-listing audience check passes for that
// poster.
func seedLocalListing(t *testing.T, db *gorm.DB, offerID, posterID uint64) {
	t.Helper()
	pid := posterID
	o := &model.OTCOffer{
		ID:                          offerID,
		InitiatorOwnerType:          model.OwnerClient,
		InitiatorOwnerID:            &pid,
		Direction:                   "sell_initiated",
		StockID:                     1,
		Ticker:                      "ACME",
		Quantity:                    decimal.NewFromInt(10),
		StrikePrice:                 decimal.NewFromInt(150),
		Premium:                     decimal.NewFromInt(20),
		SettlementDate:              time.Now().AddDate(0, 0, 30),
		Status:                      "open",
		LastModifiedByPrincipalType: "client",
		LastModifiedByPrincipalID:   posterID,
		InitiatorAccountID:          9001,
		CreatedAt:                   time.Now(),
		UpdatedAt:                   time.Now(),
	}
	if err := db.Create(o).Error; err != nil {
		t.Fatalf("seed local listing: %v", err)
	}
}

// fakeRemoteOfferGetter is an in-memory RemoteOfferGetter for the per-listing
// remote-id tests. A nil entry for an id models "not a remote mirror" via
// gorm.ErrRecordNotFound.
type fakeRemoteOfferGetter struct {
	byID map[uint64]*model.OTCOffer
	err  error
}

func (f *fakeRemoteOfferGetter) GetRemoteByID(id uint64) (*model.OTCOffer, error) {
	if f.err != nil {
		return nil, f.err
	}
	if m, ok := f.byID[id]; ok {
		return m, nil
	}
	return nil, gorm.ErrRecordNotFound
}

// remoteMirrorRow builds a folded-in remote OTCOffer row for the listing-view
// tests: routing=<peer>, native_id=<foreign id>, plus the remote-display
// columns. Mirrors what the refresher writes via UpsertRemote.
func remoteMirrorRow(id uint64, peerRouting int64, foreignID, bankCode, sellerID, ticker, status string) *model.OTCOffer {
	nid := foreignID
	bc := bankCode
	sid := sellerID
	return &model.OTCOffer{
		ID: id, RoutingNumber: peerRouting, NativeID: &nid,
		InitiatorBankCode: &bc, RemoteSellerID: &sid,
		InitiatorOwnerType: model.OwnerBank,
		Ticker:             ticker, Status: status,
	}
}

// peerRowWithParent is peerRow plus the (ParentOfferRouting, ParentOfferID) lot
// key that ties a peer chain to a specific remote listing.
func peerRowWithParent(id uint64, buyerRouting int64, buyerID string, sellerRouting int64, sellerID, status string, parentRouting int64, parentID string) model.PeerOtcNegotiation {
	row := peerRow(id, buyerRouting, buyerID, sellerRouting, sellerID, status)
	pr := parentRouting
	pid := parentID
	row.ParentOfferRouting = &pr
	row.ParentOfferID = &pid
	return row
}

// newListingViewsFixture builds an OTCOptionsHandler whose per-listing path is
// backed by a sqlite negotiation service (LOCAL listings) plus a fake remote
// mirror + fake peer lister (REMOTE listings).
func newListingViewsFixture(t *testing.T, ownRouting int64, remote RemoteOfferGetter, peer PeerNegotiationLister) (*OTCOptionsHandler, *gorm.DB) {
	t.Helper()
	h, db := newUnifiedNegFixture(t, ownRouting, "111", peer)
	// Re-wire remote offers with the supplied getter (newUnifiedNegFixture
	// wires a nil one).
	h = h.WithRemoteOffers(remote, "111")
	return h, db
}

// --- ListNegotiationsByListing -------------------------------------------

// TestListingNegotiations_LocalUnchanged_Stamped: the listing's poster (client
// 3) views all chains; each item is kind=local, own provenance, and
// me_owner=true (spec §5: me_owner ⇔ the caller owns the PARENT OFFER).
func TestListingNegotiations_LocalUnchanged_Stamped(t *testing.T) {
	const ownRouting int64 = 111
	h, db := newListingViewsFixture(t, ownRouting, &fakeRemoteOfferGetter{}, &fakePeerNegLister{})
	// Local offer id 100 posted by client 3; two bidders open chains.
	seedLocalListing(t, db, 100, 3)
	seedBidderChain(t, db, 7, 100)
	seedBidderChain(t, db, 8, 100)

	// Poster (client 3) views all chains on their listing.
	resp, err := h.ListNegotiationsByListing(context.Background(), &stockpb.ListNegotiationsByListingRequest{
		ParentOfferId: 100, CallerOwnerType: "client", CallerOwnerId: 3,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(resp.GetNegotiations()) != 2 {
		t.Fatalf("want 2 chains on the local listing, got %d", len(resp.GetNegotiations()))
	}
	for _, n := range resp.GetNegotiations() {
		if n.GetKind() != "local" {
			t.Errorf("kind = %q want local", n.GetKind())
		}
		if n.GetRoutingNumber() != ownRouting {
			t.Errorf("routing_number = %d want %d", n.GetRoutingNumber(), ownRouting)
		}
		// Poster owns the parent offer → me_owner must be true for all chains.
		if !n.GetMeOwner() {
			t.Errorf("me_owner=false; poster owns the parent offer so me_owner must be true")
		}
	}
}

// TestListingNegotiations_EmployeeViewer_MeOwnerFalse: an employee viewing a
// client-owned listing via otc.read.all (owner_type="bank") does NOT own the
// offer, so me_owner must be false for every returned chain.
func TestListingNegotiations_EmployeeViewer_MeOwnerFalse(t *testing.T) {
	const ownRouting int64 = 111
	h, db := newListingViewsFixture(t, ownRouting, &fakeRemoteOfferGetter{}, &fakePeerNegLister{})
	// Local offer id 200 posted by client 5; one bidder opens a chain.
	seedLocalListing(t, db, 200, 5)
	seedBidderChain(t, db, 9, 200)

	// Employee (owner_type="bank") views chains — gateway already enforced otc.read.all.
	resp, err := h.ListNegotiationsByListing(context.Background(), &stockpb.ListNegotiationsByListingRequest{
		ParentOfferId: 200, CallerOwnerType: "bank", CallerOwnerId: 0,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(resp.GetNegotiations()) != 1 {
		t.Fatalf("want 1 chain, got %d", len(resp.GetNegotiations()))
	}
	if resp.GetNegotiations()[0].GetMeOwner() {
		t.Errorf("me_owner=true; employee viewing a client-owned listing is NOT the owner")
	}
}

// TestListingNegotiations_BidderForbidden: a client who is NOT the listing
// poster must receive PermissionDenied (403) and must NOT be silently routed
// into the remote-mirror path (Fix 3 test).
func TestListingNegotiations_BidderForbidden(t *testing.T) {
	const ownRouting int64 = 111
	// Wire a fake remote-offer getter that has NO mirrors, so a fallback to
	// the remote path would return ok=false and then re-surface the error.
	h, db := newListingViewsFixture(t, ownRouting, &fakeRemoteOfferGetter{}, &fakePeerNegLister{})
	// Local offer id 300 posted by client 3.
	seedLocalListing(t, db, 300, 3)
	// Client 7 is a bidder on this listing (has a chain) but is not the poster.
	seedBidderChain(t, db, 7, 300)

	_, err := h.ListNegotiationsByListing(context.Background(), &stockpb.ListNegotiationsByListingRequest{
		ParentOfferId: 300, CallerOwnerType: "client", CallerOwnerId: 7,
	})
	if err == nil {
		t.Fatalf("want PermissionDenied error for bidder, got nil")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Errorf("want PermissionDenied, got %v", err)
	}
}

// TestListingNegotiations_RemoteId_OwnChainOnly: a remote listing id surfaces
// ONLY the caller's own chain(s) against it, never other parties'.
func TestListingNegotiations_RemoteId_OwnChainOnly(t *testing.T) {
	const ownRouting int64 = 111
	const peerSellerRouting int64 = 222
	remote := &fakeRemoteOfferGetter{byID: map[uint64]*model.OTCOffer{
		900: remoteMirrorRow(900, peerSellerRouting, "foreign-7", "222", "client-3", "", ""),
	}}
	peer := &fakePeerNegLister{rows: []model.PeerOtcNegotiation{
		// Caller's own chain against the remote listing (matching lot key).
		peerRowWithParent(55, ownRouting, "client-7", peerSellerRouting, "client-3", "ongoing", peerSellerRouting, "foreign-7"),
		// Caller's chain against a DIFFERENT remote listing — must be excluded.
		peerRowWithParent(56, ownRouting, "client-7", peerSellerRouting, "client-3", "ongoing", peerSellerRouting, "foreign-other"),
		// A chain with no lot key — must be excluded.
		peerRow(57, ownRouting, "client-7", peerSellerRouting, "client-3", "ongoing"),
	}}
	h, _ := newListingViewsFixture(t, ownRouting, remote, peer)

	resp, err := h.ListNegotiationsByListing(context.Background(), &stockpb.ListNegotiationsByListingRequest{
		ParentOfferId: 900, CallerOwnerType: "client", CallerOwnerId: 7,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(resp.GetNegotiations()) != 1 {
		t.Fatalf("want exactly 1 own chain on the remote listing, got %d", len(resp.GetNegotiations()))
	}
	got := resp.GetNegotiations()[0]
	if got.GetKind() != "remote" {
		t.Errorf("kind = %q want remote", got.GetKind())
	}
	if got.GetId() != 55 {
		t.Errorf("id = %d want 55 (caller's matching peer chain)", got.GetId())
	}
	if got.GetRoutingNumber() != peerSellerRouting {
		t.Errorf("routing_number = %d want %d (counterparty seller bank)", got.GetRoutingNumber(), peerSellerRouting)
	}
}

// TestListingNegotiations_RemoteId_NoOwnChain_Empty: a remote listing on which
// the caller has no chain returns an empty list, NOT a 404.
func TestListingNegotiations_RemoteId_NoOwnChain_Empty(t *testing.T) {
	const ownRouting int64 = 111
	remote := &fakeRemoteOfferGetter{byID: map[uint64]*model.OTCOffer{
		900: remoteMirrorRow(900, 222, "foreign-7", "222", "client-3", "", ""),
	}}
	h, _ := newListingViewsFixture(t, ownRouting, remote, &fakePeerNegLister{})

	resp, err := h.ListNegotiationsByListing(context.Background(), &stockpb.ListNegotiationsByListingRequest{
		ParentOfferId: 900, CallerOwnerType: "client", CallerOwnerId: 7,
	})
	if err != nil {
		t.Fatalf("err: %v (want empty list, not error)", err)
	}
	if len(resp.GetNegotiations()) != 0 {
		t.Fatalf("want 0 chains, got %d", len(resp.GetNegotiations()))
	}
}

// TestListingNegotiations_UnknownId_NotFound: an id that is neither a local
// offer nor a remote mirror returns NotFound (existing behavior preserved).
func TestListingNegotiations_UnknownId_NotFound(t *testing.T) {
	const ownRouting int64 = 111
	h, _ := newListingViewsFixture(t, ownRouting, &fakeRemoteOfferGetter{}, &fakePeerNegLister{})

	_, err := h.ListNegotiationsByListing(context.Background(), &stockpb.ListNegotiationsByListingRequest{
		ParentOfferId: 99999, CallerOwnerType: "client", CallerOwnerId: 7,
	})
	if err == nil {
		t.Fatalf("want NotFound error, got nil")
	}
}

// --- GetOfferTimeline -----------------------------------------------------

// TestTimeline_RemoteId_OwnChainOnly: a remote listing timeline surfaces the
// offer header from the mirror plus one entry per the caller's own chain.
func TestTimeline_RemoteId_OwnChainOnly(t *testing.T) {
	const ownRouting int64 = 111
	const peerSellerRouting int64 = 222
	remote := &fakeRemoteOfferGetter{byID: map[uint64]*model.OTCOffer{
		900: remoteMirrorRow(900, peerSellerRouting, "foreign-7", "222", "client-3", "ACME", "open"),
	}}
	peer := &fakePeerNegLister{rows: []model.PeerOtcNegotiation{
		peerRowWithParent(55, ownRouting, "client-7", peerSellerRouting, "client-3", "ongoing", peerSellerRouting, "foreign-7"),
		peerRowWithParent(56, ownRouting, "client-7", peerSellerRouting, "client-3", "ongoing", peerSellerRouting, "foreign-other"),
	}}
	h, _ := newListingViewsFixture(t, ownRouting, remote, peer)

	resp, err := h.GetOfferTimeline(context.Background(), &stockpb.GetOfferTimelineRequest{
		ParentOfferId: 900, CallerOwnerType: "client", CallerOwnerId: 7,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.GetOffer() == nil || resp.GetOffer().GetKind() != "remote" {
		t.Fatalf("offer header missing/not remote: %+v", resp.GetOffer())
	}
	if resp.GetOffer().GetRoutingNumber() != peerSellerRouting {
		t.Errorf("offer routing_number = %d want %d", resp.GetOffer().GetRoutingNumber(), peerSellerRouting)
	}
	if len(resp.GetTimeline()) != 1 {
		t.Fatalf("want exactly 1 timeline entry (caller's own chain), got %d", len(resp.GetTimeline()))
	}
	if resp.GetTimeline()[0].GetNegotiationId() != 55 {
		t.Errorf("entry negotiation_id = %d want 55", resp.GetTimeline()[0].GetNegotiationId())
	}
}

// TestTimeline_RemoteId_NoOwnChain_HeaderOnly: a remote listing the caller has
// no chain on returns the offer header with an empty timeline, not a 404.
func TestTimeline_RemoteId_NoOwnChain_HeaderOnly(t *testing.T) {
	const ownRouting int64 = 111
	remote := &fakeRemoteOfferGetter{byID: map[uint64]*model.OTCOffer{
		900: remoteMirrorRow(900, 222, "foreign-7", "222", "client-3", "ACME", ""),
	}}
	h, _ := newListingViewsFixture(t, ownRouting, remote, &fakePeerNegLister{})

	resp, err := h.GetOfferTimeline(context.Background(), &stockpb.GetOfferTimelineRequest{
		ParentOfferId: 900, CallerOwnerType: "client", CallerOwnerId: 7,
	})
	if err != nil {
		t.Fatalf("err: %v (want header + empty timeline)", err)
	}
	if resp.GetOffer() == nil || resp.GetOffer().GetKind() != "remote" {
		t.Fatalf("offer header missing/not remote")
	}
	if len(resp.GetTimeline()) != 0 {
		t.Fatalf("want 0 timeline entries, got %d", len(resp.GetTimeline()))
	}
}

// TestTimeline_UnknownId_NotFound: an id that is neither local nor remote → 404.
func TestTimeline_UnknownId_NotFound(t *testing.T) {
	const ownRouting int64 = 111
	h, _ := newListingViewsFixture(t, ownRouting, &fakeRemoteOfferGetter{}, &fakePeerNegLister{})

	_, err := h.GetOfferTimeline(context.Background(), &stockpb.GetOfferTimelineRequest{
		ParentOfferId: 99999, CallerOwnerType: "client", CallerOwnerId: 7,
	})
	if err == nil {
		t.Fatalf("want NotFound error, got nil")
	}
}
