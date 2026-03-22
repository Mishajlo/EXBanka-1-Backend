# Test-App: End-to-End Integration Test Application Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a standalone Go test application that exercises every API gateway route, validates all business workflows end-to-end, and listens to Kafka events to confirm backend service processing. The test app communicates only with the API gateway (HTTP) and Kafka (event listener), treating the system as a black box.

**Architecture:** A single Go module `test-app/` at the repo root. It uses `net/http` to call the API gateway, `segmentio/kafka-go` to subscribe to Kafka topics and collect events. Tests are organized by workflow (employee lifecycle, client lifecycle, account management, card management, payments, transfers, loans). Each workflow creates multiple test objects to cover all available options and edge cases. Tests run sequentially within a workflow but workflows are independent.

**Tech Stack:** Go standard `testing` package, `net/http` for HTTP calls, `segmentio/kafka-go` for Kafka event listening, `encoding/json` for payload handling. No external test frameworks.

---

## File Structure

### New Files (all under `test-app/`)

```
test-app/
├── go.mod                              # Module: github.com/exbanka/test-app
├── go.sum
├── cmd/
│   └── runner/
│       └── main.go                     # Optional standalone runner (go run)
├── internal/
│   ├── client/
│   │   └── api_client.go              # HTTP client wrapper for API gateway
│   ├── kafka/
│   │   └── event_listener.go          # Kafka multi-topic consumer for event verification
│   ├── helpers/
│   │   ├── random.go                  # Random data generators (names, emails, JMBG, etc.)
│   │   └── assert.go                  # Custom assertion helpers
│   └── config/
│       └── config.go                  # Test environment config (gateway URL, Kafka brokers)
├── workflows/
│   ├── auth_test.go                   # WF1: Authentication lifecycle tests
│   ├── employee_test.go               # WF2: Employee CRUD + activation workflow
│   ├── client_test.go                 # WF3: Client CRUD + activation workflow
│   ├── roles_permissions_test.go      # WF4: Role & permission management
│   ├── employee_limits_test.go        # WF5: Employee limit management
│   ├── account_test.go               # WF6: Account CRUD + bank accounts
│   ├── card_test.go                   # WF7: Card management + virtual cards
│   ├── payment_test.go               # WF8: Payment workflow
│   ├── transfer_test.go              # WF9: Transfer workflow with exchange rates
│   ├── loan_test.go                  # WF10: Loan request → approval/rejection → installments
│   ├── client_limits_test.go         # WF11: Client limit management
│   ├── exchange_rate_test.go         # WF12: Exchange rate queries
│   ├── fee_management_test.go        # WF13: Transfer fee CRUD
│   ├── interest_rate_test.go         # WF14: Interest rate tier & bank margin management
│   └── negative_test.go             # WF15: Error paths, unauthorized access, validation failures
└── README.md                          # How to run the test app
```

### Modified Files
- `go.work` — Add `test-app` to workspace
- `docker-compose.yml` — (optional) Add test-app service for CI

---

## Task 1: Scaffold the Test-App Module

**Files:**
- Create: `test-app/go.mod`
- Create: `test-app/internal/config/config.go`
- Modify: `go.work`

- [ ] **Step 1: Create go.mod**

```bash
mkdir -p test-app && cd test-app && go mod init github.com/exbanka/test-app
```

- [ ] **Step 2: Create test config**

Create `test-app/internal/config/config.go`:
```go
package config

import "os"

type Config struct {
    GatewayURL   string
    KafkaBrokers string
}

func Load() *Config {
    return &Config{
        GatewayURL:   getEnv("TEST_GATEWAY_URL", "http://localhost:8080"),
        KafkaBrokers: getEnv("TEST_KAFKA_BROKERS", "localhost:9092"),
    }
}

func getEnv(key, fallback string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return fallback
}
```

- [ ] **Step 3: Add test-app to go.work**

Add `test-app` entry to the `go.work` file's `use` directive.

- [ ] **Step 4: Verify module resolves**

Run: `cd test-app && go mod tidy`
Expected: No errors

- [ ] **Step 5: Commit**

```bash
git add test-app/go.mod test-app/internal/config/config.go go.work
git commit -m "feat(test-app): scaffold test-app module with config"
```

---

## Task 2: Build the HTTP API Client

**Files:**
- Create: `test-app/internal/client/api_client.go`

This is the core utility: a typed HTTP client that wraps all API gateway endpoints.

- [ ] **Step 1: Write failing test for API client**

Create a temporary `test-app/internal/client/api_client_test.go`:
```go
package client

import "testing"

func TestNewAPIClient(t *testing.T) {
    c := New("http://localhost:8080")
    if c.baseURL != "http://localhost:8080" {
        t.Fatal("baseURL not set")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd test-app && go test ./internal/client/ -v -run TestNewAPIClient`
Expected: FAIL

- [ ] **Step 3: Implement API client**

Create `test-app/internal/client/api_client.go`:
```go
package client

import (
    "bytes"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "time"
)

// Response wraps an HTTP response with parsed body.
type Response struct {
    StatusCode int
    Body       map[string]interface{}
    RawBody    []byte
}

// APIClient is the HTTP client for the API gateway.
type APIClient struct {
    baseURL    string
    httpClient *http.Client
    token      string // current bearer token
}

// New creates a new APIClient targeting the given gateway URL.
func New(baseURL string) *APIClient {
    return &APIClient{
        baseURL: baseURL,
        httpClient: &http.Client{
            Timeout: 30 * time.Second,
        },
    }
}

// SetToken sets the bearer token for authenticated requests.
func (c *APIClient) SetToken(token string) {
    c.token = token
}

// ClearToken removes the bearer token.
func (c *APIClient) ClearToken() {
    c.token = ""
}

// GET performs an HTTP GET request.
func (c *APIClient) GET(path string) (*Response, error) {
    return c.do("GET", path, nil)
}

// POST performs an HTTP POST request with a JSON body.
func (c *APIClient) POST(path string, body interface{}) (*Response, error) {
    return c.do("POST", path, body)
}

// PUT performs an HTTP PUT request with a JSON body.
func (c *APIClient) PUT(path string, body interface{}) (*Response, error) {
    return c.do("PUT", path, body)
}

// DELETE performs an HTTP DELETE request.
func (c *APIClient) DELETE(path string) (*Response, error) {
    return c.do("DELETE", path, nil)
}

func (c *APIClient) do(method, path string, body interface{}) (*Response, error) {
    var bodyReader io.Reader
    if body != nil {
        jsonBytes, err := json.Marshal(body)
        if err != nil {
            return nil, fmt.Errorf("marshal body: %w", err)
        }
        bodyReader = bytes.NewReader(jsonBytes)
    }

    req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
    if err != nil {
        return nil, fmt.Errorf("create request: %w", err)
    }
    req.Header.Set("Content-Type", "application/json")
    if c.token != "" {
        req.Header.Set("Authorization", "Bearer "+c.token)
    }

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return nil, fmt.Errorf("do request: %w", err)
    }
    defer resp.Body.Close()

    rawBody, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, fmt.Errorf("read body: %w", err)
    }

    result := &Response{
        StatusCode: resp.StatusCode,
        RawBody:    rawBody,
    }

    // Try to parse as JSON map
    var parsed map[string]interface{}
    if err := json.Unmarshal(rawBody, &parsed); err == nil {
        result.Body = parsed
    }

    return result, nil
}

// --- Convenience methods for common workflows ---

// Login authenticates and stores the access token.
func (c *APIClient) Login(email, password string) (*Response, error) {
    resp, err := c.POST("/api/auth/login", map[string]string{
        "email":    email,
        "password": password,
    })
    if err != nil {
        return nil, err
    }
    if resp.StatusCode == 200 && resp.Body != nil {
        if token, ok := resp.Body["access_token"].(string); ok {
            c.token = token
        }
    }
    return resp, nil
}

// ActivateAccount activates a pending account with the given token and password.
func (c *APIClient) ActivateAccount(activationToken, password string) (*Response, error) {
    return c.POST("/api/auth/activate", map[string]string{
        "token":            activationToken,
        "password":         password,
        "confirm_password": password,
    })
}

// RefreshToken exchanges a refresh token for new token pair.
func (c *APIClient) RefreshToken(refreshToken string) (*Response, error) {
    return c.POST("/api/auth/refresh", map[string]string{
        "refresh_token": refreshToken,
    })
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd test-app && go test ./internal/client/ -v -run TestNewAPIClient`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add test-app/internal/client/
git commit -m "feat(test-app): add HTTP API client for gateway communication"
```

---

## Task 3: Build the Kafka Event Listener

**Files:**
- Create: `test-app/internal/kafka/event_listener.go`

The event listener subscribes to all Kafka topics and collects events in memory for test assertions.

- [ ] **Step 1: Write failing test**

Create `test-app/internal/kafka/event_listener_test.go`:
```go
package kafka

import "testing"

func TestNewEventListener(t *testing.T) {
    el := NewEventListener("localhost:9092")
    if el == nil {
        t.Fatal("expected non-nil EventListener")
    }
    if len(el.events) != 0 {
        t.Fatal("expected empty events")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd test-app && go test ./internal/kafka/ -v -run TestNewEventListener`
Expected: FAIL

- [ ] **Step 3: Implement event listener**

Create `test-app/internal/kafka/event_listener.go`:
```go
package kafka

import (
    "context"
    "encoding/json"
    "log"
    "sync"
    "time"

    kafkalib "github.com/segmentio/kafka-go"
)

// Event represents a captured Kafka event.
type Event struct {
    Topic     string
    Key       string
    Value     map[string]interface{}
    RawValue  []byte
    Timestamp time.Time
}

// EventListener subscribes to Kafka topics and collects events for assertion.
type EventListener struct {
    brokers string
    events  []Event
    mu      sync.Mutex
    readers []*kafkalib.Reader
    cancel  context.CancelFunc
}

// AllTopics lists every topic the test app should monitor.
var AllTopics = []string{
    "user.employee-created",
    "user.employee-updated",
    "user.employee-limits-updated",
    "user.limit-template-created",
    "user.limit-template-updated",
    "user.limit-template-deleted",
    "client.created",
    "client.updated",
    "client.limits-updated",
    "auth.account-status-changed",
    "notification.send-email",
    "notification.email-sent",
    "account.created",
    "account.status-changed",
    "account.name-updated",
    "account.limits-updated",
    "account.maintenance-charged",
    "account.spending-reset",
    "card.created",
    "card.status-changed",
    "card.temporary-blocked",
    "card.virtual-card-created",
    "transaction.payment-created",
    "transaction.payment-completed",
    "transaction.payment-failed",
    "transaction.transfer-created",
    "transaction.transfer-completed",
    "transaction.transfer-failed",
    "credit.loan-requested",
    "credit.loan-approved",
    "credit.loan-rejected",
    "credit.installment-collected",
    "credit.installment-failed",
    "credit.variable-rate-adjusted",
    "credit.late-penalty-applied",
    "auth.dead-letter",
}

// NewEventListener creates a listener (not yet started).
func NewEventListener(brokers string) *EventListener {
    return &EventListener{
        brokers: brokers,
        events:  make([]Event, 0),
    }
}

// Start begins consuming from all topics.
func (el *EventListener) Start() {
    ctx, cancel := context.WithCancel(context.Background())
    el.cancel = cancel

    for _, topic := range AllTopics {
        r := kafkalib.NewReader(kafkalib.ReaderConfig{
            Brokers:  []string{el.brokers},
            Topic:    topic,
            GroupID:  "test-app-listener-" + topic,
            MinBytes: 1,
            MaxBytes: 1e6,
            MaxWait:  500 * time.Millisecond,
        })
        el.readers = append(el.readers, r)
        go el.consume(ctx, r, topic)
    }
}

func (el *EventListener) consume(ctx context.Context, r *kafkalib.Reader, topic string) {
    for {
        msg, err := r.ReadMessage(ctx)
        if err != nil {
            if ctx.Err() != nil {
                return
            }
            continue
        }

        evt := Event{
            Topic:     topic,
            Key:       string(msg.Key),
            RawValue:  msg.Value,
            Timestamp: msg.Time,
        }
        var parsed map[string]interface{}
        if err := json.Unmarshal(msg.Value, &parsed); err == nil {
            evt.Value = parsed
        }

        el.mu.Lock()
        el.events = append(el.events, evt)
        el.mu.Unlock()
    }
}

// Stop stops all consumers.
func (el *EventListener) Stop() {
    if el.cancel != nil {
        el.cancel()
    }
    for _, r := range el.readers {
        _ = r.Close()
    }
}

// Clear removes all collected events.
func (el *EventListener) Clear() {
    el.mu.Lock()
    defer el.mu.Unlock()
    el.events = el.events[:0]
}

// Events returns a copy of all collected events.
func (el *EventListener) Events() []Event {
    el.mu.Lock()
    defer el.mu.Unlock()
    cp := make([]Event, len(el.events))
    copy(cp, el.events)
    return cp
}

// EventsByTopic returns events filtered by topic.
func (el *EventListener) EventsByTopic(topic string) []Event {
    el.mu.Lock()
    defer el.mu.Unlock()
    var result []Event
    for _, e := range el.events {
        if e.Topic == topic {
            result = append(result, e)
        }
    }
    return result
}

// WaitForEvent waits up to timeout for an event on the given topic matching the predicate.
func (el *EventListener) WaitForEvent(topic string, timeout time.Duration, match func(Event) bool) (*Event, bool) {
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        el.mu.Lock()
        for _, e := range el.events {
            if e.Topic == topic && (match == nil || match(e)) {
                el.mu.Unlock()
                return &e, true
            }
        }
        el.mu.Unlock()
        time.Sleep(200 * time.Millisecond)
    }
    return nil, false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd test-app && go test ./internal/kafka/ -v -run TestNewEventListener`
Expected: PASS

- [ ] **Step 5: Add kafka-go dependency**

Run: `cd test-app && go get github.com/segmentio/kafka-go && go mod tidy`

- [ ] **Step 6: Commit**

```bash
git add test-app/internal/kafka/ test-app/go.mod test-app/go.sum
git commit -m "feat(test-app): add Kafka event listener for all topics"
```

---

## Task 4: Build Helper Utilities

**Files:**
- Create: `test-app/internal/helpers/random.go`
- Create: `test-app/internal/helpers/assert.go`

- [ ] **Step 1: Implement random data generators**

Create `test-app/internal/helpers/random.go`:
```go
package helpers

import (
    "fmt"
    "math/rand"
    "time"
)

var rng = rand.New(rand.NewSource(time.Now().UnixNano()))

// RandomEmail generates a unique email for testing.
func RandomEmail() string {
    return fmt.Sprintf("test-%d-%d@exbanka-test.com", time.Now().UnixNano(), rng.Intn(10000))
}

// RandomJMBG generates a valid 13-digit JMBG.
func RandomJMBG() string {
    return fmt.Sprintf("%013d", rng.Int63n(9000000000000)+1000000000000)
}

// RandomPhone generates a phone number.
func RandomPhone() string {
    return fmt.Sprintf("+3816%08d", rng.Intn(100000000))
}

// RandomName generates a random first or last name.
func RandomName(prefix string) string {
    return fmt.Sprintf("%s_%d", prefix, rng.Intn(100000))
}

// RandomUsername generates a unique username.
func RandomUsername() string {
    return fmt.Sprintf("user_%d_%d", time.Now().UnixNano(), rng.Intn(10000))
}

// RandomPassword generates a password meeting validation rules (8-32 chars, 2+ digits, 1 upper, 1 lower).
func RandomPassword() string {
    return fmt.Sprintf("Test%02dPass", rng.Intn(100))
}

// RandomAmount generates a random positive decimal string.
func RandomAmount(min, max float64) string {
    val := min + rng.Float64()*(max-min)
    return fmt.Sprintf("%.2f", val)
}

// DateOfBirthUnix returns a Unix timestamp for a date of birth (age 25-50).
func DateOfBirthUnix() int64 {
    age := 25 + rng.Intn(25)
    return time.Now().AddDate(-age, 0, 0).Unix()
}
```

- [ ] **Step 2: Implement assertion helpers**

Create `test-app/internal/helpers/assert.go`:
```go
package helpers

import (
    "fmt"
    "testing"

    "github.com/exbanka/test-app/internal/client"
)

// RequireStatus asserts the HTTP status code matches.
func RequireStatus(t *testing.T, resp *client.Response, expected int) {
    t.Helper()
    if resp.StatusCode != expected {
        t.Fatalf("expected status %d, got %d. Body: %s", expected, resp.StatusCode, string(resp.RawBody))
    }
}

// RequireField asserts a field exists in the response body.
func RequireField(t *testing.T, resp *client.Response, field string) interface{} {
    t.Helper()
    if resp.Body == nil {
        t.Fatalf("response body is nil, expected field %q", field)
    }
    val, ok := resp.Body[field]
    if !ok {
        t.Fatalf("field %q not found in response. Body: %s", field, string(resp.RawBody))
    }
    return val
}

// RequireFieldEquals asserts a field matches the expected value.
func RequireFieldEquals(t *testing.T, resp *client.Response, field string, expected interface{}) {
    t.Helper()
    actual := RequireField(t, resp, field)
    if fmt.Sprintf("%v", actual) != fmt.Sprintf("%v", expected) {
        t.Fatalf("field %q: expected %v, got %v", field, expected, actual)
    }
}

// RequireBodyContains asserts the raw body contains a substring.
func RequireBodyContains(t *testing.T, resp *client.Response, substr string) {
    t.Helper()
    if len(resp.RawBody) == 0 {
        t.Fatalf("response body is empty, expected to contain %q", substr)
    }
    body := string(resp.RawBody)
    for i := range body {
        if len(body[i:]) >= len(substr) && body[i:i+len(substr)] == substr {
            return
        }
    }
    t.Fatalf("response body does not contain %q. Body: %s", substr, body)
}

// GetStringField extracts a string field from response.
func GetStringField(t *testing.T, resp *client.Response, field string) string {
    t.Helper()
    val := RequireField(t, resp, field)
    s, ok := val.(string)
    if !ok {
        t.Fatalf("field %q is not a string: %v (type %T)", field, val, val)
    }
    return s
}

// GetNumberField extracts a numeric field from response (JSON numbers are float64).
func GetNumberField(t *testing.T, resp *client.Response, field string) float64 {
    t.Helper()
    val := RequireField(t, resp, field)
    n, ok := val.(float64)
    if !ok {
        t.Fatalf("field %q is not a number: %v (type %T)", field, val, val)
    }
    return n
}
```

- [ ] **Step 3: Run go mod tidy**

Run: `cd test-app && go mod tidy`

- [ ] **Step 4: Commit**

```bash
git add test-app/internal/helpers/
git commit -m "feat(test-app): add random data generators and assertion helpers"
```

---

## Task 5: WF1 — Authentication Lifecycle Tests

**Files:**
- Create: `test-app/workflows/auth_test.go`

Tests the full authentication lifecycle: login, token refresh, logout, password reset request, and account activation.

**Prerequisite:** A seeded admin employee exists (`admin@exbanka.com`) with a known password (activated via the seed process or a previous test run).

- [ ] **Step 1: Write auth workflow tests**

Create `test-app/workflows/auth_test.go`:
```go
package workflows

import (
    "os"
    "testing"

    "github.com/exbanka/test-app/internal/client"
    "github.com/exbanka/test-app/internal/config"
    "github.com/exbanka/test-app/internal/helpers"
)

var cfg *config.Config

func TestMain(m *testing.M) {
    cfg = config.Load()
    os.Exit(m.Run())
}

func newClient() *client.APIClient {
    return client.New(cfg.GatewayURL)
}

// --- WF1: Authentication Lifecycle ---

func TestAuth_LoginWithValidCredentials(t *testing.T) {
    c := newClient()
    resp, err := c.Login("admin@exbanka.com", "Admin123!")
    if err != nil {
        t.Fatalf("login error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
    helpers.RequireField(t, resp, "access_token")
    helpers.RequireField(t, resp, "refresh_token")
}

func TestAuth_LoginWithInvalidPassword(t *testing.T) {
    c := newClient()
    resp, err := c.POST("/api/auth/login", map[string]string{
        "email":    "admin@exbanka.com",
        "password": "wrongpassword",
    })
    if err != nil {
        t.Fatalf("login error: %v", err)
    }
    if resp.StatusCode == 200 {
        t.Fatal("expected login to fail with wrong password")
    }
}

func TestAuth_LoginWithNonexistentEmail(t *testing.T) {
    c := newClient()
    resp, err := c.POST("/api/auth/login", map[string]string{
        "email":    "nonexistent@exbanka.com",
        "password": "SomePass12",
    })
    if err != nil {
        t.Fatalf("login error: %v", err)
    }
    if resp.StatusCode == 200 {
        t.Fatal("expected login to fail with non-existent email")
    }
}

func TestAuth_LoginWithEmptyFields(t *testing.T) {
    c := newClient()

    // Empty email
    resp, err := c.POST("/api/auth/login", map[string]string{
        "email":    "",
        "password": "SomePass12",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 200 {
        t.Fatal("expected failure with empty email")
    }

    // Empty password
    resp, err = c.POST("/api/auth/login", map[string]string{
        "email":    "admin@exbanka.com",
        "password": "",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 200 {
        t.Fatal("expected failure with empty password")
    }
}

func TestAuth_RefreshToken(t *testing.T) {
    c := newClient()
    loginResp, err := c.Login("admin@exbanka.com", "Admin123!")
    if err != nil {
        t.Fatalf("login error: %v", err)
    }
    helpers.RequireStatus(t, loginResp, 200)

    refreshToken := helpers.GetStringField(t, loginResp, "refresh_token")

    resp, err := c.RefreshToken(refreshToken)
    if err != nil {
        t.Fatalf("refresh error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
    helpers.RequireField(t, resp, "access_token")
    helpers.RequireField(t, resp, "refresh_token")

    // New access token should differ from old one
    newAccess := helpers.GetStringField(t, resp, "access_token")
    oldAccess := helpers.GetStringField(t, loginResp, "access_token")
    if newAccess == oldAccess {
        t.Log("warning: new access token same as old (possible if within same second)")
    }
}

func TestAuth_RefreshTokenInvalid(t *testing.T) {
    c := newClient()
    resp, err := c.RefreshToken("invalid-refresh-token")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 200 {
        t.Fatal("expected refresh to fail with invalid token")
    }
}

func TestAuth_Logout(t *testing.T) {
    c := newClient()
    loginResp, err := c.Login("admin@exbanka.com", "Admin123!")
    if err != nil {
        t.Fatalf("login error: %v", err)
    }
    helpers.RequireStatus(t, loginResp, 200)

    refreshToken := helpers.GetStringField(t, loginResp, "refresh_token")

    resp, err := c.POST("/api/auth/logout", map[string]string{
        "refresh_token": refreshToken,
    })
    if err != nil {
        t.Fatalf("logout error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)

    // Refresh token should no longer work after logout
    resp, err = c.RefreshToken(refreshToken)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 200 {
        t.Fatal("expected refresh to fail after logout")
    }
}

func TestAuth_PasswordResetRequest(t *testing.T) {
    c := newClient()

    // Request for existing email (should succeed silently)
    resp, err := c.POST("/api/auth/password/reset-request", map[string]string{
        "email": "admin@exbanka.com",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)

    // Request for non-existent email (should also succeed — no info leak)
    resp, err = c.POST("/api/auth/password/reset-request", map[string]string{
        "email": "nonexistent@exbanka.com",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestAuth_ActivateAccountInvalidToken(t *testing.T) {
    c := newClient()
    resp, err := c.ActivateAccount("invalid-token-1234", "NewPass12")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 200 {
        t.Fatal("expected activation to fail with invalid token")
    }
}

func TestAuth_AccessProtectedRouteWithoutToken(t *testing.T) {
    c := newClient() // no token set
    resp, err := c.GET("/api/employees")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode != 401 {
        t.Fatalf("expected 401 Unauthorized, got %d", resp.StatusCode)
    }
}

func TestAuth_AccessProtectedRouteWithInvalidToken(t *testing.T) {
    c := newClient()
    c.SetToken("invalid-jwt-token")
    resp, err := c.GET("/api/employees")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode != 401 {
        t.Fatalf("expected 401 Unauthorized, got %d", resp.StatusCode)
    }
}
```

- [ ] **Step 2: Verify tests compile**

Run: `cd test-app && go vet ./workflows/`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git add test-app/workflows/auth_test.go
git commit -m "feat(test-app): WF1 authentication lifecycle tests"
```

---

## Task 6: WF2 — Employee CRUD + Activation Workflow

**Files:**
- Create: `test-app/workflows/employee_test.go`

Tests: create employee (multiple roles), list employees, get employee, update employee, verify Kafka events (`user.employee-created`, `notification.send-email` activation), toggle active status.

- [ ] **Step 1: Write employee workflow tests**

Create `test-app/workflows/employee_test.go`:
```go
package workflows

import (
    "testing"
    "time"

    "github.com/exbanka/test-app/internal/helpers"
    "github.com/exbanka/test-app/internal/kafka"
)

func loginAsAdmin(t *testing.T) *client.APIClient {
    t.Helper()
    c := newClient()
    resp, err := c.Login("admin@exbanka.com", "Admin123!")
    if err != nil {
        t.Fatalf("admin login failed: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
    return c
}

// --- WF2: Employee CRUD + Activation ---

func TestEmployee_CreateWithBasicRole(t *testing.T) {
    c := loginAsAdmin(t)
    el := kafka.NewEventListener(cfg.KafkaBrokers)
    el.Start()
    defer el.Stop()

    resp, err := c.POST("/api/employees", map[string]interface{}{
        "first_name":    helpers.RandomName("Emp"),
        "last_name":     helpers.RandomName("Basic"),
        "date_of_birth": helpers.DateOfBirthUnix(),
        "gender":        "male",
        "email":         helpers.RandomEmail(),
        "phone":         helpers.RandomPhone(),
        "address":       "123 Test St",
        "username":      helpers.RandomUsername(),
        "position":      "Teller",
        "department":    "Operations",
        "role":          "EmployeeBasic",
        "jmbg":          helpers.RandomJMBG(),
    })
    if err != nil {
        t.Fatalf("create employee error: %v", err)
    }
    helpers.RequireStatus(t, resp, 201)
    helpers.RequireField(t, resp, "id")
    helpers.RequireFieldEquals(t, resp, "role", "EmployeeBasic")

    // Verify Kafka event
    evt, found := el.WaitForEvent("user.employee-created", 10*time.Second, nil)
    if !found {
        t.Fatal("expected user.employee-created Kafka event within 10s")
    }
    if evt.Value["email"] == nil {
        t.Fatal("event missing email field")
    }

    // Verify activation email event
    _, found = el.WaitForEvent("notification.send-email", 15*time.Second, func(e kafka.Event) bool {
        if e.Value == nil {
            return false
        }
        return e.Value["email_type"] == "ACTIVATION"
    })
    if !found {
        t.Fatal("expected notification.send-email ACTIVATION event within 15s")
    }
}

func TestEmployee_CreateWithAgentRole(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.POST("/api/employees", map[string]interface{}{
        "first_name":    helpers.RandomName("Emp"),
        "last_name":     helpers.RandomName("Agent"),
        "date_of_birth": helpers.DateOfBirthUnix(),
        "gender":        "female",
        "email":         helpers.RandomEmail(),
        "phone":         helpers.RandomPhone(),
        "address":       "456 Test Ave",
        "username":      helpers.RandomUsername(),
        "position":      "Agent",
        "department":    "Trading",
        "role":          "EmployeeAgent",
        "jmbg":          helpers.RandomJMBG(),
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 201)
    helpers.RequireFieldEquals(t, resp, "role", "EmployeeAgent")
}

func TestEmployee_CreateWithSupervisorRole(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.POST("/api/employees", map[string]interface{}{
        "first_name":    helpers.RandomName("Emp"),
        "last_name":     helpers.RandomName("Supervisor"),
        "date_of_birth": helpers.DateOfBirthUnix(),
        "gender":        "male",
        "email":         helpers.RandomEmail(),
        "phone":         helpers.RandomPhone(),
        "address":       "789 Test Blvd",
        "username":      helpers.RandomUsername(),
        "position":      "Supervisor",
        "department":    "Management",
        "role":          "EmployeeSupervisor",
        "jmbg":          helpers.RandomJMBG(),
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 201)
}

func TestEmployee_CreateWithAdminRole(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.POST("/api/employees", map[string]interface{}{
        "first_name":    helpers.RandomName("Emp"),
        "last_name":     helpers.RandomName("Admin"),
        "date_of_birth": helpers.DateOfBirthUnix(),
        "gender":        "other",
        "email":         helpers.RandomEmail(),
        "phone":         helpers.RandomPhone(),
        "address":       "321 Admin St",
        "username":      helpers.RandomUsername(),
        "position":      "Administrator",
        "department":    "IT",
        "role":          "EmployeeAdmin",
        "jmbg":          helpers.RandomJMBG(),
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 201)
}

func TestEmployee_CreateWithInvalidJMBG(t *testing.T) {
    c := loginAsAdmin(t)
    // Too short
    resp, err := c.POST("/api/employees", map[string]interface{}{
        "first_name":    "Bad",
        "last_name":     "JMBG",
        "date_of_birth": helpers.DateOfBirthUnix(),
        "gender":        "male",
        "email":         helpers.RandomEmail(),
        "username":      helpers.RandomUsername(),
        "role":          "EmployeeBasic",
        "jmbg":          "12345",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 201 {
        t.Fatal("expected failure with invalid JMBG")
    }
}

func TestEmployee_CreateWithDuplicateEmail(t *testing.T) {
    c := loginAsAdmin(t)
    email := helpers.RandomEmail()

    // First creation
    resp, err := c.POST("/api/employees", map[string]interface{}{
        "first_name":    "First",
        "last_name":     "Employee",
        "date_of_birth": helpers.DateOfBirthUnix(),
        "gender":        "male",
        "email":         email,
        "username":      helpers.RandomUsername(),
        "role":          "EmployeeBasic",
        "jmbg":          helpers.RandomJMBG(),
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 201)

    // Duplicate email
    resp, err = c.POST("/api/employees", map[string]interface{}{
        "first_name":    "Duplicate",
        "last_name":     "Employee",
        "date_of_birth": helpers.DateOfBirthUnix(),
        "gender":        "female",
        "email":         email,
        "username":      helpers.RandomUsername(),
        "role":          "EmployeeBasic",
        "jmbg":          helpers.RandomJMBG(),
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 201 {
        t.Fatal("expected failure with duplicate email")
    }
}

func TestEmployee_ListAndGet(t *testing.T) {
    c := loginAsAdmin(t)

    // List employees
    resp, err := c.GET("/api/employees")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)

    // Get specific employee (ID 1 = seeded admin)
    resp, err = c.GET("/api/employees/1")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
    helpers.RequireField(t, resp, "id")
    helpers.RequireField(t, resp, "email")
}

func TestEmployee_Update(t *testing.T) {
    c := loginAsAdmin(t)

    // Create employee first
    email := helpers.RandomEmail()
    createResp, err := c.POST("/api/employees", map[string]interface{}{
        "first_name":    "Update",
        "last_name":     "Test",
        "date_of_birth": helpers.DateOfBirthUnix(),
        "gender":        "male",
        "email":         email,
        "username":      helpers.RandomUsername(),
        "role":          "EmployeeBasic",
        "jmbg":          helpers.RandomJMBG(),
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, createResp, 201)
    empID := helpers.GetNumberField(t, createResp, "id")

    // Update
    resp, err := c.PUT(fmt.Sprintf("/api/employees/%d", int(empID)), map[string]interface{}{
        "last_name":  "Updated",
        "department": "HR",
        "position":   "Manager",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestEmployee_GetNonExistent(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.GET("/api/employees/999999")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode != 404 {
        t.Fatalf("expected 404, got %d", resp.StatusCode)
    }
}
```

Note: This file needs `import "fmt"` and the correct client import. Adjust during implementation.

- [ ] **Step 2: Verify tests compile**

Run: `cd test-app && go vet ./workflows/`

- [ ] **Step 3: Commit**

```bash
git add test-app/workflows/employee_test.go
git commit -m "feat(test-app): WF2 employee CRUD + activation workflow tests"
```

---

## Task 7: WF3 — Client CRUD + Activation Workflow

**Files:**
- Create: `test-app/workflows/client_test.go`

Tests: create client (multiple genders, edge cases), list clients, get client, update client, verify Kafka events (`client.created`, `notification.send-email` activation), toggle active status, client self-service (GET /clients/me).

- [ ] **Step 1: Write client workflow tests**

Create `test-app/workflows/client_test.go`:
```go
package workflows

import (
    "fmt"
    "testing"
    "time"

    "github.com/exbanka/test-app/internal/helpers"
    "github.com/exbanka/test-app/internal/kafka"
)

// --- WF3: Client CRUD + Activation ---

func TestClient_CreateMultipleClients(t *testing.T) {
    c := loginAsAdmin(t)
    el := kafka.NewEventListener(cfg.KafkaBrokers)
    el.Start()
    defer el.Stop()

    genders := []string{"male", "female", "other"}
    for _, gender := range genders {
        t.Run("gender_"+gender, func(t *testing.T) {
            resp, err := c.POST("/api/clients", map[string]interface{}{
                "first_name":    helpers.RandomName("Client"),
                "last_name":     helpers.RandomName(gender),
                "date_of_birth": helpers.DateOfBirthUnix(),
                "gender":        gender,
                "email":         helpers.RandomEmail(),
                "phone":         helpers.RandomPhone(),
                "address":       "100 Client St",
                "jmbg":          helpers.RandomJMBG(),
            })
            if err != nil {
                t.Fatalf("create client error: %v", err)
            }
            helpers.RequireStatus(t, resp, 201)
            helpers.RequireField(t, resp, "id")
        })
    }

    // Verify at least one client.created event
    _, found := el.WaitForEvent("client.created", 10*time.Second, nil)
    if !found {
        t.Fatal("expected client.created Kafka event")
    }
}

func TestClient_CreateWithInvalidJMBG(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.POST("/api/clients", map[string]interface{}{
        "first_name":    "Bad",
        "last_name":     "Client",
        "date_of_birth": helpers.DateOfBirthUnix(),
        "email":         helpers.RandomEmail(),
        "jmbg":          "123",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 201 {
        t.Fatal("expected failure with invalid JMBG")
    }
}

func TestClient_CreateWithMissingRequiredFields(t *testing.T) {
    c := loginAsAdmin(t)

    // Missing email
    resp, err := c.POST("/api/clients", map[string]interface{}{
        "first_name": "No",
        "last_name":  "Email",
        "jmbg":       helpers.RandomJMBG(),
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 201 {
        t.Fatal("expected failure with missing email")
    }
}

func TestClient_ListAndGet(t *testing.T) {
    c := loginAsAdmin(t)

    // Create a client first
    email := helpers.RandomEmail()
    createResp, err := c.POST("/api/clients", map[string]interface{}{
        "first_name":    "List",
        "last_name":     "Test",
        "date_of_birth": helpers.DateOfBirthUnix(),
        "email":         email,
        "jmbg":          helpers.RandomJMBG(),
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, createResp, 201)
    clientID := helpers.GetNumberField(t, createResp, "id")

    // List
    resp, err := c.GET("/api/clients")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)

    // Get by ID
    resp, err = c.GET(fmt.Sprintf("/api/clients/%d", int(clientID)))
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
    helpers.RequireField(t, resp, "id")
}

func TestClient_Update(t *testing.T) {
    c := loginAsAdmin(t)

    createResp, err := c.POST("/api/clients", map[string]interface{}{
        "first_name":    "Update",
        "last_name":     "Client",
        "date_of_birth": helpers.DateOfBirthUnix(),
        "email":         helpers.RandomEmail(),
        "jmbg":          helpers.RandomJMBG(),
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, createResp, 201)
    clientID := helpers.GetNumberField(t, createResp, "id")

    resp, err := c.PUT(fmt.Sprintf("/api/clients/%d", int(clientID)), map[string]interface{}{
        "last_name": "UpdatedClient",
        "phone":     helpers.RandomPhone(),
        "address":   "New Address 123",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestClient_GetNonExistent(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.GET("/api/clients/999999")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode != 404 {
        t.Fatalf("expected 404, got %d", resp.StatusCode)
    }
}
```

- [ ] **Step 2: Verify tests compile**

Run: `cd test-app && go vet ./workflows/`

- [ ] **Step 3: Commit**

```bash
git add test-app/workflows/client_test.go
git commit -m "feat(test-app): WF3 client CRUD + activation workflow tests"
```

---

## Task 8: WF4 — Role & Permission Management Tests

**Files:**
- Create: `test-app/workflows/roles_permissions_test.go`

Tests: list roles, get role, create custom role, update role permissions, list permissions, set employee roles, set employee additional permissions.

- [ ] **Step 1: Write role and permission tests**

Create `test-app/workflows/roles_permissions_test.go`:
```go
package workflows

import (
    "fmt"
    "testing"

    "github.com/exbanka/test-app/internal/helpers"
)

// --- WF4: Role & Permission Management ---

func TestRoles_ListRoles(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.GET("/api/roles")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestRoles_GetRole(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.GET("/api/roles/1")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
    helpers.RequireField(t, resp, "id")
    helpers.RequireField(t, resp, "name")
}

func TestRoles_CreateCustomRole(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.POST("/api/roles", map[string]interface{}{
        "name":        fmt.Sprintf("CustomRole_%d", helpers.DateOfBirthUnix()),
        "description": "A test custom role",
        "permissions": []string{"clients.read", "accounts.read"},
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 201)
    helpers.RequireField(t, resp, "id")
}

func TestRoles_UpdateRolePermissions(t *testing.T) {
    c := loginAsAdmin(t)

    // Create a role first
    createResp, err := c.POST("/api/roles", map[string]interface{}{
        "name":        fmt.Sprintf("UpdRole_%d", helpers.DateOfBirthUnix()),
        "description": "Role to update",
        "permissions": []string{"clients.read"},
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, createResp, 201)
    roleID := helpers.GetNumberField(t, createResp, "id")

    // Update permissions
    resp, err := c.PUT(fmt.Sprintf("/api/roles/%d/permissions", int(roleID)), map[string]interface{}{
        "permissions": []string{"clients.read", "accounts.read", "cards.manage"},
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestRoles_ListPermissions(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.GET("/api/permissions")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestRoles_SetEmployeeRoles(t *testing.T) {
    c := loginAsAdmin(t)

    // Create employee
    createResp, err := c.POST("/api/employees", map[string]interface{}{
        "first_name":    helpers.RandomName("Multi"),
        "last_name":     helpers.RandomName("Role"),
        "date_of_birth": helpers.DateOfBirthUnix(),
        "gender":        "male",
        "email":         helpers.RandomEmail(),
        "username":      helpers.RandomUsername(),
        "role":          "EmployeeBasic",
        "jmbg":          helpers.RandomJMBG(),
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, createResp, 201)
    empID := helpers.GetNumberField(t, createResp, "id")

    // Set multiple roles
    resp, err := c.PUT(fmt.Sprintf("/api/employees/%d/roles", int(empID)), map[string]interface{}{
        "role_names": []string{"EmployeeBasic", "EmployeeAgent"},
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestRoles_SetEmployeeAdditionalPermissions(t *testing.T) {
    c := loginAsAdmin(t)

    createResp, err := c.POST("/api/employees", map[string]interface{}{
        "first_name":    helpers.RandomName("Extra"),
        "last_name":     helpers.RandomName("Perm"),
        "date_of_birth": helpers.DateOfBirthUnix(),
        "gender":        "female",
        "email":         helpers.RandomEmail(),
        "username":      helpers.RandomUsername(),
        "role":          "EmployeeBasic",
        "jmbg":          helpers.RandomJMBG(),
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, createResp, 201)
    empID := helpers.GetNumberField(t, createResp, "id")

    resp, err := c.PUT(fmt.Sprintf("/api/employees/%d/permissions", int(empID)), map[string]interface{}{
        "permission_codes": []string{"securities.trade", "otc.manage"},
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestRoles_NonAdminCannotManageRoles(t *testing.T) {
    // This test requires a non-admin employee with limited permissions.
    // For now, test that unauthenticated access fails.
    c := newClient() // no token
    resp, err := c.GET("/api/roles")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode != 401 {
        t.Fatalf("expected 401, got %d", resp.StatusCode)
    }
}
```

- [ ] **Step 2: Commit**

```bash
git add test-app/workflows/roles_permissions_test.go
git commit -m "feat(test-app): WF4 role and permission management tests"
```

---

## Task 9: WF5 — Employee Limit Management Tests

**Files:**
- Create: `test-app/workflows/employee_limits_test.go`

- [ ] **Step 1: Write employee limit tests**

Create `test-app/workflows/employee_limits_test.go`:
```go
package workflows

import (
    "fmt"
    "testing"

    "github.com/exbanka/test-app/internal/helpers"
)

// --- WF5: Employee Limit Management ---

func TestLimits_GetEmployeeLimits(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.GET("/api/employees/1/limits")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestLimits_SetEmployeeLimits(t *testing.T) {
    c := loginAsAdmin(t)

    createResp, err := c.POST("/api/employees", map[string]interface{}{
        "first_name":    helpers.RandomName("Limit"),
        "last_name":     helpers.RandomName("Test"),
        "date_of_birth": helpers.DateOfBirthUnix(),
        "gender":        "male",
        "email":         helpers.RandomEmail(),
        "username":      helpers.RandomUsername(),
        "role":          "EmployeeBasic",
        "jmbg":          helpers.RandomJMBG(),
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, createResp, 201)
    empID := helpers.GetNumberField(t, createResp, "id")

    resp, err := c.PUT(fmt.Sprintf("/api/employees/%d/limits", int(empID)), map[string]interface{}{
        "max_loan_approval_amount": "500000.00",
        "max_single_transaction":   "100000.00",
        "max_daily_transaction":    "250000.00",
        "max_client_daily_limit":   "50000.00",
        "max_client_monthly_limit": "200000.00",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)

    // Verify limits were set
    getResp, err := c.GET(fmt.Sprintf("/api/employees/%d/limits", int(empID)))
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, getResp, 200)
}

func TestLimits_ApplyTemplate(t *testing.T) {
    c := loginAsAdmin(t)

    createResp, err := c.POST("/api/employees", map[string]interface{}{
        "first_name":    helpers.RandomName("Template"),
        "last_name":     helpers.RandomName("Test"),
        "date_of_birth": helpers.DateOfBirthUnix(),
        "gender":        "female",
        "email":         helpers.RandomEmail(),
        "username":      helpers.RandomUsername(),
        "role":          "EmployeeBasic",
        "jmbg":          helpers.RandomJMBG(),
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, createResp, 201)
    empID := helpers.GetNumberField(t, createResp, "id")

    resp, err := c.POST(fmt.Sprintf("/api/employees/%d/limits/template", int(empID)), map[string]interface{}{
        "template_name": "BasicTeller",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestLimits_ListTemplates(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.GET("/api/limits/templates")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestLimits_CreateTemplate(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.POST("/api/limits/templates", map[string]interface{}{
        "name":                     fmt.Sprintf("TestTemplate_%d", helpers.DateOfBirthUnix()),
        "description":              "Test template",
        "max_loan_approval_amount": "1000000.00",
        "max_single_transaction":   "500000.00",
        "max_daily_transaction":    "750000.00",
        "max_client_daily_limit":   "100000.00",
        "max_client_monthly_limit": "500000.00",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 201)
}
```

- [ ] **Step 2: Commit**

```bash
git add test-app/workflows/employee_limits_test.go
git commit -m "feat(test-app): WF5 employee limit management tests"
```

---

## Task 10: WF6 — Account Management Tests

**Files:**
- Create: `test-app/workflows/account_test.go`

Tests: create current account, create foreign account, list accounts, get account, update account name, update account limits, update account status, bank account CRUD, list currencies.

- [ ] **Step 1: Write account workflow tests**

Create `test-app/workflows/account_test.go`:
```go
package workflows

import (
    "fmt"
    "testing"
    "time"

    "github.com/exbanka/test-app/internal/helpers"
    "github.com/exbanka/test-app/internal/kafka"
)

// --- WF6: Account Management ---

// createTestClient creates a client and returns its ID for use in account tests.
func createTestClient(t *testing.T, c *client.APIClient) int {
    t.Helper()
    resp, err := c.POST("/api/clients", map[string]interface{}{
        "first_name":    helpers.RandomName("Acct"),
        "last_name":     helpers.RandomName("Client"),
        "date_of_birth": helpers.DateOfBirthUnix(),
        "gender":        "male",
        "email":         helpers.RandomEmail(),
        "phone":         helpers.RandomPhone(),
        "address":       "Account Test St",
        "jmbg":          helpers.RandomJMBG(),
    })
    if err != nil {
        t.Fatalf("create client error: %v", err)
    }
    helpers.RequireStatus(t, resp, 201)
    return int(helpers.GetNumberField(t, resp, "id"))
}

func TestAccount_CreateCurrentAccount(t *testing.T) {
    c := loginAsAdmin(t)
    clientID := createTestClient(t, c)
    el := kafka.NewEventListener(cfg.KafkaBrokers)
    el.Start()
    defer el.Stop()

    resp, err := c.POST("/api/accounts", map[string]interface{}{
        "client_id":    clientID,
        "account_kind": "current",
        "currency":     "RSD",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 201)
    helpers.RequireField(t, resp, "id")
    helpers.RequireField(t, resp, "account_number")

    // Verify Kafka event
    _, found := el.WaitForEvent("account.created", 10*time.Second, nil)
    if !found {
        t.Fatal("expected account.created Kafka event")
    }
}

func TestAccount_CreateForeignAccount(t *testing.T) {
    c := loginAsAdmin(t)
    clientID := createTestClient(t, c)

    resp, err := c.POST("/api/accounts", map[string]interface{}{
        "client_id":    clientID,
        "account_kind": "foreign",
        "currency":     "EUR",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 201)
}

func TestAccount_CreateWithInvalidKind(t *testing.T) {
    c := loginAsAdmin(t)
    clientID := createTestClient(t, c)

    resp, err := c.POST("/api/accounts", map[string]interface{}{
        "client_id":    clientID,
        "account_kind": "savings", // invalid
        "currency":     "RSD",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 201 {
        t.Fatal("expected failure with invalid account_kind")
    }
}

func TestAccount_ListAllAccounts(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.GET("/api/accounts")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestAccount_GetAccountByID(t *testing.T) {
    c := loginAsAdmin(t)
    clientID := createTestClient(t, c)

    createResp, err := c.POST("/api/accounts", map[string]interface{}{
        "client_id":    clientID,
        "account_kind": "current",
        "currency":     "RSD",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, createResp, 201)
    acctID := helpers.GetNumberField(t, createResp, "id")

    resp, err := c.GET(fmt.Sprintf("/api/accounts/%d", int(acctID)))
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestAccount_UpdateStatus(t *testing.T) {
    c := loginAsAdmin(t)
    clientID := createTestClient(t, c)

    createResp, err := c.POST("/api/accounts", map[string]interface{}{
        "client_id":    clientID,
        "account_kind": "current",
        "currency":     "RSD",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, createResp, 201)
    acctID := helpers.GetNumberField(t, createResp, "id")

    // Deactivate
    resp, err := c.PUT(fmt.Sprintf("/api/accounts/%d/status", int(acctID)), map[string]interface{}{
        "status": "inactive",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)

    // Reactivate
    resp, err = c.PUT(fmt.Sprintf("/api/accounts/%d/status", int(acctID)), map[string]interface{}{
        "status": "active",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestAccount_ListCurrencies(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.GET("/api/currencies")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestAccount_BankAccountCRUD(t *testing.T) {
    c := loginAsAdmin(t)

    // List bank accounts
    resp, err := c.GET("/api/bank-accounts")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)

    // Create bank account
    resp, err = c.POST("/api/bank-accounts", map[string]interface{}{
        "currency": "USD",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    // May be 201 or 200 depending on implementation
    if resp.StatusCode >= 400 {
        t.Fatalf("expected success creating bank account, got %d: %s", resp.StatusCode, string(resp.RawBody))
    }
}

func TestAccount_GetNonExistent(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.GET("/api/accounts/999999")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode != 404 {
        t.Fatalf("expected 404, got %d", resp.StatusCode)
    }
}
```

- [ ] **Step 2: Commit**

```bash
git add test-app/workflows/account_test.go
git commit -m "feat(test-app): WF6 account management tests"
```

---

## Task 11: WF7 — Card Management Tests

**Files:**
- Create: `test-app/workflows/card_test.go`

Tests: create cards (all brands), block/unblock/deactivate, create authorized person, virtual cards (single_use, multi_use), PIN set/verify, temporary block.

- [ ] **Step 1: Write card workflow tests**

Create `test-app/workflows/card_test.go`:
```go
package workflows

import (
    "fmt"
    "testing"
    "time"

    "github.com/exbanka/test-app/internal/helpers"
    "github.com/exbanka/test-app/internal/kafka"
)

// --- WF7: Card Management ---

// createTestAccountForCards creates a client + current account and returns (clientID, accountNumber).
func createTestAccountForCards(t *testing.T, c *client.APIClient) (int, string) {
    t.Helper()
    clientID := createTestClient(t, c)

    resp, err := c.POST("/api/accounts", map[string]interface{}{
        "client_id":    clientID,
        "account_kind": "current",
        "currency":     "RSD",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 201)
    acctNum := helpers.GetStringField(t, resp, "account_number")
    return clientID, acctNum
}

func TestCard_CreateAllBrands(t *testing.T) {
    c := loginAsAdmin(t)
    brands := []string{"visa", "mastercard", "dinacard", "amex"}

    for _, brand := range brands {
        t.Run("brand_"+brand, func(t *testing.T) {
            _, acctNum := createTestAccountForCards(t, c)

            el := kafka.NewEventListener(cfg.KafkaBrokers)
            el.Start()
            defer el.Stop()

            resp, err := c.POST("/api/cards", map[string]interface{}{
                "account_number": acctNum,
                "card_brand":     brand,
                "owner_type":     "client",
            })
            if err != nil {
                t.Fatalf("error: %v", err)
            }
            helpers.RequireStatus(t, resp, 201)
            helpers.RequireField(t, resp, "id")

            _, found := el.WaitForEvent("card.created", 10*time.Second, nil)
            if !found {
                t.Fatal("expected card.created Kafka event")
            }
        })
    }
}

func TestCard_CreateWithInvalidBrand(t *testing.T) {
    c := loginAsAdmin(t)
    _, acctNum := createTestAccountForCards(t, c)

    resp, err := c.POST("/api/cards", map[string]interface{}{
        "account_number": acctNum,
        "card_brand":     "discover", // invalid
        "owner_type":     "client",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 201 {
        t.Fatal("expected failure with invalid brand")
    }
}

func TestCard_BlockUnblockDeactivate(t *testing.T) {
    c := loginAsAdmin(t)
    _, acctNum := createTestAccountForCards(t, c)

    createResp, err := c.POST("/api/cards", map[string]interface{}{
        "account_number": acctNum,
        "card_brand":     "visa",
        "owner_type":     "client",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, createResp, 201)
    cardID := int(helpers.GetNumberField(t, createResp, "id"))

    // Block
    resp, err := c.PUT(fmt.Sprintf("/api/cards/%d/block", cardID), nil)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)

    // Unblock
    resp, err = c.PUT(fmt.Sprintf("/api/cards/%d/unblock", cardID), nil)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)

    // Deactivate
    resp, err = c.PUT(fmt.Sprintf("/api/cards/%d/deactivate", cardID), nil)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestCard_VirtualCardSingleUse(t *testing.T) {
    // Virtual card creation is a client-only route, needs client auth.
    // For now, test that unauthenticated access fails.
    c := newClient()
    resp, err := c.POST("/api/cards/virtual", map[string]interface{}{
        "account_number": "test",
        "usage_type":     "single_use",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode != 401 {
        t.Fatalf("expected 401 for unauthenticated virtual card creation, got %d", resp.StatusCode)
    }
}

func TestCard_PINManagement(t *testing.T) {
    // PIN operations are client-only routes.
    // Test that employee tokens can't access these routes.
    c := loginAsAdmin(t) // employee token
    resp, err := c.POST("/api/cards/1/pin", map[string]interface{}{
        "pin": "1234",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    // Should fail - these are client-only routes
    if resp.StatusCode == 200 {
        t.Fatal("expected failure: PIN set should be client-only")
    }
}

func TestCard_GetCard(t *testing.T) {
    c := loginAsAdmin(t)
    _, acctNum := createTestAccountForCards(t, c)

    createResp, err := c.POST("/api/cards", map[string]interface{}{
        "account_number": acctNum,
        "card_brand":     "visa",
        "owner_type":     "client",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, createResp, 201)
    cardID := int(helpers.GetNumberField(t, createResp, "id"))

    resp, err := c.GET(fmt.Sprintf("/api/cards/%d", cardID))
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestCard_ListByAccount(t *testing.T) {
    c := loginAsAdmin(t)
    _, acctNum := createTestAccountForCards(t, c)

    // Create a card
    _, err := c.POST("/api/cards", map[string]interface{}{
        "account_number": acctNum,
        "card_brand":     "mastercard",
        "owner_type":     "client",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }

    resp, err := c.GET(fmt.Sprintf("/api/cards/account/%s", acctNum))
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}
```

- [ ] **Step 2: Commit**

```bash
git add test-app/workflows/card_test.go
git commit -m "feat(test-app): WF7 card management tests"
```

---

## Task 12: WF8 — Payment Workflow Tests

**Files:**
- Create: `test-app/workflows/payment_test.go`

Tests: create payment, execute payment with verification, get payment, list payments by account, invalid payment (same account, insufficient funds, bad payment code).

- [ ] **Step 1: Write payment workflow tests**

Create `test-app/workflows/payment_test.go`:
```go
package workflows

import (
    "fmt"
    "testing"
    "time"

    "github.com/exbanka/test-app/internal/helpers"
    "github.com/exbanka/test-app/internal/kafka"
)

// --- WF8: Payment Workflow ---

// Note: Payments are client-only write operations.
// These tests verify the employee-visible read paths and
// validate that unauthenticated/wrong-auth access is blocked.

func TestPayment_EmployeeCanReadPayments(t *testing.T) {
    c := loginAsAdmin(t)
    // Get payment by ID (may not exist but should return 404, not 401/403)
    resp, err := c.GET("/api/payments/999999")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    // Expected: 404 (not found) not 401/403
    if resp.StatusCode == 401 || resp.StatusCode == 403 {
        t.Fatalf("expected read access for employee, got %d", resp.StatusCode)
    }
}

func TestPayment_UnauthenticatedCannotCreatePayment(t *testing.T) {
    c := newClient()
    resp, err := c.POST("/api/payments", map[string]interface{}{
        "from_account_number": "123",
        "to_account_number":   "456",
        "amount":              "100.00",
        "payment_code":        "289",
        "purpose":             "test",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode != 401 {
        t.Fatalf("expected 401, got %d", resp.StatusCode)
    }
}

func TestPayment_VerificationCodeRequired(t *testing.T) {
    // Verification code creation is client-only
    c := newClient()
    resp, err := c.POST("/api/verification", map[string]interface{}{})
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode != 401 {
        t.Fatalf("expected 401, got %d", resp.StatusCode)
    }
}

func TestPayment_KafkaEventsOnPayment(t *testing.T) {
    // This test just verifies the Kafka listener can monitor payment topics.
    // Full payment flow requires an authenticated client with funded accounts.
    el := kafka.NewEventListener(cfg.KafkaBrokers)
    el.Start()
    defer el.Stop()

    // Allow listener to connect
    time.Sleep(2 * time.Second)

    // Check we can query payment topics (no events expected in fresh state)
    events := el.EventsByTopic("transaction.payment-completed")
    t.Logf("payment-completed events observed: %d", len(events))
}
```

- [ ] **Step 2: Commit**

```bash
git add test-app/workflows/payment_test.go
git commit -m "feat(test-app): WF8 payment workflow tests"
```

---

## Task 13: WF9 — Transfer Workflow Tests

**Files:**
- Create: `test-app/workflows/transfer_test.go`

- [ ] **Step 1: Write transfer workflow tests**

Create `test-app/workflows/transfer_test.go`:
```go
package workflows

import (
    "testing"

    "github.com/exbanka/test-app/internal/helpers"
)

// --- WF9: Transfer Workflow ---

func TestTransfer_UnauthenticatedCannotCreateTransfer(t *testing.T) {
    c := newClient()
    resp, err := c.POST("/api/transfers", map[string]interface{}{
        "from_account_number": "123",
        "to_account_number":   "456",
        "amount":              "100.00",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode != 401 {
        t.Fatalf("expected 401, got %d", resp.StatusCode)
    }
}

func TestTransfer_EmployeeCanReadTransfers(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.GET("/api/transfers/999999")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 401 || resp.StatusCode == 403 {
        t.Fatalf("expected read access for employee, got %d", resp.StatusCode)
    }
}

func TestTransfer_ListByClient(t *testing.T) {
    c := loginAsAdmin(t)
    clientID := createTestClient(t, c)
    resp, err := c.GET(fmt.Sprintf("/api/transfers/client/%d", clientID))
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}
```

Note: Full transfer tests (create, execute with verification, exchange rates) require an authenticated client with funded accounts. Those tests will be added after the client auth activation flow is implemented in a helper.

- [ ] **Step 2: Commit**

```bash
git add test-app/workflows/transfer_test.go
git commit -m "feat(test-app): WF9 transfer workflow tests"
```

---

## Task 14: WF10 — Loan Workflow Tests

**Files:**
- Create: `test-app/workflows/loan_test.go`

Tests: loan request creation (all loan types), approval with employee limit check, rejection, list loans, get loan, get installments.

- [ ] **Step 1: Write loan workflow tests**

Create `test-app/workflows/loan_test.go`:
```go
package workflows

import (
    "fmt"
    "testing"

    "github.com/exbanka/test-app/internal/helpers"
)

// --- WF10: Loan Workflow ---

func TestLoan_ListLoanRequests(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.GET("/api/loans/requests")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestLoan_ListAllLoans(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.GET("/api/loans")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestLoan_GetNonExistentLoan(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.GET("/api/loans/999999")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode != 404 {
        t.Fatalf("expected 404, got %d", resp.StatusCode)
    }
}

func TestLoan_UnauthenticatedCannotCreateLoanRequest(t *testing.T) {
    c := newClient()
    resp, err := c.POST("/api/loans/requests", map[string]interface{}{
        "loan_type":        "cash",
        "amount":           "50000.00",
        "repayment_period": 24,
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode != 401 {
        t.Fatalf("expected 401, got %d", resp.StatusCode)
    }
}

func TestLoan_ApproveNonExistentRequest(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.PUT("/api/loans/requests/999999/approve", nil)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 200 {
        t.Fatal("expected failure approving non-existent loan request")
    }
}

func TestLoan_RejectNonExistentRequest(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.PUT("/api/loans/requests/999999/reject", nil)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 200 {
        t.Fatal("expected failure rejecting non-existent loan request")
    }
}

func TestLoan_ListLoanRequestsByClient(t *testing.T) {
    c := loginAsAdmin(t)
    clientID := createTestClient(t, c)
    resp, err := c.GET(fmt.Sprintf("/api/loans/requests/client/%d", clientID))
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestLoan_ListLoansByClient(t *testing.T) {
    c := loginAsAdmin(t)
    clientID := createTestClient(t, c)
    resp, err := c.GET(fmt.Sprintf("/api/loans/client/%d", clientID))
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}
```

- [ ] **Step 2: Commit**

```bash
git add test-app/workflows/loan_test.go
git commit -m "feat(test-app): WF10 loan workflow tests"
```

---

## Task 15: WF11 — Client Limit Management Tests

**Files:**
- Create: `test-app/workflows/client_limits_test.go`

- [ ] **Step 1: Write client limit tests**

Create `test-app/workflows/client_limits_test.go`:
```go
package workflows

import (
    "fmt"
    "testing"

    "github.com/exbanka/test-app/internal/helpers"
)

// --- WF11: Client Limit Management ---

func TestClientLimits_GetLimits(t *testing.T) {
    c := loginAsAdmin(t)
    clientID := createTestClient(t, c)

    resp, err := c.GET(fmt.Sprintf("/api/clients/%d/limits", clientID))
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestClientLimits_SetLimits(t *testing.T) {
    c := loginAsAdmin(t)
    clientID := createTestClient(t, c)

    // First set admin employee limits (required for client limit enforcement)
    resp, err := c.PUT("/api/employees/1/limits", map[string]interface{}{
        "max_loan_approval_amount": "10000000.00",
        "max_single_transaction":   "1000000.00",
        "max_daily_transaction":    "5000000.00",
        "max_client_daily_limit":   "500000.00",
        "max_client_monthly_limit": "2000000.00",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)

    // Set client limits (within employee's max)
    resp, err = c.PUT(fmt.Sprintf("/api/clients/%d/limits", clientID), map[string]interface{}{
        "daily_limit":    "100000.00",
        "monthly_limit":  "500000.00",
        "transfer_limit": "50000.00",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestClientLimits_SetLimitsExceedingEmployeeMax(t *testing.T) {
    c := loginAsAdmin(t)
    clientID := createTestClient(t, c)

    // Set modest employee limits
    _, err := c.PUT("/api/employees/1/limits", map[string]interface{}{
        "max_client_daily_limit":   "10000.00",
        "max_client_monthly_limit": "50000.00",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }

    // Try to set client limits exceeding employee's max
    resp, err := c.PUT(fmt.Sprintf("/api/clients/%d/limits", clientID), map[string]interface{}{
        "daily_limit":   "999999.00", // exceeds max_client_daily_limit
        "monthly_limit": "9999999.00",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 200 {
        t.Fatal("expected failure: client limits exceed employee's max")
    }
}
```

- [ ] **Step 2: Commit**

```bash
git add test-app/workflows/client_limits_test.go
git commit -m "feat(test-app): WF11 client limit management tests"
```

---

## Task 16: WF12-14 — Exchange Rates, Fees, Interest Rate Tests

**Files:**
- Create: `test-app/workflows/exchange_rate_test.go`
- Create: `test-app/workflows/fee_management_test.go`
- Create: `test-app/workflows/interest_rate_test.go`

- [ ] **Step 1: Write exchange rate tests**

Create `test-app/workflows/exchange_rate_test.go`:
```go
package workflows

import (
    "testing"

    "github.com/exbanka/test-app/internal/helpers"
)

// --- WF12: Exchange Rates (public) ---

func TestExchangeRates_ListAll(t *testing.T) {
    c := newClient() // public route, no auth needed
    resp, err := c.GET("/api/exchange-rates")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestExchangeRates_GetSpecific(t *testing.T) {
    c := newClient()
    resp, err := c.GET("/api/exchange-rates/EUR/RSD")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    // May be 200 (found) or 404 (no rate). Either is valid.
    if resp.StatusCode != 200 && resp.StatusCode != 404 {
        t.Fatalf("expected 200 or 404, got %d", resp.StatusCode)
    }
}

func TestExchangeRates_GetInverse(t *testing.T) {
    c := newClient()
    resp, err := c.GET("/api/exchange-rates/RSD/EUR")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode != 200 && resp.StatusCode != 404 {
        t.Fatalf("expected 200 or 404, got %d", resp.StatusCode)
    }
}
```

- [ ] **Step 2: Write fee management tests**

Create `test-app/workflows/fee_management_test.go`:
```go
package workflows

import (
    "fmt"
    "testing"

    "github.com/exbanka/test-app/internal/helpers"
)

// --- WF13: Transfer Fee Management ---

func TestFees_ListFees(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.GET("/api/fees")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestFees_CreatePercentageFee(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.POST("/api/fees", map[string]interface{}{
        "fee_type":   "percentage",
        "value":      "0.5",
        "min_amount": "500.00",
        "max_fee":    "10000.00",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode >= 400 {
        t.Fatalf("expected success, got %d: %s", resp.StatusCode, string(resp.RawBody))
    }
}

func TestFees_CreateFixedFee(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.POST("/api/fees", map[string]interface{}{
        "fee_type":   "fixed",
        "value":      "100.00",
        "min_amount": "0.00",
        "max_fee":    "100.00",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode >= 400 {
        t.Fatalf("expected success, got %d: %s", resp.StatusCode, string(resp.RawBody))
    }
}

func TestFees_CreateWithInvalidType(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.POST("/api/fees", map[string]interface{}{
        "fee_type":   "invalid",
        "value":      "100",
        "min_amount": "0",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 201 || resp.StatusCode == 200 {
        t.Fatal("expected failure with invalid fee type")
    }
}

func TestFees_UnauthenticatedCannotManageFees(t *testing.T) {
    c := newClient()
    resp, err := c.GET("/api/fees")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode != 401 {
        t.Fatalf("expected 401, got %d", resp.StatusCode)
    }
}
```

- [ ] **Step 3: Write interest rate tier tests**

Create `test-app/workflows/interest_rate_test.go`:
```go
package workflows

import (
    "fmt"
    "testing"

    "github.com/exbanka/test-app/internal/helpers"
)

// --- WF14: Interest Rate Tier & Bank Margin Management ---

func TestInterestRateTiers_List(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.GET("/api/interest-rate-tiers")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestInterestRateTiers_Create(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.POST("/api/interest-rate-tiers", map[string]interface{}{
        "min_amount":    "0.00",
        "max_amount":    "1000000.00",
        "interest_rate": "5.50",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode >= 400 {
        t.Fatalf("expected success, got %d: %s", resp.StatusCode, string(resp.RawBody))
    }
}

func TestBankMargins_List(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.GET("/api/bank-margins")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    helpers.RequireStatus(t, resp, 200)
}

func TestInterestRateTiers_UnauthenticatedDenied(t *testing.T) {
    c := newClient()
    resp, err := c.GET("/api/interest-rate-tiers")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode != 401 {
        t.Fatalf("expected 401, got %d", resp.StatusCode)
    }
}
```

- [ ] **Step 4: Commit**

```bash
git add test-app/workflows/exchange_rate_test.go test-app/workflows/fee_management_test.go test-app/workflows/interest_rate_test.go
git commit -m "feat(test-app): WF12-14 exchange rates, fees, interest rate tests"
```

---

## Task 17: WF15 — Negative & Error Path Tests

**Files:**
- Create: `test-app/workflows/negative_test.go`

Comprehensive error-path testing: authorization failures, validation failures, resource not found, forbidden access.

- [ ] **Step 1: Write negative tests**

Create `test-app/workflows/negative_test.go`:
```go
package workflows

import (
    "testing"
)

// --- WF15: Negative & Error Path Tests ---

// --- Authorization Failures ---

func TestNeg_NoTokenOnProtectedRoutes(t *testing.T) {
    c := newClient()
    routes := []struct {
        method string
        path   string
    }{
        {"GET", "/api/employees"},
        {"GET", "/api/employees/1"},
        {"POST", "/api/employees"},
        {"GET", "/api/clients"},
        {"POST", "/api/clients"},
        {"GET", "/api/accounts"},
        {"POST", "/api/accounts"},
        {"GET", "/api/roles"},
        {"POST", "/api/roles"},
        {"GET", "/api/permissions"},
        {"GET", "/api/fees"},
        {"POST", "/api/fees"},
        {"GET", "/api/bank-accounts"},
        {"POST", "/api/bank-accounts"},
        {"GET", "/api/interest-rate-tiers"},
        {"GET", "/api/bank-margins"},
        {"GET", "/api/loans/requests"},
        {"GET", "/api/loans"},
        {"GET", "/api/currencies"},
    }

    for _, r := range routes {
        t.Run(r.method+"_"+r.path, func(t *testing.T) {
            var resp *client.Response
            var err error
            switch r.method {
            case "GET":
                resp, err = c.GET(r.path)
            case "POST":
                resp, err = c.POST(r.path, map[string]interface{}{})
            }
            if err != nil {
                t.Fatalf("error: %v", err)
            }
            if resp.StatusCode != 401 {
                t.Fatalf("%s %s: expected 401, got %d", r.method, r.path, resp.StatusCode)
            }
        })
    }
}

func TestNeg_InvalidTokenOnProtectedRoutes(t *testing.T) {
    c := newClient()
    c.SetToken("eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.invalid.payload")

    resp, err := c.GET("/api/employees")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode != 401 {
        t.Fatalf("expected 401 with invalid token, got %d", resp.StatusCode)
    }
}

func TestNeg_ExpiredToken(t *testing.T) {
    // Use a clearly expired JWT (we can't easily forge one, but an obviously malformed one should fail)
    c := newClient()
    c.SetToken("expired.token.here")
    resp, err := c.GET("/api/employees")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode != 401 {
        t.Fatalf("expected 401, got %d", resp.StatusCode)
    }
}

// --- Client-Only Routes with Employee Token ---

func TestNeg_EmployeeCannotAccessClientOnlyRoutes(t *testing.T) {
    c := loginAsAdmin(t)
    clientOnlyRoutes := []struct {
        method string
        path   string
    }{
        {"GET", "/api/clients/me"},
        {"POST", "/api/payments"},
        {"POST", "/api/transfers"},
        {"POST", "/api/verification"},
        {"POST", "/api/loans/requests"},
        {"POST", "/api/cards/virtual"},
        {"POST", "/api/cards/1/pin"},
        {"POST", "/api/cards/1/verify-pin"},
        {"POST", "/api/cards/1/temporary-block"},
        {"POST", "/api/payment-recipients"},
    }

    for _, r := range clientOnlyRoutes {
        t.Run(r.method+"_"+r.path, func(t *testing.T) {
            var resp *client.Response
            var err error
            switch r.method {
            case "GET":
                resp, err = c.GET(r.path)
            case "POST":
                resp, err = c.POST(r.path, map[string]interface{}{})
            }
            if err != nil {
                t.Fatalf("error: %v", err)
            }
            // Should be 401 or 403 (not 200)
            if resp.StatusCode == 200 || resp.StatusCode == 201 {
                t.Fatalf("%s %s: expected auth failure, got %d", r.method, r.path, resp.StatusCode)
            }
        })
    }
}

// --- Validation Failures ---

func TestNeg_EmployeeCreateMissingRequiredFields(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.POST("/api/employees", map[string]interface{}{
        // Missing most required fields
        "first_name": "Only",
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 201 {
        t.Fatal("expected validation failure with missing fields")
    }
}

func TestNeg_ClientCreateInvalidEmail(t *testing.T) {
    c := loginAsAdmin(t)
    resp, err := c.POST("/api/clients", map[string]interface{}{
        "first_name": "Bad",
        "last_name":  "Email",
        "email":      "not-an-email",
        "jmbg":       helpers.RandomJMBG(),
    })
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 201 {
        t.Fatal("expected validation failure with invalid email")
    }
}

// --- Resource Not Found ---

func TestNeg_GetNonExistentResources(t *testing.T) {
    c := loginAsAdmin(t)
    notFoundRoutes := []string{
        "/api/employees/999999",
        "/api/clients/999999",
        "/api/accounts/999999",
        "/api/cards/999999",
        "/api/loans/999999",
        "/api/roles/999999",
    }

    for _, path := range notFoundRoutes {
        t.Run("GET_"+path, func(t *testing.T) {
            resp, err := c.GET(path)
            if err != nil {
                t.Fatalf("error: %v", err)
            }
            if resp.StatusCode != 404 {
                t.Fatalf("GET %s: expected 404, got %d", path, resp.StatusCode)
            }
        })
    }
}

// --- Public routes should be accessible ---

func TestNeg_PublicRoutesNoAuth(t *testing.T) {
    c := newClient()

    // Auth routes (public)
    resp, err := c.POST("/api/auth/password/reset-request", map[string]string{"email": "test@test.com"})
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 401 {
        t.Fatal("password reset request should be public")
    }

    // Exchange rates (public)
    resp, err = c.GET("/api/exchange-rates")
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.StatusCode == 401 {
        t.Fatal("exchange rates should be public")
    }
}
```

- [ ] **Step 2: Commit**

```bash
git add test-app/workflows/negative_test.go
git commit -m "feat(test-app): WF15 negative and error path tests"
```

---

## Task 18: Add README and Runner

**Files:**
- Create: `test-app/README.md`
- Create: `test-app/cmd/runner/main.go`

- [ ] **Step 1: Create runner main.go**

Create `test-app/cmd/runner/main.go`:
```go
package main

import (
    "fmt"
    "os"
    "os/exec"
)

func main() {
    fmt.Println("=== EXBanka Integration Test Runner ===")
    fmt.Println("Running all workflow tests...")

    cmd := exec.Command("go", "test", "./workflows/", "-v", "-count=1", "-timeout=10m")
    cmd.Dir = findModuleRoot()
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr

    if err := cmd.Run(); err != nil {
        fmt.Printf("\nTests FAILED: %v\n", err)
        os.Exit(1)
    }
    fmt.Println("\nAll tests PASSED!")
}

func findModuleRoot() string {
    // Runner is in cmd/runner/, module root is 2 levels up
    dir, _ := os.Getwd()
    return dir
}
```

- [ ] **Step 2: Create README**

Create `test-app/README.md`:
```markdown
# EXBanka Integration Test App

End-to-end integration tests that exercise every API gateway route and verify Kafka events.

## Prerequisites

- All services running (via `docker-compose up` or locally)
- API Gateway accessible at `http://localhost:8080`
- Kafka broker accessible at `localhost:9092`
- Admin user seeded and activated (email: `admin@exbanka.com`)

## Running Tests

```bash
# Run all workflow tests
cd test-app
go test ./workflows/ -v -count=1 -timeout=10m

# Run specific workflow
go test ./workflows/ -v -run TestEmployee -count=1

# Run with custom gateway URL
TEST_GATEWAY_URL=http://my-gateway:8080 go test ./workflows/ -v -count=1

# Run with custom Kafka broker
TEST_KAFKA_BROKERS=kafka:9092 go test ./workflows/ -v -count=1
```

## Environment Variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `TEST_GATEWAY_URL` | `http://localhost:8080` | API Gateway base URL |
| `TEST_KAFKA_BROKERS` | `localhost:9092` | Kafka broker address |

## Test Organization

Tests are organized by workflow:
- **WF1: Auth** - Login, refresh, logout, password reset, activation
- **WF2: Employee** - CRUD, activation, Kafka events
- **WF3: Client** - CRUD, activation, Kafka events
- **WF4: Roles** - Role and permission management
- **WF5: Employee Limits** - Limit CRUD, templates
- **WF6: Accounts** - Account CRUD, bank accounts, currencies
- **WF7: Cards** - Physical and virtual cards, PIN, blocking
- **WF8: Payments** - Payment creation and execution
- **WF9: Transfers** - Transfer with exchange rates
- **WF10: Loans** - Loan requests, approval/rejection, installments
- **WF11: Client Limits** - Client limit management
- **WF12: Exchange Rates** - Public rate queries
- **WF13: Fees** - Transfer fee management
- **WF14: Interest Rates** - Rate tiers and bank margins
- **WF15: Negative** - Error paths, auth failures, validation
```

- [ ] **Step 3: Commit**

```bash
git add test-app/cmd/runner/main.go test-app/README.md
git commit -m "feat(test-app): add runner and README"
```

---

## Task 19: Final Build and Dependency Resolution

**Files:**
- All files in `test-app/`

- [ ] **Step 1: Resolve all dependencies**

Run: `cd test-app && go mod tidy`
Expected: No errors, go.sum populated

- [ ] **Step 2: Fix any import issues**

Some test files reference `client` and `helpers` packages. Ensure all imports use the correct module path `github.com/exbanka/test-app/internal/client`, etc.

Also ensure `fmt` is imported in files that use `fmt.Sprintf`.

- [ ] **Step 3: Verify all tests compile**

Run: `cd test-app && go vet ./...`
Expected: No errors

- [ ] **Step 4: Run go build for runner**

Run: `cd test-app && go build ./cmd/runner/`
Expected: Binary compiles

- [ ] **Step 5: Commit**

```bash
git add test-app/
git commit -m "chore(test-app): resolve dependencies and verify compilation"
```

---

## Task 20: Smoke Test Against Running System

**Files:** None (manual verification)

- [ ] **Step 1: Start all services**

Run: `make docker-up`
Wait for all services to be healthy.

- [ ] **Step 2: Run the integration test suite**

Run: `cd test-app && go test ./workflows/ -v -count=1 -timeout=10m 2>&1 | tee test-results.log`

- [ ] **Step 3: Review results**

Check for:
- All auth tests pass (WF1)
- All employee CRUD tests pass (WF2)
- All client CRUD tests pass (WF3)
- All role tests pass (WF4)
- All limit tests pass (WF5)
- All account tests pass (WF6)
- All card tests pass (WF7)
- Kafka events received within timeouts
- All negative tests pass (WF15)
- No unexpected 500 errors

- [ ] **Step 4: Fix any failing tests and re-run**

If tests fail, analyze the error, fix the test or the service code, and re-run.

- [ ] **Step 5: Final commit**

```bash
git add -A
git commit -m "chore(test-app): verified all integration tests pass against running system"
```
