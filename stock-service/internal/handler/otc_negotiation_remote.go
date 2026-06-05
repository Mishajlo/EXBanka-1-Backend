// Package handler — cross-bank (REMOTE) bid dispatch for OpenNegotiation
// (Unified OTC SP-2b). When the bid route's parent :id resolves to a folded-in
// REMOTE OTCOffer (a peer-hosted listing) rather than a local one, the bid is
// dispatched to the seller's bank over SI-TX instead of running the local
// first-accept-wins saga.
//
// This relocates the egress logic that previously lived in the api-gateway
// (peer_otc_initiate_handler.go CreatePeerNegotiation) into stock-service per
// decision A: composes the SI-TX OtcOffer, validates the bidder account,
// POSTs to the peer via peerotc.Client, and records the local mirror row. The
// gateway stays thin — it forwards every bid through OpenNegotiation and the
// dispatch happens here based on whether the parent listing is local or remote.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	accountpb "github.com/exbanka/contract/accountpb"
	contractsitx "github.com/exbanka/contract/sitx"
	stockpb "github.com/exbanka/contract/stockpb"
	"github.com/exbanka/stock-service/internal/model"
)

// bankOwnerSentinel is the legacy owner_id stamped on bank-owned accounts that
// pre-date the account_kind="bank" flag. Mirrors the gateway's bankSentinelOwnerID
// (api-gateway/internal/handler/validation.go) and account-service's check.
const bankOwnerSentinel uint64 = 1_000_000_000

// isBankAccount reports whether an account-service AccountResponse is a
// bank-owned account, using the same predicate the gateway and account-service
// use: the account_kind == "bank" flag OR the legacy owner sentinel.
func isBankAccount(acct *accountpb.AccountResponse) bool {
	return acct.GetAccountKind() == "bank" || acct.GetOwnerId() == bankOwnerSentinel
}

// openRemoteNegotiation places a cross-bank bid against a REMOTE (peer-hosted)
// OTC option listing (SP-2b). It is invoked by OpenNegotiation when the local
// path reports the parent :id is not a local offer.
//
// The bool reports whether the :id was a remote listing this handler could
// dispatch to. false (with nil error) means "not a remote listing either" —
// the caller surfaces the original local NotFound. A non-nil error is a real
// failure (bad account, peer rejected, persistence) to surface.
//
//   - Both CLIENT and BANK bidders are supported (SP-3 Task 4). The wire buyer
//     id is "client-<ownerID>" for a client bid, or the stable
//     "employee-<actingEmployeeID>" for an employee acting AS the bank.
//   - The bidder account is validated against account-service (status active,
//     currency == the listing's premium currency — no cross-bank FX in SI-TX),
//     and its account number is threaded to the seller's bank as
//     buyerAccountNumber. The OWNERSHIP assertion branches on owner type: a
//     client bid requires owner_id == the client principal; a bank bid requires
//     a BANK account (account_kind == "bank" or the bank owner sentinel).
//   - The composed SI-TX OtcOffer mirrors CreatePeerNegotiation's wire shape;
//     parentOfferId carries the remote listing's (routing, native_id) lot key
//     so the seller's bank can cascade-cancel siblings on accept.
func (h *OTCOptionsHandler) openRemoteNegotiation(
	ctx context.Context,
	in *stockpb.OpenNegotiationRequest,
	bidderOwnerType model.OwnerType,
	bidderOwnerID *uint64,
	actingEmployeeID uint64,
	qty, strike, premium decimal.Decimal,
	settle time.Time,
) (*stockpb.OTCNegotiationResponse, bool, error) {
	if h.remoteOffers == nil || h.peerDispatch == nil || h.remoteNegWriter == nil {
		return nil, false, nil // cross-bank dispatch not wired → not a remote listing here
	}

	remoteOffer, err := h.remoteOffers.GetRemoteByID(in.GetParentOfferId())
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil // not a remote listing → surface the local NotFound
		}
		return nil, false, status.Errorf(codes.Internal, "remote listing lookup failed: %v", err)
	}

	// Build the SI-TX wire buyer identity per owner type (SP-3 Task 4).
	//   - client bid → "client-<ownerID>"
	//   - bank bid   → "employee-<actingEmployeeID>" (the stable wire identity
	//     for the bank party; independent of which employee acts later).
	var buyerID string
	switch bidderOwnerType {
	case model.OwnerClient:
		if bidderOwnerID == nil {
			return nil, false, status.Error(codes.InvalidArgument, "client bidder requires an owner id")
		}
		buyerID = "client-" + strconv.FormatUint(*bidderOwnerID, 10)
	case model.OwnerBank:
		if actingEmployeeID == 0 {
			return nil, false, status.Error(codes.InvalidArgument, "bank bidder requires an acting employee id")
		}
		buyerID = "employee-" + strconv.FormatUint(actingEmployeeID, 10)
	default:
		return nil, false, status.Error(codes.InvalidArgument, "unsupported bidder owner type")
	}

	// The bid's premium currency is the listing's published premium currency
	// (the gateway sends premium as a bare decimal; the currency is the peer's).
	premiumCurrency := ""
	if remoteOffer.PremiumCurrency != nil {
		premiumCurrency = *remoteOffer.PremiumCurrency
	}
	strikeCurrency := ""
	if remoteOffer.StrikeCurrency != nil {
		strikeCurrency = *remoteOffer.StrikeCurrency
	}
	if premiumCurrency == "" {
		return nil, false, status.Error(codes.FailedPrecondition,
			"remote listing has no premium currency; cannot validate bidder account")
	}

	// Validate the bidder account against account-service: ownership, active,
	// and currency == premium currency (no cross-bank FX). Read its number so
	// the seller's bank debits this exact account on accept.
	if h.accounts == nil {
		return nil, false, status.Error(codes.FailedPrecondition, "account-service client not wired for cross-bank bidding")
	}
	acct, gerr := h.accounts.GetAccount(ctx, &accountpb.GetAccountRequest{Id: in.GetBidderAccountId()})
	if gerr != nil {
		return nil, false, status.Errorf(codes.NotFound, "bidder account not found: %v", gerr)
	}
	// Ownership assertion branches on owner type. A client bid must bind an
	// account the client owns; a bank bid must bind a BANK account (the gateway
	// already forces this via ResolveAndCheckAccount, but re-assert defensively).
	switch bidderOwnerType {
	case model.OwnerClient:
		if acct.GetOwnerId() != *bidderOwnerID {
			return nil, false, status.Error(codes.PermissionDenied, "bidder account does not belong to caller")
		}
	case model.OwnerBank:
		if !isBankAccount(acct) {
			return nil, false, status.Error(codes.PermissionDenied, "bank bidder must bind a bank account")
		}
	}
	if acct.GetStatus() != "active" {
		return nil, false, status.Error(codes.FailedPrecondition, "bidder account is not active")
	}
	if acct.GetCurrencyCode() != premiumCurrency {
		return nil, false, status.Errorf(codes.InvalidArgument,
			"currency mismatch: account is %s but the listing's premium is %s (cross-bank SI-TX has no FX)",
			acct.GetCurrencyCode(), premiumCurrency)
	}
	buyerAccountNumber := acct.GetAccountNumber()

	// The cross-bank cascade-cancel lot key = the remote listing's
	// (peer routing, native_id).
	peerRouting := remoteOffer.RoutingNumber
	peerBankCode := ""
	if remoteOffer.InitiatorBankCode != nil {
		peerBankCode = *remoteOffer.InitiatorBankCode
	}
	if peerBankCode == "" {
		peerBankCode = strconv.FormatInt(peerRouting, 10)
	}
	parentNativeID := ""
	if remoteOffer.NativeID != nil {
		parentNativeID = *remoteOffer.NativeID
	}
	sellerID := ""
	if remoteOffer.RemoteSellerID != nil {
		sellerID = *remoteOffer.RemoteSellerID
	}

	// Compose the SI-TX OtcOffer (mirrors CreatePeerNegotiation's wire shape).
	// SI-TX §2.5 / §2.8.1 require monetary amounts to be JSON numbers, not
	// quoted strings. DecimalNumber.MarshalJSON emits a bare numeric token.
	settlementDate := settle.Format("2006-01-02")
	offer := map[string]any{
		"stock":              map[string]any{"ticker": remoteOffer.Ticker},
		"settlementDate":     settlementDate,
		"pricePerUnit":       map[string]any{"amount": contractsitx.DecimalNumber{Decimal: strike}, "currency": strikeCurrency},
		"premium":            map[string]any{"amount": contractsitx.DecimalNumber{Decimal: premium}, "currency": premiumCurrency},
		"buyerId":            map[string]any{"routingNumber": h.ownRouting, "id": buyerID},
		"sellerId":           map[string]any{"routingNumber": peerRouting, "id": sellerID},
		"amount":             qty.IntPart(),
		"lastModifiedBy":     map[string]any{"routingNumber": h.ownRouting, "id": buyerID},
		"buyerAccountNumber": buyerAccountNumber,
	}
	// parentOfferId — the per-listing cascade-cancel grouping key. The seller's
	// bank groups sibling chains by this key and cascade-cancels them on accept.
	if parentNativeID != "" {
		offer["parentOfferId"] = map[string]any{
			"routingNumber": peerRouting,
			"id":            parentNativeID,
		}
	}

	// Dispatch to the seller's bank.
	gotRouting, foreignID, derr := h.peerDispatch.CreateNegotiation(ctx, peerBankCode, offer)
	if derr != nil {
		return nil, false, status.Errorf(codes.FailedPrecondition, "cross-bank dispatch failed: %v", derr)
	}

	// Record the local REMOTE mirror row so the bidder can list / drive the
	// negotiation. native_id = peer foreign id; routing_number = peer routing.
	// buildRemoteNeg sets the NOT-NULL terms + ValidateOwner-valid bidder
	// columns; the cross-bank parties live in the Remote* columns.
	offerStruct := contractsitx.OtcOffer{
		Ticker:          remoteOffer.Ticker,
		Amount:          qty.IntPart(),
		PricePerStock:   strike,
		Currency:        strikeCurrency,
		Premium:         premium,
		PremiumCurrency: premiumCurrency,
		SettlementDate:  settlementDate,
		LastModifiedBy:  contractsitx.ForeignBankId{RoutingNumber: h.ownRouting, ID: buyerID},
	}
	offerJSON, jerr := json.Marshal(offerStruct)
	if jerr != nil {
		return nil, false, status.Errorf(codes.Internal, "marshal mirror offer: %v", jerr)
	}
	var parentRoutingPtr *int64
	var parentNativePtr *string
	if parentNativeID != "" {
		pr := peerRouting
		pn := parentNativeID
		parentRoutingPtr = &pr
		parentNativePtr = &pn
	}
	mirror := buildRemoteNeg(
		gotRouting, foreignID, offerStruct, string(offerJSON),
		h.ownRouting, buyerID,
		peerRouting, sellerID,
		parentRoutingPtr, parentNativePtr, "ongoing",
	)
	// SP-3 Task 4: a bank bid stamps the stable acting-employee wire identity.
	// buildRemoteNeg already sets BidderOwnerType=bank for every remote mirror
	// (the real parties live in the Remote* columns), so the ActingEmployeeID
	// nil-unless-bank invariant (model BeforeSave) holds in both cases. A client
	// bid leaves it nil — the bank wire identity is meaningless there.
	if bidderOwnerType == model.OwnerBank {
		emp := actingEmployeeID
		mirror.ActingEmployeeID = &emp
	}
	if err := h.remoteNegWriter.UpsertRemoteNeg(mirror); err != nil {
		return nil, false, status.Errorf(codes.Internal, "record remote negotiation mirror: %v", err)
	}

	// Return the unified negotiation response (kind=remote derived from
	// routing; id = the upserted surrogate id; terms from the mirror).
	resp, _ := peerNegToProto(mirror, h.ownRouting)
	return resp, true, nil
}
