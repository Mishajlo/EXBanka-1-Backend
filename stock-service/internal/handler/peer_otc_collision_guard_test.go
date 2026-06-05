package handler_test

// Collision guard tests for SP-2a Task 7 Part B.
//
// Part B: ingestion collision guards — a peer must never write a row that
// claims OUR routing (routing_number == OwnRouting()), which would make the
// row look LOCAL and corrupt the local-vs-remote invariant.
//
// These tests cover the RecordOptionContract guard. The CreateNegotiation and
// RecordOutboundNegotiation guards were added in Task 5 and verified there;
// the otccache guard is tested in internal/otccache/option_cache_test.go.

import (
	"context"
	"encoding/json"
	"testing"

	contractsitx "github.com/exbanka/contract/sitx"
	stockpb "github.com/exbanka/contract/stockpb"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestPeerOTC_RecordOptionContract_OwnCounterpartyRouting_Rejected verifies
// that RecordOptionContract rejects a payload where the derived counterparty
// routing equals this bank's own routing. Such a payload would stamp the row
// with routing_number=OwnRouting(), making it indistinguishable from a local
// contract row and leaking into local money paths.
//
// Scenario: direction=DEBIT, buyer routing=111 (this bank), seller routing=111
// (also this bank) → both sides are local → counterparty routing = buyer routing
// = 111 = OwnRouting() → must be rejected.
func TestPeerOTC_RecordOptionContract_OwnCounterpartyRouting_Rejected(t *testing.T) {
	h, _, _, _ := newPeerOtcHandler(t) // ownRouting = 111

	optDesc := contractsitx.OptionDescription{
		NegotiationID:  contractsitx.ForeignBankId{RoutingNumber: 111, ID: "neg-own"},
		Stock:          contractsitx.StockDescription{Ticker: "AAPL"},
		PricePerUnit:   contractsitx.MonetaryValue{Amount: contractsitx.DecimalNumber{Decimal: decimal.NewFromInt(100)}, Currency: "USD"},
		SettlementDate: "2026-12-31",
		Amount:         5,
	}
	optJSON, _ := json.Marshal(optDesc)

	// DEBIT direction: counterparty = buyer routing = 111 = OwnRouting().
	_, err := h.RecordOptionContract(context.Background(), &stockpb.RecordOptionContractRequest{
		CrossbankTxId:         "tx-own-rout",
		PostingIndex:          0,
		Direction:             contractsitx.DirectionDebit,
		BuyerId:               &stockpb.PeerForeignBankId{RoutingNumber: 111, Id: "client-10"},
		SellerId:              &stockpb.PeerForeignBankId{RoutingNumber: 111, Id: "client-7"},
		OptionDescriptionJson: string(optJSON),
	})
	if err == nil {
		t.Fatal("expected error: counterparty routing == OwnRouting() must be rejected")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

// TestPeerOTC_RecordOptionContract_OwnCounterpartyRouting_CreditDirection_Rejected
// verifies the CREDIT direction variant: buyer=this bank (111), seller=other bank (222)
// → CREDIT direction counterparty = seller routing = 222 ≠ OwnRouting() → ALLOWED.
// CREDIT direction, buyer=222, seller=111 → counterparty = buyer routing = 222 ≠ OwnRouting() → ALLOWED.
// CREDIT direction, buyer=111, seller=111 → counterparty = seller routing = 111 = OwnRouting() → REJECTED.
func TestPeerOTC_RecordOptionContract_OwnCounterpartyRouting_CreditBothOwn_Rejected(t *testing.T) {
	h, _, _, _ := newPeerOtcHandler(t) // ownRouting = 111

	optDesc := contractsitx.OptionDescription{
		NegotiationID:  contractsitx.ForeignBankId{RoutingNumber: 111, ID: "neg-own-credit"},
		Stock:          contractsitx.StockDescription{Ticker: "MSFT"},
		PricePerUnit:   contractsitx.MonetaryValue{Amount: contractsitx.DecimalNumber{Decimal: decimal.NewFromInt(50)}, Currency: "USD"},
		SettlementDate: "2026-12-31",
		Amount:         1,
	}
	optJSON, _ := json.Marshal(optDesc)

	// CREDIT direction: counterparty = seller routing = 111 = OwnRouting().
	_, err := h.RecordOptionContract(context.Background(), &stockpb.RecordOptionContractRequest{
		CrossbankTxId:         "tx-own-credit",
		PostingIndex:          0,
		Direction:             contractsitx.DirectionCredit,
		BuyerId:               &stockpb.PeerForeignBankId{RoutingNumber: 111, Id: "client-5"},
		SellerId:              &stockpb.PeerForeignBankId{RoutingNumber: 111, Id: "client-9"},
		OptionDescriptionJson: string(optJSON),
	})
	if err == nil {
		t.Fatal("expected error: counterparty routing == OwnRouting() must be rejected (CREDIT direction, both parties on own bank)")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

// TestPeerOTC_RecordOptionContract_LegitCrossBank_Allowed verifies that a
// genuine cross-bank contract (buyer on one bank, seller on the other) is
// NOT blocked by the collision guard. This is the happy-path sanity check.
func TestPeerOTC_RecordOptionContract_LegitCrossBank_Allowed(t *testing.T) {
	h, _, _, _ := newPeerOtcHandler(t) // ownRouting = 111
	reserver := &fakeReserver{}
	h.SetHoldingReserver(reserver)

	optDesc := contractsitx.OptionDescription{
		NegotiationID:  contractsitx.ForeignBankId{RoutingNumber: 222, ID: "neg-legit"},
		Stock:          contractsitx.StockDescription{Ticker: "AAPL"},
		PricePerUnit:   contractsitx.MonetaryValue{Amount: contractsitx.DecimalNumber{Decimal: decimal.NewFromInt(100)}, Currency: "USD"},
		SettlementDate: "2026-12-31",
		Amount:         5,
	}
	optJSON, _ := json.Marshal(optDesc)

	// DEBIT direction: seller=111(us), buyer=222(peer) → counterparty=222 ≠ OwnRouting() → OK.
	resp, err := h.RecordOptionContract(context.Background(), &stockpb.RecordOptionContractRequest{
		CrossbankTxId:         "tx-legit",
		PostingIndex:          0,
		Direction:             contractsitx.DirectionDebit,
		BuyerId:               &stockpb.PeerForeignBankId{RoutingNumber: 222, Id: "client-99"},
		SellerId:              &stockpb.PeerForeignBankId{RoutingNumber: 111, Id: "client-7"},
		OptionDescriptionJson: string(optJSON),
	})
	if err != nil {
		t.Fatalf("legitimate cross-bank contract must not be rejected: %v", err)
	}
	if resp.GetContractId() == 0 {
		t.Errorf("expected a non-zero contract id")
	}
}
