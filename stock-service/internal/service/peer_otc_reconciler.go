// Package service — PeerOTCNegotiationReconciler is the safety-net
// background poller for missed cross-bank negotiation cancels (SP-1 Task 9).
//
// Normal flow: when a peer cancels a negotiation they call our inbound
// DELETE /api/v3/cross-bank-protocol/negotiations/:rid/:id webhook, which
// flips our peer_otc_negotiation.status to "cancelled".  If that webhook
// is lost (network, restart, or peer crash) our row stays "ongoing" forever.
//
// This reconciler ticks every `interval` (default 2 min), lists our local
// "ongoing" rows, and for each row where the COUNTERPARTY bank is
// authoritative (i.e. they issued the ForeignID — identified as "outbound"
// rows where our ownRouting does NOT match the seller's routing), it polls
// the peer's GET /negotiations/{rid}/{id}. If the peer reports non-ongoing
// and we have 2xx, we flip the local row to "cancelled" via the same
// UpdateStatus path the inbound webhook uses.
//
// False-cancel guard: ANY transport error, non-2xx, or JSON parse failure
// on a poll causes that row to be SKIPPED for this tick. We never cancel
// based on ambiguous data.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	contractkafka "github.com/exbanka/contract/kafka"
	transactionpb "github.com/exbanka/contract/transactionpb"
	"github.com/exbanka/stock-service/internal/model"
	"github.com/exbanka/stock-service/internal/repository"
)

// PeerNegStatusFetcher is the narrow HTTP-poll dependency. Defined as a
// function type so tests can inject a fake without standing up an HTTP
// server. The real implementation is newHTTPStatusFetcher.
//
// Returns (isOngoing bool, err error).  err non-nil means "could not
// determine status — skip this row" (false-cancel guard). isOngoing==false
// with err==nil means the peer reports a terminal state.
type PeerNegStatusFetcher func(ctx context.Context, baseURL, apiKey, rid, foreignID string) (isOngoing bool, err error)

// ReconcilerNotifier is the narrow notification dependency. The Kafka
// producer (*kafkaprod.Producer) satisfies it.  nil ⇒ silent (no
// notifications on reconciler-driven cancels).
type ReconcilerNotifier interface {
	PublishGeneralNotification(ctx context.Context, msg contractkafka.GeneralNotificationMessage) error
}

// peerOtcNegRepo is the narrow repo surface the reconciler uses. Satisfied
// by *repository.PeerOtcNegotiationRepository.
type peerOtcNegRepo interface {
	ListOngoing() ([]model.PeerOtcNegotiation, error)
	UpdateStatus(peerCode, foreignID, status string) error
}

// PeerOTCNegotiationReconciler polls every active peer bank for the current
// status of our outbound "ongoing" negotiations, reconciling any that the
// peer has moved to a terminal state.
type PeerOTCNegotiationReconciler struct {
	repo       peerOtcNegRepo
	peerAdmin  transactionpb.PeerBankAdminServiceClient
	fetcher    PeerNegStatusFetcher
	notifier   ReconcilerNotifier // optional; nil ⇒ silent
	ownRouting int64
	interval   time.Duration
}

// NewPeerOTCNegotiationReconciler constructs the reconciler with a real
// HTTP-based fetcher. Pass httpClient=nil to use the default client with a
// 5-second timeout.
func NewPeerOTCNegotiationReconciler(
	repo *repository.PeerOtcNegotiationRepository,
	peerAdmin transactionpb.PeerBankAdminServiceClient,
	httpClient *http.Client,
	ownRouting int64,
	interval time.Duration,
) *PeerOTCNegotiationReconciler {
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	client := httpClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &PeerOTCNegotiationReconciler{
		repo:       repo,
		peerAdmin:  peerAdmin,
		fetcher:    newHTTPStatusFetcher(client),
		ownRouting: ownRouting,
		interval:   interval,
	}
}

// WithNotifier wires the optional notification producer. Returns the
// reconciler so callers can chain.
func (r *PeerOTCNegotiationReconciler) WithNotifier(n ReconcilerNotifier) *PeerOTCNegotiationReconciler {
	r.notifier = n
	return r
}

// Run blocks until ctx is cancelled. Runs an initial reconcile immediately,
// then ticks at r.interval. Suitable for standalone use in tests.
func (r *PeerOTCNegotiationReconciler) Run(ctx context.Context) {
	r.reconcile(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.reconcile(ctx)
		}
	}
}

// RunOnce executes a single reconcile cycle. Used by the cronreg-gated
// goroutine in main.go so each tick is registered with the cron registry
// for operator visibility and manual triggering.
func (r *PeerOTCNegotiationReconciler) RunOnce(ctx context.Context) {
	r.reconcile(ctx)
}

// reconcile performs one full poll cycle. Per-row errors are logged and
// skipped — the loop never aborts on a single failure.
func (r *PeerOTCNegotiationReconciler) reconcile(ctx context.Context) {
	rows, err := r.repo.ListOngoing()
	if err != nil {
		log.Printf("peer-otc-reconciler: list ongoing failed: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}

	// Use a per-cycle context with a generous timeout so the full fan-out
	// of HTTP polls doesn't run forever if peers are slow.
	cycleCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Resolve all active peers once per cycle (avoids an RPC per row).
	peerMap, err := r.buildPeerMap(cycleCtx)
	if err != nil {
		log.Printf("peer-otc-reconciler: resolve peers failed: %v", err)
		return
	}

	for i := range rows {
		row := &rows[i]
		if err := r.reconcileRow(cycleCtx, row, peerMap); err != nil {
			log.Printf("peer-otc-reconciler: row peer=%s fid=%s error: %v",
				row.PeerBankCode, row.ForeignID, err)
		}
	}
}

// peerEntry holds resolved base URL + API key for one peer bank.
type peerEntry struct {
	baseURL string
	apiKey  string
}

func (r *PeerOTCNegotiationReconciler) buildPeerMap(ctx context.Context) (map[string]peerEntry, error) {
	list, err := r.peerAdmin.ListPeerBanks(ctx, &transactionpb.ListPeerBanksRequest{ActiveOnly: true})
	if err != nil {
		return nil, err
	}
	out := make(map[string]peerEntry, len(list.GetPeerBanks()))
	for _, p := range list.GetPeerBanks() {
		resp, err := r.peerAdmin.ResolvePeerByBankCode(ctx,
			&transactionpb.ResolvePeerByBankCodeRequest{BankCode: p.GetBankCode()})
		if err != nil {
			log.Printf("peer-otc-reconciler: resolve peer %s failed: %v", p.GetBankCode(), err)
			continue
		}
		full := resp.GetPeerBank()
		if full == nil || !full.GetActive() {
			continue
		}
		out[p.GetBankCode()] = peerEntry{
			baseURL: strings.TrimRight(full.GetBaseUrl(), "/"),
			apiKey:  full.GetApiTokenPlaintext(),
		}
	}
	return out, nil
}

// reconcileRow checks one ongoing row against its authoritative peer bank.
// "Authoritative" = the counterparty who issued ForeignID (PeerBankCode).
// We skip rows where WE are the seller and use our ownRouting — those are
// inbound negotiations where WE are authoritative, not the peer.
//
// The decision logic:
//  1. Is this a row where the counterparty is authoritative?
//     We identify this by checking whether the seller routing == ownRouting
//     AND seller bank == our bank (i.e. we issued the listing, so the buyer's
//     bank — PeerBankCode — issued the ForeignID). In that case we ARE the
//     seller bank and the counterparty (buyer bank = PeerBankCode) is
//     authoritative over the negotiation id.
//
//     Alternatively, if the buyer routing == ownRouting, WE are the buyer
//     and the seller bank (PeerBankCode) is authoritative.
//
//     In both cases PeerBankCode is the bank that holds the authoritative
//     copy of the negotiation, so we poll it.
//
//  2. Poll GET /negotiations/{rid}/{id} on the peer. Strict 2xx-only guard.
//  3. If peer says not-ongoing and we're still ongoing → flip to cancelled.
func (r *PeerOTCNegotiationReconciler) reconcileRow(
	ctx context.Context,
	row *model.PeerOtcNegotiation,
	peerMap map[string]peerEntry,
) error {
	peer, ok := peerMap[row.PeerBankCode]
	if !ok {
		// Peer not active or not reachable — skip silently.
		return nil
	}

	// Determine the rid to use for the GET request. The ForeignID was
	// minted by the peer (PeerBankCode), so we use the peer's routing
	// number. We derive routing from the BuyerRoutingNumber or
	// SellerRoutingNumber that belongs to the peer (not us).
	peerRouting := r.peerRoutingForRow(row)
	if peerRouting == 0 {
		// Can't determine peer routing — skip.
		return nil
	}

	ridStr := fmt.Sprintf("%d", peerRouting)
	ongoing, err := r.fetcher(ctx, peer.baseURL, peer.apiKey, ridStr, row.ForeignID)
	if err != nil {
		// False-cancel guard: any error ⇒ skip this row.
		return fmt.Errorf("poll peer %s: %w", row.PeerBankCode, err)
	}
	if ongoing {
		// Peer says still ongoing — nothing to do.
		return nil
	}

	// Peer reports terminal status and we're still "ongoing" — reconcile.
	log.Printf("peer-otc-reconciler: reconciling missed cancel peer=%s fid=%s (peer reports non-ongoing)",
		row.PeerBankCode, row.ForeignID)
	if err := r.repo.UpdateStatus(row.PeerBankCode, row.ForeignID, "cancelled"); err != nil {
		return fmt.Errorf("update status: %w", err)
	}

	// Best-effort notification to the local party (mirrors what the inbound
	// DELETE webhook would have sent). Failures are logged but don't
	// block the reconcile.
	r.notifyLocalParty(ctx, row)
	return nil
}

// peerRoutingForRow returns the routing number of the peer bank (PeerBankCode)
// by choosing whichever routing number in the row does NOT match ownRouting.
// If both sides are ownRouting (shouldn't happen in practice), returns 0.
func (r *PeerOTCNegotiationReconciler) peerRoutingForRow(row *model.PeerOtcNegotiation) int64 {
	if row.BuyerRoutingNumber != r.ownRouting {
		return row.BuyerRoutingNumber
	}
	if row.SellerRoutingNumber != r.ownRouting {
		return row.SellerRoutingNumber
	}
	// Both sides are ownRouting — intra-bank or malformed row; skip.
	return 0
}

// notifyLocalParty sends a best-effort OTC_OFFER_CANCELLED notification to
// whichever party in the row is a local client (client-N on ownRouting).
func (r *PeerOTCNegotiationReconciler) notifyLocalParty(ctx context.Context, row *model.PeerOtcNegotiation) {
	if r.notifier == nil {
		return
	}
	uid, ok := localClientID(r.ownRouting, row.BuyerRoutingNumber, row.BuyerID)
	if !ok {
		uid, ok = localClientID(r.ownRouting, row.SellerRoutingNumber, row.SellerID)
	}
	if !ok || uid == 0 {
		return
	}
	if err := r.notifier.PublishGeneralNotification(ctx, contractkafka.GeneralNotificationMessage{
		UserID:  uid,
		Type:    "OTC_OFFER_CANCELLED",
		Data:    map[string]string{},
		RefType: "otc_negotiation",
		RefID:   row.ID,
	}); err != nil {
		log.Printf("peer-otc-reconciler: notification for user %d failed: %v", uid, err)
	}
}

// localClientID resolves "client-N" → N when routing matches ownRouting.
func localClientID(ownRouting, routing int64, participantID string) (uint64, bool) {
	if routing != ownRouting {
		return 0, false
	}
	const prefix = "client-"
	if !strings.HasPrefix(participantID, prefix) {
		return 0, false
	}
	var id uint64
	if _, err := fmt.Sscanf(participantID[len(prefix):], "%d", &id); err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

// peerNegStatusResponse is the minimal wire shape of GET /negotiations/{rid}/{id}.
// The full OtcNegotiation struct isn't needed — we only care about isOngoing.
type peerNegStatusResponse struct {
	IsOngoing bool `json:"isOngoing"`
}

// newHTTPStatusFetcher returns a PeerNegStatusFetcher backed by the given
// *http.Client. It calls GET {baseURL}/negotiations/{rid}/{id} with
// X-Api-Key authentication and returns (isOngoing, nil) on success, or
// (false, err) on any transport error or non-2xx response (false-cancel
// guard — caller skips the row on error).
func newHTTPStatusFetcher(client *http.Client) PeerNegStatusFetcher {
	return func(ctx context.Context, baseURL, apiKey, rid, foreignID string) (bool, error) {
		url := baseURL + "/negotiations/" + rid + "/" + foreignID
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("X-Api-Key", apiKey)

		resp, err := client.Do(req)
		if err != nil {
			return false, fmt.Errorf("http get: %w", err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			// Non-2xx — could be a temporary peer error; false-cancel guard.
			return false, fmt.Errorf("peer returned status %d: %s", resp.StatusCode, string(body))
		}

		var parsed peerNegStatusResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return false, fmt.Errorf("parse response: %w", err)
		}
		return parsed.IsOngoing, nil
	}
}
