# Verification Flow Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove explicit `/api/me/verification` and `/api/me/verification/validate` endpoints; auto-generate and email verification codes on payment/transfer creation; add dev console logging to notification-service; clean up related tests and docs.

**Architecture:** Proto gains `client_id`/`client_email` on Create*Request and `verification_code_expires_at` on response messages. Transaction-service auto-generates the code immediately after creating a payment/transfer and publishes it to Kafka → notification-service. The execute endpoints already validate internally — no change needed there. Swagger and REST_API.md are updated to remove deleted routes.

**Tech Stack:** Go, protobuf/gRPC, Kafka (segmentio/kafka-go), Gin, swaggo/swag, integration tests (build tag `integration`)

---

## File Map

| File | Role in this change |
|---|---|
| `contract/proto/transaction/transaction.proto` | Add fields to Create*Request/Response; remove Verification RPCs |
| `contract/transactionpb/` | Regenerated — never edit by hand |
| `transaction-service/internal/handler/grpc_handler.go` | Auto-generate code in Create*; remove Verification handlers |
| `api-gateway/internal/handler/transaction_handler.go` | Extract user_id/email from JWT in Create*; update JSON helpers; remove Verification handlers |
| `api-gateway/internal/router/router.go` | Remove 2 verification routes |
| `notification-service/internal/consumer/email_consumer.go` | Add dev console log |
| `api-gateway/docs/swagger.json`, `docs.go`, `swagger.yaml` | Regenerated — never edit by hand |
| `docs/api/REST_API.md` | Remove verification sections; note cards/:id is employee-only |
| `test-app/workflows/helpers_test.go` | Add `scanKafkaForVerificationCode`; update `setupActivatedClient` to return email |
| `test-app/workflows/payment_test.go` | Delete `TestPayment_VerificationCodeRequired`; replace verification calls with Kafka scan |
| `test-app/workflows/transfer_test.go` | Replace verification calls with Kafka scan |
| `test-app/workflows/workflow_test.go` | Replace verification calls with Kafka scan |
| `test-app/workflows/negative_test.go` | Remove `/api/me/verification` entry from route table |

---

## Task 1: Proto — add fields, remove Verification RPCs

**Files:**
- Modify: `contract/proto/transaction/transaction.proto`

- [ ] **Step 1: Edit the proto file**

  In `CreatePaymentRequest` (currently ends at field 7), add:
  ```proto
  message CreatePaymentRequest {
    string from_account_number = 1;
    string to_account_number = 2;
    string amount = 3;
    string recipient_name = 4;
    string payment_code = 5;
    string reference_number = 6;
    string payment_purpose = 7;
    uint64 client_id = 8;
    string client_email = 9;
  }
  ```

  In `CreateTransferRequest` (currently ends at field 5), add:
  ```proto
  message CreateTransferRequest {
    string from_account_number = 1;
    string to_account_number = 2;
    string amount = 3;
    string from_currency = 4;
    string to_currency = 5;
    uint64 client_id = 6;
    string client_email = 7;
  }
  ```

  In `PaymentResponse` (currently ends at field 12), add:
  ```proto
  int64 verification_code_expires_at = 13;
  ```

  In `TransferResponse` (currently ends at field 9), add:
  ```proto
  int64 verification_code_expires_at = 10;
  ```

  In `TransactionService`, remove these two RPC lines:
  ```proto
  rpc CreateVerificationCode(CreateVerificationCodeRequest) returns (CreateVerificationCodeResponse);
  rpc ValidateVerificationCode(ValidateVerificationCodeRequest) returns (ValidateVerificationCodeResponse);
  ```

  Delete these four message definitions entirely:
  - `CreateVerificationCodeRequest`
  - `CreateVerificationCodeResponse`
  - `ValidateVerificationCodeRequest`
  - `ValidateVerificationCodeResponse`

- [ ] **Step 2: Regenerate proto**

  Run from repo root:
  ```bash
  make proto
  ```
  Expected: exits 0, no errors. Files updated in `contract/transactionpb/`.

- [ ] **Step 3: Verify the generated Go files compile**

  ```bash
  cd contract && go build ./...
  ```
  Expected: exits 0.

- [ ] **Step 4: Commit**

  ```bash
  git add contract/proto/transaction/transaction.proto contract/transactionpb/
  git commit -m "feat(proto): add client_id/email to Create*Request, expires_at to responses, remove Verification RPCs"
  ```

---

## Task 2: transaction-service — auto-generate code on Create*

**Files:**
- Modify: `transaction-service/internal/handler/grpc_handler.go`

- [ ] **Step 1: Update `CreatePayment` to auto-generate verification code**

  Find `CreatePayment`. Currently ends with:
  ```go
  // Publish payment-created Kafka event
  msg := kafkamsg.PaymentCompletedMessage{ ... }
  if err := h.producer.PublishPaymentCreated(ctx, msg); err != nil { ... }
  return paymentToProto(payment), nil
  ```

  Replace the `return` with:
  ```go
  // Auto-generate verification code and email it to the client
  var vcExpiresAt int64
  if req.GetClientId() > 0 {
      vc, code, vcErr := h.verificationSvc.CreateVerificationCode(ctx, req.GetClientId(), payment.ID, "payment")
      if vcErr != nil {
          log.Printf("warn: failed to create verification code for payment %d: %v", payment.ID, vcErr)
      } else {
          vcExpiresAt = vc.ExpiresAt.Unix()
          if email := req.GetClientEmail(); email != "" {
              _ = h.producer.SendEmail(ctx, kafkamsg.SendEmailMessage{
                  To:        email,
                  EmailType: kafkamsg.EmailTypeTransactionVerify,
                  Data: map[string]string{
                      "verification_code": code,
                      "expires_in":        "5 minutes",
                  },
              })
          }
      }
  }

  resp := paymentToProto(payment)
  resp.VerificationCodeExpiresAt = vcExpiresAt
  return resp, nil
  ```

- [ ] **Step 2: Update `CreateTransfer` the same way**

  Find `CreateTransfer`. Currently ends with:
  ```go
  // Publish transfer-created Kafka event
  msg := kafkamsg.TransferCompletedMessage{ ... }
  if err := h.producer.PublishTransferCreated(ctx, msg); err != nil { ... }
  return transferToProto(transfer), nil
  ```

  Replace the `return` with:
  ```go
  // Auto-generate verification code and email it to the client
  var vcExpiresAt int64
  if req.GetClientId() > 0 {
      vc, code, vcErr := h.verificationSvc.CreateVerificationCode(ctx, req.GetClientId(), transfer.ID, "transfer")
      if vcErr != nil {
          log.Printf("warn: failed to create verification code for transfer %d: %v", transfer.ID, vcErr)
      } else {
          vcExpiresAt = vc.ExpiresAt.Unix()
          if email := req.GetClientEmail(); email != "" {
              _ = h.producer.SendEmail(ctx, kafkamsg.SendEmailMessage{
                  To:        email,
                  EmailType: kafkamsg.EmailTypeTransactionVerify,
                  Data: map[string]string{
                      "verification_code": code,
                      "expires_in":        "5 minutes",
                  },
              })
          }
      }
  }

  resp := transferToProto(transfer)
  resp.VerificationCodeExpiresAt = vcExpiresAt
  return resp, nil
  ```

- [ ] **Step 3: Delete `CreateVerificationCode` and `ValidateVerificationCode` handler methods**

  Delete both functions entirely (approximately lines 369–404):
  ```go
  // ---- Verification Code RPCs ----
  func (h *TransactionGRPCHandler) CreateVerificationCode(...) { ... }
  func (h *TransactionGRPCHandler) ValidateVerificationCode(...) { ... }
  ```

- [ ] **Step 4: Build to verify**

  ```bash
  cd transaction-service && go build ./...
  ```
  Expected: exits 0.

- [ ] **Step 5: Commit**

  ```bash
  git add transaction-service/internal/handler/grpc_handler.go
  git commit -m "feat(transaction-service): auto-generate verification code on payment/transfer creation"
  ```

---

## Task 3: api-gateway — pass client identity, update JSON helpers, remove Verification handlers

**Files:**
- Modify: `api-gateway/internal/handler/transaction_handler.go`
- Modify: `api-gateway/internal/router/router.go`

- [ ] **Step 1: Update `CreatePayment` to extract user_id and email from JWT**

  Find `CreatePayment` in `transaction_handler.go` (around line 62). After validation (before the gRPC call), add:
  ```go
  userID, _ := c.Get("user_id")
  uid, _ := userID.(int64)
  emailVal, _ := c.Get("email")
  clientEmail, _ := emailVal.(string)
  ```

  Update the gRPC call to pass the new fields:
  ```go
  resp, err := h.txClient.CreatePayment(c.Request.Context(), &transactionpb.CreatePaymentRequest{
      FromAccountNumber: req.FromAccountNumber,
      ToAccountNumber:   req.ToAccountNumber,
      Amount:            fmt.Sprintf("%.4f", req.Amount),
      RecipientName:     req.RecipientName,
      PaymentCode:       req.PaymentCode,
      ReferenceNumber:   req.ReferenceNumber,
      PaymentPurpose:    req.PaymentPurpose,
      ClientId:          uint64(uid),
      ClientEmail:       clientEmail,
  })
  ```

- [ ] **Step 2: Find `CreateTransfer` and apply the same change**

  The handler is in the same file. Add the same `user_id`/`email` extraction and pass `ClientId`/`ClientEmail` in `CreateTransferRequest`.

- [ ] **Step 3: Update `paymentToJSON` to include `verification_code_expires_at`**

  Find `paymentToJSON` (around line 941). Change it to:
  ```go
  func paymentToJSON(p *transactionpb.PaymentResponse) gin.H {
      h := gin.H{
          "id":                  p.Id,
          "from_account_number": p.FromAccountNumber,
          "to_account_number":   p.ToAccountNumber,
          "initial_amount":      p.InitialAmount,
          "final_amount":        p.FinalAmount,
          "commission":          p.Commission,
          "recipient_name":      p.RecipientName,
          "payment_code":        p.PaymentCode,
          "reference_number":    p.ReferenceNumber,
          "payment_purpose":     p.PaymentPurpose,
          "status":              p.Status,
          "timestamp":           p.Timestamp,
      }
      if p.VerificationCodeExpiresAt > 0 {
          h["verification_code_expires_at"] = p.VerificationCodeExpiresAt
      }
      return h
  }
  ```

- [ ] **Step 4: Update `transferToJSON` the same way**

  ```go
  func transferToJSON(t *transactionpb.TransferResponse) gin.H {
      h := gin.H{
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
      if t.VerificationCodeExpiresAt > 0 {
          h["verification_code_expires_at"] = t.VerificationCodeExpiresAt
      }
      return h
  }
  ```

- [ ] **Step 5: Delete `CreateVerificationCode` and `ValidateVerificationCode` handler functions**

  Delete both handler functions and their associated request structs (`createVerificationCodeRequest`, `validateVerificationCodeRequest`) from `transaction_handler.go`.

- [ ] **Step 6: Remove verification routes from router**

  In `router.go`, delete the two lines under the `// Verification` comment (around line 113):
  ```go
  // Verification
  me.POST("/verification", txHandler.CreateVerificationCode)
  me.POST("/verification/validate", txHandler.ValidateVerificationCode)
  ```
  Also delete the `// Verification` comment line.

- [ ] **Step 7: Build to verify**

  ```bash
  cd api-gateway && go build ./...
  ```
  Expected: exits 0.

- [ ] **Step 8: Commit**

  ```bash
  git add api-gateway/internal/handler/transaction_handler.go api-gateway/internal/router/router.go
  git commit -m "feat(api-gateway): pass client identity on Create*, add expires_at to responses, remove Verification endpoints"
  ```

---

## Task 4: notification-service — dev console logging

**Files:**
- Modify: `notification-service/internal/consumer/email_consumer.go`

- [ ] **Step 1: Add dev log in `handleMessage`**

  In `handleMessage`, after the `json.Unmarshal` call and before `sender.BuildEmail`, insert:
  ```go
  log.Printf("[DEV] email queued | type=%s to=%s data=%v", emailMsg.EmailType, emailMsg.To, emailMsg.Data)
  ```

  The function currently looks like:
  ```go
  func (c *EmailConsumer) handleMessage(ctx context.Context, data []byte) {
      var emailMsg kafkamsg.SendEmailMessage
      if err := json.Unmarshal(data, &emailMsg); err != nil {
          log.Printf("error unmarshaling email message: %v", err)
          return
      }
      // INSERT HERE
      subject, body := sender.BuildEmail(emailMsg.EmailType, emailMsg.Data)
      ...
  ```

- [ ] **Step 2: Build to verify**

  ```bash
  cd notification-service && go build ./...
  ```
  Expected: exits 0.

- [ ] **Step 3: Commit**

  ```bash
  git add notification-service/internal/consumer/email_consumer.go
  git commit -m "feat(notification-service): log all email tokens and codes to console for local dev"
  ```

---

## Task 5: REST_API.md — remove verification sections, fix cards/:id docs

**Files:**
- Modify: `docs/api/REST_API.md`

- [ ] **Step 1: Remove the deprecated `POST /api/verification` section**

  Find and delete the section:
  ```markdown
  ### ~~POST /api/verification~~ (moved — use POST /api/me/verification)
  > **Moved to `/api/me/*`.** ...
  ```

- [ ] **Step 2: Remove `POST /api/me/verification` section**

  Find and delete the full section starting with `### POST /api/me/verification` through its closing `---` separator.

- [ ] **Step 3: Remove the deprecated `POST /api/verification/validate` section**

  Find and delete:
  ```markdown
  ### ~~POST /api/verification/validate~~ (moved — use POST /api/me/verification/validate)
  > **Moved to `/api/me/*`.** ...
  ```

- [ ] **Step 4: Remove `POST /api/me/verification/validate` section**

  Find and delete the full section starting with `### POST /api/me/verification/validate`.

- [ ] **Step 5: Update references to removed endpoints**

  Search for any remaining references to `/api/me/verification` in `REST_API.md` (e.g., in the execute payment/transfer sections that say "obtain via POST /api/me/verification"). Update them to say the verification code is sent automatically by email when the payment/transfer is created.

  Search:
  ```bash
  grep -n "api/me/verification\|api/verification" docs/api/REST_API.md
  ```

  For each remaining mention, update the text to describe the new auto-send behavior.

- [ ] **Step 6: Find `GET /api/cards/:id` section and add employee-only note**

  Find the section for `GET /api/cards/:id`. Add a note near the top:
  ```
  **Authentication:** Employee JWT only (`AuthMiddleware` + `cards.read` permission). Clients must use `GET /api/me/cards/:id` instead.
  ```

- [ ] **Step 7: Update `POST /api/me/payments` and `POST /api/me/transfers` response documentation**

  In both sections, add `verification_code_expires_at` to the response body example:
  ```json
  {
    "id": 42,
    ...
    "verification_code_expires_at": 1743000300
  }
  ```
  And a note: "A verification code has been sent to the client's registered email. Use it when calling the execute endpoint."

- [ ] **Step 8: Commit**

  ```bash
  git add docs/api/REST_API.md
  git commit -m "docs(rest-api): remove verification endpoints, document auto-send flow, clarify cards/:id is employee-only"
  ```

---

## Task 6: Swagger regeneration

**Files:**
- Modify: `api-gateway/internal/handler/transaction_handler.go` (update swagger annotations)
- Modify (generated): `api-gateway/docs/swagger.json`, `api-gateway/docs/docs.go`, `api-gateway/docs/swagger.yaml`

- [ ] **Step 1: Update swagger annotations on `CreatePayment`**

  Find the `// @Router /api/payments [post]` annotation on `CreatePayment` (around line 61). Update the `@Success` line to document the new `verification_code_expires_at` field. The swagger comment block for `CreatePayment` should become:
  ```go
  // @Summary      Create payment
  // @Description  Creates a pending payment and automatically sends a verification code to the client's email.
  // @Tags         payments
  // @Accept       json
  // @Produce      json
  // @Param        body  body  createPaymentRequest  true  "Payment data"
  // @Security     BearerAuth
  // @Success      201   {object}  map[string]interface{}  "Payment created with verification_code_expires_at"
  // @Failure      400   {object}  map[string]string
  // @Failure      401   {object}  map[string]string
  // @Failure      500   {object}  map[string]string
  // @Router       /api/me/payments [post]
  ```
  Note: verify the current `@Router` value — it may already say `/api/me/payments` or `/api/payments`. Use `/api/me/payments`.

- [ ] **Step 2: Update swagger annotations on `CreateTransfer`**

  Same pattern: update `@Description` and `@Router` to `/api/me/transfers`.

- [ ] **Step 3: Regenerate swagger**

  ```bash
  cd api-gateway && swag init -g cmd/main.go --output docs
  ```
  Expected: exits 0, updates `docs/swagger.json`, `docs/docs.go`, `docs/swagger.yaml`.

- [ ] **Step 4: Verify the verification routes are gone from swagger**

  ```bash
  grep -c "verification" api-gateway/docs/swagger.json
  ```
  Expected: matches should only be for `verification_code` fields in execute requests — not for the removed `/api/verification` routes.

- [ ] **Step 5: Commit**

  ```bash
  git add api-gateway/internal/handler/transaction_handler.go api-gateway/docs/
  git commit -m "docs(swagger): regenerate after verification endpoint removal"
  ```

---

## Task 7: test-app helpers — add Kafka scanner and update setupActivatedClient

**Files:**
- Modify: `test-app/workflows/helpers_test.go`

- [ ] **Step 1: Update `setupActivatedClient` return signature to include email**

  Change:
  ```go
  func setupActivatedClient(t *testing.T, adminC *client.APIClient) (clientID int, accountNumber string, clientC *client.APIClient) {
  ```
  To:
  ```go
  func setupActivatedClient(t *testing.T, adminC *client.APIClient) (clientID int, accountNumber string, clientC *client.APIClient, email string) {
  ```

  At the start of the function, replace:
  ```go
  email := helpers.RandomEmail()
  ```
  With:
  ```go
  email = helpers.RandomEmail()
  ```
  (assign to named return `email` instead of declaring a new variable)

  The `return` at the end is implicit via named returns — no change needed there (or add `return clientID, accountNumber, clientC, email` explicitly if not already named-return style).

- [ ] **Step 2: Update all 17 callers of `setupActivatedClient` that don't use the email**

  These files have callers that need `_` added as 4th return:
  - `test-app/workflows/workflow_test.go` lines 141 and 428
  - `test-app/workflows/payment_test.go` line 539
  - `test-app/workflows/card_test.go` lines 205, 225, 246, 268, 288, 325, 374, 414
  - `test-app/workflows/loan_test.go` lines 111, 238, 282
  - `test-app/workflows/transfer_test.go` lines 310 and 339 (these WILL use the email — see Tasks 8 and 9)
  - `test-app/workflows/card_request_test.go` line 169

  Callers that DO need the email (capture it as a named variable):
  - `workflow_test.go:141` — `_, acctNumA, clientA, emailA := setupActivatedClient(t, adminClient)` (email used in Task 9)
  - `workflow_test.go:428` — `_, accountNumber, clientC, clientEmail := setupActivatedClient(t, adminClient)` (email used in Task 9)
  - `transfer_test.go:339` — `_, accountNumber, clientC, clientEmail := setupActivatedClient(t, adminClient)` (email used in Task 9; note: the `GET /api/me` + `meClientID` in this test must be KEPT — it's used for a second account creation, not just verification)

  Callers that do NOT need email (add `_` as 4th return):
  - `card_test.go` lines 205, 225, 246, 268, 288, 325, 374, 414
  - `loan_test.go` lines 111, 238, 282
  - `transfer_test.go:310` (`TestTransfer_PaymentRecipientCRUD`) — no verification call in this test
  - `card_request_test.go:169`

  Note: `payment_test.go:539` is handled explicitly in Task 8 Step 6 — do NOT add `_` here; Task 8 will set the correct variable name.

- [ ] **Step 3: Add `scanKafkaForVerificationCode` helper**

  Add the following function after `scanKafkaForActivationToken`:
  ```go
  // scanKafkaForVerificationCode reads the notification.send-email topic from the earliest
  // offset and returns the most recent TRANSACTION_VERIFICATION code sent to the given email.
  // Blocks up to 15 seconds; fails the test if no code is found.
  func scanKafkaForVerificationCode(t *testing.T, email string) string {
      t.Helper()
      r := kafkalib.NewReader(kafkalib.ReaderConfig{
          Brokers:     []string{cfg.KafkaBrokers},
          Topic:       "notification.send-email",
          Partition:   0,
          StartOffset: kafkalib.FirstOffset,
          MaxWait:     500 * time.Millisecond,
      })
      defer r.Close()

      ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
      defer cancel()

      var latestCode string
      for {
          msg, err := r.ReadMessage(ctx)
          if err != nil {
              break
          }
          var body struct {
              To        string            `json:"to"`
              EmailType string            `json:"email_type"`
              Data      map[string]string `json:"data"`
          }
          if json.Unmarshal(msg.Value, &body) != nil {
              continue
          }
          if body.To == email && body.EmailType == "TRANSACTION_VERIFICATION" {
              if code := body.Data["verification_code"]; code != "" {
                  latestCode = code
              }
          }
      }

      if latestCode == "" {
          t.Fatalf("scanKafkaForVerificationCode: no TRANSACTION_VERIFICATION code found for %s within 15s", email)
      }
      return latestCode
  }
  ```

- [ ] **Step 4: Build test-app to catch compilation errors**

  ```bash
  cd test-app && go build ./...
  ```
  Expected: exits 0.

- [ ] **Step 5: Commit**

  ```bash
  git add test-app/workflows/helpers_test.go
  git commit -m "test(helpers): add scanKafkaForVerificationCode helper, add email return to setupActivatedClient"
  ```

---

## Task 8: test-app — update payment_test.go

**Files:**
- Modify: `test-app/workflows/payment_test.go`

- [ ] **Step 1: Delete `TestPayment_VerificationCodeRequired`**

  Delete the entire function (lines 53–64):
  ```go
  func TestPayment_VerificationCodeRequired(t *testing.T) { ... }
  ```

- [ ] **Step 2: Update `TestPayment_EndToEnd`**

  Remove the `/api/me` call and `meClientID` extraction (lines ~152–157) — it's only used for verification.

  Replace the verification block (lines ~181–191):
  ```go
  // Create verification code
  verResp, err := clientA.POST("/api/me/verification", map[string]interface{}{
      "client_id":        meClientID,
      "transaction_id":   paymentID,
      "transaction_type": "payment",
  })
  ...
  verCode := helpers.GetStringField(t, verResp, "code")
  ```
  With:
  ```go
  verCode := scanKafkaForVerificationCode(t, emailA)
  ```

- [ ] **Step 3: Update `TestPayment_WithFee`**

  Same pattern: remove `/api/me` call and `meClientID`, replace verification block with:
  ```go
  verCode := scanKafkaForVerificationCode(t, emailA)
  ```

- [ ] **Step 4: Update `TestPayment_ExternalPayment`**

  Same pattern: remove `/api/me` call and `meClientID`, replace verification block (inside `if payResp.StatusCode != 201` guard) with:
  ```go
  verCode := scanKafkaForVerificationCode(t, emailA)
  ```

- [ ] **Step 5: Update `TestPayment_WrongOTPCodeRejected`**

  The test currently calls `POST /api/me/verification` just to ensure a code exists before sending the wrong one. Remove that call entirely — `CreatePayment` now auto-sends the code, so there's no setup needed.

  Remove the `/api/me` call, `meClientID`, and the verification POST block (lines ~495–522).

  The test flow becomes:
  ```go
  // Payment created — code auto-sent to emailA
  // Execute with WRONG code (code was auto-generated; we just use a wrong one)
  execResp, err := clientA.POST(...)
  ```

- [ ] **Step 6: Update `TestPayment_InsufficientBalance`**

  This test uses `setupActivatedClient`. Update call to capture email:
  ```go
  _, accountNumber, clientC, clientEmail := setupActivatedClient(t, adminClient)
  ```

  In the `if payResp.StatusCode == 201` block, remove the `/api/me` call, `meClientID`, and verification POST. Replace with:
  ```go
  paymentID := int(helpers.GetNumberField(t, payResp, "id"))
  verCode := scanKafkaForVerificationCode(t, clientEmail)
  execResp, err := clientC.POST(fmt.Sprintf("/api/me/payments/%d/execute", paymentID), map[string]interface{}{
      "verification_code": verCode,
  })
  ```

- [ ] **Step 7: Remove unused imports if any**

  Check if `"strconv"` (used only in `TestPayment_WithFee` for commission parsing) and `"time"` are still needed. Remove if not.

- [ ] **Step 8: Build to verify**

  ```bash
  cd test-app && go build ./...
  ```
  Expected: exits 0.

- [ ] **Step 9: Commit**

  ```bash
  git add test-app/workflows/payment_test.go
  git commit -m "test(payments): replace explicit verification calls with scanKafkaForVerificationCode"
  ```

---

## Task 9: test-app — update transfer_test.go and workflow_test.go

**Files:**
- Modify: `test-app/workflows/transfer_test.go`
- Modify: `test-app/workflows/workflow_test.go`

- [ ] **Step 1: Update the three verification blocks in transfer_test.go**

  Verification calls are at lines 138, 276, and 373.

  **Lines 138 and 276** — these tests set up clients with known email variables (e.g., `email1`). Apply the standard replacement:
  ```go
  // Remove this block:
  verResp, err := client1.POST("/api/me/verification", map[string]interface{}{
      "client_id":        meClientID,
      "transaction_id":   transferID,
      "transaction_type": "transfer",
  })
  if err != nil { t.Fatalf(...) }
  helpers.RequireStatus(t, verResp, 201)
  verCode := helpers.GetStringField(t, verResp, "code")

  // Replace with:
  verCode := scanKafkaForVerificationCode(t, email1)
  ```
  Also remove the `GET /api/me` call and `meClientID` variable if they are only used for the verification block.

  **Line 373** — this is in `TestTransfer_InsufficientBalance` which uses `setupActivatedClient` at line 339 (already updated to capture email in Task 7):
  ```go
  _, accountNumber, clientC, clientEmail := setupActivatedClient(t, adminClient)
  ```
  Replace the verification block at line 373 with:
  ```go
  verCode := scanKafkaForVerificationCode(t, clientEmail)
  ```
  **Important:** In `TestTransfer_InsufficientBalance`, there is a `GET /api/me` call and `meClientID` variable that is used to create a **second bank account** (not only for verification). Do NOT remove the `GET /api/me` call or `meClientID` — only remove the verification POST block.

- [ ] **Step 2: Update workflow_test.go**

  Lines 206 and 377 have verification calls.

  **Line 206** — `TestWorkflow_FullPaymentWithFee`. The test sets up `clientA` via `setupActivatedClient` at line 141, which was updated in Task 7 to capture email as `emailA`. The test also calls `GET /api/me` (around lines 180–185) purely to get `meClientID` for the verification request. Steps:
  1. Remove the entire `GET /api/me` block (lines ~180–185) and the `meClientID` variable — it is only used for verification.
  2. Replace the verification block at line 206 with:
     ```go
     verCode := scanKafkaForVerificationCode(t, emailA)
     ```

  **Line 377** — uses `clientC` from `setupActivatedClient` at line 428. Task 7 already updates line 428 to capture `clientEmail`. Replace the verification block with:
  ```go
  verCode := scanKafkaForVerificationCode(t, clientEmail)
  ```

- [ ] **Step 4: Build to verify**

  ```bash
  cd test-app && go build ./...
  ```
  Expected: exits 0.

- [ ] **Step 5: Commit**

  ```bash
  git add test-app/workflows/transfer_test.go test-app/workflows/workflow_test.go
  git commit -m "test(transfers/workflow): replace explicit verification calls with scanKafkaForVerificationCode"
  ```

---

## Task 10: test-app — remove verification route from negative_test.go

**Files:**
- Modify: `test-app/workflows/negative_test.go`

- [ ] **Step 1: Find and remove the `/api/me/verification` entry**

  In `TestNeg_EmployeeCannotAccessClientOnlyRoutes`, the `clientOnlyRoutes` slice (around line 93) currently contains:
  ```go
  {"POST", "/api/me/verification"},
  ```
  Delete that line.

- [ ] **Step 2: Build to verify**

  ```bash
  cd test-app && go build ./...
  ```
  Expected: exits 0.

- [ ] **Step 3: Commit**

  ```bash
  git add test-app/workflows/negative_test.go
  git commit -m "test(negative): remove deleted /api/me/verification from client-only route table"
  ```

---

## Task 11: Full build verification

- [ ] **Step 1: Build all services**

  ```bash
  make build
  ```
  Expected: exits 0. All services build, swagger is regenerated as part of api-gateway build.

- [ ] **Step 2: Verify no references to removed proto RPCs remain**

  ```bash
  grep -r "CreateVerificationCode\|ValidateVerificationCode" --include="*.go" .
  ```
  Expected: zero matches (the service and repository still have the model/table — those are fine; only the gRPC handler and gateway handler should be gone).

  If there are matches, fix them before proceeding.

- [ ] **Step 3: Verify no remaining `/api/me/verification` route references in Go code**

  ```bash
  grep -r "api/me/verification" --include="*.go" .
  ```
  Expected: only test helper comments or zero matches. No router or handler registrations.

- [ ] **Step 4: Final commit if any cleanup was needed**

  ```bash
  git add -A
  git commit -m "chore: cleanup after verification flow refactor"
  ```
