# Transaction Bug Fixes, Payment History Endpoint, and Test-App Email Configuration

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix four bugs (verification OTP email uses wrong template, transfer history missing status field, transfer test targets cross-client accounts, balance test helpers don't compile), add a `GET /api/payments/client/:client_id` endpoint symmetric with transfers, and update the test-app to use a configurable base email address with numbered client/admin tags.

**Architecture:** The verification email bug is a one-line fix in the transaction-service gRPC handler. The missing helpers are a new file in the test-app `workflows/` package. The new payment-by-client endpoint follows the exact same pattern as the existing `ListTransfersByClient`: gateway resolves account numbers → calls a new gRPC RPC → service queries by account numbers. Email configuration adds an env-var-backed field and numbered helper methods to the existing config struct.

**Tech Stack:** Go, gRPC/protobuf, Gin, GORM, Kafka (segmentio/kafka-go), shopspring/decimal.

---

## Root Cause Analysis

| Reported symptom | Root cause | Fix location |
|---|---|---|
| Verification code email not received | `EmailTypeConfirmation` ("Account Activated Successfully") sent instead of `EmailTypeTransactionVerify` ("Transaction Verification"); data keys also wrong (`code` vs `verification_code`) | `transaction-service/internal/handler/grpc_handler.go:362` |
| Transactions don't appear in history (test-app) | Test helpers `getAccountBalance`, `getBankRSDAccount`, `scanKafkaForActivationToken` are called but never defined → compile failure → no tests can run at all | `test-app/workflows/` (new `helpers_test.go`) |
| Transactions don't appear in history (production) | No `GET /api/payments/client/:client_id` endpoint exists — clients can only query per-account, while the frontend needs per-client aggregation (transfers already have this; payments don't) | All layers from proto → service → gateway |
| Balance never changes (test-app) | Same compile failure + `TestTransfer_SameCurrency_EndToEnd` creates cross-client transfer that service rejects with 400 → execute step never reached → balance never updated | `test-app/workflows/transfer_test.go` |
| Balance never changes (service/observable) | **Root cause is Task 1 (OTP email bug):** `ExecutePayment`/`ExecuteTransfer` both require a valid `VerificationCode` before touching balances. Because `CreateVerificationCode` sends the wrong email template, the user never receives a valid OTP → `ValidateVerificationCode` rejects every attempt → `ExecutePayment`/`ExecuteTransfer` is never called → balance stays unchanged. The actual `UpdateBalance` gRPC calls in the service are correct. Fix Task 1 first; account state will update correctly once valid OTP codes are delivered. | `transaction-service/internal/handler/grpc_handler.go:362` (same as Task 1) |
| Transfer response missing status | `TransferResponse` proto has no `status` field; `PaymentResponse` does — transfer history can't show lifecycle state | `contract/proto/transaction/transaction.proto:93` |
| Test-app admin email mismatch | `docker-compose.yml` seeds admin with `ADMIN_EMAIL=vlupsic11723rn@raf.rs` (no tag) but test-app config hardcodes `lsavic12123rn@raf.rs` → `AdminEmail()` returns `lsavic12123rn+admin@raf.rs` which doesn't match → `loginAsAdmin` fails; `ensureAdminActivated` also only warns (does not fail fast) when no Kafka activation token is found | `test-app/internal/config/config.go` + `docker-compose.yml` + `test-app/workflows/auth_test.go` |

---

## File Structure

### Modified Files

```
contract/proto/transaction/transaction.proto          # add status to TransferResponse; add ListPaymentsByClient RPC
contract/transactionpb/*.go                           # REGENERATED — run `make proto`

transaction-service/internal/handler/grpc_handler.go # fix email type+keys; add transferToProto status; add ListPaymentsByClient handler
transaction-service/internal/service/payment_service.go # add ListPaymentsByClient; add ListByAccountNumbers to PaymentRepo interface
transaction-service/internal/repository/payment_repository.go # add ListByAccountNumbers method

api-gateway/internal/handler/transaction_handler.go  # add ListPaymentsByClient handler; add status to transferToJSON
api-gateway/internal/router/router.go                # register GET /api/payments/client/:client_id

test-app/internal/config/config.go                   # configurable base email; ClientEmail(n int); AdminEmail()
test-app/workflows/payment_test.go                   # use cfg.ClientEmail(1)/cfg.ClientEmail(2)
test-app/workflows/transfer_test.go                  # fix cross-client accounts; fix commission assertion; use cfg.ClientEmail(1)
docs/api/REST_API.md                                 # add status to transfer response; add new endpoint section
```

### New Files

```
test-app/workflows/helpers_test.go     # getAccountBalance, getBankRSDAccount, scanKafkaForActivationToken
api-gateway/docs/swagger.yaml (etc.)  # regenerated after `make swagger`
```

---

## Task 1: Fix Verification Code Email Type and Data Keys

**Files:**
- Modify: `transaction-service/internal/handler/grpc_handler.go:360-370`

The `CreateVerificationCode` RPC currently sends `EmailTypeConfirmation` which maps to the
"Account Activated Successfully" template. The correct type is `EmailTypeTransactionVerify`
which maps to the OTP template expecting `verification_code` and `expires_in` keys.

- [ ] **Step 1: Read the current handler (already done in analysis) and locate line 362**

Confirm these are the offending lines in `CreateVerificationCode`:
```go
_ = h.producer.SendEmail(ctx, kafkamsg.SendEmailMessage{
    To:        email,
    EmailType: kafkamsg.EmailTypeConfirmation,        // WRONG
    Data: map[string]string{
        "code":             code,                     // WRONG key
        "transaction_type": req.GetTransactionType(), // unused by template
    },
})
```

- [ ] **Step 2: Apply the fix**

Replace the block above with:
```go
_ = h.producer.SendEmail(ctx, kafkamsg.SendEmailMessage{
    To:        email,
    EmailType: kafkamsg.EmailTypeTransactionVerify,
    Data: map[string]string{
        "verification_code": code,
        "expires_in":        "5 minutes",
    },
})
```

Verify: `notification-service/internal/sender/templates.go` case `EmailTypeTransactionVerify`
expects exactly `verification_code` and `expires_in` keys (confirmed in analysis, lines 118-126).

- [ ] **Step 3: Run the service build to verify no compile errors**

```bash
cd transaction-service && go build ./...
```
Expected: exits 0, no errors.

- [ ] **Step 4: Commit**

```bash
git add transaction-service/internal/handler/grpc_handler.go
git commit -m "fix(transaction): send correct OTP email type and data keys for verification codes"
```

---

## Task 2: Add `status` Field to TransferResponse (proto + handlers)

**Files:**
- Modify: `contract/proto/transaction/transaction.proto:93-102`
- Modify: `transaction-service/internal/handler/grpc_handler.go:408-419` (transferToProto)
- Modify: `api-gateway/internal/handler/transaction_handler.go:650-661` (transferToJSON)

The `PaymentResponse` proto has a `status` field (field 11); `TransferResponse` does not.
Without it, transfer history responses omit lifecycle state entirely.

- [ ] **Step 1: Add `status` to `TransferResponse` in the proto**

Edit `contract/proto/transaction/transaction.proto`. Current `TransferResponse` (lines 93-102):
```proto
message TransferResponse {
  uint64 id = 1;
  string from_account_number = 2;
  string to_account_number = 3;
  string initial_amount = 4;
  string final_amount = 5;
  string exchange_rate = 6;
  string commission = 7;
  string timestamp = 8;
}
```
Change to:
```proto
message TransferResponse {
  uint64 id = 1;
  string from_account_number = 2;
  string to_account_number = 3;
  string initial_amount = 4;
  string final_amount = 5;
  string exchange_rate = 6;
  string commission = 7;
  string timestamp = 8;
  string status = 9;
}
```

- [ ] **Step 2: Regenerate protobuf Go code**

```bash
make proto
```
Expected: `contract/transactionpb/transaction.pb.go` updated; no errors.

- [ ] **Step 3: Update `transferToProto` in transaction-service handler**

Edit `transaction-service/internal/handler/grpc_handler.go`. Current (lines 408-419):
```go
func transferToProto(t *model.Transfer) *pb.TransferResponse {
    return &pb.TransferResponse{
        Id:                t.ID,
        FromAccountNumber: t.FromAccountNumber,
        ToAccountNumber:   t.ToAccountNumber,
        InitialAmount:     t.InitialAmount.StringFixed(4),
        FinalAmount:       t.FinalAmount.StringFixed(4),
        ExchangeRate:      t.ExchangeRate.StringFixed(4),
        Commission:        t.Commission.StringFixed(4),
        Timestamp:         t.Timestamp.String(),
    }
}
```
Change to:
```go
func transferToProto(t *model.Transfer) *pb.TransferResponse {
    return &pb.TransferResponse{
        Id:                t.ID,
        FromAccountNumber: t.FromAccountNumber,
        ToAccountNumber:   t.ToAccountNumber,
        InitialAmount:     t.InitialAmount.StringFixed(4),
        FinalAmount:       t.FinalAmount.StringFixed(4),
        ExchangeRate:      t.ExchangeRate.StringFixed(4),
        Commission:        t.Commission.StringFixed(4),
        Timestamp:         t.Timestamp.String(),
        Status:            t.Status,
    }
}
```

- [ ] **Step 4: Update `transferToJSON` in api-gateway handler**

Edit `api-gateway/internal/handler/transaction_handler.go`. Current (lines 650-661):
```go
func transferToJSON(t *transactionpb.TransferResponse) gin.H {
    return gin.H{
        "id":                  t.Id,
        "from_account_number": t.FromAccountNumber,
        "to_account_number":   t.ToAccountNumber,
        "initial_amount":      t.InitialAmount,
        "final_amount":        t.FinalAmount,
        "exchange_rate":       t.ExchangeRate,
        "commission":          t.Commission,
        "timestamp":           t.Timestamp,
    }
}
```
Change to:
```go
func transferToJSON(t *transactionpb.TransferResponse) gin.H {
    return gin.H{
        "id":                  t.Id,
        "from_account_number": t.FromAccountNumber,
        "to_account_number":   t.ToAccountNumber,
        "initial_amount":      t.InitialAmount,
        "final_amount":        t.FinalAmount,
        "exchange_rate":       t.ExchangeRate,
        "commission":          t.Commission,
        "timestamp":           t.Timestamp,
        "status":              t.Status,
    }
}
```

- [ ] **Step 5: Build both services to verify no compile errors**

```bash
cd transaction-service && go build ./...
cd ../api-gateway && go build ./...
```
Expected: exits 0 for both.

- [ ] **Step 6: Commit**

```bash
git add contract/proto/transaction/transaction.proto contract/transactionpb/ \
    transaction-service/internal/handler/grpc_handler.go \
    api-gateway/internal/handler/transaction_handler.go
git commit -m "feat(transaction): add status field to TransferResponse proto and API response"
```

---

## Task 3: Add `GET /api/payments/client/:client_id` Endpoint

**Files:**
- Modify: `contract/proto/transaction/transaction.proto` — add `ListPaymentsByClient` RPC
- Modify: `contract/transactionpb/*.go` — regenerated
- Modify: `transaction-service/internal/repository/payment_repository.go` — add `ListByAccountNumbers`
- Modify: `transaction-service/internal/service/payment_service.go` — update interface + add method
- Modify: `transaction-service/internal/handler/grpc_handler.go` — add RPC handler
- Modify: `api-gateway/internal/handler/transaction_handler.go` — add HTTP handler
- Modify: `api-gateway/internal/router/router.go` — register route

This endpoint mirrors `GET /api/transfers/client/:client_id`: the gateway resolves the client's
account numbers and passes them to the service, which queries `from_account_number IN (...)
OR to_account_number IN (...)`.

### 3a: Add RPC to proto

- [ ] **Step 1: Add `ListPaymentsByClient` to `TransactionService` and add request message**

In `contract/proto/transaction/transaction.proto`, inside `service TransactionService { ... }`,
after the `ListPaymentsByAccount` line, add:
```proto
  rpc ListPaymentsByClient(ListPaymentsByClientRequest) returns (ListPaymentsResponse);
```

Then add the request message (after `ListPaymentsByAccountRequest`):
```proto
message ListPaymentsByClientRequest {
  uint64 client_id = 1;
  int32 page = 2;
  int32 page_size = 3;
  repeated string account_numbers = 4;
}
```

- [ ] **Step 2: Regenerate proto**

```bash
make proto
```
Expected: exits 0, `TransactionServiceClient` and `TransactionServiceServer` now include
`ListPaymentsByClient`.

### 3b: Implement in payment repository

- [ ] **Step 3: Add `ListByAccountNumbers` to `PaymentRepository`**

Edit `transaction-service/internal/repository/payment_repository.go`. Add after the existing
`ListByAccount` method:
```go
// ListByAccountNumbers returns all payments where the given account numbers appear
// as sender or recipient. Used to aggregate payment history across all client accounts.
func (r *PaymentRepository) ListByAccountNumbers(accountNumbers []string, page, pageSize int) ([]model.Payment, int64, error) {
    if len(accountNumbers) == 0 {
        return nil, 0, nil
    }
    q := r.db.Model(&model.Payment{}).Where(
        "from_account_number IN ? OR to_account_number IN ?",
        accountNumbers, accountNumbers,
    )
    var total int64
    if err := q.Count(&total).Error; err != nil {
        return nil, 0, err
    }
    if page < 1 {
        page = 1
    }
    if pageSize < 1 {
        pageSize = 20
    }
    offset := (page - 1) * pageSize
    var payments []model.Payment
    if err := q.Order("timestamp DESC").Offset(offset).Limit(pageSize).Find(&payments).Error; err != nil {
        return nil, 0, err
    }
    return payments, total, nil
}
```

### 3c: Update PaymentRepo interface and service

- [ ] **Step 4: Add `ListByAccountNumbers` to `PaymentRepo` interface**

Edit `transaction-service/internal/service/payment_service.go`. The `PaymentRepo` interface
(lines 21-28) currently has 6 methods. Add a 7th:
```go
ListByAccountNumbers(accountNumbers []string, page, pageSize int) ([]model.Payment, int64, error)
```

- [ ] **Step 5: Add `ListPaymentsByClient` method to `PaymentService`**

Below `ListPaymentsByAccount` in `payment_service.go`, add:
```go
func (s *PaymentService) ListPaymentsByClient(accountNumbers []string, page, pageSize int) ([]model.Payment, int64, error) {
    return s.paymentRepo.ListByAccountNumbers(accountNumbers, page, pageSize)
}
```

- [ ] **Step 6: Build transaction-service to verify no compile errors**

```bash
cd transaction-service && go build ./...
```
Expected: exits 0.

### 3d: Add gRPC handler

- [ ] **Step 7: Add `ListPaymentsByClient` RPC handler in transaction-service**

Edit `transaction-service/internal/handler/grpc_handler.go`. After `ListPaymentsByAccount`,
add:
```go
func (h *TransactionGRPCHandler) ListPaymentsByClient(ctx context.Context, req *pb.ListPaymentsByClientRequest) (*pb.ListPaymentsResponse, error) {
    payments, total, err := h.paymentSvc.ListPaymentsByClient(
        req.GetAccountNumbers(),
        int(req.GetPage()),
        int(req.GetPageSize()),
    )
    if err != nil {
        return nil, status.Errorf(mapServiceError(err), "list payments by client: %v", err)
    }

    pbPayments := make([]*pb.PaymentResponse, 0, len(payments))
    for i := range payments {
        pbPayments = append(pbPayments, paymentToProto(&payments[i]))
    }
    return &pb.ListPaymentsResponse{Payments: pbPayments, Total: total}, nil
}
```

- [ ] **Step 8: Build transaction-service to verify no compile errors**

```bash
cd transaction-service && go build ./...
```
Expected: exits 0.

### 3e: Add gateway handler and route

- [ ] **Step 9: Add `ListPaymentsByClient` HTTP handler in api-gateway**

Edit `api-gateway/internal/handler/transaction_handler.go`. After `ListPaymentsByAccount`,
add (including Swagger annotations):
```go
// @Summary      List payments by client
// @Description  Returns all payments (sent or received) for all accounts belonging to a client. Clients can only view their own payments.
// @Tags         payments
// @Produce      json
// @Param        client_id  path   int  true   "Client ID"
// @Param        page       query  int  false  "Page number (default 1)"
// @Param        page_size  query  int  false  "Items per page (default 20)"
// @Security     BearerAuth
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/payments/client/{client_id} [get]
func (h *TransactionHandler) ListPaymentsByClient(c *gin.Context) {
    clientID, err := strconv.ParseUint(c.Param("client_id"), 10, 64)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "invalid client_id"})
        return
    }
    if !enforceClientSelf(c, clientID) {
        return
    }

    accountNumbers, err := h.resolveClientAccountNumbers(c, clientID)
    if err != nil {
        handleGRPCError(c, err)
        return
    }

    page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
    pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

    resp, err := h.txClient.ListPaymentsByClient(c.Request.Context(), &transactionpb.ListPaymentsByClientRequest{
        ClientId:       clientID,
        Page:           int32(page),
        PageSize:       int32(pageSize),
        AccountNumbers: accountNumbers,
    })
    if err != nil {
        handleGRPCError(c, err)
        return
    }

    payments := make([]gin.H, 0, len(resp.Payments))
    for _, p := range resp.Payments {
        payments = append(payments, paymentToJSON(p))
    }
    c.JSON(http.StatusOK, gin.H{
        "payments": payments,
        "total":    resp.Total,
    })
}
```

- [ ] **Step 10: Register the route in `router.go`**

Edit `api-gateway/internal/router/router.go`. In the `anyAuth` block (around line 304-306),
after the existing `GET /payments/account/:account_number` line, add:
```go
anyAuth.GET("/payments/client/:client_id", txHandler.ListPaymentsByClient)
```

- [ ] **Step 11: Build api-gateway to verify no compile errors**

```bash
cd api-gateway && go build ./...
```
Expected: exits 0.

- [ ] **Step 12: Regenerate Swagger docs**

```bash
cd api-gateway && swag init -g cmd/main.go --output docs
```
Expected: exits 0; `api-gateway/docs/swagger.json` updated.

- [ ] **Step 13: Commit**

```bash
git add contract/proto/transaction/transaction.proto contract/transactionpb/ \
    transaction-service/internal/repository/payment_repository.go \
    transaction-service/internal/service/payment_service.go \
    transaction-service/internal/handler/grpc_handler.go \
    api-gateway/internal/handler/transaction_handler.go \
    api-gateway/internal/router/router.go \
    api-gateway/docs/
git commit -m "feat(payments): add GET /api/payments/client/:client_id endpoint for payment history by client"
```

---

## Task 4: Add Missing Test-App Helper Functions

**Files:**
- Create: `test-app/workflows/helpers_test.go`

Three functions are called in `payment_test.go` and `transfer_test.go` but never defined,
causing compile failure of the entire `workflows` package.

- `getAccountBalance(t, c, accountNumber)` — fetches available balance from
  `GET /api/accounts/by-number/:account_number` (returns string in JSON, needs ParseFloat)
- `getBankRSDAccount(t, c)` — fetches first RSD account from `GET /api/bank-accounts`
- `scanKafkaForActivationToken(t, email)` — reads `notification.send-email` Kafka topic
  from the beginning, returns latest ACTIVATION token for the given email address
  (same logic as `ensureAdminActivated` in `auth_test.go`)

- [ ] **Step 1: Create `test-app/workflows/helpers_test.go`**

```go
package workflows

import (
    "context"
    "encoding/json"
    "fmt"
    "strconv"
    "testing"
    "time"

    kafkalib "github.com/segmentio/kafka-go"

    "github.com/exbanka/test-app/internal/client"
    "github.com/exbanka/test-app/internal/helpers"
)

// getAccountBalance fetches the available_balance for a single account by account number.
// Uses GET /api/accounts/by-number/:account_number (anyAuth — employee or client token).
// The balance field is returned as a JSON string by the account service; this function
// parses it to float64 for arithmetic comparisons in tests.
func getAccountBalance(t *testing.T, c *client.APIClient, accountNumber string) float64 {
    t.Helper()
    resp, err := c.GET("/api/accounts/by-number/" + accountNumber)
    if err != nil {
        t.Fatalf("getAccountBalance: GET /api/accounts/by-number/%s: %v", accountNumber, err)
    }
    helpers.RequireStatus(t, resp, 200)
    return parseJSONBalance(t, resp.Body, "available_balance")
}

// getBankRSDAccount returns the account number and available balance of the first
// bank-owned RSD account. Uses GET /api/bank-accounts (employee auth required).
func getBankRSDAccount(t *testing.T, c *client.APIClient) (string, float64) {
    t.Helper()
    resp, err := c.GET("/api/bank-accounts")
    if err != nil {
        t.Fatalf("getBankRSDAccount: GET /api/bank-accounts: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
    accts, ok := resp.Body["accounts"].([]interface{})
    if !ok {
        t.Fatalf("getBankRSDAccount: response missing 'accounts' array. Body: %s", string(resp.RawBody))
    }
    for _, a := range accts {
        m, ok := a.(map[string]interface{})
        if !ok {
            continue
        }
        if m["currency_code"] != "RSD" {
            continue
        }
        acctNum, _ := m["account_number"].(string)
        bal := parseJSONBalance(t, m, "available_balance")
        return acctNum, bal
    }
    t.Fatal("getBankRSDAccount: no bank account with currency_code=RSD found")
    return "", 0
}

// scanKafkaForActivationToken reads the notification.send-email topic from the earliest
// offset and returns the most recent activation token sent to the given email address.
// Blocks up to 15 seconds waiting for messages; fails the test if no token is found.
//
// This mirrors ensureAdminActivated in auth_test.go but is parameterised by email.
func scanKafkaForActivationToken(t *testing.T, email string) string {
    t.Helper()
    r := kafkalib.NewReader(kafkalib.ReaderConfig{
        Brokers:     []string{cfg.KafkaBrokers},
        Topic:       "notification.send-email",
        GroupID:     fmt.Sprintf("test-app-scan-activation-%d", time.Now().UnixNano()),
        StartOffset: kafkalib.FirstOffset,
        MaxWait:     500 * time.Millisecond,
    })
    defer r.Close()

    ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
    defer cancel()

    var latestToken string
    for {
        msg, err := r.ReadMessage(ctx)
        if err != nil {
            break // context timeout or EOF
        }
        var body struct {
            To        string            `json:"to"`
            EmailType string            `json:"email_type"`
            Data      map[string]string `json:"data"`
        }
        if json.Unmarshal(msg.Value, &body) != nil {
            continue
        }
        if body.To == email && body.EmailType == "ACTIVATION" {
            if token := body.Data["token"]; token != "" {
                latestToken = token // keep scanning for the latest one
            }
        }
    }

    if latestToken == "" {
        t.Fatalf("scanKafkaForActivationToken: no ACTIVATION token found for %s within 15s", email)
    }
    return latestToken
}

// parseJSONBalance extracts a balance field that may be a JSON number (float64) or
// a JSON string (e.g., "50000.0000" from the decimal serialisation). Returns float64.
func parseJSONBalance(t *testing.T, m map[string]interface{}, field string) float64 {
    t.Helper()
    val, ok := m[field]
    if !ok {
        t.Fatalf("parseJSONBalance: field %q not found in map", field)
    }
    switch v := val.(type) {
    case float64:
        return v
    case string:
        f, err := strconv.ParseFloat(v, 64)
        if err != nil {
            t.Fatalf("parseJSONBalance: field %q value %q is not a valid float: %v", field, v, err)
        }
        return f
    default:
        t.Fatalf("parseJSONBalance: field %q has unexpected type %T: %v", field, val, val)
        return 0
    }
}
```

- [ ] **Step 2: Build test-app to verify it now compiles**

```bash
cd test-app && go build ./...
```
Expected: exits 0 (no "undefined" errors for the three functions).

- [ ] **Step 3: Run tests to verify they can at least start**

```bash
cd test-app && go test ./workflows/... -run TestPayment_KafkaEventsOnPayment -v -timeout 30s
```
Expected: test runs (may pass or fail due to environment, but no compile errors).

- [ ] **Step 4: Commit**

```bash
git add test-app/workflows/helpers_test.go
git commit -m "fix(test-app): add missing getAccountBalance, getBankRSDAccount, scanKafkaForActivationToken helpers"
```

---

## Task 5: Fix Transfer End-to-End Test (Cross-Client Accounts)

**Files:**
- Modify: `test-app/workflows/transfer_test.go` — `TestTransfer_SameCurrency_EndToEnd`

**Root cause:** The test creates `acctNum1` for `client1ID` and `acctNum2` for `client2ID`
(two different clients), then calls `POST /api/transfers`. The transfer service rejects this
with HTTP 400 because transfers must be between accounts of the **same** client (different-client
transactions must use payments). Additionally, the test asserts `commission > 0`, but
same-currency same-client transfers always have zero commission (the service explicitly sets
`transfer.Commission = decimal.Zero` for this case).

**Fix:** Create `acctNum2` for `client1ID` (same client). Remove the `commission > 0`
assertion; for same-currency transfers, commission is 0 by design.

- [ ] **Step 1: Read `TestTransfer_SameCurrency_EndToEnd` in `transfer_test.go` lines 49-195**

Confirm the bug: `acct2Resp` creation block at ~line 89 passes `owner_id: client2ID`.

- [ ] **Step 2: Fix the destination account owner**

In `transfer_test.go`, in the "Create RSD account for client 2" block (~lines 89-101):

Current:
```go
// Create RSD account for client 2 with 100000 RSD
acct2Resp, err := adminClient.POST("/api/accounts", map[string]interface{}{
    "owner_id":        client2ID,
    "account_kind":    "current",
    ...
})
```

Change to:
```go
// Create second RSD account for client 1 (transfers are same-client only)
acct2Resp, err := adminClient.POST("/api/accounts", map[string]interface{}{
    "owner_id":        client1ID,
    "account_kind":    "current",
    ...
})
```

Also update the comment that says "Create RSD account for client 2".

- [ ] **Step 3: Remove the `client2ID` variable and fix `commission` assertion**

`client2ID` is created at ~line 73 via `createTestClient`. After Step 2 changes `acct2Resp`
to use `client1ID`, `client2ID` is no longer referenced anywhere in the test. Go treats unused
variables as compile errors, so **remove this line entirely**:
```go
client2ID := createTestClient(t, adminClient)
```

Find and remove or change the commission assertion:
```go
// BEFORE (wrong — same-currency transfers have 0 commission):
if commission <= 0 {
    t.Fatalf("expected non-zero commission for 5000 RSD transfer, got %f", commission)
}

// AFTER:
if commission != 0 {
    t.Fatalf("expected zero commission for same-currency same-client transfer, got %f", commission)
}
```

- [ ] **Step 4: Remove bank balance assertion (no commission = no bank balance change)**

The current test checks that `bankBalanceAfter - bankBalanceBefore ≈ commission`. With
`commission = 0`, this means the bank balance should not change. The test assertion
`actualBankIncrease ≈ commission` already handles commission=0 correctly
(it checks `|increase - 0| < 0.01`). Verify this and leave it as-is.

- [ ] **Step 5: Build test-app to verify no compile errors**

```bash
cd test-app && go build ./...
```
Expected: exits 0.

- [ ] **Step 6: Run the transfer test in isolation to verify it compiles and runs**

```bash
cd test-app && go test ./workflows/... -run TestTransfer_SameCurrency_EndToEnd -v -timeout 60s
```
Expected (with a live environment): test passes. Without a live environment: test may timeout
on Kafka/HTTP, but should not fail with 400/wrong status.

- [ ] **Step 7: Commit**

```bash
git add test-app/workflows/transfer_test.go
git commit -m "fix(test-app): fix transfer test to use same-client accounts; correct commission assertion to 0"
```

---

## Task 6: Update Test-App Config for Configurable Base Email with Numbered Tags

**Files:**
- Modify: `test-app/internal/config/config.go`
- Modify: `test-app/workflows/payment_test.go` — use `cfg.ClientEmail(1)` / `cfg.ClientEmail(2)`
- Modify: `test-app/workflows/transfer_test.go` — use `cfg.ClientEmail(1)`

**Goal:** All test workflows that need to activate a client (and therefore receive an email)
should use a predictable tagged email (`vlupsic11723rn+client1@raf.rs`) rather than a random
address. The base email is configurable via `TEST_BASE_EMAIL` env var.

- Tag format for clients: `base+clientN@domain` (e.g., `vlupsic11723rn+client1@raf.rs`)
- Tag format for admin: `base+admin@domain` (e.g., `vlupsic11723rn+admin@raf.rs`)
- Default base: `vlupsic11723rn@raf.rs`

**Important:** Only tests that call `scanKafkaForActivationToken` need a real tagged email.
Other tests that use `helpers.RandomEmail()` can continue doing so — they don't interact
with activation flows.

### 6a: Update config

- [ ] **Step 1: Read `test-app/internal/config/config.go` (already read in analysis)**

Current config has `testEmail string` hardcoded to `"lsavic12123rn@raf.rs"`, unexported.
`AdminEmail()` and `ClientEmail()` exist but are not numbered.

- [ ] **Step 2: Rewrite `config.go`**

Replace the entire file with:
```go
package config

import (
    "fmt"
    "os"
    "strings"
)

// Config holds test environment configuration.
type Config struct {
    GatewayURL   string
    KafkaBrokers string
    BaseEmail    string // base email used to derive tagged addresses
    Password     string
}

// Load reads configuration from environment variables with defaults.
func Load() *Config {
    return &Config{
        GatewayURL:   getEnv("TEST_GATEWAY_URL", "http://localhost:8080"),
        KafkaBrokers: getEnv("TEST_KAFKA_BROKERS", "localhost:9092"),
        BaseEmail:    getEnv("TEST_BASE_EMAIL", "vlupsic11723rn@raf.rs"),
        Password:     "AdminAdmin2026.!",
    }
}

// AdminEmail returns the base email with the +admin tag.
// e.g. vlupsic11723rn@raf.rs → vlupsic11723rn+admin@raf.rs
// This is the address used by the seeded admin employee account.
func (c *Config) AdminEmail() string {
    return buildTaggedEmail(c.BaseEmail, "admin")
}

// ClientEmail returns the base email with a numbered +clientN tag.
// e.g. vlupsic11723rn@raf.rs + n=1 → vlupsic11723rn+client1@raf.rs
// Use sequential numbers across tests to avoid email collisions.
func (c *Config) ClientEmail(n int) string {
    return buildTaggedEmail(c.BaseEmail, fmt.Sprintf("client%d", n))
}

// buildTaggedEmail inserts +tag before the @ in an email address.
func buildTaggedEmail(base, tag string) string {
    at := strings.Index(base, "@")
    if at < 0 {
        return base
    }
    return base[:at] + "+" + tag + base[at:]
}

func getEnv(key, fallback string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return fallback
}
```

- [ ] **Step 3: Update `docker-compose.yml` to seed admin with the tagged address**

The user-service seeds its admin employee from the `ADMIN_EMAIL` env var.  Currently
`docker-compose.yml` has `ADMIN_EMAIL: "vlupsic11723rn@raf.rs"` (no tag).  The updated
`AdminEmail()` returns `vlupsic11723rn+admin@raf.rs`, so the seeded address must match.

Edit `docker-compose.yml`, find the `user-service` environment block, and change:
```yaml
ADMIN_EMAIL: "vlupsic11723rn@raf.rs"
```
to:
```yaml
ADMIN_EMAIL: "vlupsic11723rn+admin@raf.rs"
```

> **Note:** After this change, a fresh `make docker-up` (or `make docker-down && make docker-up`)
> is required to re-seed the admin employee with the new address. Existing environments that
> already have the old address seeded will need their database wiped or a manual UPDATE.

### 6a-extra: Improve `ensureAdminActivated` to fail fast and use config password

- [ ] **Step 3b: Update `ensureAdminActivated` in `test-app/workflows/auth_test.go`**

Currently, if no Kafka activation token is found for the admin, the function only prints a
warning and returns — all downstream tests silently fail without a clear error. Change the
`if latestToken == ""` branch to call `log.Fatal(...)` instead of printing a warning, so
tests abort immediately with a clear message. This surfaces the email-mismatch problem right
away rather than every individual test failing with a cryptic "loginAsAdmin failed".

Also verify that `ActivateAccount` uses `adminPassword` (which it already does via `cfg.Password`)
so the password is driven by config throughout.

Current `if latestToken == ""` block (lines 74-77):
```go
if latestToken == "" {
    fmt.Println("[test-app] Warning: no activation token found for admin. Tests requiring admin login will fail.")
    return
}
```
Change to:
```go
if latestToken == "" {
    log.Fatalf("[test-app] FATAL: no ACTIVATION token found in Kafka for admin email %q. "+
        "Ensure the user-service ADMIN_EMAIL env var matches cfg.AdminEmail() (%q) and "+
        "the service has been restarted so a fresh activation token was published.", adminEmail, adminEmail)
}
```

- [ ] **Step 4: Build test-app to verify no compile errors**

```bash
cd test-app && go build ./...
```
Expected: exits 0. If `ClientEmail()` (no-arg) was called anywhere besides the two tests
we're about to fix, update those calls too.

### 6b: Update payment_test.go to use tagged emails

Tests `TestPayment_EndToEnd` and `TestPayment_WithFee` each create a client A that needs
activation. Assign numbered emails from the config so all notification emails land in the
same inbox.

- [ ] **Step 5: Update `TestPayment_EndToEnd` in `payment_test.go`**

Replace:
```go
emailA := helpers.RandomEmail()
```
with:
```go
emailA := cfg.ClientEmail(1)
```

- [ ] **Step 6: Update `TestPayment_WithFee` in `payment_test.go`**

Replace:
```go
emailA := helpers.RandomEmail()
```
with:
```go
emailA := cfg.ClientEmail(2)
```

### 6c: Update transfer_test.go to use tagged email

`TestTransfer_SameCurrency_EndToEnd` creates a client1 that needs activation.

- [ ] **Step 7: Update `TestTransfer_SameCurrency_EndToEnd` in `transfer_test.go`**

Replace:
```go
email1 := helpers.RandomEmail()
```
with:
```go
email1 := cfg.ClientEmail(3)
```

- [ ] **Step 8: Build and verify**

```bash
cd test-app && go build ./...
```
Expected: exits 0.

- [ ] **Step 9: Commit**

```bash
git add test-app/internal/config/config.go \
    test-app/workflows/payment_test.go \
    test-app/workflows/transfer_test.go \
    docker-compose.yml
git commit -m "feat(test-app): configurable base email with numbered client tags via TEST_BASE_EMAIL env var; align docker-compose ADMIN_EMAIL to +admin tag"
```

---

## Task 7: Update REST_API.md Documentation

**Files:**
- Modify: `docs/api/REST_API.md`

Two changes:
1. Add `status` field to the transfer response examples in section 8
2. Add a new section for `GET /api/payments/client/:client_id`

- [ ] **Step 1: Open `docs/api/REST_API.md` and find the `GET /api/transfers/:id` section (~line 1618)**

Verify the response example shows the transfer object fields. The example will be missing
`status`. Add it:

In the **Response body** example for any transfer response, add:
```json
"status": "completed"
```
alongside the other transfer fields. Apply this to all three transfer endpoint response
examples (POST /api/transfers, GET /api/transfers/:id, GET /api/transfers/client/:client_id,
POST /api/transfers/:id/execute).

- [ ] **Step 2: Add a new subsection for `GET /api/payments/client/:client_id` in section 7 (Payments)**

After the `### POST /api/payments/:id/execute` subsection (~line 1520), add:

```markdown
### GET /api/payments/client/:client_id

Returns all payments where any of the client's accounts appears as sender or recipient.
Equivalent to `GET /api/transfers/client/:client_id` but for payments.

**Authentication:** Required (employee or client; clients may only query their own `client_id`)

**Path parameters:**

| Parameter   | Type    | Description |
|-------------|---------|-------------|
| `client_id` | integer | Client ID   |

**Query parameters:**

| Parameter   | Type    | Default | Description             |
|-------------|---------|---------|-------------------------|
| `page`      | integer | 1       | Page number             |
| `page_size` | integer | 20      | Items per page          |

**Example request:**
```
GET /api/payments/client/42?page=1&page_size=20
Authorization: Bearer <token>
```

**Response 200:**
```json
{
  "payments": [
    {
      "id": 7,
      "from_account_number": "115-0001234567-10",
      "to_account_number": "115-0009876543-10",
      "initial_amount": "500.0000",
      "final_amount": "500.0000",
      "commission": "0.0000",
      "recipient_name": "John Doe",
      "payment_code": "289",
      "reference_number": "",
      "payment_purpose": "Test",
      "status": "completed",
      "timestamp": "2026-03-24 10:30:00 +0000 UTC"
    }
  ],
  "total": 1
}
```

**Response 400:** `{ "error": "invalid client_id" }`
**Response 401:** `{ "error": "not authenticated" }`
**Response 403:** `{ "error": "forbidden" }` (client accessing another client's data)
**Response 500:** `{ "error": "..." }`
```

- [ ] **Step 3: Commit**

```bash
git add docs/api/REST_API.md
git commit -m "docs: add status to transfer responses; document GET /api/payments/client/:client_id"
```

---

## Task 8: Full Build and Test Verification

- [ ] **Step 1: Build all services**

```bash
make build
```
Expected: exits 0; all services compile cleanly.

- [ ] **Step 2: Run all tests across all services**

```bash
make test
```
Expected: exits 0.

- [ ] **Step 3: Verify test-app compiles**

```bash
cd test-app && go vet ./...
```
Expected: no errors.

- [ ] **Step 4: (If environment available) Run test-app end-to-end**

```bash
cd test-app && go test ./workflows/... -v -timeout 300s 2>&1 | tee /tmp/testapp-run.log
grep -E "PASS|FAIL|panic" /tmp/testapp-run.log
```
Expected: `TestTransfer_SameCurrency_EndToEnd` PASS, `TestPayment_EndToEnd` PASS,
no undefined function panics.

- [ ] **Step 5: Verify verification email uses correct template**

With services running, create a payment, request a verification code, and check the
`notification.send-email` Kafka topic. The message should have `email_type: TRANSACTION_VERIFICATION`
and the data map should contain `verification_code` and `expires_in`:

```bash
# consume one message from notification.send-email
docker exec -it <kafka-container> kafka-console-consumer.sh \
  --bootstrap-server localhost:9092 \
  --topic notification.send-email --from-beginning --max-messages 5
```
Expected: one of the messages has `"email_type":"TRANSACTION_VERIFICATION"`.

---

## Quick Reference: All Files Changed

| File | Change type | Description |
|------|-------------|-------------|
| `transaction-service/internal/handler/grpc_handler.go` | Bug fix + feature | Email type fix; transferToProto status; ListPaymentsByClient RPC |
| `contract/proto/transaction/transaction.proto` | Feature | status in TransferResponse; ListPaymentsByClient RPC+message |
| `contract/transactionpb/*.go` | Auto-generated | Regenerated via `make proto` |
| `transaction-service/internal/repository/payment_repository.go` | Feature | ListByAccountNumbers |
| `transaction-service/internal/service/payment_service.go` | Feature | Interface + ListPaymentsByClient method |
| `api-gateway/internal/handler/transaction_handler.go` | Feature | ListPaymentsByClient handler; status in transferToJSON |
| `api-gateway/internal/router/router.go` | Feature | Register GET /api/payments/client/:client_id |
| `api-gateway/docs/` | Auto-generated | Regenerated Swagger |
| `test-app/internal/config/config.go` | Feature | BaseEmail field; ClientEmail(n int); TEST_BASE_EMAIL env var |
| `test-app/workflows/helpers_test.go` | Bug fix (new file) | getAccountBalance, getBankRSDAccount, scanKafkaForActivationToken |
| `test-app/workflows/transfer_test.go` | Bug fix | Cross-client accounts → same-client; commission assertion |
| `test-app/workflows/payment_test.go` | Feature | Tagged emails cfg.ClientEmail(1/2) |
| `docs/api/REST_API.md` | Docs | status in transfer responses; new endpoint section |
