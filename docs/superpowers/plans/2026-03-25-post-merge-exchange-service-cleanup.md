# Post-Merge Exchange-Service Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix build-breaking proto/handler issues from the exchange-service split merge, bring the new exchange-service integration up to project standards (error handling, docs, tests, CI), and add integration tests for the new exchange endpoints.

**Architecture:** The merge introduced a new `exchange-service` (gRPC port 50059) that owns exchange rates. Transaction-service now calls exchange-service via gRPC for currency conversion. The api-gateway exposes three public exchange endpoints. The proto cleanup removes orphaned exchange RPCs from `transaction.proto` since they now live in `exchange.proto`.

**Tech Stack:** Go, protobuf/gRPC, Kafka (segmentio/kafka-go), Gin, swaggo/swag, integration tests (build tag `integration`)

---

## File Map

| File | Role in this change |
|---|---|
| `contract/proto/transaction/transaction.proto` | Remove orphaned exchange rate RPCs and messages |
| `contract/transactionpb/` | Regenerated — never edit by hand |
| `transaction-service/internal/handler/grpc_handler.go` | Delete orphaned exchange rate handler methods |
| `exchange-service/internal/service/exchange_service.go` | Fix silent config parse errors |
| `api-gateway/internal/handler/exchange_handler.go` | Fix error format violations (use `apiError()`) |
| `api-gateway/docs/` | Regenerated swagger |
| `.github/workflows/Build&Publish_Docker_Images.yml` | Add exchange-service to CI/CD build matrix |
| `docs/api/REST_API.md` | Add `POST /api/exchange/calculate` documentation |
| `CLAUDE.md` | Add exchange-service to repository layout |
| `test-app/workflows/exchange_rate_test.go` | Add integration tests for new exchange paths and calculate endpoint |

---

## Task 1: Fix proto — remove orphaned exchange RPCs from transaction.proto

**Files:**
- Modify: `contract/proto/transaction/transaction.proto`

The merge left `GetExchangeRate` and `ListExchangeRates` RPCs in `transaction.proto` even though they now live in `exchange.proto`. The generated Go file has duplicate constant declarations causing a compile error.

- [ ] **Step 1: Remove the exchange rate RPCs from the service block**

  In `contract/proto/transaction/transaction.proto`, delete lines 22-23:
  ```proto
  rpc GetExchangeRate(GetExchangeRateRequest) returns (ExchangeRateResponse);
  rpc ListExchangeRates(ListExchangeRatesRequest) returns (ListExchangeRatesResponse);
  ```

- [ ] **Step 2: Remove the orphaned exchange rate message definitions**

  Delete these messages (lines 146-161):
  ```proto
  message GetExchangeRateRequest {
    string from_currency = 1;
    string to_currency = 2;
  }

  message ListExchangeRatesRequest {}

  message ExchangeRateResponse {
    string from_currency = 1;
    string to_currency = 2;
    string buy_rate = 3;
    string sell_rate = 4;
    string updated_at = 5;
  }

  message ListExchangeRatesResponse { repeated ExchangeRateResponse rates = 1; }
  ```

- [ ] **Step 3: Regenerate proto**

  ```bash
  make proto
  ```
  Expected: exits 0, no errors.

- [ ] **Step 4: Verify contract compiles**

  ```bash
  cd contract && go build ./...
  ```
  Expected: exits 0. No more duplicate constant declarations.

- [ ] **Step 5: Commit**

  ```bash
  git add contract/proto/transaction/transaction.proto contract/transactionpb/
  git commit -m "fix(proto): remove orphaned exchange rate RPCs from transaction.proto"
  ```

---

## Task 2: Fix transaction-service — delete orphaned exchange rate handlers

**Files:**
- Modify: `transaction-service/internal/handler/grpc_handler.go`

The handler still has `GetExchangeRate` and `ListExchangeRates` methods that reference a non-existent `h.exchangeSvc` field and missing `exchangeRateToProto` helper.

- [ ] **Step 1: Delete the orphaned exchange rate handler methods**

  In `transaction-service/internal/handler/grpc_handler.go`, delete lines 389-410 (the section starting with `// ---- Exchange Rate RPCs ----`):

  ```go
  // ---- Exchange Rate RPCs ----

  func (h *TransactionGRPCHandler) GetExchangeRate(ctx context.Context, req *pb.GetExchangeRateRequest) (*pb.ExchangeRateResponse, error) {
  	rate, err := h.exchangeSvc.GetExchangeRate(req.GetFromCurrency(), req.GetToCurrency())
  	if err != nil {
  		return nil, status.Errorf(codes.NotFound, "exchange rate not found: %v", err)
  	}
  	return exchangeRateToProto(rate), nil
  }

  func (h *TransactionGRPCHandler) ListExchangeRates(ctx context.Context, req *pb.ListExchangeRatesRequest) (*pb.ListExchangeRatesResponse, error) {
  	rates, err := h.exchangeSvc.ListExchangeRates()
  	if err != nil {
  		return nil, status.Errorf(mapServiceError(err), "list exchange rates: %v", err)
  	}

  	pbRates := make([]*pb.ExchangeRateResponse, 0, len(rates))
  	for i := range rates {
  		pbRates = append(pbRates, exchangeRateToProto(&rates[i]))
  	}
  	return &pb.ListExchangeRatesResponse{Rates: pbRates}, nil
  }
  ```

- [ ] **Step 2: Check for any remaining references to removed exchange types**

  ```bash
  grep -n "ExchangeRate\|exchangeSvc\|exchangeRateToProto" transaction-service/internal/handler/grpc_handler.go
  ```
  Expected: zero matches. If any remain, delete them.

- [ ] **Step 3: Build to verify**

  ```bash
  cd transaction-service && go build ./...
  ```
  Expected: exits 0.

- [ ] **Step 4: Commit**

  ```bash
  git add transaction-service/internal/handler/grpc_handler.go
  git commit -m "fix(transaction-service): remove orphaned exchange rate handler methods"
  ```

---

## Task 3: Fix exchange-service — handle config parse errors

**Files:**
- Modify: `exchange-service/internal/service/exchange_service.go`

`NewExchangeService` silently ignores `decimal.NewFromString` parse failures for commission rate and spread, defaulting them to zero and breaking all conversions.

- [ ] **Step 1: Fix `NewExchangeService` to return an error on bad config**

  Replace lines 23-27 of `exchange-service/internal/service/exchange_service.go`:
  ```go
  func NewExchangeService(repo *repository.ExchangeRateRepository, commissionRate, spread string) *ExchangeService {
  	cr, _ := decimal.NewFromString(commissionRate)
  	sp, _ := decimal.NewFromString(spread)
  	return &ExchangeService{repo: repo, commissionRate: cr, spread: sp}
  }
  ```

  With:
  ```go
  func NewExchangeService(repo *repository.ExchangeRateRepository, commissionRate, spread string) (*ExchangeService, error) {
  	cr, err := decimal.NewFromString(commissionRate)
  	if err != nil {
  		return nil, fmt.Errorf("invalid commission rate %q: %w", commissionRate, err)
  	}
  	sp, err := decimal.NewFromString(spread)
  	if err != nil {
  		return nil, fmt.Errorf("invalid spread %q: %w", spread, err)
  	}
  	return &ExchangeService{repo: repo, commissionRate: cr, spread: sp}, nil
  }
  ```

- [ ] **Step 2: Update the caller in `cmd/main.go`**

  In `exchange-service/cmd/main.go`, find line 44:
  ```go
  svc := service.NewExchangeService(repo, cfg.CommissionRate, cfg.Spread)
  ```
  Replace with:
  ```go
  svc, err := service.NewExchangeService(repo, cfg.CommissionRate, cfg.Spread)
  if err != nil {
  	log.Fatalf("failed to create exchange service: %v", err)
  }
  ```
  Note: keep the variable name `svc` — it is referenced throughout the rest of `main.go`.

- [ ] **Step 3: Update tests**

  In `exchange-service/internal/service/exchange_service_test.go`, find the `newTestService` helper (line 35):
  ```go
  svc := service.NewExchangeService(repo, "0.005", "0.003")
  ```
  Replace with:
  ```go
  svc, err := service.NewExchangeService(repo, "0.005", "0.003")
  require.NoError(t, err)
  ```
  Note: `newTestService` must now accept `t *testing.T` as a parameter if it doesn't already. Update its signature and all callers accordingly.

- [ ] **Step 4: Build and run tests**

  ```bash
  cd exchange-service && go build ./... && go test ./...
  ```
  Expected: exits 0.

- [ ] **Step 5: Commit**

  ```bash
  git add exchange-service/internal/service/exchange_service.go exchange-service/cmd/main.go exchange-service/internal/service/exchange_service_test.go
  git commit -m "fix(exchange-service): fail fast on invalid commission/spread config instead of silently defaulting to zero"
  ```

---

## Task 4: Fix api-gateway — use apiError() in exchange handler

**Files:**
- Modify: `api-gateway/internal/handler/exchange_handler.go`

The handler uses raw `gin.H{"error": "string"}` responses instead of the standardized `apiError()` helper required by CLAUDE.md.

- [ ] **Step 1: Fix all error responses to use `apiError()`**

  Replace line 86:
  ```go
  c.JSON(http.StatusBadRequest, gin.H{"error": "fromCurrency, toCurrency, and amount are required"})
  ```
  With:
  ```go
  apiError(c, http.StatusBadRequest, ErrValidation, "fromCurrency, toCurrency, and amount are required")
  ```

  Replace line 93:
  ```go
  c.JSON(http.StatusBadRequest, gin.H{"error": "amount must be a positive number"})
  ```
  With:
  ```go
  apiError(c, http.StatusBadRequest, ErrValidation, "amount must be a positive number")
  ```

  Replace line 104:
  ```go
  c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported fromCurrency: " + from})
  ```
  With:
  ```go
  apiError(c, http.StatusBadRequest, ErrValidation, "unsupported fromCurrency: "+from)
  ```

  Replace line 108:
  ```go
  c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported toCurrency: " + to})
  ```
  With:
  ```go
  apiError(c, http.StatusBadRequest, ErrValidation, "unsupported toCurrency: "+to)
  ```

- [ ] **Step 2: Build to verify**

  ```bash
  cd api-gateway && go build ./...
  ```
  Expected: exits 0.

- [ ] **Step 3: Commit**

  ```bash
  git add api-gateway/internal/handler/exchange_handler.go
  git commit -m "fix(api-gateway): use apiError() for exchange handler validation errors"
  ```

---

## Task 5: Add exchange-service to CI/CD build matrix

**Files:**
- Modify: `.github/workflows/Build&Publish_Docker_Images.yml`

- [ ] **Step 1: Add exchange-service to the matrix**

  In `.github/workflows/Build&Publish_Docker_Images.yml`, add `- exchange-service` to the `matrix.service` array (after line 34):
  ```yaml
      matrix:
        service:
          - api-gateway
          - auth-service
          - user-service
          - notification-service
          - client-service
          - account-service
          - card-service
          - transaction-service
          - credit-service
          - exchange-service
  ```

- [ ] **Step 2: Commit**

  ```bash
  git add .github/workflows/Build\&Publish_Docker_Images.yml
  git commit -m "ci: add exchange-service to Docker image build matrix"
  ```

---

## Task 6: Update docs — REST_API.md and CLAUDE.md

**Files:**
- Modify: `docs/api/REST_API.md`
- Modify: `CLAUDE.md`

- [ ] **Step 1: Add `POST /api/exchange/calculate` to REST_API.md**

  Find the exchange rates section in `docs/api/REST_API.md`. After the existing `GET /api/exchange/rates/:from/:to` subsection, add:

  ```markdown
  ### POST /api/exchange/calculate

  Calculate a currency conversion including the bank's commission. Informational only — no transaction is created.

  **Authentication:** None (public endpoint)

  **Request body:**
  | Field | Type | Required | Description |
  |---|---|---|---|
  | `fromCurrency` | string | yes | Source currency code (e.g. `EUR`) |
  | `toCurrency` | string | yes | Target currency code (e.g. `USD`) |
  | `amount` | string | yes | Amount to convert (must be positive decimal) |

  Supported currencies: `RSD`, `EUR`, `USD`, `CHF`, `GBP`, `JPY`, `CAD`, `AUD`.

  **Example request:**
  ```json
  POST /api/exchange/calculate
  Content-Type: application/json

  {
    "fromCurrency": "EUR",
    "toCurrency": "RSD",
    "amount": "100.00"
  }
  ```

  **Responses:**
  | Code | Description |
  |---|---|
  | 200 | Conversion result |
  | 400 | Validation error (missing fields, invalid amount, unsupported currency) |
  | 404 | Exchange rate not found for the requested pair |
  | 500 | Internal error |

  **200 response body:**
  ```json
  {
    "from_currency": "EUR",
    "to_currency": "RSD",
    "input_amount": "100.0000",
    "converted_amount": "11700.0000",
    "commission_rate": "0.005",
    "effective_rate": "117.3000"
  }
  ```

  ---
  ```

- [ ] **Step 2: Ensure the new canonical routes are documented**

  Verify that `GET /api/exchange/rates` and `GET /api/exchange/rates/:from/:to` are documented in REST_API.md. If they are only documented under the legacy paths (`/api/exchange-rates`), add a note that the canonical paths are `/api/exchange/rates` and `/api/exchange/rates/:from/:to`, with the old paths kept as backward-compatible aliases.

- [ ] **Step 3: Update CLAUDE.md — add exchange-service to repository layout**

  In `CLAUDE.md`, find the "Repository Layout" section. Add exchange-service to the tree:
  ```
  ├── exchange-service/          # Currency exchange rates and conversion (gRPC, port 50059)
  ```

  Add after the `transaction-service` entry.

- [ ] **Step 4: Update CLAUDE.md — add exchange-service port and env vars**

  In the environment variables table, add:
  ```
  | `EXCHANGE_GRPC_ADDR` | localhost:50059 | exchange-service gRPC address; also required by transaction-service |
  ```

  In the "New service DB ports" table, add:
  ```
  | exchange-service | 5439 |
  ```

  Also add a note under environment variables that exchange-service has its own config variables:
  ```
  | `EXCHANGE_API_KEY` | *(optional)* | API key for paid exchange rate provider tier |
  | `EXCHANGE_COMMISSION_RATE` | 0.005 | Commission rate applied to conversions (0.5%) |
  | `EXCHANGE_SPREAD` | 0.003 | Spread applied to derive buy/sell from mid rate (0.3%) |
  | `EXCHANGE_SYNC_INTERVAL_HOURS` | 6 | How often to sync rates from external provider |
  ```

- [ ] **Step 5: Commit**

  ```bash
  git add docs/api/REST_API.md CLAUDE.md
  git commit -m "docs: add exchange-service to CLAUDE.md layout, document POST /api/exchange/calculate"
  ```

---

## Task 7: Regenerate swagger

**Files:**
- Modify (generated): `api-gateway/docs/swagger.json`, `api-gateway/docs/docs.go`, `api-gateway/docs/swagger.yaml`

- [ ] **Step 1: Regenerate swagger**

  ```bash
  cd api-gateway && swag init -g cmd/main.go --output docs
  ```
  Expected: exits 0.

- [ ] **Step 2: Verify exchange endpoints appear and old transaction exchange routes are gone**

  ```bash
  grep -c "/api/exchange" api-gateway/docs/swagger.json
  ```
  Expected: matches for `/api/exchange/rates`, `/api/exchange/rates/{from}/{to}`, `/api/exchange/calculate`, plus backward-compat `/api/exchange-rates` routes.

- [ ] **Step 3: Commit**

  ```bash
  git add api-gateway/docs/
  git commit -m "docs(swagger): regenerate after exchange handler error format fixes"
  ```

---

## Task 8: Add integration tests for exchange endpoints

**Files:**
- Modify: `test-app/workflows/exchange_rate_test.go`

The existing tests only cover `GET /api/exchange-rates` and `GET /api/exchange-rates/:from/:to`. Missing tests for:
- The new `/api/exchange/rates` paths
- `POST /api/exchange/calculate`
- Error cases (invalid currency, missing fields)

- [ ] **Step 1: Add tests for the new exchange paths and calculate endpoint**

  Append to `test-app/workflows/exchange_rate_test.go`:

  ```go
  // --- New exchange paths ---

  func TestExchangeRates_NewPath_ListAll(t *testing.T) {
  	c := newClient()
  	resp, err := c.GET("/api/exchange/rates")
  	if err != nil {
  		t.Fatalf("error: %v", err)
  	}
  	helpers.RequireStatus(t, resp, 200)
  }

  func TestExchangeRates_NewPath_GetSpecific(t *testing.T) {
  	c := newClient()
  	resp, err := c.GET("/api/exchange/rates/EUR/RSD")
  	if err != nil {
  		t.Fatalf("error: %v", err)
  	}
  	if resp.StatusCode != 200 && resp.StatusCode != 404 {
  		t.Fatalf("expected 200 or 404, got %d", resp.StatusCode)
  	}
  }

  func TestExchangeRates_Calculate(t *testing.T) {
  	c := newClient()
  	resp, err := c.POST("/api/exchange/calculate", map[string]interface{}{
  		"fromCurrency": "EUR",
  		"toCurrency":   "RSD",
  		"amount":       "100.00",
  	})
  	if err != nil {
  		t.Fatalf("error: %v", err)
  	}
  	// 200 if rates are seeded, 404 if no rate exists yet
  	if resp.StatusCode != 200 && resp.StatusCode != 404 {
  		t.Fatalf("expected 200 or 404, got %d", resp.StatusCode)
  	}
  	if resp.StatusCode == 200 {
  		// Verify response has expected fields
  		fromCurrency := helpers.GetStringField(t, resp, "from_currency")
  		if fromCurrency != "EUR" {
  			t.Fatalf("expected from_currency=EUR, got %s", fromCurrency)
  		}
  		toCurrency := helpers.GetStringField(t, resp, "to_currency")
  		if toCurrency != "RSD" {
  			t.Fatalf("expected to_currency=RSD, got %s", toCurrency)
  		}
  	}
  }

  func TestExchangeRates_Calculate_MissingFields(t *testing.T) {
  	c := newClient()
  	resp, err := c.POST("/api/exchange/calculate", map[string]interface{}{
  		"fromCurrency": "EUR",
  	})
  	if err != nil {
  		t.Fatalf("error: %v", err)
  	}
  	helpers.RequireStatus(t, resp, 400)
  }

  func TestExchangeRates_Calculate_InvalidAmount(t *testing.T) {
  	c := newClient()
  	resp, err := c.POST("/api/exchange/calculate", map[string]interface{}{
  		"fromCurrency": "EUR",
  		"toCurrency":   "RSD",
  		"amount":       "-100",
  	})
  	if err != nil {
  		t.Fatalf("error: %v", err)
  	}
  	helpers.RequireStatus(t, resp, 400)
  }

  func TestExchangeRates_Calculate_UnsupportedCurrency(t *testing.T) {
  	c := newClient()
  	resp, err := c.POST("/api/exchange/calculate", map[string]interface{}{
  		"fromCurrency": "XYZ",
  		"toCurrency":   "RSD",
  		"amount":       "100",
  	})
  	if err != nil {
  		t.Fatalf("error: %v", err)
  	}
  	helpers.RequireStatus(t, resp, 400)
  }

  func TestExchangeRates_Calculate_SameCurrency(t *testing.T) {
  	c := newClient()
  	resp, err := c.POST("/api/exchange/calculate", map[string]interface{}{
  		"fromCurrency": "RSD",
  		"toCurrency":   "RSD",
  		"amount":       "1000",
  	})
  	if err != nil {
  		t.Fatalf("error: %v", err)
  	}
  	// Same currency should return amount as-is with zero commission
  	if resp.StatusCode != 200 && resp.StatusCode != 404 {
  		t.Fatalf("expected 200 or 404, got %d", resp.StatusCode)
  	}
  }
  ```

- [ ] **Step 2: Build to verify**

  ```bash
  cd test-app && go build ./...
  ```
  Expected: exits 0.

- [ ] **Step 3: Commit**

  ```bash
  git add test-app/workflows/exchange_rate_test.go
  git commit -m "test(exchange): add integration tests for new exchange paths and calculate endpoint"
  ```

---

## Task 9: Full build verification

- [ ] **Step 1: Build all services**

  ```bash
  make build
  ```
  Expected: exits 0. All services build, swagger is regenerated.

- [ ] **Step 2: Verify no references to removed exchange RPCs remain in transaction-service**

  ```bash
  grep -r "GetExchangeRate\|ListExchangeRates\|ExchangeRateResponse\|exchangeSvc\|exchangeRateToProto" --include="*.go" --exclude="*.pb.go" transaction-service/
  ```
  Expected: zero matches (excluding generated proto files).

- [ ] **Step 3: Verify exchange RPCs exist only in exchange-service**

  ```bash
  grep -r "GetExchangeRate\|ListExchangeRates" --include="*.proto" contract/
  ```
  Expected: matches only in `contract/proto/exchange/exchange.proto`, NOT in `contract/proto/transaction/transaction.proto`.

- [ ] **Step 4: Run exchange-service unit tests**

  ```bash
  cd exchange-service && go test ./...
  ```
  Expected: all tests pass.

- [ ] **Step 5: Final commit if any cleanup was needed**

  ```bash
  git add -A && git diff --cached --stat
  ```
  If there are changes:
  ```bash
  git commit -m "chore: cleanup after post-merge exchange-service fixes"
  ```
