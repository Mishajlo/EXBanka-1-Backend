# API Route Changes — canonical migration log

This file is the **canonical old→new diff** for the EXBanka REST API. It records
routes that were **removed, renamed, or re-shaped** plus the **request/response
body changes** that came with them. The current, working API surface is documented
in [`REST_API_v3.md`](./REST_API_v3.md) — that file is **current-state only** and
no longer carries migration prose; this file holds the history.

All routes are served under `/api/v3/`. v1 and v2 were retired (plan E, 2026-04-27):
any `/api/v1/*` or `/api/v2/*` request returns **404**.

---

## 1. Unified OTC marketplace + bank-as-cross-bank-principal effort

The OTC (over-the-counter) surface was consolidated so that **one route serves
both intra-bank (LOCAL) and cross-bank (REMOTE) flows**. The frontend no longer
chooses a "peer" route — dispatch happens inside stock-service based on the
listing/contract routing. The separate `/me/peer-otc/*` client surface and the
peer-exercise route were deleted in a clean cut.

### 1.1 Removed routes → unified replacements

| OLD route (removed) | NEW route (unified, local + cross-bank) |
|---|---|
| `POST /api/v3/me/peer-otc/negotiations` | `POST /api/v3/otc/options/:id/bid` (open a chain; `:id` = the discovery row's `local_id`, local or remote) |
| `GET /api/v3/me/peer-otc/negotiations` | `GET /api/v3/me/otc/options/negotiations` (caller's chains, LOCAL + REMOTE merged; remote rows carry `kind="remote"`) |
| `PUT /api/v3/me/peer-otc/negotiations/:rid/:id` | `POST /api/v3/me/otc/options/:id/negotiations/:nid/counter` |
| `POST /api/v3/me/peer-otc/negotiations/:rid/:id/accept` | `POST /api/v3/me/otc/options/:id/negotiations/:nid/accept` |
| `DELETE /api/v3/me/peer-otc/negotiations/:rid/:id` | `DELETE /api/v3/me/otc/options/:id/negotiations/:nid` (bidder withdraws) |
| `POST /api/v3/me/otc/contracts/peer/:id/exercise` | `POST /api/v3/otc/contracts/:id/exercise` (unified; cross-bank path gained an optional `buyer_account_number` body field) |

These removals landed in the SP-2b clean cut (commit `eebdcdc`); 4 dead
`PeerOTCService` RPCs and the gateway `PeerOTCInitiateHandler` were retired with
them (`8313fa1`). The cross-bank behaviour they used to carry is now dispatched by
stock-service behind the unified routes above.

> The peer-facing **wire-protocol** routes under `/api/v3/cross-bank-protocol/*`
> (`/interbank`, `/public-stock`, `/public-option-offers`, `/negotiations/*`,
> `/user/*`) are UNCHANGED — they are the frozen SI-TX protocol that cohort banks
> call. Only the *client-facing* `/me/peer-otc/*` convenience surface was removed.

### 1.2 OTC offer namespace rename (Phase 8)

The earlier single-chain `/otc/offers/...` surface was replaced by two separated
marketplaces: `/otc/stocks/...` (no negotiation) and `/otc/options/...` (parallel
per-bidder negotiation chains). Mapping:

| OLD route (removed) | NEW route |
|---|---|
| `GET /api/v3/otc/offers` | `GET /api/v3/otc/stocks` (and `GET /api/v3/otc/options` for options) |
| `POST /api/v3/otc/offers/:id/buy` | `POST /api/v3/otc/stocks/:id/buy` |
| `POST /api/v3/otc/offers/:id/buy-on-behalf` | `POST /api/v3/otc/stocks/:id/buy-on-behalf` |
| `POST /api/v3/otc/offers` (create listing) | `POST /api/v3/me/otc/options` |
| `POST /api/v3/otc/offers/:id/counter` | `POST /api/v3/me/otc/options/:id/negotiations/:nid/counter` |
| `POST /api/v3/otc/offers/:id/accept` | `POST /api/v3/me/otc/options/:id/negotiations/:nid/accept` |
| `POST /api/v3/otc/offers/:id/reject` | `POST /api/v3/me/otc/options/:id/negotiations/:nid/reject` |
| `GET /api/v3/otc/offers/:id` | `GET /api/v3/otc/options/:id` |
| `GET /api/v3/me/otc/offers` | `GET /api/v3/me/otc/options` |
| `POST /api/v3/me/portfolio/:id/make-public` | `POST /api/v3/me/otc/stocks` with `direction=sell` |

New routes introduced by the negotiation-chain model (no OLD equivalent):

| NEW route | Purpose |
|---|---|
| `POST /api/v3/otc/options/:id/bid` | Open a negotiation chain (one per bidder per listing) |
| `DELETE /api/v3/me/otc/options/:id/negotiations/:nid` | Bidder withdraws their chain |
| `DELETE /api/v3/me/otc/options/:id` | Initiator cancels the listing (cascade-cancels child chains) |
| `GET /api/v3/otc/options/:id/negotiations` | Every chain on a listing (poster / `otc.read.all` only) |
| `GET /api/v3/otc/options/:id/timeline` | Cross-chain interaction timeline (poster / `otc.read.all` only) |
| `GET /api/v3/me/otc/options/negotiations` | Caller's chains (LOCAL + REMOTE merged) |
| `GET /api/v3/me/otc/options/negotiations/:nid/revisions` | One chain's full revision history |
| `GET /api/v3/me/otc/options/posted` | History of every listing the caller posted (any status) |
| `GET /api/v3/otc/options` | Unified local + cross-bank discovery of open listings |
| `POST /api/v3/me/otc/stocks`, `GET /api/v3/me/otc/stocks`, `DELETE /api/v3/me/otc/stocks/:id` | Caller's stock offers (sell + buy direction) |
| `POST /api/v3/otc/stocks/:id/sell` | Fill a buy-direction offer with the caller's shares |

### 1.3 Removed (no replacement)

| OLD route (removed) | Note |
|---|---|
| `POST /api/v3/me/portfolio/:id/make-public` | Folded into `POST /api/v3/me/otc/stocks` (`direction=sell`) — see 1.2 |

---

## 2. OTC request/response body changes

### 2.1 Offer read bodies (`OTCOfferResponse` — surfaced by `GET /api/v3/otc/options`, `GET /api/v3/otc/options/:id`, `GET /api/v3/me/otc/options`, create/counter/cancel responses)

**Fields gained** (all additive):

| Field | Type | Meaning |
|---|---|---|
| `kind` | string | `"local"` (this bank hosts the listing) or `"remote"` (peer-bank mirror). Derived from the new `local` discriminator (see below). |
| `me_owner` | bool | `true` only when the acting caller owns the listing (its poster/seller); always `false` for remote rows. |
| `routing_number` | int64 | Hosting bank's routing number. |
| `bank_code` | string | Hosting bank's 3-digit code. |
| `seller_id` | string | The listing initiator's LOCAL SI-TX seller identity: `"bank"` for a bank-owned listing, `"client-<N>"` for a client-owned one. Now stamped uniformly on every single-offer response (create / detail / counter / cancel). Distinct from the cross-bank wire id `"employee-<N>"` composed only on the SI-TX publish path. |
| `best_bid` | string (decimal) | MAX premium across active (`open`/`countered`) chains on a `sell_initiated` listing. Omitted when no active chains. |
| `best_ask` | string (decimal) | MIN premium across active chains on a `buy_initiated` listing. Omitted when no active chains. |
| `active_chains_count` | int | Count of `open`/`countered` chains. Omitted when zero. |
| `local_id` | uint64 | Stable local surrogate id (on discovery-list rows). Use as `:id` in `GET /api/v3/otc/options/:id`. |
| `my_negotiation_id` | uint64 | The authenticated caller's own (bidder) chain id against this offer, so the FE can jump to it. Omitted/0 when the caller has no chain (a poster who never bid is `me_owner=true` with no `my_negotiation_id` — the two are independent). |
| `my_negotiation_status` | string | That chain's status. Omitted when `my_negotiation_id` absent. |

**Access change:** `GET /api/v3/otc/options/:id` (offer detail) is now **public to any
authenticated caller** (was participant-gated). A non-participant receives the offer with
`me_owner=false` and an **empty `revisions[]`** (the counter history stays gated to
participants). Previously a non-participant got a `not_found` masked as 500.

**Internal discriminator:** an explicit `local bool` column was added to the three
unified OTC tables (`OTCOffer`, `OTCNegotiation`, `OptionContract`) as the
authoritative local-vs-remote flag (replacing the implicit `routing_number == own`
check). `kind` is derived from it. The column is internal; the JSON discriminator
clients read is `kind`.

### 2.2 Negotiation read bodies (`OTCNegotiationResponse`)

**Fields gained:** `kind`, `routing_number`, `bank_code`, `me_owner`
(`true` only when the caller is the parent listing's poster — someone bidding on
*my* listing; a chain the caller opened as the bidder is `false`), and
`minted_contract_id` (uint64, 0 when absent; set on accepted rows that minted a
contract).

### 2.3 Contract read bodies (`OptionContractResponse` — `GET /api/v3/me/otc/contracts`, `GET /api/v3/otc/contracts/:id`)

**Fields gained:** `kind`, `routing_number`, `bank_code`, `me_owner`
(`true` only when the caller is the contract's **buyer/holder** — the formed option
is the buyer's owned asset, so the seller/writer is `false`; note this is the
opposite owner-rule from offers/negotiations).

**Removed:** `peer_contracts[]` and `peer_total` are **no longer returned** by
`GET /api/v3/me/otc/contracts` (proto fields 3 and 4 reserved in `ListContractsResponse`).
Remote contracts now appear exclusively in the unified `contracts[]` with `kind="remote"`.

### 2.4 Bid / counter request validation

`POST /api/v3/otc/options/:id/bid` and `POST /api/v3/me/otc/options/:id/negotiations/:nid/counter`
now **reject non-positive `quantity` / `strike_price`** with HTTP 400. `premium`
must be `>= 0` (zero allowed, negative rejected). The gateway validates these as
strict decimals before forwarding (commit `51afd79`).

### 2.5 Accept response

`POST /api/v3/me/otc/options/:id/negotiations/:nid/accept` response gained
**`cross_bank_transaction_id`** (string, optional). Populated **only** when the
accepted chain resolves to a folded-in cross-bank (REMOTE) chain — it carries the
peer bank's SI-TX `transactionId` so the FE can poll
`GET /api/v3/me/otc/transactions/:txid/status`. Empty string for a LOCAL accept
(commit `92f96ff`).

### 2.6 Exercise request

`POST /api/v3/otc/contracts/:id/exercise` (the unified exercise route) gained an
optional **`buyer_account_number`** body field for the **cross-bank (REMOTE)** path
— the buyer's currency account that pays the strike. Required for a remote
contract, ignored for a local one. The gateway gates it before forwarding (`403`
on mismatch) authoritatively for all principals: a client must own it, a
bank-acting employee must bind a BANK account, an on-behalf employee must bind that
client's account.

---

## 3. Bank as a first-class cross-bank OTC principal (SP-3)

No route changes — behavioural only. An employee acting **as the bank** can now
bid / counter / accept / reject / cancel / exercise in the cross-bank OTC
marketplace exactly like a client, settling against **BANK** accounts/holdings.
On the SI-TX wire, bank-owned offers/bids publish the stable identity
`employee-<ActingEmployeeID>` (never the legacy literal `"bank"`); inbound peers
that still send `"bank"` are parsed identically. The bank now sees its own remote
chains in every read view (`GET /api/v3/me/otc/options/negotiations`, the
per-listing `negotiations`/`timeline` views, history, and the `my_negotiation_id`
stamp), matched by the `employee-<N>` prefix. Client and bank principal scopes
never cross.

---

## 4. Earlier additive release — TODO_final + options-tax (SP1–SP6, VERSION 1.0.0 → 1.6.0)

This release was **fully backward-compatible**: it added routes, optional query
params, and additive response fields only. No existing route, method, status code,
auth requirement, request field, or response field was removed, renamed, or
re-typed.

### 4.1 New routes

| Method | Path | Auth | Sub-project |
|---|---|---|---|
| `GET` | `/api/v3/admin/audit/business-actions` | `admin.audit.view` | SP2 — business audit log |
| `GET` | `/api/v3/me/watchlists` | AnyAuth | SP6 — named watchlists |
| `POST` | `/api/v3/me/watchlists` | AnyAuth | SP6 |
| `DELETE` | `/api/v3/me/watchlists/:watchlist_id` | AnyAuth | SP6 |
| `GET` | `/api/v3/me/watchlists/:watchlist_id/items` | AnyAuth | SP6 |
| `POST` | `/api/v3/me/watchlists/:watchlist_id/items` | AnyAuth | SP6 |
| `DELETE` | `/api/v3/me/watchlists/:watchlist_id/items/:listing_id` | AnyAuth | SP6 |

**SP2 — `GET /api/v3/admin/audit/business-actions`** — who changed an employee limit,
reset a usedLimit, approved/rejected an order, changed permissions, or triggered
manual tax collection. Query: `action` (`limit.set`\|`limit.used_reset`\|`order.approve`\|`order.decline`\|`permissions.set`\|`tax.collect`),
`target_type` (`employee`\|`order`\|`role`\|`tax`), `actor_id`, `since`/`until`
(`YYYY-MM-DD`), `page`/`page_size`. Returns `{entries:[{id, action, actor_id,
target_type, target_id, detail, timestamp}], total, page, page_size}`.

**SP6 — named watchlists** — a user may keep several named lists. `POST /me/watchlists {name}`
(1–64 chars, idempotent on name). `GET /me/watchlists` → `{watchlists:[{id, name,
item_count, created_at}]}` (always includes the lazily-created default). Per-list
item ops mirror the legacy single-list ops but are scoped to `:watchlist_id`. A
list is owner-scoped (403 if not yours); the same listing may live in multiple lists.

### 4.2 Changed routes (additive only — existing clients unaffected)

| Method | Path | What was added |
|---|---|---|
| `GET` | `/api/v3/investment-funds` | New query params `sort_by` (`name`\|`value`\|`profit`\|`annualized_return`\|`volatility`\|`reward_to_variability`\|`max_drawdown`) + `sort_order` (`asc`\|`desc`). New per-fund fields: `annualized_return_pct, volatility_pct, reward_to_variability, max_drawdown_pct, metrics_available, dividend_mode`. (SP3, SP4) |
| `GET` | `/api/v3/investment-funds/:id` | New fields: the four metrics above + `metrics_available`, `dividend_mode`, plus `history` (daily NAV series `[{date, total_value_rsd}]`) and `average_history` (system-average series, indexed to 100). (SP3, SP4) |
| `POST` | `/api/v3/investment-funds` | New optional body field `dividend_mode` (`payout` default \| `reinvest`). (SP4) |
| `PUT` | `/api/v3/investment-funds/:id` | New optional body field `dividend_mode`. (SP4) |
| `GET/POST/DELETE` | `/api/v3/me/watchlist[/:listing_id]` | Behaviour clarified (not changed): the legacy single-list routes operate on the owner's lazily-created default "My Watchlist". Same request/response shapes. (SP6) |

### 4.3 Behaviour-only (no REST changes)

- **SP1 (options/premium tax)** — internal tax recording on the existing OTC accept/exercise sagas and the monthly `POST /api/v3/tax/collect`. Seller premium taxed at accept; buyer taxed at exercise on `(market−strike)×qty − premium`; buyer `−premium` loss at expiry; bank-owned (actuary-on-behalf) gains exempt.
- **SP5 (notifications)** — new in-app/email notification *types* (`LIMIT_CHANGED`, `OTC_CONTRACT_EXPIRING_SOON`) surfaced through the existing `GET /api/v3/me/notifications`.

### 4.4 New config / env vars

| Var | Default | Service |
|---|---|---|
| `FUND_SNAPSHOT_CRON_UTC` | `23:50` | stock-service (SP3) |
| `FUND_METRICS_MIN_MONTHLY_RETURNS` | `2` | stock-service (SP3) |
| `OTC_EXPIRY_WARNING_DAYS` | `3` | stock-service (SP5-E) |

### 4.5 Serialization note

`GET /investment-funds` and `GET /me/watchlists` hand-shape their items so every
field is always present — `metrics_available` (`false`) and `item_count` (`0`) no
longer drop out when they hold default values (raw proto-JSON omits `false`/`0`).
Field names and types are unchanged (numeric ids stay JSON numbers); only the
previously-omitted default fields are now always included.
