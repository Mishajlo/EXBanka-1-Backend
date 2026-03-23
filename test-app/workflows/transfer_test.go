package workflows

import (
	"fmt"
	"strconv"
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

func TestTransfer_SameCurrency_EndToEnd(t *testing.T) {
	adminClient := loginAsAdmin(t)

	// Create client 1 with known email for activation
	email1 := helpers.RandomEmail()
	password1 := helpers.RandomPassword()

	createResp1, err := adminClient.POST("/api/clients", map[string]interface{}{
		"first_name":    helpers.RandomName("TrfA"),
		"last_name":     helpers.RandomName("Client"),
		"date_of_birth": helpers.DateOfBirthUnix(),
		"gender":        "male",
		"email":         email1,
		"phone":         helpers.RandomPhone(),
		"address":       "Transfer Test St",
		"jmbg":          helpers.RandomJMBG(),
	})
	if err != nil {
		t.Fatalf("create client 1 error: %v", err)
	}
	helpers.RequireStatus(t, createResp1, 201)
	client1ID := int(helpers.GetNumberField(t, createResp1, "id"))

	// Create client 2 (destination)
	client2ID := createTestClient(t, adminClient)

	// Create RSD account for client 1 with 100000 RSD
	acct1Resp, err := adminClient.POST("/api/accounts", map[string]interface{}{
		"owner_id":        client1ID,
		"account_kind":    "current",
		"account_type":    "personal",
		"currency_code":   "RSD",
		"initial_balance": 100000,
	})
	if err != nil {
		t.Fatalf("create account 1 error: %v", err)
	}
	helpers.RequireStatus(t, acct1Resp, 201)
	acctNum1 := helpers.GetStringField(t, acct1Resp, "account_number")

	// Create RSD account for client 2 with 100000 RSD
	acct2Resp, err := adminClient.POST("/api/accounts", map[string]interface{}{
		"owner_id":        client2ID,
		"account_kind":    "current",
		"account_type":    "personal",
		"currency_code":   "RSD",
		"initial_balance": 100000,
	})
	if err != nil {
		t.Fatalf("create account 2 error: %v", err)
	}
	helpers.RequireStatus(t, acct2Resp, 201)
	acctNum2 := helpers.GetStringField(t, acct2Resp, "account_number")

	// Activate client 1
	token1 := scanKafkaForActivationToken(t, email1)
	activateResp, err := newClient().ActivateAccount(token1, password1)
	if err != nil {
		t.Fatalf("activate client 1 error: %v", err)
	}
	helpers.RequireStatus(t, activateResp, 200)

	client1 := loginAsClient(t, email1, password1)

	// Get client 1's own ID from /api/clients/me
	meResp, err := client1.GET("/api/clients/me")
	if err != nil {
		t.Fatalf("get /api/clients/me error: %v", err)
	}
	helpers.RequireStatus(t, meResp, 200)
	meClientID := int(helpers.GetNumberField(t, meResp, "id"))

	// Record balances and bank RSD account balance before
	srcBalanceBefore := getAccountBalance(t, adminClient, acctNum1)
	dstBalanceBefore := getAccountBalance(t, adminClient, acctNum2)
	_, bankBalanceBefore := getBankRSDAccount(t, adminClient)

	// Client 1 creates transfer of 5000 RSD (above 1000 fee threshold)
	tfrResp, err := client1.POST("/api/transfers", map[string]interface{}{
		"from_account_number": acctNum1,
		"to_account_number":   acctNum2,
		"amount":              5000,
	})
	if err != nil {
		t.Fatalf("create transfer error: %v", err)
	}
	helpers.RequireStatus(t, tfrResp, 201)
	transferID := int(helpers.GetNumberField(t, tfrResp, "id"))

	// Create verification code
	verResp, err := client1.POST("/api/verification", map[string]interface{}{
		"client_id":        meClientID,
		"transaction_id":   transferID,
		"transaction_type": "transfer",
	})
	if err != nil {
		t.Fatalf("create verification code error: %v", err)
	}
	helpers.RequireStatus(t, verResp, 201)
	verCode := helpers.GetStringField(t, verResp, "code")

	// Execute transfer
	execResp, err := client1.POST(fmt.Sprintf("/api/transfers/%d/execute", transferID), map[string]interface{}{
		"verification_code": verCode,
	})
	if err != nil {
		t.Fatalf("execute transfer error: %v", err)
	}
	helpers.RequireStatus(t, execResp, 200)

	// Parse commission
	commissionStr := helpers.GetStringField(t, execResp, "commission")
	commission, err := strconv.ParseFloat(commissionStr, 64)
	if err != nil {
		t.Fatalf("parse commission %q: %v", commissionStr, err)
	}
	if commission <= 0 {
		t.Fatalf("expected non-zero commission for 5000 RSD transfer, got %f", commission)
	}
	t.Logf("transfer commission for 5000 RSD: %f", commission)

	// Verify balances after
	srcBalanceAfter := getAccountBalance(t, adminClient, acctNum1)
	dstBalanceAfter := getAccountBalance(t, adminClient, acctNum2)
	_, bankBalanceAfter := getBankRSDAccount(t, adminClient)

	// Source decreased by 5000 + commission
	expectedSrcDecrease := 5000 + commission
	actualSrcDecrease := srcBalanceBefore - srcBalanceAfter
	if actualSrcDecrease < expectedSrcDecrease-0.01 || actualSrcDecrease > expectedSrcDecrease+0.01 {
		t.Fatalf("source balance decreased by %f, expected %f (5000 + commission %f)",
			actualSrcDecrease, expectedSrcDecrease, commission)
	}

	// Destination increased by 5000
	actualDstIncrease := dstBalanceAfter - dstBalanceBefore
	if actualDstIncrease < 5000-0.01 || actualDstIncrease > 5000+0.01 {
		t.Fatalf("dest balance increased by %f, expected 5000", actualDstIncrease)
	}

	// Bank RSD account increased by commission
	actualBankIncrease := bankBalanceAfter - bankBalanceBefore
	if actualBankIncrease < commission-0.01 || actualBankIncrease > commission+0.01 {
		t.Fatalf("bank RSD account balance increased by %f, expected commission %f",
			actualBankIncrease, commission)
	}
}
