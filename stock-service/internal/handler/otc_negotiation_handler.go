// Package handler — gRPC surface for the OTCNegotiationService (Phase 2).
// Methods are attached to the existing OTCOptionsHandler so they can
// share the embedded UnimplementedOTCOptionsServiceServer and the
// already-registered server instance.
//
// Wiring: cmd/main.go calls otcOptionsHandler.WithNegotiations(svc)
// before registering; without the wire-up these methods return
// Unimplemented (typed sentinel) instead of panicking on nil deref.
package handler

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	contractsitx "github.com/exbanka/contract/sitx"
	stockpb "github.com/exbanka/contract/stockpb"
	"github.com/exbanka/stock-service/internal/model"
	"github.com/exbanka/stock-service/internal/service"
)

// WithNegotiations wires the Phase-2 parallel-negotiations service into
// the handler. Returns a copy (mutating-copy style matches WithRatings,
// WithPeerContracts).
func (h *OTCOptionsHandler) WithNegotiations(neg *service.OTCNegotiationService) *OTCOptionsHandler {
	cp := *h
	cp.negotiations = neg
	return &cp
}

// ---------- helpers ----------

func ownerTypeFromProto(s string) (model.OwnerType, error) {
	switch s {
	case "client":
		return model.OwnerClient, nil
	case "bank":
		return model.OwnerBank, nil
	default:
		return "", status.Errorf(codes.InvalidArgument, "owner_type must be 'client' or 'bank', got %q", s)
	}
}

// resolveOwnerID returns nil for OwnerBank, &id for OwnerClient (with
// a zero-id rejection to catch accidental "0 means bank" callers).
func resolveOwnerID(ot model.OwnerType, rawID uint64) (*uint64, error) {
	if ot == model.OwnerBank {
		return nil, nil
	}
	if rawID == 0 {
		return nil, status.Error(codes.InvalidArgument, "client owner requires non-zero owner_id")
	}
	id := rawID
	return &id, nil
}

func parseDecimalArg(name, v string) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(v)
	if err != nil {
		return decimal.Zero, status.Errorf(codes.InvalidArgument, "%s must be a decimal: %v", name, err)
	}
	return d, nil
}

func parseTimestampArg(name, v string) (parsed time.Time, err error) {
	// Try RFC3339 then date-only; both are accepted by the gateway.
	if t, e := time.Parse(time.RFC3339, v); e == nil {
		return t, nil
	}
	if t, e := time.Parse("2006-01-02", v); e == nil {
		return t, nil
	}
	return time.Time{}, status.Errorf(codes.InvalidArgument, "%s must be RFC3339 or YYYY-MM-DD", name)
}

func negToProto(n *model.OTCNegotiation) *stockpb.OTCNegotiationResponse {
	if n == nil {
		return nil
	}
	bidderID := uint64(0)
	if n.BidderOwnerID != nil {
		bidderID = *n.BidderOwnerID
	}
	lastOwnerID := uint64(0)
	if n.LastActionByOwnerID != nil {
		lastOwnerID = *n.LastActionByOwnerID
	}
	mintedContractID := uint64(0)
	if n.MintedContractID != nil {
		mintedContractID = *n.MintedContractID
	}
	return &stockpb.OTCNegotiationResponse{
		Id:                    n.ID,
		ParentOfferId:         n.ParentOfferID,
		BidderOwnerType:       string(n.BidderOwnerType),
		BidderOwnerId:         bidderID,
		BidderAccountId:       n.BidderAccountID,
		Quantity:              n.Quantity.String(),
		StrikePrice:           n.StrikePrice.String(),
		Premium:               n.Premium.String(),
		SettlementDate:        n.SettlementDate.UTC().Format(time.RFC3339),
		Status:                n.Status,
		LastActionByOwnerType: n.LastActionByOwnerType,
		LastActionByOwnerId:   lastOwnerID,
		LastActionAt:          n.LastActionAt.UTC().Format(time.RFC3339),
		CreatedAt:             n.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:             n.UpdatedAt.UTC().Format(time.RFC3339),
		Version:               n.Version,
		MintedContractId:      mintedContractID,
	}
}

func negsToProto(rows []model.OTCNegotiation) []*stockpb.OTCNegotiationResponse {
	out := make([]*stockpb.OTCNegotiationResponse, 0, len(rows))
	for i := range rows {
		out = append(out, negToProto(&rows[i]))
	}
	return out
}

// ---------- RPCs ----------

func (h *OTCOptionsHandler) OpenNegotiation(ctx context.Context, in *stockpb.OpenNegotiationRequest) (*stockpb.OTCNegotiationResponse, error) {
	if h.negotiations == nil {
		return nil, status.Error(codes.Unimplemented, "OTCNegotiationService not wired")
	}
	ot, err := ownerTypeFromProto(in.GetBidderOwnerType())
	if err != nil {
		return nil, err
	}
	oid, err := resolveOwnerID(ot, in.GetBidderOwnerId())
	if err != nil {
		return nil, err
	}
	qty, err := parseDecimalArg("quantity", in.GetQuantity())
	if err != nil {
		return nil, err
	}
	strike, err := parseDecimalArg("strike_price", in.GetStrikePrice())
	if err != nil {
		return nil, err
	}
	premium, err := parseDecimalArg("premium", in.GetPremium())
	if err != nil {
		return nil, err
	}
	settle, err := parseTimestampArg("settlement_date", in.GetSettlementDate())
	if err != nil {
		return nil, err
	}
	actingEmp := optionalPtr(in.GetActingEmployeeId())

	neg, err := h.negotiations.OpenNegotiation(ctx, service.OpenNegotiationInput{
		ParentOfferID:       in.GetParentOfferId(),
		BidderOwnerType:     ot,
		BidderOwnerID:       oid,
		BidderAccountID:     in.GetBidderAccountId(),
		Quantity:            qty,
		StrikePrice:         strike,
		Premium:             premium,
		SettlementDate:      settle,
		ActingPrincipalType: in.GetActingPrincipalType(),
		ActingPrincipalID:   in.GetActingPrincipalId(),
		ActingEmployeeID:    actingEmp,
	})
	if err != nil {
		return nil, err
	}
	return negToProto(neg), nil
}

func (h *OTCOptionsHandler) CounterNegotiation(ctx context.Context, in *stockpb.CounterNegotiationRequest) (*stockpb.OTCNegotiationResponse, error) {
	if h.negotiations == nil {
		return nil, status.Error(codes.Unimplemented, "OTCNegotiationService not wired")
	}
	ot, err := ownerTypeFromProto(in.GetCallerOwnerType())
	if err != nil {
		return nil, err
	}
	oid, err := resolveOwnerID(ot, in.GetCallerOwnerId())
	if err != nil {
		return nil, err
	}
	qty, err := parseDecimalArg("quantity", in.GetQuantity())
	if err != nil {
		return nil, err
	}
	strike, err := parseDecimalArg("strike_price", in.GetStrikePrice())
	if err != nil {
		return nil, err
	}
	premium, err := parseDecimalArg("premium", in.GetPremium())
	if err != nil {
		return nil, err
	}
	settle, err := parseTimestampArg("settlement_date", in.GetSettlementDate())
	if err != nil {
		return nil, err
	}
	neg, err := h.negotiations.CounterNegotiation(ctx, service.CounterNegotiationInput{
		NegotiationID:       in.GetNegotiationId(),
		CallerOwnerType:     ot,
		CallerOwnerID:       oid,
		Quantity:            qty,
		StrikePrice:         strike,
		Premium:             premium,
		SettlementDate:      settle,
		ActingPrincipalType: in.GetActingPrincipalType(),
		ActingPrincipalID:   in.GetActingPrincipalId(),
		ActingEmployeeID:    optionalPtr(in.GetActingEmployeeId()),
	})
	if err != nil {
		return nil, err
	}
	return negToProto(neg), nil
}

func (h *OTCOptionsHandler) AcceptNegotiationChain(ctx context.Context, in *stockpb.OTCAcceptNegotiationRequest) (*stockpb.OTCAcceptNegotiationResponse, error) {
	if h.negotiations == nil {
		return nil, status.Error(codes.Unimplemented, "OTCNegotiationService not wired")
	}
	ot, err := ownerTypeFromProto(in.GetCallerOwnerType())
	if err != nil {
		return nil, err
	}
	oid, err := resolveOwnerID(ot, in.GetCallerOwnerId())
	if err != nil {
		return nil, err
	}

	// E2: on_behalf_of_fund_id validation — manager-only, account must be fund's RSD.
	onBehalfOfFundID := in.GetOnBehalfOfFundId()
	if onBehalfOfFundID != 0 {
		if h.fundRepo == nil {
			return nil, status.Error(codes.FailedPrecondition, "fund support not configured on OTC handler")
		}
		fund, ferr := h.fundRepo.GetByID(onBehalfOfFundID)
		if ferr != nil {
			return nil, status.Errorf(codes.NotFound, "fund %d not found", onBehalfOfFundID)
		}
		actingEmpID := int64(in.GetActingEmployeeId())
		if actingEmpID == 0 {
			return nil, status.Error(codes.PermissionDenied, "fund orders require acting_employee_id")
		}
		if actingEmpID != fund.ManagerEmployeeID {
			return nil, status.Error(codes.PermissionDenied, "fund_not_managed_by_actor")
		}
		if in.GetAcceptorAccountId() != fund.RSDAccountID {
			return nil, status.Error(codes.InvalidArgument, "acceptor_account_id must equal fund RSD account for fund orders")
		}
	}

	result, err := h.negotiations.AcceptNegotiation(ctx, service.AcceptNegotiationInput{
		NegotiationID:       in.GetNegotiationId(),
		CallerOwnerType:     ot,
		CallerOwnerID:       oid,
		ActingPrincipalType: in.GetActingPrincipalType(),
		ActingPrincipalID:   in.GetActingPrincipalId(),
		ActingEmployeeID:    optionalPtr(in.GetActingEmployeeId()),
		AcceptorAccountID:   in.GetAcceptorAccountId(),
		OnBehalfOfFundID:    onBehalfOfFundID,
	})
	if err != nil {
		return nil, err
	}
	return &stockpb.OTCAcceptNegotiationResponse{
		Winning:           negToProto(result.WinningNegotiation),
		ParentOfferId:     result.ParentOffer.ID,
		ParentStatus:      result.ParentOffer.Status,
		CancelledSiblings: negsToProto(result.CancelledSiblings),
		Contract:          mintedContractToProto(result.Contract),
	}, nil
}

func (h *OTCOptionsHandler) RejectNegotiation(ctx context.Context, in *stockpb.RejectNegotiationRequest) (*stockpb.OTCNegotiationResponse, error) {
	if h.negotiations == nil {
		return nil, status.Error(codes.Unimplemented, "OTCNegotiationService not wired")
	}
	ot, err := ownerTypeFromProto(in.GetCallerOwnerType())
	if err != nil {
		return nil, err
	}
	oid, err := resolveOwnerID(ot, in.GetCallerOwnerId())
	if err != nil {
		return nil, err
	}
	neg, err := h.negotiations.RejectNegotiation(ctx, service.RejectNegotiationInput{
		NegotiationID:       in.GetNegotiationId(),
		CallerOwnerType:     ot,
		CallerOwnerID:       oid,
		ActingPrincipalType: in.GetActingPrincipalType(),
		ActingPrincipalID:   in.GetActingPrincipalId(),
		ActingEmployeeID:    optionalPtr(in.GetActingEmployeeId()),
	})
	if err != nil {
		return nil, err
	}
	return negToProto(neg), nil
}

func (h *OTCOptionsHandler) CancelNegotiation(ctx context.Context, in *stockpb.CancelNegotiationRequest) (*stockpb.OTCNegotiationResponse, error) {
	if h.negotiations == nil {
		return nil, status.Error(codes.Unimplemented, "OTCNegotiationService not wired")
	}
	ot, err := ownerTypeFromProto(in.GetCallerOwnerType())
	if err != nil {
		return nil, err
	}
	oid, err := resolveOwnerID(ot, in.GetCallerOwnerId())
	if err != nil {
		return nil, err
	}
	neg, err := h.negotiations.CancelNegotiation(ctx, service.CancelNegotiationInput{
		NegotiationID:       in.GetNegotiationId(),
		CallerOwnerType:     ot,
		CallerOwnerID:       oid,
		ActingPrincipalType: in.GetActingPrincipalType(),
		ActingPrincipalID:   in.GetActingPrincipalId(),
		ActingEmployeeID:    optionalPtr(in.GetActingEmployeeId()),
	})
	if err != nil {
		return nil, err
	}
	return negToProto(neg), nil
}

func (h *OTCOptionsHandler) CancelListing(ctx context.Context, in *stockpb.CancelListingRequest) (*stockpb.CancelListingResponse, error) {
	if h.negotiations == nil {
		return nil, status.Error(codes.Unimplemented, "OTCNegotiationService not wired")
	}
	ot, err := ownerTypeFromProto(in.GetCallerOwnerType())
	if err != nil {
		return nil, err
	}
	oid, err := resolveOwnerID(ot, in.GetCallerOwnerId())
	if err != nil {
		return nil, err
	}
	res, err := h.negotiations.CancelListing(ctx, service.CancelListingInput{
		OfferID:             in.GetOfferId(),
		CallerOwnerType:     ot,
		CallerOwnerID:       oid,
		ActingPrincipalType: in.GetActingPrincipalType(),
		ActingPrincipalID:   in.GetActingPrincipalId(),
		ActingEmployeeID:    optionalPtr(in.GetActingEmployeeId()),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*stockpb.OTCNegotiationResponse, 0, len(res.CancelledChains))
	for i := range res.CancelledChains {
		out = append(out, negToProto(&res.CancelledChains[i]))
	}
	return &stockpb.CancelListingResponse{
		OfferId:         res.Offer.ID,
		Status:          res.Offer.Status,
		CancelledChains: out,
	}, nil
}

// ListMyNegotiations merges the caller's LOCAL (intra-bank) and REMOTE
// (cross-bank peer) negotiation chains into one list, stamping provenance
// (kind / routing_number / bank_code) and me_owner on every item (SP-1
// Task 7). The gateway is a uniform passthrough — it forwards the merged
// list and the new fields flow through automatically.
//
// me_owner = "I posted/originated the parent listing", NOT "I'm a party".
// A bidder is never an owner. For LOCAL chains, ListMyNegotiations returns
// only the caller's BIDDER chains (the poster sees their chains via the
// per-listing path), so me_owner is always false there. For REMOTE chains,
// me_owner is true only when WE host the seller/poster side (the row's
// seller routing == our own routing).
//
// Paging: page/page_size apply to the LOCAL set only (the repository
// paginates it). Remote rows are appended in full after the local page —
// they are never silently truncated. total reflects the local total only,
// matching the local pagination semantics; the merged slice length may
// exceed it by the remote count. This is a deliberate "don't drop remote"
// choice; unified cross-source paging is out of scope for SP-1.
func (h *OTCOptionsHandler) ListMyNegotiations(ctx context.Context, in *stockpb.ListMyNegotiationsRequest) (*stockpb.ListNegotiationsResponse, error) {
	if h.negotiations == nil {
		return nil, status.Error(codes.Unimplemented, "OTCNegotiationService not wired")
	}
	ot, err := ownerTypeFromProto(in.GetOwnerType())
	if err != nil {
		return nil, err
	}
	oid, err := resolveOwnerID(ot, in.GetOwnerId())
	if err != nil {
		return nil, err
	}
	rows, total, err := h.negotiations.ListMyNegotiations(ctx, ot, oid, in.GetStatuses(), int(in.GetPage()), int(in.GetPageSize()))
	if err != nil {
		return nil, err
	}

	out := make([]*stockpb.OTCNegotiationResponse, 0, len(rows))
	for i := range rows {
		item := negToProto(&rows[i])
		// LOCAL provenance. These are bidder chains (the service returns
		// only chains where the caller is the bidder), so me_owner is
		// false by the strict rule — a bidder is not an owner.
		item.Kind = "local"
		item.RoutingNumber = h.ownRouting
		item.BankCode = h.ownBankCode
		item.MeOwner = false
		out = append(out, item)
	}

	// REMOTE merge — cross-bank peer negotiations where the caller is a
	// party. Only meaningful for client principals (cross-bank party ids
	// are "client-<N>"); employees acting as the bank have no cross-bank
	// negotiation identity here.
	if h.peerNegs != nil && ot == model.OwnerClient && oid != nil {
		principal := "client-" + strconv.FormatUint(*oid, 10)
		peerRows, perr := h.peerNegs.ListByClient(h.ownRouting, principal, "")
		if perr != nil {
			return nil, status.Errorf(codes.Internal, "list peer negotiations: %v", perr)
		}
		statusFilter := statusSet(in.GetStatuses())
		for i := range peerRows {
			item := peerNegToProto(&peerRows[i], h.ownRouting)
			if item == nil {
				continue
			}
			if statusFilter != nil {
				if _, ok := statusFilter[item.GetStatus()]; !ok {
					continue
				}
			}
			out = append(out, item)
		}
	}

	return &stockpb.ListNegotiationsResponse{
		Negotiations: out,
		Total:        total,
	}, nil
}

// statusSet builds a lookup set from the request's status filter, or nil
// when no filter was supplied (all statuses pass).
func statusSet(statuses []string) map[string]struct{} {
	if len(statuses) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(statuses))
	for _, s := range statuses {
		set[s] = struct{}{}
	}
	return set
}

// peerNegToProto maps a cross-bank peer-negotiation mirror row onto the
// unified OTCNegotiationResponse wire shape (SP-1 Task 7).
//
//   - Id is the local surrogate primary key of the mirror row (so callers
//     can correlate within THIS bank's namespace).
//   - kind = "remote"; routing_number + bank_code identify the
//     COUNTERPARTY/peer bank — the side WE do NOT host. When we host the
//     buyer, the counterparty is the seller's bank; when we host the
//     seller, the counterparty is the buyer's bank.
//   - terms are read from the parsed sitx.OtcOffer carried in OfferJSON.
//   - me_owner = WE host the seller/poster side = SellerRoutingNumber
//     == our own routing — i.e. someone is bidding on a listing we host.
func peerNegToProto(row *model.PeerOtcNegotiation, ownRouting int64) *stockpb.OTCNegotiationResponse {
	if row == nil {
		return nil
	}
	var offer contractsitx.OtcOffer
	if err := json.Unmarshal([]byte(row.OfferJSON), &offer); err != nil {
		log.Printf("WARN peerNegToProto: row %d OfferJSON decode failed: %v", row.ID, err)
		// best-effort: id + status still valid; terms left zero
	}

	meOwner := row.SellerRoutingNumber == ownRouting
	// The counterparty is the side we do NOT host. If we host the seller,
	// the peer bank is the buyer's; otherwise the peer is the seller's.
	peerRouting := row.SellerRoutingNumber
	if meOwner {
		peerRouting = row.BuyerRoutingNumber
	}
	// Use the stored authoritative bank code for the counterparty. The row's
	// PeerBankCode is ALWAYS the counterparty's human-readable code (set at
	// row creation time), so it is more reliable than re-deriving it from
	// the routing number. Fall back to the formatted routing number if the
	// field is somehow empty (legacy rows).
	peerBankCode := row.PeerBankCode
	if peerBankCode == "" {
		peerBankCode = strconv.FormatInt(peerRouting, 10)
	}

	return &stockpb.OTCNegotiationResponse{
		Id:             row.ID,
		Quantity:       strconv.FormatInt(offer.Amount, 10),
		StrikePrice:    offer.PricePerStock.String(),
		Premium:        offer.Premium.String(),
		SettlementDate: offer.SettlementDate,
		Status:         row.Status,
		CreatedAt:      row.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:      row.UpdatedAt.UTC().Format(time.RFC3339),
		Kind:           "remote",
		RoutingNumber:  peerRouting,
		BankCode:       peerBankCode,
		MeOwner:        meOwner,
	}
}

func (h *OTCOptionsHandler) ListNegotiationRevisions(ctx context.Context, in *stockpb.ListNegotiationRevisionsRequest) (*stockpb.ListNegotiationRevisionsResponse, error) {
	if h.negotiations == nil {
		return nil, status.Error(codes.Unimplemented, "OTCNegotiationService not wired")
	}
	ot, err := ownerTypeFromProto(in.GetCallerOwnerType())
	if err != nil {
		return nil, err
	}
	oid, err := resolveOwnerID(ot, in.GetCallerOwnerId())
	if err != nil {
		return nil, err
	}
	revs, err := h.negotiations.ListRevisions(ctx, in.GetNegotiationId(), ot, oid)
	if err != nil {
		return nil, err
	}
	out := make([]*stockpb.OTCNegotiationRevisionResponse, 0, len(revs))
	for i := range revs {
		r := &revs[i]
		out = append(out, &stockpb.OTCNegotiationRevisionResponse{
			Id:                    r.ID,
			NegotiationId:         r.NegotiationID,
			RevisionNumber:        int32(r.RevisionNumber),
			Action:                r.Action,
			Quantity:              r.Quantity.String(),
			StrikePrice:           r.StrikePrice.String(),
			Premium:               r.Premium.String(),
			SettlementDate:        r.SettlementDate.UTC().Format(time.RFC3339),
			ActionByPrincipalType: r.ModifiedByPrincipalType,
			ActionByPrincipalId:   r.ModifiedByPrincipalID,
			CreatedAt:             r.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return &stockpb.ListNegotiationRevisionsResponse{Revisions: out}, nil
}

func (h *OTCOptionsHandler) ListNegotiationsByListing(ctx context.Context, in *stockpb.ListNegotiationsByListingRequest) (*stockpb.ListNegotiationsResponse, error) {
	if h.negotiations == nil {
		return nil, status.Error(codes.Unimplemented, "OTCNegotiationService not wired")
	}
	ot, err := ownerTypeFromProto(in.GetCallerOwnerType())
	if err != nil {
		return nil, err
	}
	oid, err := resolveOwnerID(ot, in.GetCallerOwnerId())
	if err != nil {
		return nil, err
	}
	rows, err := h.negotiations.ListByParentOffer(ctx, in.GetParentOfferId(), ot, oid)
	if err != nil {
		return nil, err
	}
	return &stockpb.ListNegotiationsResponse{
		Negotiations: negsToProto(rows),
		Total:        int64(len(rows)),
	}, nil
}

// GetOfferTimeline returns the parent offer plus every chain's revisions
// merged and sorted by created_at — the poster's cross-chain audit view.
// Audience authorization is enforced in the service layer.
func (h *OTCOptionsHandler) GetOfferTimeline(ctx context.Context, in *stockpb.GetOfferTimelineRequest) (*stockpb.GetOfferTimelineResponse, error) {
	if h.negotiations == nil {
		return nil, status.Error(codes.Unimplemented, "OTCNegotiationService not wired")
	}
	ot, err := ownerTypeFromProto(in.GetCallerOwnerType())
	if err != nil {
		return nil, err
	}
	oid, err := resolveOwnerID(ot, in.GetCallerOwnerId())
	if err != nil {
		return nil, err
	}
	offer, items, err := h.negotiations.OfferTimeline(ctx, in.GetParentOfferId(), ot, oid)
	if err != nil {
		return nil, err
	}
	timeline := make([]*stockpb.OTCTimelineEntry, 0, len(items))
	for i := range items {
		neg := items[i].Negotiation
		r := items[i].Revision
		bidderID := uint64(0)
		if neg.BidderOwnerID != nil {
			bidderID = *neg.BidderOwnerID
		}
		timeline = append(timeline, &stockpb.OTCTimelineEntry{
			NegotiationId:         neg.ID,
			BidderOwnerType:       string(neg.BidderOwnerType),
			BidderOwnerId:         bidderID,
			RevisionNumber:        int32(r.RevisionNumber),
			Action:                r.Action,
			Quantity:              r.Quantity.String(),
			StrikePrice:           r.StrikePrice.String(),
			Premium:               r.Premium.String(),
			SettlementDate:        r.SettlementDate.UTC().Format(time.RFC3339),
			ActionByPrincipalType: r.ModifiedByPrincipalType,
			ActionByPrincipalId:   r.ModifiedByPrincipalID,
			CreatedAt:             r.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return &stockpb.GetOfferTimelineResponse{
		Offer:    toOTCOfferProto(offer, false),
		Timeline: timeline,
	}, nil
}

func optionalPtr(v uint64) *uint64 {
	if v == 0 {
		return nil
	}
	return &v
}

// mintedContractToProto projects a minted OptionContract onto the
// thin wire shape carried in OTCAcceptNegotiationResponse.contract.
// Returns nil for a nil input so the proto field stays unset when
// the negotiation state flipped but the formation saga failed
// (caller can detect this and surface a "minted=false" warning).
func mintedContractToProto(c *model.OptionContract) *stockpb.OTCMintedContract {
	if c == nil {
		return nil
	}
	buyerID := uint64(0)
	if c.BuyerOwnerID != nil {
		buyerID = *c.BuyerOwnerID
	}
	sellerID := uint64(0)
	if c.SellerOwnerID != nil {
		sellerID = *c.SellerOwnerID
	}
	return &stockpb.OTCMintedContract{
		Id:              c.ID,
		OfferId:         c.OfferID,
		BuyerOwnerType:  string(c.BuyerOwnerType),
		BuyerOwnerId:    buyerID,
		SellerOwnerType: string(c.SellerOwnerType),
		SellerOwnerId:   sellerID,
		Ticker:          c.Ticker,
		Quantity:        c.Quantity.String(),
		StrikePrice:     c.StrikePrice.String(),
		PremiumPaid:     c.PremiumPaid.String(),
		PremiumCurrency: c.PremiumCurrency,
		StrikeCurrency:  c.StrikeCurrency,
		SettlementDate:  c.SettlementDate.UTC().Format(time.RFC3339),
		BuyerAccountId:  c.BuyerAccountID,
		SellerAccountId: c.SellerAccountID,
		Status:          c.Status,
		PremiumPaidAt:   c.PremiumPaidAt.UTC().Format(time.RFC3339),
	}
}
