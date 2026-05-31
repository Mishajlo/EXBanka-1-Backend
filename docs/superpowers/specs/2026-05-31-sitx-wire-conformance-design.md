# SI-TX wire-format conformance — design

**Date:** 2026-05-31
**Status:** Approved (design); plan pending
**Authors:** Claude + lukasavic

## 1. Motivation

Multiple cohort teams interoperate over the bank-to-bank asset-exchange protocol
defined in `docs/A protocol for bank-to-bank asset exchange.htm` (the authors'
SI-TX draft, "the spec"). For interop to work, the **wire format** — the JSON
bodies and HTTP shapes exchanged between banks — must match the spec exactly.

An audit on 2026-05-31 found that our routing, authentication (`X-Api-Key`),
idempotence model, message envelope, vote reason codes, and the 2PC
vote/commit/rollback *flow* all conform — but **every message body is a flatter,
non-conformant dialect**. A peer bank implementing the spec strictly would fail
to interoperate with us on a single call.

This design brings our **wire format** into exact conformance with the spec while
**keeping our internal saga, executor, idempotence/outbound tables, and internal
route names unchanged**. Conformance lives in two layers: the JSON DTOs in
`contract/sitx/*` (rewritten to spec shapes) and the translation functions at the
gateway / transaction-service / stock-service boundaries. The internal gRPC proto
is *enriched* (not reshaped) so no spec data is lost in translation.

### Decisions locked during brainstorming

- **Cutover:** Hard cut to the spec dialect only. No dual-parsing / no version
  negotiation. Old-dialect peers (including our own older stacks) must upgrade in
  lockstep.
- **Metadata fidelity:** Full — inbound `message` surfaces in the affected user's
  ledger entry; `paymentCode` / `paymentPurpose` / `callNumber` persist on the
  local transaction record and appear in statements.
- **Boundary approach (Approach A):** Spec-shaped wire DTOs + enriched internal
  proto. The saga is *not* rewritten.

## 2. Authoritative reference

The spec is the single source of truth. Section numbers below (e.g. "§2.8.1")
refer to the spec document. Where our implementation uses different *internal*
identifiers, that is irrelevant — only the serialized JSON and HTTP semantics are
judged.

## 3. What already conforms (do not change)

- Transport: JSON over HTTP; `POST {base}/interbank`. Our base URL is
  `…/api/v3/cross-bank-protocol`, registered by peers per §2.9 ("on some base
  URL"). **Keep.**
- Auth: `X-Api-Key` header (§2.10). The optional HMAC headers are a local
  extension layered on top; plain `X-Api-Key` remains accepted. **Keep.**
- `IdempotenceKey { routingNumber, locallyGeneratedKey }` (§2.2). **Keep.**
- `Message<T> { idempotenceKey, messageType, message }` (§2.11). **Keep.**
- `messageType` discriminator values `NEW_TX` / `COMMIT_TX` / `ROLLBACK_TX`
  (§2.12). **Keep.**
- `ForeignBankId { routingNumber, id }` (§2.3). **Keep.**
- NoVote reason code strings (all eight, §2.12.1). **Keep the strings**; only the
  surrounding structure changes (see §6.4).

## 4. Scope rationale (why the out-of-scope items are safe)

The following are deliberately **out of scope** because none of them breaks wire
conformance or interop with a spec-strict peer:

| Item | Why leaving it out is safe |
|---|---|
| **Saga rewrite** | The saga is local execution only. Interop sees only its wire effects (vote / commit / rollback), which become spec-shaped via §6. The spec mandates nothing about local execution shape. Widening scope here adds risk to a component that has broken repeatedly, for zero conformance gain. |
| **Receiver-side 202 async** | §2.11 makes 202 an *option*, not a requirement. Our receiver records the idempotence key and reserves *before* responding 200 — exactly what §2.9 mandates. Residual is operational only: if a synchronous reserve exceeds a peer's timeout, the peer retries and our idempotence cache returns the same vote. Sender-side *handling* of an inbound 202 stays in scope (§7). |
| **`/public-option-offers` + CHECK_STATUS extensions** | Additive endpoints under our base prefix at leaf names the spec doesn't use. No collision. Caveat: cross-bank cascade-cancel grouping (via the non-spec `parentOfferId` field) degrades to no-op with spec-strict peers — a graceful feature degradation, not a conformance break. |
| **Internal route prefix** | §2.9 says `/interbank` sits "on some base URL" that peers register. Our prefix *is* that base URL. Zero impact. |

The genuine risk lives in the **in-scope** changes that brush against saga state:
§6.2 (sign/direction inversion), §6.5 (transactionId correlation), §6.6 (metadata
→ ledger). These are the high-care, heavily-tested sections.

## 5. Architecture

```
Peer bank ──HTTP(JSON spec shapes)──▶ api-gateway
                                        │  (spec DTO  ⟷  internal gRPC proto)
                                        ▼
                              transaction-service / stock-service
                                        │  (enriched proto → existing saga/executor)
                                        ▼
                              account-service, ledger, DB (unchanged shapes)
```

- **`contract/sitx/*.go`** — the JSON DTOs. Rewritten to the exact spec shapes.
  These are the only structs that touch the wire.
- **Gateway handlers** (`peer_tx_handler.go`, `peer_otc_handler.go`,
  `peer_user_handler.go`, …) — translate spec DTO ⟷ internal gRPC proto.
- **Internal gRPC proto** (`contract/proto/transaction/transaction.proto`) —
  *enriched* with the fields needed to carry spec data losslessly (account/asset
  type tags, transactionId as ForeignBankId, transaction metadata).
- **transaction-service / stock-service** — existing saga, executor, repositories
  keep their logic; they consume the enriched proto and persist the new metadata.

## 6. Detailed design

### 6.1 Transaction-protocol DTOs (`contract/sitx/types.go`)

Rewrite to spec shapes. Target JSON (field names = spec):

```go
// §2.6
type TxAccount struct {
    Type string         `json:"type"`           // "PERSON" | "ACCOUNT" | "OPTION"
    ID   *ForeignBankId `json:"id,omitempty"`   // set for PERSON, OPTION
    Num  string         `json:"num,omitempty"`  // set for ACCOUNT
}

// §2.7
type Asset struct {
    Type  string      `json:"type"`  // "MONAS" | "STOCK" | "OPTION"
    Asset interface{} `json:"asset"` // MonetaryAsset | StockDescription | OptionDescription
}
type MonetaryAsset   struct { Currency string `json:"currency"` }     // §2.7.1
type StockDescription struct { Ticker  string `json:"ticker"`  }     // §2.7.3

// §2.8.1 — amount is SIGNED; negative = credit (asset leaves), positive = debit.
// No `direction` field.
type Posting struct {
    Account TxAccount       `json:"account"`
    Amount  decimal.Decimal `json:"amount"`
    Asset   Asset           `json:"asset"`
}

// §2.8.2
type Transaction struct {
    Postings       []Posting     `json:"postings"`
    TransactionID  ForeignBankId `json:"transactionId"`
    Message        string        `json:"message"`
    CallNumber     string        `json:"callNumber,omitempty"`
    PaymentCode    string        `json:"paymentCode"`
    PaymentPurpose string        `json:"paymentPurpose"`
}

// §2.12.2 / §2.12.3 — transactionId is a ForeignBankId, NOT a string.
type CommitTransaction   struct { TransactionID ForeignBankId `json:"transactionId"` }
type RollbackTransaction struct { TransactionID ForeignBankId `json:"transactionId"` }

// §2.12.1
type TransactionVote struct {
    Vote    string         `json:"vote"`              // "YES" | "NO"
    Reasons []NoVoteReason `json:"reasons,omitempty"` // present only on NO
}
type NoVoteReason struct {
    Reason  string   `json:"reason"`
    Posting *Posting `json:"posting,omitempty"` // the FULL offending posting, not an index
}
```

Reason code constants are unchanged (`UNBALANCED_TX`, `NO_SUCH_ACCOUNT`,
`NO_SUCH_ASSET`, `UNACCEPTABLE_ASSET`, `INSUFFICIENT_ASSET`,
`OPTION_AMOUNT_INCORRECT`, `OPTION_USED_OR_EXPIRED`,
`OPTION_NEGOTIATION_NOT_FOUND`). The `Message[T]` envelope and `IdempotenceKey`
are unchanged.

**Decimal-as-number note (§2.5 / §2.8.1):** `amount` must serialize as a JSON
number, never a string. Verify `decimal.Decimal` marshals as a bare number
(`shopspring/decimal` does when not configured for string output); if any path
emits a quoted string, fix the marshaller. Do **not** interpret amounts as
float64 internally — keep `decimal.Decimal`.

### 6.2 Account/Asset union ↔ internal flat mapping (HIGH CARE)

The internal `SiTxPosting` stays flat (`accountId`, `assetId`, `direction`,
`amount`, `routingNumber`) but the proto gains `account_type` and `asset_type`
tags (§6.7) so the translation is lossless.

**Inbound `Posting` → internal `SiTxPosting`:**

| Spec `account` | internal |
|---|---|
| `{type:"ACCOUNT", num}` | `accountId = num`; `routingNumber` = prefix of `num`; `account_type="ACCOUNT"` |
| `{type:"PERSON", id}` | `accountId = id.id`; `routingNumber = id.routingNumber`; `account_type="PERSON"` (keeps the `client-<n>` participant-resolution path) |
| `{type:"OPTION", id}` | option pseudo-account; `accountId` carries the negotiation `ForeignBankId`; `account_type="OPTION"` |

| Spec `asset` | internal |
|---|---|
| `{type:"MONAS", asset:{currency}}` | `assetId = currency`; `asset_type="MONAS"` |
| `{type:"STOCK", asset:{ticker}}` | `assetId = ticker`; `asset_type="STOCK"` |
| `{type:"OPTION", asset: OptionDescription}` | `assetId` = JSON-encoded internal option terms (built from the spec-shaped `OptionDescription`); `asset_type="OPTION"` |

**Direction by ECONOMIC EFFECT, not by the spec word (the inversion trap):**

The spec's bookkeeping convention is the *inverse* of our internal naming:

- Spec §2.8: a **credit** *reduces* the asset on an account (negative amount —
  the asset leaves). A **debit** *increases* it (positive amount — the asset
  arrives).
- Our executor: internal `CREDIT` → `ReserveIncoming` (asset *arrives*); internal
  `DEBIT` → `ReserveOutgoing` (asset *leaves*).

Therefore the mapping is, by effect:

```
spec amount < 0  (asset leaves account)  → internal DEBIT  (ReserveOutgoing)
spec amount > 0  (asset arrives)         → internal CREDIT (ReserveIncoming)
internal magnitude = abs(amount)
```

**Outbound** is the exact inverse: internal `DEBIT` → negative spec amount,
internal `CREDIT` → positive spec amount; flat `accountId`/`assetId` re-expanded
into the correct `TxAccount` / `Asset` variant using the stored type tags.

**Balance check (§2.8 / `UNBALANCED_TX`):** computed on the **signed** spec
amounts — the sum across all postings, per asset, must be zero. This is done on
the spec representation (before/independently of direction translation) to avoid
sign confusion.

### 6.3 Verification of received transactions (§2.8.6)

Map each spec verification step to a NoVote reason, emitting the **full offending
posting** in `NoVoteReason.posting`:

- Unbalanced → `UNBALANCED_TX` (no posting).
- Account missing → `NO_SUCH_ACCOUNT` (posting).
- Asset missing → `NO_SUCH_ASSET` (posting).
- Account can't hold asset (e.g. stock into currency account, wrong currency) →
  `UNACCEPTABLE_ASSET` (posting).
- Credited (asset-leaving) account lacks funds to reserve → `INSUFFICIENT_ASSET`
  (posting). Option contracts are exempt from the funds check (§2.7.2).
- Option account not credited exactly `k` stocks / debited exactly `k·π` →
  `OPTION_AMOUNT_INCORRECT` (posting).
- Option used or settlement date passed → `OPTION_USED_OR_EXPIRED` (posting).
- Option negotiation id invalid → `OPTION_NEGOTIATION_NOT_FOUND` (posting).

More than one reason may be present. The existing executor already performs these
checks against the flat representation; the translation layer attaches the
spec-shaped offending posting.

### 6.4 Vote response

`TransactionVote` is `{vote:"YES"}` or `{vote:"NO", reasons:[…]}`. The
receiver-generated UUID currently emitted in `SiTxVoteResponse.transaction_id`
is **removed from the wire** (the spec vote carries no id). The UUID may remain
internally for replay-cache bookkeeping but must not appear in the JSON.

### 6.5 transactionId & NEW_TX ↔ COMMIT/ROLLBACK correlation (HIGH CARE)

The spec requires per-message idempotence keys that are **never reused** (§2.9),
and correlates COMMIT/ROLLBACK to the original transaction via `transactionId`
(§2.12.2 / §2.12.3). Today we reuse one idem key across NEW_TX + COMMIT and
correlate on it — a §2.9 violation. The conformant, **surgical** design:

- **Initiator** assigns `transactionId = { routingNumber: ownRouting, id: L }`
  in the NEW_TX `Transaction`, where `L` is the locally generated key it used for
  the **NEW_TX** message. Each message (NEW_TX, COMMIT_TX, ROLLBACK_TX) gets its
  **own** unique `idempotenceKey`.
- **Receiver** stores `transactionId` on its idempotence/tx record at NEW_TX time
  (since `transactionId.id == L`, the NEW_TX record is already keyed under `L`).
  COMMIT/ROLLBACK look up the record by `transactionId` — i.e. by
  `transactionId.id`, which equals the original `L`. Each COMMIT/ROLLBACK
  message's *own* idem key is still recorded for dedup (prevents double-commit on
  retransmit).

Net internal change: the correlation key source moves from
`commit.idempotenceKey.locallyGeneratedKey` → `commit.transactionId.id`. Record
storage and saga step logic are otherwise untouched.

### 6.6 Metadata persistence (§2.8.2; full-fidelity choice)

- `message` → the local ledger entry `description` (via account-service's `memo`
  parameter on the commit-time `UpdateBalance` / credit write).
- `paymentCode`, `paymentPurpose`, `callNumber` → persisted on the inbound TX
  record and surfaced in statements.

Requires: new fields on `SiTxNewTxRequest` (§6.7), new columns on
`peer_idempotence_records` (receiver) and `outbound_peer_txs` (sender), and
threading the values into the commit-path ledger write. The auto-migration
(`AutoMigrate`) handles the column adds.

### 6.7 Internal proto enrichment (`contract/proto/transaction/transaction.proto`)

Additive only (no field removed/renumbered):

- `SiTxPosting`: add `string account_type` (PERSON|ACCOUNT|OPTION) and
  `string asset_type` (MONAS|STOCK|OPTION).
- `SiTxNewTxRequest`: add `SiTxForeignBankId transaction_id`, `string message`,
  `string payment_code`, `string payment_purpose`, `string call_number`. Define
  `SiTxForeignBankId { int64 routing_number = 1; string id = 2; }`.
- `SiTxCommitRequest` / `SiTxRollbackRequest`: `transaction_id` becomes the
  initiator's ForeignBankId. (Migrate the existing `string transaction_id` to the
  new message type, or carry both `routing_number` + `id` — decided in the plan.)
- `SiTxVoteResponse`: stop populating `transaction_id` on the wire response
  (field may remain in proto for internal use but is not serialized to peers).

Run `make proto` after editing.

### 6.8 OTC DTOs (`contract/sitx/otc_types.go`) + handlers

Rewrite to spec shapes; local extension fields stay as `omitempty` additions
(spec peers ignore unknown fields):

```go
// §3 OtcOffer
type OtcOffer struct {
    Stock          StockDescription `json:"stock"`           // {ticker}
    SettlementDate string           `json:"settlementDate"`
    PricePerUnit   MonetaryValue    `json:"pricePerUnit"`    // {currency, amount}
    Premium        MonetaryValue    `json:"premium"`         // {currency, amount}
    BuyerID        ForeignBankId    `json:"buyerId"`
    SellerID       ForeignBankId    `json:"sellerId"`
    Amount         int64            `json:"amount"`
    LastModifiedBy ForeignBankId    `json:"lastModifiedBy"`

    // Local extensions — omitempty (non-spec; peers ignore)
    ParentOfferID      *ForeignBankId `json:"parentOfferId,omitempty"`
    BuyerAccountNumber string         `json:"buyerAccountNumber,omitempty"`
}
type MonetaryValue struct {            // §2.5
    Currency string          `json:"currency"`
    Amount   decimal.Decimal `json:"amount"`
}

// §3.4 — OtcOffer & { isOngoing } (flattened; isOngoing derived from status)
type OtcNegotiation struct {
    OtcOffer
    IsOngoing bool `json:"isOngoing"`
}

// §3.1 — BARE array
type PublicStock struct {
    Stock   StockDescription `json:"stock"`   // {ticker}
    Sellers []PublicSeller   `json:"sellers"` // grouped per stock
}
type PublicSeller struct {
    Seller ForeignBankId `json:"seller"`
    Amount int64         `json:"amount"`
}
// handler returns []PublicStock directly (no { "stocks": … } wrapper)

// §3.7
type UserInformation struct {
    BankDisplayName string `json:"bankDisplayName"`
    DisplayName     string `json:"displayName"`
}

// §2.7.2
type OptionDescription struct {
    NegotiationID  ForeignBankId    `json:"negotiationId"`
    Stock          StockDescription `json:"stock"`        // {ticker}
    PricePerUnit   MonetaryValue    `json:"pricePerUnit"` // {currency, amount}
    SettlementDate string           `json:"settlementDate"`
    Amount         int64            `json:"amount"`

    Intent string `json:"intent,omitempty"` // local extension
}
```

Handler implications:

- `GET /public-stock` (§3.1): group the per-owner holdings the stock-service
  returns by ticker into `PublicStock.sellers[]`; return a bare JSON array.
- `POST /negotiations` (§3.2): accept a spec `OtcOffer`; respond with the new
  negotiation's `ForeignBankId`.
- `PUT /negotiations/{rid}/{id}` (§3.3): counter-offer; **409** when it is not the
  caller's turn (turn is buyer's iff `lastModifiedBy != buyerId`) or negotiations
  are closed.
- `GET /negotiations/{rid}/{id}` (§3.4): return `OtcNegotiation` (offer +
  `isOngoing`).
- `DELETE /negotiations/{rid}/{id}` (§3.5): close; sets `isOngoing=false`.
- `GET /negotiations/{rid}/{id}/accept` (§3.6): form the 4-posting transaction
  (Buyer credit premium, Seller debit premium, Buyer debit one optionContract,
  Seller credit one optionContract) expressed in spec `TxAccount` / `Asset` /
  signed-amount terms, submit to the executor, and respond only on successful
  submission. `optionContract(O)` per §3.6.1.
- `GET /user/{rid}/{id}` (§3.7): return `{bankDisplayName, displayName}`; **404**
  on unknown id.

### 6.9 HTTP semantics (§2.11)

- **Sender** must treat peer responses as: `202` → retry the message later;
  `200` → final, body is the response (e.g. a vote); `204` → final, empty. Verify
  `PeerHTTPClient` handles `202` as retry-later rather than as an error. (Today it
  likely treats non-`200` as failure — confirm and fix.)
- **Receiver** continues to respond `200` (with vote) / `204` (commit/rollback
  ack). Emitting `202` is out of scope (see §4).
- A higher-layer failure (a NO vote) is still `200 OK` with the NO vote in the
  body — never a non-2xx status (§2.11 note).

## 7. Files touched (indicative)

- `contract/sitx/types.go`, `contract/sitx/otc_types.go` — DTO rewrite.
- `contract/proto/transaction/transaction.proto` (+ regenerated `transactionpb`).
- `api-gateway/internal/handler/peer_tx_handler.go`, `peer_otc_handler.go`,
  `peer_user_handler.go`, `peer_tx_status_handler.go` — translation.
- `transaction-service/internal/handler/peer_tx_grpc_handler.go`,
  `transaction-service/internal/sitx/posting_executor.go`,
  `transaction-service/internal/sitx/peer_http_client.go` — enriched proto,
  sign/direction mapping, transactionId correlation, 202 handling, metadata.
- `transaction-service/internal/model/peer_idempotence_record.go`,
  `outbound_peer_tx.go` — metadata columns.
- `stock-service/internal/handler/peer_otc_grpc_handler.go`,
  `internal/service/otc_negotiation_service.go`,
  `internal/otccache/cache.go` — OTC reshaping, public-stock grouping.
- Specification.md, `docs/api/REST_API_v3.md`, Swagger — doc updates.

## 8. Test plan

**Unit (per service + `contract/sitx`):**

- Golden marshal/unmarshal tests asserting *exact* JSON for every rewritten DTO
  against the spec (`Posting`, `Transaction`, `TransactionVote`,
  `CommitTransaction`, `RollbackTransaction`, `OtcOffer`, `OtcNegotiation`,
  `PublicStock[]`, `UserInformation`, `OptionDescription`, the `TxAccount` /
  `Asset` variants).
- Union mapping tests including the **sign/direction inversion** (negative →
  internal DEBIT/outgoing, positive → internal CREDIT/incoming) and round-trip
  (inbound → internal → outbound reproduces the original signed posting).
- Balance check on signed amounts (`UNBALANCED_TX`).
- Vote reshaping: NO vote carries `reasons[].posting` as the full posting object.
- transactionId correlation: COMMIT/ROLLBACK resolve the record via
  `transactionId.id`; per-message unique idem keys; retransmit returns cached
  result.
- OTC reshaping: `sellers[]` grouping, `isOngoing` derivation, MonetaryValue
  encoding.
- `amount` serializes as a JSON number, not a string.

**Integration (`test-app/workflows`):**

- Full cross-bank payment NEW_TX → COMMIT asserting spec-shaped wire bodies in
  both directions; balances move correctly (validates the inversion end-to-end).
- NO-vote path asserting `{vote:"NO", reasons:[{reason, posting}]}`.
- OTC negotiate → counter (409 on wrong turn) → accept → exercise asserting spec
  `OtcOffer` / `OptionDescription` and the 4-posting accept transaction.
- `GET /public-stock` (bare array, grouped sellers), `GET /user` (display names),
  `GET /negotiations/{rid}/{id}` (`isOngoing`).
- Metadata: `message` appears on the affected user's ledger entry; `paymentCode`
  / `paymentPurpose` persist and surface in statements.

**Conformance fixtures:** capture the spec's example bodies (e.g. the §2.8 coffee
transaction) as `testdata` and assert our encoder reproduces them
byte-equivalently — the cohort-interop guard.

## 9. Out of scope

Saga rewrite; receiver-side 202 async emission; the `/public-option-offers` and
CHECK_STATUS cohort extensions (kept, clearly marked as local extensions); any
internal route renames. Rationale in §4.

## 10. Risks & mitigations

- **Sign/direction inversion (§6.2):** wrong mapping silently moves money the
  wrong way. Mitigation: map by economic effect with explicit round-trip and
  end-to-end balance-movement tests before any saga code is touched.
- **transactionId correlation (§6.5):** changing the correlation key can strand
  commits/rollbacks. Mitigation: keep `transactionId.id == L` so storage is
  unchanged; only the read-source moves. Test retransmit + dedup.
- **Hard cutover:** all peers (incl. our own older stacks) break until upgraded.
  Mitigation: coordinate the flag-day with the cohort; the conformance fixtures
  give every team a shared byte-level target.
- **Saga fragility:** §6.2 / §6.5 / §6.6 brush against saga state. Mitigation:
  the implementation plan sequences these as isolated, individually-tested steps;
  no unrelated saga refactoring.
