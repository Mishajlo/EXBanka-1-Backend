# Service-by-Service Review

A living document. For each service we capture what should change — and only the
things that are genuinely worth changing. Process: **Claude's opinion + your idea =
agreed action items**. Findings are proposals until we both sign off in the
"Decision" block.

Status legend: 🔴 high / 🟡 medium / 🟢 low / ✅ already good (no change).

---

## 1. api-gateway

**Intended responsibilities (your model):** rate-limiting · auth · JWT · token
white/black-lists · REST↔gRPC mapping · some caching · *limited* cross-service
aggregation (never two calls to the **same** service stitched into one response —
that should be one gRPC method) · no stale routes.

### Findings (Claude's opinion)

#### 🔴 1.1 — No rate limiting anywhere — **IN SCOPE, tune for FE polling**
The gateway does zero throttling. `rate_limited` exists only as a gRPC→HTTP error
mapping (`validation.go:285,320`); nothing in the gateway ever produces it.
`/auth/login`, `/auth/refresh`, `/auth/password/reset-request` are wide open to
brute-force / credential-stuffing. The gateway is the correct single choke-point
for this. Redis is already wired in `main.go`, so a distributed limiter is cheap.

**Constraint (yours):** the frontend polls state from multiple routes every ~1s, so
we must NOT throttle normal read traffic. Design = **tiered**, not one global cap:
- **Sensitive endpoints — strict:** `POST /auth/login`, `/auth/password/reset-request`
  (e.g. ~5–10/min per IP+identifier), `/auth/refresh` moderate. These are the real
  brute-force surface.
- **Everything else (GET polling + normal mutations) — generous safety ceiling
  only:** a high per-IP burst (e.g. ~50–100 req/s) purely to stop a runaway/abuse
  client, sized well above legit 1s polling across several routes. Shared NAT/IP
  means we keep this loose. Effectively invisible to the FE.
- Limiter keyed per-IP (and per-identifier on auth). Skip GET read routes from the
  tight bucket entirely.

#### 🔴 1.2 — Auth validates via a gRPC round-trip on *every* request → go asymmetric, verify locally
`AuthMiddleware`/`AnyAuthMiddleware`/`MobileAuthMiddleware` each call
`authClient.ValidateToken` over gRPC for every single request
(`middleware/auth.go:32`). The gateway holds **no** JWT key and does **no** local
verification — it's a pure proxy to auth-service. This is the biggest inefficiency
and it touches three of your bullets at once (JWT, caching, white/black-lists).

**Chosen approach (your idea — asymmetric keys):**
- Switch access-token signing to **asymmetric** (ES256 / EdDSA / RS256).
  **auth-service holds the PRIVATE key** → it is the only party that can mint/sign.
  **api-gateway holds only the PUBLIC key** → it verifies signature + `exp` + claims
  **locally**, with no gRPC call on the hot path. A gateway compromise can't forge
  tokens (only the public half lives there) — safer than a shared `JWT_SECRET`.
- **Caveat → denylist (this IS the gateway white/black-list, item 1.3):** a locally
  verified signature can't see a revocation. So pair local verify with a small
  **Redis denylist**: on logout / RevokeSession / RevokeAllSessions / password change,
  auth-service writes the revoked `jti`/`sid` (or a per-user valid-not-before stamp)
  with TTL = the token's remaining life (≤15 min, so the set stays tiny). Gateway hot
  path = **local signature check + one Redis lookup**, no auth-service hop.
- **Dynamic key rotation (your call):** keys are NOT static. auth-service exposes a
  **gRPC route** (e.g. `GetSigningKeys` → a JWKS-style set of `{kid, public_key}`,
  current + previous during overlap). The gateway fetches at startup, caches, and
  **refreshes** (periodic tick + on-demand when it sees a `kid` it doesn't know).
  Tokens carry `kid` in the header so the gateway picks the right key. Rotation =
  auth mints a new keypair, signs with the new `kid`, keeps serving the old public
  key until the last token signed with it expires (≤15 min), then drops it. No
  gateway redeploy needed to rotate.
- **Moving parts:** signing-alg change in auth-service; access tokens need `jti`
  (ideally `sid`) + `kid` header + a reliable `iat`. Refresh tokens stay
  opaque/DB-stored (already revocable) — only access-token verify moves local.
- Drops the earlier "light cache" idea — local verify is already faster than a cache
  hit, so no `ValidateToken` cache is needed.
- Spans **api-gateway + auth-service** — see auth-service section when we get there.

#### 🟡 1.3 — Gateway-owned denylists: TWO distinct purposes
Under 1.2 the gateway checks Redis after local verify. There are **two** separate
mechanisms (different outcomes), both written by auth-service, both checked at the
gateway:

**(a) Hard revocation — token is dead.** logout / RevokeSession / RevokeAllSessions →
auth adds the `jti`/`sid` to a revocation set (TTL = remaining token life). Gateway
hit → **401 `unauthorized`** (distinct from expiry), client must **re-authenticate**
— the refresh token is also revoked, so this drives a **logout** in the FE.

**(b) Force-refresh — claims are stale (your idea).** When ANY in-token data changes
(permissions, roles, `account_active`, lock state, name, biometrics flag…), the user
must obtain a fresh access token before continuing — the refresh token stays valid, so
the client **silently refreshes** and gets updated claims.
- **Cleanest implementation:** a per-principal **`stale_after[user]` = now** timestamp
  in Redis (not a per-token list). Gateway compares the token's `iat` to it: `iat <
  stale_after` ⇒ stale. One key invalidates *all* that user's outstanding access
  tokens across devices at once (which is what you want on a permission change), TTL =
  access-token lifetime (auto-cleans). This realises your "denylist of tokens that must
  refresh" with a single key instead of enumerating jtis.
- **Gateway response on stale ⇒ the SAME 401 as a normal access-token expiry**
  (`token_expired`, your call), so the FE's existing expiry→refresh path triggers
  transparently — it refreshes and gets new claims, **never logs out**. This is the
  deliberate opposite of (a): stale ⇒ `token_expired` (refresh); revoked ⇒
  `unauthorized` (logout). The gateway tells them apart because they're separate Redis
  structures (stale-after timestamp vs. revocation set).
- **Wiring (cross-service):** whoever mutates token-relevant data must bump
  `stale_after` — permissions/roles live in **user-service**, client active state in
  **client-service**, account-active/lock in **auth-service**. Signal via a Kafka
  `*.claims-invalidated` event consumed by auth (which owns the Redis key), or a direct
  shared-Redis write. Decide the exact channel when we hit those services.

#### 🔴 1.4 / OWN-1 — Move ALL ownership checks OUT of the gateway, INTO each owning service
**Your decision, and it reverses a current CLAUDE.md hard rule.** Today the gateway
verifies ownership *before* forwarding (CLAUDE.md "Resource Ownership Verification
Requirement"). New principle: **the gateway does NOT check ownership — each service
checks ownership of its own resources.** The gateway's only job becomes propagating
the *caller identity* so the owning service can decide.

This also dissolves the "double gRPC" smell: most gateway double-calls are an
**ownership pre-fetch** (`GetAccount`/`GetHolding` just to read `owner_id`, then act).
Once the owning service enforces, those pre-fetch RPCs disappear and the calls
collapse to one — e.g. `GetMyAccountActivity` becomes a single account-service RPC
that checks ownership and returns the ledger.

**What the gateway loses (delete these):**
- Helpers in `validation.go`: `enforceOwnership` (:146), `ResolveAndCheckAccount`
  (:174), `ResolveAndCheckAccountByNumber` (:193), `checkAccountOwnership` (:213).
- All call sites: `transaction_handler.go` (692,719,772,802,1076),
  `account_handler.go` inline (671,716,775), `card_handler.go` (403,761,897,1019),
  `credit_handler.go` (831,886), `stock_order_handler.go` (106,123,158,413,425),
  `portfolio_handler.go` (478,528), `otc_options_handler.go` (~475),
  `peer_tx_dispatcher_handler.go` (109).
- The ownership-only pre-fetch gRPC calls those sites depend on.

**What the gateway gains:** propagate full caller identity to services on every gRPC
call via metadata (extend the existing `changed_by.go` `x-changed-by` pattern into a
proper identity context: `principal_id`, `principal_type`, `on_behalf_of_client_id`,
`acting_employee_id`). `ResolveIdentity` middleware stays only as the place that
builds that metadata.

**Where each check lands (owning service enforces, returns PermissionDenied/NotFound
→ gateway maps to 403/404):**
| Resource | Owning service | Checks to absorb |
|---|---|---|
| accounts, ledger, account-number bindings | account-service | account detail/activity, every "is this the caller's account" check, order/transfer/OTC account bindings (acting service passes identity; account-service enforces at reserve/debit) |
| cards, authorized persons | card-service | card detail, PIN, block, card-by-account |
| loans, installments | credit-service | loan/installment reads & actions |
| holdings, orders, offers, contracts, portfolios | stock-service | holding/order/portfolio ownership, OTC offer/negotiation/contract ownership, exercise strike-account binding |
| payments, transfers | transaction-service | payment/transfer reads, cross-bank outbound source-account ownership |

**Risks / must-preserve (do NOT regress):**
- Several of these checks are on the **cross-bank / SI-TX money path** and were added
  to fix real money-creation bugs (see `project_crossbank_adversarial_findings`,
  ROUTE-CHANGES.md §6/§8). Relocating them must keep the exact guarantee — the
  inbound peer path already enforces inside stock-service, which is the model.
- Identity-in-metadata is only trustworthy at the gateway↔service boundary. Services
  must trust it from the gateway only (same trust model as today's gRPC calls) — do
  not expose those gRPC ports outside the cluster.
- **404-vs-403 semantics:** today client mismatch → 404 (existence hiding), employee
  mismatch → 403. Services must reproduce that, and any change gets logged in
  ROUTE-CHANGES.md.
- **CLAUDE.md** "Resource Ownership Verification Requirement" must be rewritten to the
  new principle as part of this work.

#### ✅ 1.5 — Cross-service aggregations are correct
`GetMe` (user/client + auth), `ListClients`/`GetClient` (client + auth batch),
`ListEmployees` (user + auth), `CreateAccount` (account + card),
`ListCardsByAccount` (account + card) — these combine **different** services into one
REST response. This is exactly your allowed pattern. No change.

#### ✅ 1.6 — Routes are clean
177 routes, no stale/dead/stub routes, no commented-out routes, no v1/v2 leftovers
(v1+v2 retired 2026-04-27). Recent deprecations were hard-deleted, not left dangling.
Nothing to do here.

#### 🟡 1.7 — Edge logging — **IN SCOPE** (CORS: no change)
- **Logging (DO):** `gin.Default()` only gives an unstructured text logger. Add
  **structured request logging + a request-ID correlation** middleware at the edge:
  per-request `request_id` (generate, or honor inbound `X-Request-Id`), method, path,
  status, latency, principal_id, and propagate the request_id downstream via gRPC
  metadata so service logs can be tied together. For a bank, an edge access log is
  worth having.
- **CORS (your call): leave as-is.** `AllowAllOrigins: true` (`router_v3.go:26`) stays
  — low risk (`AllowCredentials:false` + Bearer-header auth, no cookies). No change.
- `FaultHeaderForwarder` (saga fault-injection) is wired globally but compile-time
  no-op in prod builds — fine, just noting it's test scaffolding at the edge.

### Decision / agreed action items

- [x] **1.1 Rate limiting — IN SCOPE.** Tiered: strict on `/auth/login` +
  `/auth/password/reset-request` (+ moderate `/auth/refresh`); generous per-IP safety
  ceiling everywhere else so the FE's ~1s multi-route polling is never throttled.
  Redis-backed.
- [x] **1.2 Auth path — asymmetric JWT + dynamic rotation (DECIDED, your idea).**
  auth-service signs with a PRIVATE key; api-gateway verifies locally with the PUBLIC
  key. **Keys are dynamic:** auth exposes a **gRPC `GetSigningKeys`** route; the
  gateway fetches + caches + refreshes (periodic + on unknown `kid`), supporting
  rotation with `kid` + current/previous overlap. Drop the light-cache idea. Spans
  gateway + auth-service.
  - Open sub-choice (defer to impl): alg = **ES256 / EdDSA / RS256** (lean ES256/EdDSA).
- [x] **1.3 Two gateway-owned denylists.** (a) **Hard revocation** (`jti`/`sid` set,
  logout/revoke → 401 `unauthorized` → FE logs out); (b) **Force-refresh on claims
  change** — per-principal `stale_after[user]` vs token `iat` → 401 **`token_expired`**
  (same as a real expiry, your call) → FE silently refreshes for new claims, never logs
  out. Auth owns the Redis keys; claims changes signalled by **direct Redis write**
  (decided) from user-service / client-service / auth.
- [x] **1.4 / OWN-1 Ownership relocation — DECIDED.** Gateway stops checking ownership;
  each owning service enforces ownership of its own resources using caller identity
  propagated via gRPC metadata. Cross-cutting — every service gets a matching item in
  its section. Rewrite CLAUDE.md's ownership rule. Preserve cross-bank money-path
  guarantees + 404/403 semantics.
- [x] **1.7 Edge logging — IN SCOPE; CORS — no change.** Add structured request logging
  + request-ID correlation (propagate downstream via gRPC metadata). Keep
  `AllowAllOrigins`.

---

## 2. auth-service
_Full review pending — but these land here from the gateway decisions:_
- **AUTH-A (from 1.2):** switch access-token signing to **asymmetric** (ES256/EdDSA);
  generate/store a keypair; sign with `kid` + `jti` (+ `sid`) + reliable `iat`. Expose
  a **gRPC `GetSigningKeys`** returning the JWKS-style set (current + previous during
  rotation). Add a key-rotation routine with overlap (serve old public key ≤ token life).
- **AUTH-B (from 1.3a):** on logout / RevokeSession / RevokeAllSessions / password
  change, write the revoked `jti`/`sid` to the Redis **revocation** set (TTL = token life).
- **AUTH-C (from 1.3b):** define the per-principal **`stale_after`** Redis key schema
  (shared helper in `contract/`). Any service that mutates token-relevant data writes it
  **directly to Redis** (decided channel): auth (account active/lock), user-service
  (permissions/roles), client-service (client active). No Kafka hop.
- **AUTH-D (from OWN-1):** `ValidateToken` gRPC stops being the per-request hot path
  (gateway verifies locally); keep it only where still genuinely needed, or retire it.

## 3. user-service
_pending_

## 4. notification-service
_pending_

## 5. client-service
_pending_

## 6. account-service
_pending_

## 7. card-service
_pending_

## 8. transaction-service
_pending_

## 9. credit-service
_pending_

## 10. exchange-service
_pending_

## 11. verification-service
_pending_

## 12. stock-service
_pending_
