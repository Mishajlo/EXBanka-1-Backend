package workflows

import (
	"fmt"
	"strconv"
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

func TestPayment_EndToEnd(t *testing.T) {
	adminClient := loginAsAdmin(t)

	// Create source client (A) and dest client (B)
	clientAID := createTestClient(t, adminClient)
	clientBID := createTestClient(t, adminClient)

	// Get activation email for client A
	// We need the client's email — re-create with known email
	emailA := helpers.RandomEmail()
	passwordA := helpers.RandomPassword()

	createRespA, err := adminClient.POST("/api/clients", map[string]interface{}{
		"first_name":    helpers.RandomName("PayA"),
		"last_name":     helpers.RandomName("Client"),
		"date_of_birth": helpers.DateOfBirthUnix(),
		"gender":        "male",
		"email":         emailA,
		"phone":         helpers.RandomPhone(),
		"address":       "Payment Test St",
		"jmbg":          helpers.RandomJMBG(),
	})
	if err != nil {
		t.Fatalf("create client A error: %v", err)
	}
	helpers.RequireStatus(t, createRespA, 201)
	clientAID = int(helpers.GetNumberField(t, createRespA, "id"))

	// Create source account for client A with 50000 RSD
	srcAcctResp, err := adminClient.POST("/api/accounts", map[string]interface{}{
		"owner_id":        clientAID,
		"account_kind":    "current",
		"account_type":    "personal",
		"currency_code":   "RSD",
		"initial_balance": 50000,
	})
	if err != nil {
		t.Fatalf("create source account error: %v", err)
	}
	helpers.RequireStatus(t, srcAcctResp, 201)
	srcAccountNumber := helpers.GetStringField(t, srcAcctResp, "account_number")

	// Create destination account for client B with 0 RSD
	_ = clientBID
	dstAcctResp, err := adminClient.POST("/api/accounts", map[string]interface{}{
		"owner_id":        clientBID,
		"account_kind":    "current",
		"account_type":    "personal",
		"currency_code":   "RSD",
		"initial_balance": 0,
	})
	if err != nil {
		t.Fatalf("create dest account error: %v", err)
	}
	helpers.RequireStatus(t, dstAcctResp, 201)
	dstAccountNumber := helpers.GetStringField(t, dstAcctResp, "account_number")

	// Activate client A
	tokenA := scanKafkaForActivationToken(t, emailA)
	activateResp, err := newClient().ActivateAccount(tokenA, passwordA)
	if err != nil {
		t.Fatalf("activate client A error: %v", err)
	}
	helpers.RequireStatus(t, activateResp, 200)

	clientA := loginAsClient(t, emailA, passwordA)

	// Get client A's own ID from /api/clients/me
	meResp, err := clientA.GET("/api/clients/me")
	if err != nil {
		t.Fatalf("get /api/clients/me error: %v", err)
	}
	helpers.RequireStatus(t, meResp, 200)
	meClientID := int(helpers.GetNumberField(t, meResp, "id"))

	// Record balances before
	srcBalanceBefore := getAccountBalance(t, adminClient, srcAccountNumber)
	dstBalanceBefore := getAccountBalance(t, adminClient, dstAccountNumber)

	// Start Kafka listener for payment events
	el := kafka.NewEventListener(cfg.KafkaBrokers)
	el.Start()
	defer el.Stop()

	// Client A creates payment (500 RSD — below 1000 fee threshold)
	payResp, err := clientA.POST("/api/payments", map[string]interface{}{
		"from_account_number": srcAccountNumber,
		"to_account_number":   dstAccountNumber,
		"amount":              500,
		"payment_purpose":     "Test payment below threshold",
	})
	if err != nil {
		t.Fatalf("create payment error: %v", err)
	}
	helpers.RequireStatus(t, payResp, 201)
	paymentID := int(helpers.GetNumberField(t, payResp, "id"))

	// Create verification code
	verResp, err := clientA.POST("/api/verification", map[string]interface{}{
		"client_id":        meClientID,
		"transaction_id":   paymentID,
		"transaction_type": "payment",
	})
	if err != nil {
		t.Fatalf("create verification code error: %v", err)
	}
	helpers.RequireStatus(t, verResp, 201)
	verCode := helpers.GetStringField(t, verResp, "code")

	// Execute payment
	execResp, err := clientA.POST(fmt.Sprintf("/api/payments/%d/execute", paymentID), map[string]interface{}{
		"verification_code": verCode,
	})
	if err != nil {
		t.Fatalf("execute payment error: %v", err)
	}
	helpers.RequireStatus(t, execResp, 200)

	// Verify commission is "0" (below 1000 RSD threshold)
	commissionStr := helpers.GetStringField(t, execResp, "commission")
	commission, err := strconv.ParseFloat(commissionStr, 64)
	if err != nil {
		t.Fatalf("parse commission %q: %v", commissionStr, err)
	}
	if commission != 0 {
		t.Fatalf("expected zero commission for 500 RSD payment, got %f", commission)
	}

	// Verify balances
	srcBalanceAfter := getAccountBalance(t, adminClient, srcAccountNumber)
	dstBalanceAfter := getAccountBalance(t, adminClient, dstAccountNumber)

	if srcBalanceAfter > srcBalanceBefore-500+0.01 {
		t.Fatalf("source balance should have decreased by 500: before=%f after=%f", srcBalanceBefore, srcBalanceAfter)
	}
	if dstBalanceAfter < dstBalanceBefore+500-0.01 {
		t.Fatalf("dest balance should have increased by 500: before=%f after=%f", dstBalanceBefore, dstBalanceAfter)
	}

	// Verify Kafka event
	_, found := el.WaitForEvent("transaction.payment-completed", 15*time.Second, nil)
	if !found {
		t.Fatal("expected transaction.payment-completed Kafka event")
	}
}

func TestPayment_WithFee(t *testing.T) {
	adminClient := loginAsAdmin(t)

	emailA := helpers.RandomEmail()
	passwordA := helpers.RandomPassword()

	createRespA, err := adminClient.POST("/api/clients", map[string]interface{}{
		"first_name":    helpers.RandomName("FeeA"),
		"last_name":     helpers.RandomName("Client"),
		"date_of_birth": helpers.DateOfBirthUnix(),
		"gender":        "female",
		"email":         emailA,
		"phone":         helpers.RandomPhone(),
		"address":       "Fee Payment St",
		"jmbg":          helpers.RandomJMBG(),
	})
	if err != nil {
		t.Fatalf("create client A error: %v", err)
	}
	helpers.RequireStatus(t, createRespA, 201)
	clientAID := int(helpers.GetNumberField(t, createRespA, "id"))

	clientBID := createTestClient(t, adminClient)

	// Source account with 50000 RSD
	srcAcctResp, err := adminClient.POST("/api/accounts", map[string]interface{}{
		"owner_id":        clientAID,
		"account_kind":    "current",
		"account_type":    "personal",
		"currency_code":   "RSD",
		"initial_balance": 50000,
	})
	if err != nil {
		t.Fatalf("create source account error: %v", err)
	}
	helpers.RequireStatus(t, srcAcctResp, 201)
	srcAccountNumber := helpers.GetStringField(t, srcAcctResp, "account_number")

	// Dest account with 0 RSD
	dstAcctResp, err := adminClient.POST("/api/accounts", map[string]interface{}{
		"owner_id":        clientBID,
		"account_kind":    "current",
		"account_type":    "personal",
		"currency_code":   "RSD",
		"initial_balance": 0,
	})
	if err != nil {
		t.Fatalf("create dest account error: %v", err)
	}
	helpers.RequireStatus(t, dstAcctResp, 201)
	dstAccountNumber := helpers.GetStringField(t, dstAcctResp, "account_number")

	// Activate client A
	tokenA := scanKafkaForActivationToken(t, emailA)
	activateResp, err := newClient().ActivateAccount(tokenA, passwordA)
	if err != nil {
		t.Fatalf("activate client A error: %v", err)
	}
	helpers.RequireStatus(t, activateResp, 200)

	clientA := loginAsClient(t, emailA, passwordA)

	meResp, err := clientA.GET("/api/clients/me")
	if err != nil {
		t.Fatalf("get /api/clients/me error: %v", err)
	}
	helpers.RequireStatus(t, meResp, 200)
	meClientID := int(helpers.GetNumberField(t, meResp, "id"))

	// Create payment of 5000 RSD (above 1000 fee threshold)
	payResp, err := clientA.POST("/api/payments", map[string]interface{}{
		"from_account_number": srcAccountNumber,
		"to_account_number":   dstAccountNumber,
		"amount":              5000,
		"payment_purpose":     "Test payment above threshold",
	})
	if err != nil {
		t.Fatalf("create payment error: %v", err)
	}
	helpers.RequireStatus(t, payResp, 201)
	paymentID := int(helpers.GetNumberField(t, payResp, "id"))

	// Create verification code
	verResp, err := clientA.POST("/api/verification", map[string]interface{}{
		"client_id":        meClientID,
		"transaction_id":   paymentID,
		"transaction_type": "payment",
	})
	if err != nil {
		t.Fatalf("create verification code error: %v", err)
	}
	helpers.RequireStatus(t, verResp, 201)
	verCode := helpers.GetStringField(t, verResp, "code")

	// Execute payment
	execResp, err := clientA.POST(fmt.Sprintf("/api/payments/%d/execute", paymentID), map[string]interface{}{
		"verification_code": verCode,
	})
	if err != nil {
		t.Fatalf("execute payment error: %v", err)
	}
	helpers.RequireStatus(t, execResp, 200)

	// Verify commission > 0 (above 1000 RSD threshold)
	commissionStr := helpers.GetStringField(t, execResp, "commission")
	commission, err := strconv.ParseFloat(commissionStr, 64)
	if err != nil {
		t.Fatalf("parse commission %q: %v", commissionStr, err)
	}
	if commission <= 0 {
		t.Fatalf("expected non-zero commission for 5000 RSD payment, got %f", commission)
	}
	t.Logf("payment commission for 5000 RSD: %f", commission)
}
