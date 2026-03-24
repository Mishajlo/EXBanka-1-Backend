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
