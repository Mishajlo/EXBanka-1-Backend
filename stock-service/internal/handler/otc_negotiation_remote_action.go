// Package handler — cross-bank (REMOTE) dispatch for the negotiation ACTIONS
// (counter / accept / reject / cancel) on an OTCNegotiation chain (Unified OTC
// SP-2b Task 4).
//
// When an action's :nid resolves to a folded-in REMOTE OTCNegotiation row (a
// chain whose authoritative state lives on a PEER bank), the action is proxied
// to that peer over SI-TX and the local mirror row's status/offer is updated to
// match. This relocates the egress logic that previously lived in the
// api-gateway (peer_otc_initiate_handler.go's CounterPeerNegotiation /
// AcceptPeerNegotiation / CancelPeerNegotiation) into stock-service per
// decision A, so the gateway stays a thin passthrough: every counter/accept/
// reject/cancel flows through the same RPC and the local-vs-remote dispatch
// happens here based on the row's routing_number.
//
//   - LOCAL row (routing == own) → the existing local service path (unchanged).
//   - REMOTE row (routing == peer) → proxy + mirror, implemented below.
//
// SI-TX wire rules honoured here (same as Task 3's bid dispatch):
//   - Monetary amounts in the counter OtcOffer are JSON NUMBERS, not quoted
//     strings (contract/sitx.DecimalNumber).
//   - accept is GET {peer}/negotiations/{rid}/{id}/accept (spec §3.6).
//   - reject AND cancel are DELETE {peer}/negotiations/{rid}/{id} — the SI-TX
//     protocol's single terminal soft-cancel (spec §3.5).
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	contractsitx "github.com/exbanka/contract/sitx"
	stockpb "github.com/exbanka/contract/stockpb"
	"github.com/exbanka/stock-service/internal/model"
	"github.com/exbanka/stock-service/internal/service"
)

// isOTCNegotiationNotFound reports whether an error means "the chain is not a
// local OTCNegotiation". Both the service sentinel and the raw GORM not-found
// are matched so the remote-dispatch fallback fires for either. (LockByID
// treats a remote row as not-found, so a REMOTE :nid always lands here.)
func isOTCNegotiationNotFound(err error) bool {
	return errors.Is(err, service.ErrOTCNegotiationNotFound) || errors.Is(err, gorm.ErrRecordNotFound)
}

// remoteNegContext bundles the data the remote-action dispatch needs once a
// REMOTE chain has been resolved and the caller authorized as a party.
type remoteNegContext struct {
	row              *model.OTCNegotiation
	rid              string // the peer routing that issued the foreign id (= row.RoutingNumber)
	foreignID        string // the peer foreign negotiation id (= row.NativeID)
	counterpartyCode string // the OTHER party's routing, stringified (the bank we proxy to)
	offer            contractsitx.OtcOffer
}

// resolveRemoteNegAction loads the REMOTE chain for :nid and authorizes the
// caller as a party. The bool is false (nil error) when :nid is NOT a remote
// chain this handler can dispatch (unwired, or a local id) — the caller then
// surfaces the original local error. A non-party caller gets NotFound (existence
// must not leak to outsiders — same policy as resolveRemoteContract).
//
// Authorization: the caller is a party iff their acting identity matches the
// side WE host. We host the buyer when RemoteBuyerRouting == ownRouting and the
// seller when RemoteSellerRouting == ownRouting. Only client principals carry a
// cross-bank identity ("client-<N>"); a bank/employee caller is never a party.
// The counterparty bank code is the OTHER party's routing as a string.
func (h *OTCOptionsHandler) resolveRemoteNegAction(
	nid uint64, callerOwnerType model.OwnerType, callerOwnerID *uint64,
) (*remoteNegContext, bool, error) {
	if h.remoteNegOps == nil || h.peerDispatch == nil {
		return nil, false, nil // cross-bank dispatch not wired → not a remote chain here
	}
	row, err := h.remoteNegOps.GetRemoteNegByID(nid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil // not a remote chain → surface the local error
		}
		return nil, false, status.Errorf(codes.Internal, "remote negotiation lookup failed: %v", err)
	}

	// Only client principals have a cross-bank identity.
	if callerOwnerType != model.OwnerClient || callerOwnerID == nil {
		return nil, false, status.Error(codes.NotFound, "negotiation not found")
	}
	callerPrincipal := "client-" + strconv.FormatUint(*callerOwnerID, 10)

	buyerRouting, buyerID := remoteBuyer(row)
	sellerRouting, sellerID := remoteSeller(row)
	own := h.ownRouting

	var counterpartyRouting int64
	switch {
	case buyerRouting == own && buyerID == callerPrincipal:
		// We host the buyer; the counterparty is the seller's bank.
		counterpartyRouting = sellerRouting
	case sellerRouting == own && sellerID == callerPrincipal:
		// We host the seller; the counterparty is the buyer's bank.
		counterpartyRouting = buyerRouting
	default:
		// Caller is not a party to this chain — do not leak existence.
		return nil, false, status.Error(codes.NotFound, "negotiation not found")
	}

	var offer contractsitx.OtcOffer
	if jerr := json.Unmarshal([]byte(remoteOfferJSONOf(row)), &offer); jerr != nil {
		// best-effort: terms left zero; the counter path re-supplies the
		// strike/premium from the request but reuses this offer's ticker +
		// currencies, so a malformed mirror silently composes a zero-value
		// (empty ticker/currency) counter — log it so it's diagnosable.
		// accept/reject/cancel don't need the offer body.
		log.Printf("WARN resolveRemoteNegAction: row %d RemoteOfferJSON decode failed: %v", row.ID, jerr)
		offer = contractsitx.OtcOffer{}
	}

	return &remoteNegContext{
		row:              row,
		rid:              strconv.FormatInt(row.RoutingNumber, 10),
		foreignID:        remoteNativeIDOf(row),
		counterpartyCode: strconv.FormatInt(counterpartyRouting, 10),
		offer:            offer,
	}, true, nil
}

// counterRemoteNegotiation proxies a counter (PUT) to the counterparty's bank
// and mirrors the new terms onto the local REMOTE row. It relocates the
// gateway's CounterPeerNegotiation → proxyPeerNegotiation(PUT, "") +
// UpdateNegotiation mirror.
func (h *OTCOptionsHandler) counterRemoteNegotiation(
	ctx context.Context, rc *remoteNegContext,
	callerPrincipal string, qty, strike, premium decimal.Decimal, settle time.Time,
) (*stockpb.OTCNegotiationResponse, error) {
	// Compose the SI-TX OtcOffer body from the request terms. SI-TX §2.5 /
	// §2.8.1 require monetary amounts to be JSON NUMBERS — DecimalNumber emits
	// a bare numeric token. Reuse the existing currencies carried on the chain.
	settlementDate := settle.Format("2006-01-02")
	buyerRouting, buyerID := remoteBuyer(rc.row)
	sellerRouting, sellerID := remoteSeller(rc.row)
	offerBody := map[string]any{
		"stock":          map[string]any{"ticker": rc.offer.Ticker},
		"settlementDate": settlementDate,
		"pricePerUnit":   map[string]any{"amount": contractsitx.DecimalNumber{Decimal: strike}, "currency": rc.offer.Currency},
		"premium":        map[string]any{"amount": contractsitx.DecimalNumber{Decimal: premium}, "currency": rc.offer.PremiumCurrency},
		"buyerId":        map[string]any{"routingNumber": buyerRouting, "id": buyerID},
		"sellerId":       map[string]any{"routingNumber": sellerRouting, "id": sellerID},
		"amount":         qty.IntPart(),
		// lastModifiedBy = the caller (the party we host placing the counter).
		"lastModifiedBy": map[string]any{"routingNumber": h.ownRouting, "id": callerPrincipal},
	}
	// Preserve the cascade-cancel lot key when this chain carries one.
	if rc.row.RemoteParentRouting != nil && rc.row.RemoteParentNativeID != nil {
		offerBody["parentOfferId"] = map[string]any{
			"routingNumber": *rc.row.RemoteParentRouting,
			"id":            *rc.row.RemoteParentNativeID,
		}
	}
	body, jerr := json.Marshal(offerBody)
	if jerr != nil {
		return nil, status.Errorf(codes.Internal, "marshal counter offer: %v", jerr)
	}

	resp, code, err := h.peerDispatch.Proxy(ctx, rc.counterpartyCode, rc.rid, rc.foreignID, "PUT", "", body)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "cross-bank counter dispatch failed: %v", err)
	}
	if code < 200 || code >= 300 {
		return nil, status.Errorf(codes.FailedPrecondition, "peer rejected counter (%d): %s", code, string(resp))
	}

	// Mirror the new terms on the local REMOTE row so the caller's own list
	// reflects the counter immediately.
	mirrorOffer := contractsitx.OtcOffer{
		Ticker:          rc.offer.Ticker,
		Amount:          qty.IntPart(),
		PricePerStock:   strike,
		Currency:        rc.offer.Currency,
		Premium:         premium,
		PremiumCurrency: rc.offer.PremiumCurrency,
		SettlementDate:  settlementDate,
		LastModifiedBy:  contractsitx.ForeignBankId{RoutingNumber: h.ownRouting, ID: callerPrincipal},
	}
	mirrorJSON, _ := json.Marshal(mirrorOffer)
	if err := h.remoteNegOps.UpdateRemoteNegOffer(rc.row.RoutingNumber, rc.foreignID, string(mirrorJSON)); err != nil {
		return nil, status.Errorf(codes.Internal, "mirror counter offer: %v", err)
	}

	// Re-read the row so the response carries the refreshed offer JSON.
	updated, gerr := h.remoteNegOps.GetRemoteNegByID(rc.row.ID)
	if gerr != nil {
		updated = rc.row // best-effort: fall back to the stale row for shaping
	}
	out, _ := peerNegToProto(updated, h.ownRouting)
	return out, nil
}

// acceptRemoteNegotiation proxies an accept (GET /accept) to the counterparty's
// bank, flips the local mirror to accepted, and runs the cross-bank cascade-
// cancel: every sibling REMOTE chain sharing the accepted chain's lot key is
// flipped cancelled locally AND a DELETE is fired to each sibling bidder's bank.
// It relocates the gateway's AcceptPeerNegotiation → proxyPeerNegotiation(GET,
// "/accept") + MarkNegotiationAccepted mirror + CascadeCancelSiblings.
func (h *OTCOptionsHandler) acceptRemoteNegotiation(
	ctx context.Context, rc *remoteNegContext,
) (*stockpb.OTCAcceptNegotiationResponse, error) {
	resp, code, err := h.peerDispatch.Proxy(ctx, rc.counterpartyCode, rc.rid, rc.foreignID, "GET", "/accept", nil)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "cross-bank accept dispatch failed: %v", err)
	}
	if code < 200 || code >= 300 {
		return nil, status.Errorf(codes.FailedPrecondition, "peer rejected accept (%d): %s", code, string(resp))
	}

	// Parse the peer's SI-TX accept body ({ transactionId, status }) for the
	// cross-bank transaction id. The FE uses this to poll cross-bank settlement
	// (GET /me/otc/transactions/:txid/status) during the accept→contract-mirror
	// window. Best-effort: a non-JSON / field-absent body leaves it empty —
	// never fail the accept (the contract already formed on the peer).
	var crossBankTxID string
	if len(resp) > 0 {
		var peerBody struct {
			TransactionID string `json:"transactionId"`
		}
		if jerr := json.Unmarshal(resp, &peerBody); jerr != nil {
			log.Printf("WARN acceptRemoteNegotiation: row %d peer /accept body decode failed: %v", rc.row.ID, jerr)
		} else {
			crossBankTxID = peerBody.TransactionID
		}
	}

	// Flip the local mirror to accepted (ongoing → accepted). The CAS serialises
	// concurrent accepts so only one wins; a no-match is tolerated (the row may
	// already be accepted via a peer-driven webhook).
	if _, serr := h.remoteNegOps.CompareAndSetRemoteNegStatus(rc.row.RoutingNumber, rc.foreignID, "ongoing", "accepted"); serr != nil {
		return nil, status.Errorf(codes.Internal, "mirror accept status: %v", serr)
	}

	// Cross-bank cascade-cancel — every sibling REMOTE chain sharing the
	// accepted chain's (parentRouting, parentNativeID) lot key under the same
	// seller. Flip them cancelled locally AND DELETE to each sibling bidder's
	// bank so their mirrors flip too. Best-effort: a cascade failure must NOT
	// reverse the accept (the contract already formed on the peer).
	cancelled := h.cascadeCancelRemoteSiblings(ctx, rc)

	// Re-read the accepted row for the winning projection.
	winningRow, gerr := h.remoteNegOps.GetRemoteNegByID(rc.row.ID)
	if gerr != nil {
		winningRow = rc.row
	}
	winning, _ := peerNegToProto(winningRow, h.ownRouting)

	return &stockpb.OTCAcceptNegotiationResponse{
		Winning:                winning,
		ParentStatus:           "accepted",
		CancelledSiblings:      cancelled,
		CrossBankTransactionId: crossBankTxID,
	}, nil
}

// cascadeCancelRemoteSiblings flips every sibling REMOTE chain under the
// accepted chain's lot key to cancelled locally and DELETEs to each sibling
// bidder's bank. Returns the cancelled siblings projected onto the wire shape.
// The just-accepted chain itself is excluded (it is not its own sibling).
func (h *OTCOptionsHandler) cascadeCancelRemoteSiblings(
	ctx context.Context, rc *remoteNegContext,
) []*stockpb.OTCNegotiationResponse {
	cancelled := []*stockpb.OTCNegotiationResponse{}
	if rc.row.RemoteParentRouting == nil || rc.row.RemoteParentNativeID == nil {
		return cancelled // free-form chain (no lot key) → no sibling group
	}
	sellerRouting, sellerID := remoteSeller(rc.row)
	siblings, lerr := h.remoteNegOps.ListRemoteNegBySellerAndParent(
		sellerRouting, sellerID, *rc.row.RemoteParentRouting, *rc.row.RemoteParentNativeID,
	)
	if lerr != nil {
		return cancelled // best-effort: a cascade list failure must not reverse the accept
	}
	for i := range siblings {
		sib := &siblings[i]
		if sib.ID == rc.row.ID {
			continue // the winning chain itself
		}
		sibNative := remoteNativeIDOf(sib)
		// Flip the sibling mirror cancelled locally.
		if err := h.remoteNegOps.UpdateRemoteNegStatus(sib.RoutingNumber, sibNative, "cancelled"); err == nil {
			sib.Status = "cancelled"
		}
		// DELETE to the sibling bidder's bank so their mirror flips too. The
		// counterparty for a sibling chain (from this seller-hosting bank's
		// perspective) is the sibling's BUYER bank.
		sibBuyerRouting, _ := remoteBuyer(sib)
		sibPeerCode := strconv.FormatInt(sibBuyerRouting, 10)
		sibRID := strconv.FormatInt(sib.RoutingNumber, 10)
		_, _, _ = h.peerDispatch.Proxy(ctx, sibPeerCode, sibRID, sibNative, "DELETE", "", nil)

		if item, _ := peerNegToProto(sib, h.ownRouting); item != nil {
			cancelled = append(cancelled, item)
		}
	}
	return cancelled
}

// cancelRemoteNegotiation proxies a reject/cancel (DELETE) to the counterparty's
// bank and flips the local mirror to cancelled. Both reject and cancel map onto
// the SI-TX DELETE terminal (spec §3.5). It relocates the gateway's
// CancelPeerNegotiation → proxyPeerNegotiation(DELETE, "") + status mirror.
func (h *OTCOptionsHandler) cancelRemoteNegotiation(
	ctx context.Context, rc *remoteNegContext,
) (*stockpb.OTCNegotiationResponse, error) {
	resp, code, err := h.peerDispatch.Proxy(ctx, rc.counterpartyCode, rc.rid, rc.foreignID, "DELETE", "", nil)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "cross-bank cancel dispatch failed: %v", err)
	}
	if code < 200 || code >= 300 {
		return nil, status.Errorf(codes.FailedPrecondition, "peer rejected cancel (%d): %s", code, string(resp))
	}
	if err := h.remoteNegOps.UpdateRemoteNegStatus(rc.row.RoutingNumber, rc.foreignID, "cancelled"); err != nil {
		return nil, status.Errorf(codes.Internal, "mirror cancel status: %v", err)
	}
	updated, gerr := h.remoteNegOps.GetRemoteNegByID(rc.row.ID)
	if gerr != nil {
		updated = rc.row
	}
	out, _ := peerNegToProto(updated, h.ownRouting)
	return out, nil
}
