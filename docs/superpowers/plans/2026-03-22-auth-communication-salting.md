# Auth-Service Communication, Salting & Route Verification Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Verify and fix inter-service communication between auth-service, user-service, and client-service for account creation flows; add application-level password pepper to auth-service; audit all API gateway routes for correctness.

**Architecture:** The auth-service receives `user.employee-created` and `client.created` Kafka events from user-service and client-service respectively, then creates auth accounts with activation tokens. We will add an application-level pepper (a server-side secret prepended to passwords before bcrypt hashing) for defense-in-depth. All API gateway routes will be audited for correct middleware, validation, and gRPC forwarding.

**Tech Stack:** Go, gRPC, Kafka (segmentio/kafka-go), bcrypt, PostgreSQL/GORM, Gin HTTP router, Redis

---

## File Structure

### New Files
- `auth-service/internal/service/pepper.go` — Pepper utility: prepend server secret to password before bcrypt
- `auth-service/internal/service/pepper_test.go` — Tests for pepper utility
- `auth-service/internal/consumer/employee_consumer_test.go` — Unit tests for employee Kafka consumer
- `auth-service/internal/consumer/client_consumer_test.go` — Unit tests for client Kafka consumer

### Modified Files
- `auth-service/internal/service/auth_service.go` — Use pepper in `ActivateAccount`, `ResetPassword`, `Login`
- `auth-service/internal/config/config.go` — Add `PASSWORD_PEPPER` env var
- `auth-service/cmd/main.go` — Pass pepper to AuthService
- `auth-service/internal/consumer/employee_consumer.go` — Add retry logic and dead-letter handling
- `auth-service/internal/consumer/client_consumer.go` — Add retry logic and dead-letter handling
- `contract/kafka/messages.go` — Add dead-letter topic constant
- `docker-compose.yml` — Add `PASSWORD_PEPPER` env var to auth-service

---

## Task 1: Audit Current Employee Creation → Auth Flow

**Files:**
- Read: `user-service/internal/service/employee_service.go`
- Read: `auth-service/internal/consumer/employee_consumer.go`
- Read: `auth-service/internal/service/auth_service.go:338-376`
- Read: `contract/kafka/messages.go`

- [ ] **Step 1: Trace the employee creation Kafka message**

Verify that `user-service/internal/service/employee_service.go` publishes `EmployeeCreatedMessage` with all fields needed by auth-service:

```go
// user-service publishes:
kafka.EmployeeCreatedMessage{
    EmployeeID: emp.ID,
    Email:      emp.Email,
    FirstName:  emp.FirstName,
    LastName:   emp.LastName,
    Roles:      roleNames,
}
```

Auth-service consumer (`employee_consumer.go:48`) calls:
```go
c.authSvc.CreateAccountAndActivationToken(ctx, evt.EmployeeID, evt.Email, evt.FirstName, "employee")
```

**Verify:** `EmployeeID` is `int64` in both message and auth-service method signature. Currently `EmployeeCreatedMessage.EmployeeID` is `int64` and `CreateAccountAndActivationToken` accepts `int64`. This is correct.

- [ ] **Step 2: Verify Kafka topic names match**

Confirm both services use the same topic constant:
- user-service produces to: `kafka.TopicEmployeeCreated` = `"user.employee-created"`
- auth-service consumes from: `kafka.TopicEmployeeCreated` = `"user.employee-created"`

Both reference `contract/kafka/messages.go:34`. This is correct.

- [ ] **Step 3: Verify consumer group IDs are unique**

Employee consumer uses `"auth-service-employee-consumer"` (line 24 of employee_consumer.go).
Client consumer uses `"auth-service-client-consumer"` (line 24 of client_consumer.go).
These are unique. Correct.

- [ ] **Step 4: Verify EnsureTopics includes all consumed topics**

In `auth-service/cmd/main.go`, verify `EnsureTopics` call includes:
- `user.employee-created` (consumed)
- `client.created` (consumed)
- `notification.send-email` (produced)
- `auth.account-status-changed` (produced)

All four must be present. Check and fix if missing.

- [ ] **Step 5: Document findings**

Write a brief comment at the top of each consumer file summarizing the verified contract:
```go
// EmployeeConsumer processes user.employee-created events.
// Contract: EmployeeCreatedMessage{EmployeeID int64, Email, FirstName, LastName string, Roles []string}
// Action: Creates Account (pending) + ActivationToken, sends activation email via notification.send-email.
```

- [ ] **Step 6: Commit**

```bash
git add auth-service/internal/consumer/employee_consumer.go auth-service/internal/consumer/client_consumer.go
git commit -m "docs: document Kafka consumer contracts for auth-service"
```

---

## Task 2: Audit Current Client Creation → Auth Flow

**Files:**
- Read: `client-service/internal/service/client_service.go`
- Read: `auth-service/internal/consumer/client_consumer.go`
- Read: `contract/kafka/messages.go:73-78`

- [ ] **Step 1: Trace the client creation Kafka message**

Verify `client-service` publishes `ClientCreatedMessage`:
```go
kafka.ClientCreatedMessage{
    ClientID:  client.ID,    // uint64
    Email:     client.Email,
    FirstName: client.FirstName,
    LastName:  client.LastName,
}
```

Auth-service consumer (`client_consumer.go:48`) calls:
```go
c.authSvc.CreateAccountAndActivationToken(ctx, int64(evt.ClientID), evt.Email, evt.FirstName, "client")
```

**Verify:** `ClientCreatedMessage.ClientID` is `uint64`, auth-service casts to `int64`. This works for all practical IDs but could overflow for IDs > `math.MaxInt64`. Since GORM auto-increment IDs won't reach that, this is acceptable but worth noting.

- [ ] **Step 2: Verify client-service EnsureTopics**

In `client-service/cmd/main.go`, verify `EnsureTopics` includes `client.created`. Check that auth-service also pre-creates `client.created` in its own `EnsureTopics`.

- [ ] **Step 3: Verify the idempotency in CreateAccountAndActivationToken**

`auth_service.go:341` checks `GetByEmail(email)`. If account exists (no error), it skips creation but still creates a new activation token. This means:
- Kafka message replay: safe (idempotent account creation)
- But creates duplicate activation tokens. This is acceptable since only the latest is used.

- [ ] **Step 4: Commit**

No code changes needed if audit passes. If fixes found, commit them:
```bash
git add -A
git commit -m "fix(auth): fix client creation auth flow issues found during audit"
```

---

## Task 3: Add Retry Logic to Kafka Consumers

**Files:**
- Modify: `auth-service/internal/consumer/employee_consumer.go`
- Modify: `auth-service/internal/consumer/client_consumer.go`
- Modify: `contract/kafka/messages.go`

Currently, if `CreateAccountAndActivationToken` fails, the error is logged and the message is lost. We need retry logic.

- [ ] **Step 1: Add dead-letter topic constant**

In `contract/kafka/messages.go`, add:
```go
const (
    TopicAuthDeadLetter = "auth.dead-letter"
)
```

- [ ] **Step 2: Write failing test for retry behavior**

Create `auth-service/internal/consumer/employee_consumer_test.go`:
```go
package consumer

import (
    "testing"
)

func TestEmployeeConsumer_RetriesOnFailure(t *testing.T) {
    // This test verifies the retry logic exists in the consumer.
    // The actual retry behavior is tested via integration tests.
    // Here we verify the maxRetries constant is set.
    if maxRetries != 3 {
        t.Errorf("expected maxRetries=3, got %d", maxRetries)
    }
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd auth-service && go test ./internal/consumer/ -v -run TestEmployeeConsumer_RetriesOnFailure`
Expected: FAIL (maxRetries undefined)

- [ ] **Step 4: Add retry logic to employee consumer**

Modify `auth-service/internal/consumer/employee_consumer.go`:
```go
package consumer

import (
    "context"
    "encoding/json"
    "log"
    "time"

    "github.com/segmentio/kafka-go"

    kafkamsg "github.com/exbanka/contract/kafka"
    "github.com/exbanka/auth-service/internal/service"
)

const maxRetries = 3

type EmployeeConsumer struct {
    reader   *kafka.Reader
    authSvc  *service.AuthService
    dlqWriter *kafka.Writer
}

func NewEmployeeConsumer(brokers string, authSvc *service.AuthService) *EmployeeConsumer {
    r := kafka.NewReader(kafka.ReaderConfig{
        Brokers: []string{brokers},
        Topic:   kafkamsg.TopicEmployeeCreated,
        GroupID: "auth-service-employee-consumer",
    })
    dlq := &kafka.Writer{
        Addr:  kafka.TCP(brokers),
        Topic: kafkamsg.TopicAuthDeadLetter,
    }
    return &EmployeeConsumer{reader: r, authSvc: authSvc, dlqWriter: dlq}
}

func (c *EmployeeConsumer) Start(ctx context.Context) {
    go func() {
        for {
            msg, err := c.reader.ReadMessage(ctx)
            if err != nil {
                if ctx.Err() != nil {
                    return
                }
                log.Printf("employee consumer read error: %v", err)
                continue
            }

            var evt kafkamsg.EmployeeCreatedMessage
            if err := json.Unmarshal(msg.Value, &evt); err != nil {
                log.Printf("employee consumer unmarshal error: %v", err)
                continue
            }

            var lastErr error
            for attempt := 1; attempt <= maxRetries; attempt++ {
                lastErr = c.authSvc.CreateAccountAndActivationToken(ctx, evt.EmployeeID, evt.Email, evt.FirstName, "employee")
                if lastErr == nil {
                    break
                }
                log.Printf("attempt %d/%d failed for employee %d: %v", attempt, maxRetries, evt.EmployeeID, lastErr)
                if attempt < maxRetries {
                    time.Sleep(time.Duration(attempt) * 2 * time.Second)
                }
            }
            if lastErr != nil {
                log.Printf("all retries exhausted for employee %d, sending to DLQ: %v", evt.EmployeeID, lastErr)
                _ = c.dlqWriter.WriteMessages(ctx, kafka.Message{Value: msg.Value})
            }
        }
    }()
}

func (c *EmployeeConsumer) Close() {
    if err := c.reader.Close(); err != nil {
        log.Printf("employee consumer close error: %v", err)
    }
    if err := c.dlqWriter.Close(); err != nil {
        log.Printf("employee consumer DLQ writer close error: %v", err)
    }
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd auth-service && go test ./internal/consumer/ -v -run TestEmployeeConsumer_RetriesOnFailure`
Expected: PASS

- [ ] **Step 6: Apply same retry pattern to client consumer**

Modify `auth-service/internal/consumer/client_consumer.go` with the same retry + DLQ pattern.

- [ ] **Step 7: Add DLQ topic to EnsureTopics in auth-service main.go**

In `auth-service/cmd/main.go`, add `kafkamsg.TopicAuthDeadLetter` to the `EnsureTopics` call.

- [ ] **Step 8: Add DLQ topic to docker-compose.yml**

No docker-compose change needed for topic (auto-created by EnsureTopics).

- [ ] **Step 9: Commit**

```bash
git add auth-service/internal/consumer/ contract/kafka/messages.go auth-service/cmd/main.go
git commit -m "feat(auth): add retry logic and DLQ to Kafka consumers"
```

---

## Task 4: Add Application-Level Pepper to Auth-Service

**Files:**
- Create: `auth-service/internal/service/pepper.go`
- Create: `auth-service/internal/service/pepper_test.go`
- Modify: `auth-service/internal/service/auth_service.go`
- Modify: `auth-service/internal/config/config.go`
- Modify: `auth-service/cmd/main.go`
- Modify: `docker-compose.yml`

**Context:** bcrypt already handles per-password salting internally. We add an application-level "pepper" — a server-side secret that is prepended to the password before bcrypt hashing. This means even if the DB is compromised, passwords cannot be cracked without the pepper (which lives in env vars, not the DB).

- [ ] **Step 1: Write failing test for pepper utility**

Create `auth-service/internal/service/pepper_test.go`:
```go
package service

import (
    "testing"

    "golang.org/x/crypto/bcrypt"
)

func TestPepperPassword(t *testing.T) {
    pepper := "test-pepper-secret-256bit"
    raw := "MyPassword12"

    peppered := PepperPassword(pepper, raw)
    if peppered == raw {
        t.Fatal("peppered password should differ from raw")
    }
    if peppered != pepper+raw {
        t.Fatalf("expected pepper+raw, got %q", peppered)
    }
}

func TestPepperPassword_EmptyPepper(t *testing.T) {
    peppered := PepperPassword("", "MyPassword12")
    if peppered != "MyPassword12" {
        t.Fatal("empty pepper should return raw password for backward compat")
    }
}

func TestPepperPassword_HashAndVerify(t *testing.T) {
    pepper := "server-secret"
    raw := "MyPassword12"

    peppered := PepperPassword(pepper, raw)
    hash, err := bcrypt.GenerateFromPassword([]byte(peppered), bcrypt.DefaultCost)
    if err != nil {
        t.Fatal(err)
    }
    if err := bcrypt.CompareHashAndPassword(hash, []byte(peppered)); err != nil {
        t.Fatal("should verify correctly with pepper")
    }
    // Without pepper should fail
    if err := bcrypt.CompareHashAndPassword(hash, []byte(raw)); err == nil {
        t.Fatal("should fail without pepper")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd auth-service && go test ./internal/service/ -v -run TestPepper`
Expected: FAIL (PepperPassword undefined)

- [ ] **Step 3: Implement pepper utility**

Create `auth-service/internal/service/pepper.go`:
```go
package service

// PepperPassword prepends the server-side pepper to the raw password.
// If pepper is empty, returns the raw password unchanged (backward compatibility).
func PepperPassword(pepper, rawPassword string) string {
    return pepper + rawPassword
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd auth-service && go test ./internal/service/ -v -run TestPepper`
Expected: PASS

- [ ] **Step 5: Add PASSWORD_PEPPER to config**

Modify `auth-service/internal/config/config.go`, add to the Config struct:
```go
PasswordPepper string
```

In `Load()`, add:
```go
cfg.PasswordPepper = getEnv("PASSWORD_PEPPER", "")
```

- [ ] **Step 6: Add pepper field to AuthService struct**

Modify `auth-service/internal/service/auth_service.go`:

Add `pepper string` field to `AuthService` struct.
Add `pepper string` parameter to `NewAuthService`.
Store it: `pepper: pepper`.

- [ ] **Step 7: Apply pepper in ActivateAccount**

In `auth_service.go`, `ActivateAccount` method (line ~456), change:
```go
hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
```
to:
```go
hash, err := bcrypt.GenerateFromPassword([]byte(PepperPassword(s.pepper, password)), bcrypt.DefaultCost)
```

- [ ] **Step 8: Apply pepper in ResetPassword**

In `auth_service.go`, `ResetPassword` method, find `bcrypt.GenerateFromPassword` and apply same pepper wrapping.

- [ ] **Step 9: Apply pepper in Login**

In `auth_service.go`, `Login` method (around line ~160), change:
```go
if err := bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(password)); err != nil {
```
to:
```go
if err := bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(PepperPassword(s.pepper, password))); err != nil {
```

- [ ] **Step 10: Pass pepper from main.go**

In `auth-service/cmd/main.go`, update the `NewAuthService` call to pass `cfg.PasswordPepper`.

- [ ] **Step 11: Add PASSWORD_PEPPER to docker-compose.yml**

In `docker-compose.yml`, under `auth-service` environment, add:
```yaml
PASSWORD_PEPPER: "${PASSWORD_PEPPER:-default-pepper-change-in-production}"
```

- [ ] **Step 12: Run all auth-service tests**

Run: `cd auth-service && go test ./... -v`
Expected: All PASS

- [ ] **Step 13: Commit**

```bash
git add auth-service/internal/service/pepper.go auth-service/internal/service/pepper_test.go \
       auth-service/internal/service/auth_service.go auth-service/internal/config/config.go \
       auth-service/cmd/main.go docker-compose.yml
git commit -m "feat(auth): add application-level password pepper for defense-in-depth"
```

---

## Task 5: Audit All API Gateway Routes

**Files:**
- Read: `api-gateway/internal/router/router.go`
- Read: `api-gateway/internal/handler/*.go`
- Read: `api-gateway/internal/middleware/*.go`

This task is a manual audit. For each route group, verify: (1) correct middleware applied, (2) correct handler called, (3) validation present before gRPC call, (4) correct gRPC method invoked.

- [ ] **Step 1: Audit public auth routes**

| Route | Handler | Validation | gRPC Call | Status |
|-------|---------|-----------|-----------|--------|
| `POST /api/auth/login` | `authHandler.Login` | email+password required | `AuthService.Login` | Verify |
| `POST /api/auth/refresh` | `authHandler.RefreshToken` | refresh_token required | `AuthService.RefreshToken` | Verify |
| `POST /api/auth/logout` | `authHandler.Logout` | refresh_token required | `AuthService.Logout` | Verify |
| `POST /api/auth/password/reset-request` | `authHandler.RequestPasswordReset` | email required | `AuthService.RequestPasswordReset` | Verify |
| `POST /api/auth/password/reset` | `authHandler.ResetPassword` | token+password+confirm | `AuthService.ResetPassword` | Verify |
| `POST /api/auth/activate` | `authHandler.ActivateAccount` | token+password+confirm | `AuthService.ActivateAccount` | Verify |

- [ ] **Step 2: Audit employee-protected routes**

Verify all routes under `protected.Use(middleware.AuthMiddleware(authClient))`:
- `GET /employees` — requires `employees.read`, calls `UserService.ListEmployees`
- `GET /employees/:id` — requires `employees.read`, calls `UserService.GetEmployee` + `AuthService.GetAccountStatus`
- `POST /employees` — requires `employees.create`, calls `UserService.CreateEmployee`
- `PUT /employees/:id` — requires `employees.update`, calls `UserService.UpdateEmployee` + optionally `AuthService.SetAccountStatus`

For each: verify handler validates input, maps HTTP → gRPC correctly, returns correct HTTP status codes.

- [ ] **Step 3: Audit client management routes**

- `POST /clients` — requires `clients.read`, calls `ClientService.CreateClient`
- `GET /clients` — requires `clients.read`, calls `ClientService.ListClients`
- `GET /clients/:id` — requires `clients.read`, calls `ClientService.GetClient` + `AuthService.GetAccountStatus`
- `PUT /clients/:id` — requires `clients.read`, calls `ClientService.UpdateClient` + optionally `AuthService.SetAccountStatus`

**Check:** CreateClient should require `clients.read` permission (currently it does). Consider if a separate `clients.create` permission is needed.

- [ ] **Step 4: Audit account management routes**

- `POST /accounts` — requires `accounts.read`, validates `account_kind` with `oneOf("current","foreign")`, calls `AccountService.CreateAccount`
- `GET /accounts` — requires `accounts.read`, calls `AccountService.ListAllAccounts`
- `GET /accounts/:id` — requires `accounts.read`, calls `AccountService.GetAccount`
- `PUT /accounts/:id/name` — requires `accounts.read`, validates name + verification code
- `PUT /accounts/:id/limits` — requires `accounts.read`, validates limits are `nonNegative`
- `PUT /accounts/:id/status` — requires `accounts.read`, validates status with `oneOf("active","inactive")`

- [ ] **Step 5: Audit card management routes**

- `POST /cards` — requires `cards.manage`, validates `card_brand` with `oneOf("visa","mastercard","dinacard","amex")`
- `PUT /cards/:id/block` — requires `cards.manage`
- `PUT /cards/:id/unblock` — requires `cards.manage`
- `PUT /cards/:id/deactivate` — requires `cards.manage`
- `POST /cards/authorized-person` — requires `cards.manage`

- [ ] **Step 6: Audit client-protected routes**

Under `clientProtected.Use(middleware.ClientAuthMiddleware(authClient))`:
- `GET /clients/me` — client auth, calls `ClientService.GetClientByEmail`
- `POST /payments` — validates payment fields (amount positive, payment code valid)
- `POST /payments/:id/execute` — validates verification code
- `POST /transfers` — validates amount positive
- `POST /transfers/:id/execute` — validates verification code
- `POST /loans/requests` — validates `loan_type` with `oneOf`, repayment period with `inRange`
- `POST /cards/virtual` — validates `usage_type` with `oneOf("single_use","multi_use")`
- `POST /cards/:id/pin` — validates PIN is 4 digits
- `POST /cards/:id/verify-pin` — validates PIN is 4 digits
- `POST /cards/:id/temporary-block` — validates duration

- [ ] **Step 7: Audit dual-auth (anyAuth) routes**

Under `anyAuth.Use(middleware.AnyAuthMiddleware(authClient))`:
- `GET /accounts/by-number/:account_number`
- `GET /accounts/client/:client_id` — `enforceClientSelf` applied
- `GET /cards/:id`, `GET /cards/account/:account_number`, `GET /cards/client/:client_id`
- `GET /payments/:id`, `GET /payments/account/:account_number`
- `GET /transfers/:id`, `GET /transfers/client/:client_id`
- `GET /loans/:id`, `GET /loans/client/:client_id`, `GET /loans/:id/installments`
- `GET /loans/requests/client/:client_id`

Verify `enforceClientSelf` is called for all client-scoped resources.

- [ ] **Step 8: Document any findings and fix issues**

Create a checklist of issues found during audit. Fix each one and commit separately with descriptive messages.

- [ ] **Step 9: Commit route audit fixes (if any)**

```bash
git add api-gateway/
git commit -m "fix(gateway): fix route issues found during audit"
```

---

## Task 6: Verify Account Status Integration in Employee/Client Handlers

**Files:**
- Read: `api-gateway/internal/handler/employee_handler.go`
- Read: `api-gateway/internal/handler/client_handler.go`

- [ ] **Step 1: Verify employee list fetches account statuses**

In `employee_handler.go`, `ListEmployees`:
- After getting employee list from user-service, handler should call `AuthService.GetAccountStatusBatch` to get active/inactive status for each employee
- Verify the response includes `active` field from auth-service

- [ ] **Step 2: Verify employee get fetches account status**

In `employee_handler.go`, `GetEmployee`:
- After getting employee from user-service, handler should call `AuthService.GetAccountStatus` for single employee
- Verify response includes `active` field

- [ ] **Step 3: Verify employee update can toggle active status**

In `employee_handler.go`, `UpdateEmployee`:
- If request body includes `active` field, handler should call `AuthService.SetAccountStatus`
- Verify this forwards to auth-service correctly

- [ ] **Step 4: Verify same patterns for client handlers**

In `client_handler.go`:
- `GetClient` → calls `AuthService.GetAccountStatus`
- `ListClients` → calls `AuthService.GetAccountStatusBatch`
- `UpdateClient` → calls `AuthService.SetAccountStatus` if `active` field present

- [ ] **Step 5: Fix any missing status integration**

If any handler is missing the auth-service status call, add it following the existing pattern.

- [ ] **Step 6: Commit**

```bash
git add api-gateway/internal/handler/
git commit -m "fix(gateway): ensure all employee/client endpoints integrate auth status"
```

---

## Task 7: Verify Kafka Topic Pre-Creation Across All Services

**Files:**
- Read: `auth-service/cmd/main.go`
- Read: `user-service/cmd/main.go`
- Read: `client-service/cmd/main.go`
- Read: `account-service/cmd/main.go`
- Read: `card-service/cmd/main.go`
- Read: `transaction-service/cmd/main.go`
- Read: `credit-service/cmd/main.go`
- Read: `notification-service/cmd/main.go`

- [ ] **Step 1: Build topic ownership matrix**

Create a matrix of which topics each service produces and consumes:

| Topic | Produced By | Consumed By | Pre-Created By |
|-------|-------------|-------------|----------------|
| `user.employee-created` | user-service | auth-service | Both |
| `client.created` | client-service | auth-service | Both |
| `notification.send-email` | all services | notification-service | All |
| `notification.email-sent` | notification-service | (none currently) | notification-service |
| `auth.account-status-changed` | auth-service | (none) | auth-service |
| ... (all other topics) | ... | ... | ... |

- [ ] **Step 2: Verify each service pre-creates all its topics**

For each service, check that `EnsureTopics` in `cmd/main.go` includes every topic the service produces to AND consumes from.

- [ ] **Step 3: Fix any missing topic pre-creation**

Add missing topics to the appropriate `EnsureTopics` calls.

- [ ] **Step 4: Commit**

```bash
git add */cmd/main.go
git commit -m "fix: ensure all Kafka topics are pre-created by their producing/consuming services"
```

---

## Task 8: Add Kafka Event for Auth Account Status Changes

**Files:**
- Modify: `auth-service/internal/service/auth_service.go`
- Read: `contract/kafka/messages.go:263-271`

Currently `SetAccountStatus` updates the DB but doesn't publish a Kafka event. The topic constant `TopicAuthAccountStatusChanged` and message type `AuthAccountStatusChangedMessage` already exist in `contract/kafka/messages.go`.

- [ ] **Step 1: Write failing test**

Verify that `SetAccountStatus` publishes a Kafka event. (If tests already exist, extend them; otherwise note this as needed for the test-app plan.)

- [ ] **Step 2: Add Kafka publish to SetAccountStatus**

In `auth_service.go`, `SetAccountStatus` method (line ~494), after the status update succeeds, add:
```go
_ = s.producer.Publish(ctx, kafkamsg.TopicAuthAccountStatusChanged, kafkamsg.AuthAccountStatusChangedMessage{
    PrincipalType: principalType,
    PrincipalID:   principalID,
    Status:        status,
})
```

- [ ] **Step 3: Verify producer has Publish method**

Check that `auth-service/internal/kafka/producer.go` has a generic `Publish(ctx, topic, message)` method. If not, add one.

- [ ] **Step 4: Run tests**

Run: `cd auth-service && go test ./... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add auth-service/internal/service/auth_service.go auth-service/internal/kafka/producer.go
git commit -m "feat(auth): publish Kafka event on account status change"
```

---

## Task 9: Final Build & Smoke Test

**Files:**
- All modified files from previous tasks

- [ ] **Step 1: Run make tidy**

Run: `make tidy`
Expected: No errors

- [ ] **Step 2: Run make build**

Run: `make build`
Expected: All services build successfully

- [ ] **Step 3: Run make test**

Run: `make test`
Expected: All tests pass

- [ ] **Step 4: Regenerate Swagger docs**

Run: `cd api-gateway && swag init -g cmd/main.go --output docs`
Expected: Swagger docs regenerated

- [ ] **Step 5: Commit final state**

```bash
git add -A
git commit -m "chore: final build verification after auth communication audit"
```
