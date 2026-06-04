# Unified OTC — SP-1: unified read model + reconciliation + `me_owner`

**Date:** 2026-06-04
**Status:** Approved (design)
**Parent:** `2026-06-04-unified-otc-local-remote-umbrella-design.md`
**Scope rule:** SP-1 unifies **reads only**. Write/action routes are untouched and still split (local `/otc/options/...` vs remote `/me/peer-otc/...`). SP-2 unifies writes.

## 1. Goal

After SP-1, every OTC **read** the frontend makes returns one shape that covers local and remote uniformly, with stable local ids for remote resources and a `me_owner` flag — so the FE never branches on `kind` to *read* offers, my-negotiations, history, or contracts. A reconciliation poll keeps remote rows honest: a peer-cancelled/finished offer or negotiation becomes `cancelled` on our side.

## 2. In scope / out of scope

**In scope**
- A **persistent remote-offer mirror** so remote offers have stable local surrogate ids and survive cache rebuilds.
- Unifying the read endpoints (discovery, my-negotiations, history, contracts, detail, on-listing, timeline) to serve local + remote from one read model.
- `me_owner` on every OTC offer/negotiation/contract read response.
- A reconciliation poller: diff persisted remote offers + active remote negotiations against successful peer responses; terminal/gone → `cancelled` + notify.

**Out of scope (SP-2/SP-3)**
- Changing/deleting any write or `/me/peer-otc/*` route (SP-2).
- Retiring `peer_otc_negotiation` / `PeerOptionContract` (SP-2 — SP-1 *reads through* them via a bridge).
- Employee/bank wire identity and bidding on bank-owned remote offers (SP-3).

## 3. Data model

### 3.1 New: `remote_otc_offer` (persistent mirror) — stock-service
The offer cache (`stock-service/internal/otccache/option_cache.go`) is rebuild-from-scratch in memory; remote offers carry the peer's id, not a stable local one, and there is no record once a peer drops an offer. Add a persisted mirror:

```go
type RemoteOTCOffer struct {
    ID                 uint64    `gorm:"primaryKey;autoIncrement"` // local surrogate id (the unified :id)
    PeerRoutingNumber  int64     `gorm:"uniqueIndex:ux_remote_offer,priority:1;not null"`
    ForeignOfferID     string    `gorm:"uniqueIndex:ux_remote_offer,priority:2;size:128;not null"`
    BankCode           string    `gorm:"size:8;not null"`
    SellerID           string    `gorm:"size:128"`  // SI-TX wire id ("client-<N>" | "employee-<N>"; legacy "bank"/"0" tolerated on read)
    Direction          string    `gorm:"size:24"`   // sell_initiated | buy_initiated
    Ticker             string    `gorm:"size:32"`
    Amount             int64
    StrikePrice        decimal.Decimal `gorm:"type:numeric(20,8)"`
    StrikeCurrency     string    `gorm:"size:8"`
    Premium            decimal.Decimal `gorm:"type:numeric(20,8)"`
    PremiumCurrency    string    `gorm:"size:8"`
    SettlementDate     string    `gorm:"size:64"` // RFC3339 UTC as published by the peer
    Status             string    `gorm:"size:24;index;not null;default:'open'"` // open | cancelled (terminal-on-peer)
    LastSeenAt         time.Time `gorm:"index"`  // last successful peer poll that still listed it
    PeerCreatedAt      string    `gorm:"size:64"`
    CreatedAt          time.Time
    UpdatedAt          time.Time
}
```

- `(PeerRoutingNumber, ForeignOfferID)` is the natural key; `ID` is the stable local surrogate id used as the unified `:id`. Surrogate ids are minted once and reused across refreshes (upsert via `clause.OnConflict` on the natural key, never SELECT-then-INSERT).
- **No `Version`/optimistic-lock field.** This mirror is written only by the single-threaded option refresher (upsert) and the per-peer reconcile bulk flip (`SkipHooks`) — there is no concurrent read-modify-write on its rows, so the optimistic-locking requirement does not apply. (If SP-2 folds this into a concurrently-written table, locking is added there.)
- `SettlementDate`/`PeerCreatedAt` are stored as the peer's published RFC3339 strings (the mirror reflects the wire verbatim; no parse/format round-trip).

> Decision: a **separate** `remote_otc_offer` table (not new rows in `OTCOffer`) keeps SP-1 strictly additive and read-only — local writes/cascade/accept logic on `OTCOffer` is not perturbed. SP-2 decides whether to fold this into `OTCOffer` when it converges writes.

### 3.2 Reused as-is (read bridge)
- Remote negotiations: `PeerOtcNegotiation` (`peer_otc_negotiation.go`) — read through it; map to the unified read shape. Its existing local `ID` is the surrogate negotiation id surfaced to the FE.
- Remote contracts: `PeerOptionContract` (`peer_option_contract.go`) — read through it.

## 4. Reconciliation poller (stock-service)

A background goroutine (context-cancellable, `defer ticker.Stop()`, `select { case <-ticker.C / case <-ctx.Done() }` per the Concurrency requirement). Two diffs, each acting **only on a successful peer response** (a transport error or non-2xx is "unknown", never a cancel — guards against false-cancels when a peer is briefly down):

1. **Offers.** The existing peer poll (`OptionRefresher.fetchPeer` → `GET /cross-bank-protocol/public-option-offers`) is extended to **upsert** each listed offer into `remote_otc_offer` (refreshing `LastSeenAt`). After a *successful* poll of a peer, any `open` row for that peer **not** in the response → flip to `cancelled` + emit notification to any local client who holds an active negotiation against it.
2. **Negotiations.** For each active (`ongoing`) `peer_otc_negotiation` row where we host a party, poll the counterparty `GET /cross-bank-protocol/negotiations/:rid/:id`; if the peer reports terminal (`cancelled`/`accepted`/`expired`) and we still show `ongoing`, reconcile our status to match. Reuses the inbound webhook reconcile paths (`peer_otc_handler.go` Delete/Accept) for the actual state flip so behavior is identical to a webhook-driven cancel.

Cadence: align with the otccache refresh interval for offers; a slower tick (≈ the SI-TX reconciler's minutes-scale) for negotiation status. Both configurable via env.

Kafka/notifications: a peer-driven cancel reuses the existing `OTC_OFFER_CANCELLED` / `OTC_OFFER_CASCADE_CANCELLED` notification path so reconciled cancels are indistinguishable from webhook cancels to the client.

## 5. `me_owner` on reads

Add `me_owner bool` to every OTC offer/negotiation/contract item returned by the read endpoints. Computed gateway-side from the resolved identity (`middleware.ResolveIdentity` / `OwnerIsBankIfEmployee`, already on these routes):
- **client** principal → `me_owner = (resource.owner_id == principal_id && !bank_owned)`.
- **employee** (no on-behalf) → `me_owner = bank_owned`.
- **employee on-behalf-of-client** → `me_owner = (resource.owner_id == on_behalf_of_client_id)`.

For an offer, "owner" = the seller/poster (local) or the listing's seller (remote — from our side a remote listing is never bank/owner-local, so `me_owner=false` unless we posted it, which makes it local). For a negotiation, "owner" = the chain's bidder party we host. For a contract, "owner" = the holder side we host. This mirrors the existing Resource Ownership Verification rules so the FE flag matches server-side authorization exactly.

## 6. Endpoints touched (reads only — no path/verb changes)

| Endpoint | Change |
|---|---|
| `GET /api/v3/otc/options` (discovery) | Already merges local+remote. Now: remote offers carry the stable surrogate `id` from `remote_otc_offer`; every item gains `me_owner`. |
| `GET /api/v3/otc/options/:id` | Resolve `:id` against local `OTCOffer` **and** `remote_otc_offer`; return unified shape + `me_owner` + `kind`. |
| `GET /api/v3/me/otc/options/negotiations` | Merge local `OTCNegotiation` rows with `peer_otc_negotiation` rows (read bridge); each item carries `kind`, surrogate ids, `me_owner`. |
| `GET /api/v3/me/otc/history` | Include remote negotiations in the caller's history. |
| `GET /api/v3/me/otc/contracts`, `GET /api/v3/otc/contracts/:id` | Merge local `OptionContract` with `PeerOptionContract`; `me_owner` + `kind`. |
| `GET /api/v3/otc/options/:id/negotiations`, `/timeline` | For a remote `:id`, return only the caller's own chain (or empty for non-parties) — never other parties' chains (umbrella req. 6). |

Response additions are **optional new fields** (`me_owner`, `kind`, and a stable numeric `id` for remote) — backward-compatible per the API Versioning requirement; no existing field changes type or disappears. VERSION bumps **MINOR**.

## 7. gRPC / service surface

- stock-service gains repo + service methods for `remote_otc_offer` (upsert, list, get-by-surrogate-id, reconcile-cancel) and unified read methods that merge local + bridged-remote. Prefer extending the existing `OTCOptionsService` read RPCs to return a `kind` + remote rows over adding parallel RPCs (avoid new stale surface).
- No new outbound wire calls; reconciliation reuses existing peer HTTP clients and `PeerBankAdminService.ResolvePeerByBankCode`.

## 8. Removed / retired in SP-1

Minimal by design (writes untouched):
- The gateway's bespoke in-line remote-offer JSON shaping in discovery is replaced by the mirror-backed unified shape; remove the now-dead shaping branch.
- No routes or RPCs are deleted in SP-1. (The large deletions — `/me/peer-otc/*`, the `peer_otc_negotiation` mirror, now-unused `PeerOTCService` RPCs — are SP-2/SP-3 per the umbrella's clean-cut requirement.)

## 9. Testing

- **Unit (stock-service):** `remote_otc_offer` upsert is idempotent on the natural key and preserves the surrogate id across refreshes; reconcile flips `open→cancelled` only when a *successful* peer poll omits the offer (a poll error is a no-op); optimistic-lock conflict path; `me_owner` computation for client / employee / on-behalf.
- **Unit (gateway):** each touched read handler returns `me_owner` + `kind` + surrogate id for both local and remote fixtures; remote `:id` resolves to the mirror; on-listing/timeline returns only the caller's own chain for remote.
- **Integration (`test-app/workflows/`):** discovery returns a remote offer with a stable numeric id and `me_owner:false`; `GET /otc/options/:id` resolves that remote id; `GET /me/otc/options/negotiations` returns a caller's remote chain alongside a local one; a simulated peer-cancel (offer removed from the peer's public list) flips our mirror row to `cancelled` and notifies the holder; a peer poll *error* does **not** cancel. Use the two-stack harness for a live remote offer.
- Validate spec behavior (bodies, surrogate-id stability, reconciliation side effects), not just status codes. `make test` + `make lint` clean on touched services.

## 10. Docs

Update `docs/api/REST_API_v3.md` (new `me_owner`/`kind`/remote-id fields on the listed read endpoints), Swagger annotations + regenerate, and `Specification.md` (new `remote_otc_offer` entity §18, new reconciliation business rule §21, response-field additions §17).
