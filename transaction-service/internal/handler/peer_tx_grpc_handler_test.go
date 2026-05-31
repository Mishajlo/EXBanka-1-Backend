package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	accountpb "github.com/exbanka/contract/accountpb"
	contractsitx "github.com/exbanka/contract/sitx"
	transactionpb "github.com/exbanka/contract/transactionpb"
	"github.com/exbanka/transaction-service/internal/handler"
	"github.com/exbanka/transaction-service/internal/model"
	"github.com/exbanka/transaction-service/internal/repository"
	"github.com/exbanka/transaction-service/internal/sitx"
	"github.com/glebarez/sqlite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

type stubAccountForHandler struct {
	getFn        func(ctx context.Context, in *accountpb.GetAccountByNumberRequest, opts ...grpc.CallOption) (*accountpb.AccountResponse, error)
	reserveFn    func(ctx context.Context, in *accountpb.ReserveIncomingRequest, opts ...grpc.CallOption) (*accountpb.ReserveIncomingResponse, error)
	commitFn     func(ctx context.Context, in *accountpb.CommitIncomingRequest, opts ...grpc.CallOption) (*accountpb.CommitIncomingResponse, error)
	releaseFn    func(ctx context.Context, in *accountpb.ReleaseIncomingRequest, opts ...grpc.CallOption) (*accountpb.ReleaseIncomingResponse, error)
	updateFn     func(ctx context.Context, in *accountpb.UpdateBalanceRequest, opts ...grpc.CallOption) (*accountpb.AccountResponse, error)
	reserveOutFn func(ctx context.Context, in *accountpb.ReserveOutgoingRequest, opts ...grpc.CallOption) (*accountpb.ReserveOutgoingResponse, error)
	settleOutFn  func(ctx context.Context, in *accountpb.SettleOutgoingRequest, opts ...grpc.CallOption) (*accountpb.SettleOutgoingResponse, error)
	releaseOutFn func(ctx context.Context, in *accountpb.ReleaseOutgoingRequest, opts ...grpc.CallOption) (*accountpb.ReleaseOutgoingResponse, error)
}

func (s *stubAccountForHandler) GetAccountByNumber(ctx context.Context, in *accountpb.GetAccountByNumberRequest, opts ...grpc.CallOption) (*accountpb.AccountResponse, error) {
	if s.getFn != nil {
		return s.getFn(ctx, in, opts...)
	}
	return &accountpb.AccountResponse{AccountNumber: in.AccountNumber, CurrencyCode: "RSD", Status: "active"}, nil
}
func (s *stubAccountForHandler) ReserveIncoming(ctx context.Context, in *accountpb.ReserveIncomingRequest, opts ...grpc.CallOption) (*accountpb.ReserveIncomingResponse, error) {
	if s.reserveFn != nil {
		return s.reserveFn(ctx, in, opts...)
	}
	return &accountpb.ReserveIncomingResponse{ReservationKey: in.ReservationKey}, nil
}
func (s *stubAccountForHandler) CommitIncoming(ctx context.Context, in *accountpb.CommitIncomingRequest, opts ...grpc.CallOption) (*accountpb.CommitIncomingResponse, error) {
	if s.commitFn != nil {
		return s.commitFn(ctx, in, opts...)
	}
	return &accountpb.CommitIncomingResponse{}, nil
}
func (s *stubAccountForHandler) ReleaseIncoming(ctx context.Context, in *accountpb.ReleaseIncomingRequest, opts ...grpc.CallOption) (*accountpb.ReleaseIncomingResponse, error) {
	if s.releaseFn != nil {
		return s.releaseFn(ctx, in, opts...)
	}
	return &accountpb.ReleaseIncomingResponse{}, nil
}
func (s *stubAccountForHandler) UpdateBalance(ctx context.Context, in *accountpb.UpdateBalanceRequest, opts ...grpc.CallOption) (*accountpb.AccountResponse, error) {
	if s.updateFn != nil {
		return s.updateFn(ctx, in, opts...)
	}
	return &accountpb.AccountResponse{AccountNumber: in.AccountNumber}, nil
}
func (s *stubAccountForHandler) ListAccountsByClient(ctx context.Context, in *accountpb.ListAccountsByClientRequest, opts ...grpc.CallOption) (*accountpb.ListAccountsResponse, error) {
	return &accountpb.ListAccountsResponse{}, nil
}
func (s *stubAccountForHandler) ReserveOutgoing(ctx context.Context, in *accountpb.ReserveOutgoingRequest, opts ...grpc.CallOption) (*accountpb.ReserveOutgoingResponse, error) {
	if s.reserveOutFn != nil {
		return s.reserveOutFn(ctx, in, opts...)
	}
	return &accountpb.ReserveOutgoingResponse{ReservationKey: in.ReservationKey}, nil
}
func (s *stubAccountForHandler) SettleOutgoing(ctx context.Context, in *accountpb.SettleOutgoingRequest, opts ...grpc.CallOption) (*accountpb.SettleOutgoingResponse, error) {
	if s.settleOutFn != nil {
		return s.settleOutFn(ctx, in, opts...)
	}
	return &accountpb.SettleOutgoingResponse{}, nil
}
func (s *stubAccountForHandler) ReleaseOutgoing(ctx context.Context, in *accountpb.ReleaseOutgoingRequest, opts ...grpc.CallOption) (*accountpb.ReleaseOutgoingResponse, error) {
	if s.releaseOutFn != nil {
		return s.releaseOutFn(ctx, in, opts...)
	}
	return &accountpb.ReleaseOutgoingResponse{Released: true}, nil
}

func newPeerTxHandler(t *testing.T) (*handler.PeerTxGRPCHandler, *gorm.DB, *stubAccountForHandler) {
	t.Helper()
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err := db.AutoMigrate(&model.PeerIdempotenceRecord{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	stub := &stubAccountForHandler{}
	idemRepo := repository.NewPeerIdempotenceRepository(db)
	exec := sitx.NewPostingExecutor(stub, 111)
	return handler.NewPeerTxGRPCHandler(idemRepo, exec, stub, nil, nil, nil, 111), db, stub
}

// TestHandleNewTx_PersistsMetadata_AndCommitPassesMemo is the Task 10 test:
// HandleNewTx persists the NEW_TX envelope's Message/PaymentCode/etc., and
// HandleCommitTx threads the stored Message into account-service as the ledger
// memo on the credit call.
func TestHandleNewTx_PersistsMetadata_AndCommitPassesMemo(t *testing.T) {
	h, db, stub := newPeerTxHandler(t)
	var capturedMemo string
	stub.commitFn = func(ctx context.Context, in *accountpb.CommitIncomingRequest, opts ...grpc.CallOption) (*accountpb.CommitIncomingResponse, error) {
		capturedMemo = in.GetMemo()
		return &accountpb.CommitIncomingResponse{}, nil
	}
	if _, err := h.HandleNewTx(context.Background(), &transactionpb.SiTxNewTxRequest{
		IdempotenceKey: &transactionpb.SiTxIdempotenceKey{RoutingNumber: 222, LocallyGeneratedKey: "meta-L"},
		PeerBankCode:   "222",
		TransactionId:  &transactionpb.SiTxForeignBankId{RoutingNumber: 222, Id: "meta-L"},
		Message:        "rent for May",
		PaymentCode:    "289",
		PaymentPurpose: "housing",
		CallNumber:     "97-1234",
		Postings: []*transactionpb.SiTxPosting{
			{RoutingNumber: 222, AccountType: "ACCOUNT", AccountId: "222000001", AssetType: "MONAS", AssetId: "RSD", Amount: "100.00", Direction: "DEBIT"},
			{RoutingNumber: 111, AccountType: "ACCOUNT", AccountId: "111000001", AssetType: "MONAS", AssetId: "RSD", Amount: "100.00", Direction: "CREDIT"},
		},
	}); err != nil {
		t.Fatalf("NEW_TX: %v", err)
	}

	// Metadata persisted on the record.
	var rec model.PeerIdempotenceRecord
	if err := db.Where("peer_bank_code = ? AND locally_generated_key = ?", "222", "meta-L").First(&rec).Error; err != nil {
		t.Fatalf("lookup rec: %v", err)
	}
	if rec.Message != "rent for May" || rec.PaymentCode != "289" || rec.PaymentPurpose != "housing" || rec.CallNumber != "97-1234" {
		t.Errorf("metadata not persisted: %+v", rec)
	}
	if rec.TxForeignID != "meta-L" || rec.TxRoutingNumber != 222 {
		t.Errorf("transactionId not persisted: routing=%d id=%q", rec.TxRoutingNumber, rec.TxForeignID)
	}

	// COMMIT (own fresh idem, correlate by transactionId) passes Message as memo.
	if _, err := h.HandleCommitTx(context.Background(), &transactionpb.SiTxCommitRequest{
		IdempotenceKey: &transactionpb.SiTxIdempotenceKey{RoutingNumber: 111, LocallyGeneratedKey: "meta-M"},
		PeerBankCode:   "222",
		TransactionId:  &transactionpb.SiTxForeignBankId{RoutingNumber: 222, Id: "meta-L"},
	}); err != nil {
		t.Fatalf("COMMIT: %v", err)
	}
	if capturedMemo != "rent for May" {
		t.Errorf("commit memo = %q, want the stored NEW_TX message", capturedMemo)
	}
}

// TestHandleCommitTx_CorrelatesByTransactionId_AndIdempotentOnRetransmit is the
// Task 11 receiver test: a COMMIT whose OWN idempotence key differs from the
// NEW_TX's resolves the same record via transactionId and commits; a second
// COMMIT (any idem) is a no-op.
func TestHandleCommitTx_CorrelatesByTransactionId_AndIdempotentOnRetransmit(t *testing.T) {
	h, _, stub := newPeerTxHandler(t)
	commitCalls := 0
	stub.commitFn = func(ctx context.Context, in *accountpb.CommitIncomingRequest, opts ...grpc.CallOption) (*accountpb.CommitIncomingResponse, error) {
		commitCalls++
		// Key is derived from the NEW_TX's L (222:L1), never from the COMMIT idem.
		if in.GetReservationKey() != "222:L1" {
			t.Errorf("reservation key = %q, want 222:L1 (derived from NEW_TX L)", in.GetReservationKey())
		}
		return &accountpb.CommitIncomingResponse{}, nil
	}
	// NEW_TX stored under L1 (peer 222, key L1); its own transactionId is {222,L1}.
	if _, err := h.HandleNewTx(context.Background(), &transactionpb.SiTxNewTxRequest{
		IdempotenceKey: &transactionpb.SiTxIdempotenceKey{RoutingNumber: 222, LocallyGeneratedKey: "L1"},
		PeerBankCode:   "222",
		TransactionId:  &transactionpb.SiTxForeignBankId{RoutingNumber: 222, Id: "L1"},
		Postings: []*transactionpb.SiTxPosting{
			{RoutingNumber: 222, AccountType: "ACCOUNT", AccountId: "222000001", AssetType: "MONAS", AssetId: "RSD", Amount: "100", Direction: "DEBIT"},
			{RoutingNumber: 111, AccountType: "ACCOUNT", AccountId: "111000001", AssetType: "MONAS", AssetId: "RSD", Amount: "100", Direction: "CREDIT"},
		},
	}); err != nil {
		t.Fatalf("NEW_TX: %v", err)
	}
	// COMMIT with a DIFFERENT idem (M1), correlating by transactionId {222,L1}.
	if _, err := h.HandleCommitTx(context.Background(), &transactionpb.SiTxCommitRequest{
		IdempotenceKey: &transactionpb.SiTxIdempotenceKey{RoutingNumber: 111, LocallyGeneratedKey: "M1"},
		PeerBankCode:   "222",
		TransactionId:  &transactionpb.SiTxForeignBankId{RoutingNumber: 222, Id: "L1"},
	}); err != nil {
		t.Fatalf("COMMIT: %v", err)
	}
	if commitCalls != 1 {
		t.Fatalf("expected exactly 1 CommitIncoming, got %d", commitCalls)
	}
	// Retransmitted COMMIT (idem M1 again) must be a no-op.
	if _, err := h.HandleCommitTx(context.Background(), &transactionpb.SiTxCommitRequest{
		IdempotenceKey: &transactionpb.SiTxIdempotenceKey{RoutingNumber: 111, LocallyGeneratedKey: "M1"},
		PeerBankCode:   "222",
		TransactionId:  &transactionpb.SiTxForeignBankId{RoutingNumber: 222, Id: "L1"},
	}); err != nil {
		t.Fatalf("COMMIT retransmit: %v", err)
	}
	if commitCalls != 1 {
		t.Errorf("retransmitted COMMIT must be a no-op; CommitIncoming called %d times", commitCalls)
	}
}

func TestHandleNewTx_HappyPath_YES(t *testing.T) {
	h, _, _ := newPeerTxHandler(t)
	resp, err := h.HandleNewTx(context.Background(), &transactionpb.SiTxNewTxRequest{
		IdempotenceKey: &transactionpb.SiTxIdempotenceKey{RoutingNumber: 222, LocallyGeneratedKey: "k1"},
		PeerBankCode:   "222",
		Postings: []*transactionpb.SiTxPosting{
			{RoutingNumber: 222, AccountType: "ACCOUNT", AccountId: "222000001", AssetType: "MONAS", AssetId: "RSD", Amount: "100.00", Direction: "DEBIT"},
			{RoutingNumber: 111, AccountType: "ACCOUNT", AccountId: "111000001", AssetType: "MONAS", AssetId: "RSD", Amount: "100.00", Direction: "CREDIT"},
		},
	})
	if err != nil {
		t.Fatalf("HandleNewTx: %v", err)
	}
	if resp.GetType() != contractsitx.VoteYes {
		t.Errorf("expected YES, got %+v", resp)
	}
	if resp.GetTransactionId() == "" {
		t.Errorf("expected transaction_id")
	}
}

func TestHandleNewTx_Unbalanced_NO(t *testing.T) {
	h, _, _ := newPeerTxHandler(t)
	resp, err := h.HandleNewTx(context.Background(), &transactionpb.SiTxNewTxRequest{
		IdempotenceKey: &transactionpb.SiTxIdempotenceKey{RoutingNumber: 222, LocallyGeneratedKey: "k2"},
		PeerBankCode:   "222",
		Postings: []*transactionpb.SiTxPosting{
			{RoutingNumber: 222, AccountType: "ACCOUNT", AccountId: "222000001", AssetType: "MONAS", AssetId: "RSD", Amount: "100.00", Direction: "DEBIT"},
			{RoutingNumber: 111, AccountType: "ACCOUNT", AccountId: "111000001", AssetType: "MONAS", AssetId: "RSD", Amount: "90.00", Direction: "CREDIT"},
		},
	})
	if err != nil {
		t.Fatalf("HandleNewTx: %v", err)
	}
	if resp.GetType() != contractsitx.VoteNo {
		t.Errorf("expected NO, got %+v", resp)
	}
}

func TestHandleNewTx_Replay_ReturnsCachedResponse(t *testing.T) {
	h, _, _ := newPeerTxHandler(t)
	in := &transactionpb.SiTxNewTxRequest{
		IdempotenceKey: &transactionpb.SiTxIdempotenceKey{RoutingNumber: 222, LocallyGeneratedKey: "k3"},
		PeerBankCode:   "222",
		Postings: []*transactionpb.SiTxPosting{
			{RoutingNumber: 222, AccountType: "ACCOUNT", AccountId: "222000001", AssetType: "MONAS", AssetId: "RSD", Amount: "100.00", Direction: "DEBIT"},
			{RoutingNumber: 111, AccountType: "ACCOUNT", AccountId: "111000001", AssetType: "MONAS", AssetId: "RSD", Amount: "100.00", Direction: "CREDIT"},
		},
	}
	r1, err := h.HandleNewTx(context.Background(), in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	r2, err := h.HandleNewTx(context.Background(), in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if r1.GetTransactionId() != r2.GetTransactionId() {
		t.Errorf("replay should return same transaction_id: %s vs %s", r1.GetTransactionId(), r2.GetTransactionId())
	}
}

func TestHandleCommitTx_AfterYes(t *testing.T) {
	h, _, stub := newPeerTxHandler(t)
	called := false
	stub.commitFn = func(ctx context.Context, in *accountpb.CommitIncomingRequest, opts ...grpc.CallOption) (*accountpb.CommitIncomingResponse, error) {
		called = true
		if in.ReservationKey != "222:k4" {
			t.Errorf("reservation key: %q", in.ReservationKey)
		}
		return &accountpb.CommitIncomingResponse{}, nil
	}
	_, _ = h.HandleNewTx(context.Background(), &transactionpb.SiTxNewTxRequest{
		IdempotenceKey: &transactionpb.SiTxIdempotenceKey{RoutingNumber: 222, LocallyGeneratedKey: "k4"},
		PeerBankCode:   "222",
		Postings: []*transactionpb.SiTxPosting{
			{RoutingNumber: 222, AccountType: "ACCOUNT", AccountId: "222000001", AssetType: "MONAS", AssetId: "RSD", Amount: "100.00", Direction: "DEBIT"},
			{RoutingNumber: 111, AccountType: "ACCOUNT", AccountId: "111000001", AssetType: "MONAS", AssetId: "RSD", Amount: "100.00", Direction: "CREDIT"},
		},
	})
	if _, err := h.HandleCommitTx(context.Background(), &transactionpb.SiTxCommitRequest{
		// COMMIT carries its OWN fresh idem (m4) and correlates to the NEW_TX
		// (L = k4) by transactionId.
		IdempotenceKey: &transactionpb.SiTxIdempotenceKey{RoutingNumber: 111, LocallyGeneratedKey: "m4"},
		PeerBankCode:   "222",
		TransactionId:  &transactionpb.SiTxForeignBankId{RoutingNumber: 222, Id: "k4"},
	}); err != nil {
		t.Fatalf("HandleCommitTx: %v", err)
	}
	if !called {
		t.Errorf("expected CommitIncoming call")
	}
}

func TestHandleRollbackTx_AfterYes(t *testing.T) {
	h, _, stub := newPeerTxHandler(t)
	called := false
	stub.releaseFn = func(ctx context.Context, in *accountpb.ReleaseIncomingRequest, opts ...grpc.CallOption) (*accountpb.ReleaseIncomingResponse, error) {
		called = true
		return &accountpb.ReleaseIncomingResponse{}, nil
	}
	_, _ = h.HandleNewTx(context.Background(), &transactionpb.SiTxNewTxRequest{
		IdempotenceKey: &transactionpb.SiTxIdempotenceKey{RoutingNumber: 222, LocallyGeneratedKey: "k5"},
		PeerBankCode:   "222",
		Postings: []*transactionpb.SiTxPosting{
			{RoutingNumber: 222, AccountType: "ACCOUNT", AccountId: "222000001", AssetType: "MONAS", AssetId: "RSD", Amount: "100.00", Direction: "DEBIT"},
			{RoutingNumber: 111, AccountType: "ACCOUNT", AccountId: "111000001", AssetType: "MONAS", AssetId: "RSD", Amount: "100.00", Direction: "CREDIT"},
		},
	})
	if _, err := h.HandleRollbackTx(context.Background(), &transactionpb.SiTxRollbackRequest{
		// ROLLBACK carries its OWN fresh idem (m5) and correlates to the NEW_TX
		// (L = k5) by transactionId.
		IdempotenceKey: &transactionpb.SiTxIdempotenceKey{RoutingNumber: 111, LocallyGeneratedKey: "m5"},
		PeerBankCode:   "222",
		TransactionId:  &transactionpb.SiTxForeignBankId{RoutingNumber: 222, Id: "k5"},
	}); err != nil {
		t.Fatalf("HandleRollbackTx: %v", err)
	}
	if !called {
		t.Errorf("expected ReleaseIncoming call")
	}
}

func TestInitiateOutboundTxWithPostings_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var probe map[string]any
		_ = json.NewDecoder(r.Body).Decode(&probe)
		if probe["messageType"] == "NEW_TX" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"vote":"YES"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err := db.AutoMigrate(&model.PeerIdempotenceRecord{}, &model.OutboundPeerTx{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	stub := &stubAccountForHandler{}
	idemRepo := repository.NewPeerIdempotenceRepository(db)
	outRepo := repository.NewOutboundPeerTxRepository(db)
	exec := sitx.NewPostingExecutor(stub, 111)
	httpClient := sitx.NewPeerHTTPClient(http.DefaultClient)
	peerLookup := func(ctx context.Context, code string) (*sitx.PeerHTTPTarget, error) {
		return &sitx.PeerHTTPTarget{BankCode: code, BaseURL: srv.URL, APIToken: "tok", OwnRouting: 111, RoutingNumber: 222}, nil
	}
	h := handler.NewPeerTxGRPCHandler(idemRepo, exec, stub, outRepo, httpClient, handler.PeerLookupFunc(peerLookup), 111)

	// Note: stubAccountForHandler returns CurrencyCode="RSD" by default,
	// so the money postings use RSD here. The option postings use a
	// JSON-shaped assetId starting with `{` which the executor skips
	// (option-asset handling is out of scope; matches production where
	// optAssetID is a marshalled OptionDescription).
	resp, err := h.InitiateOutboundTxWithPostings(context.Background(), &transactionpb.SiTxInitiateWithPostingsRequest{
		PeerBankCode: "222",
		TxKind:       "otc-accept",
		Postings: []*transactionpb.SiTxPosting{
			{RoutingNumber: 111, AccountType: "ACCOUNT", AccountId: "111-A", AssetType: "MONAS", AssetId: "RSD", Amount: "700", Direction: "DEBIT"},
			{RoutingNumber: 222, AccountType: "ACCOUNT", AccountId: "222-A", AssetType: "MONAS", AssetId: "RSD", Amount: "700", Direction: "CREDIT"},
			{RoutingNumber: 222, AccountType: "ACCOUNT", AccountId: "222-B", AssetType: "OPTION", AssetId: `{"ticker":"AAPL"}`, Amount: "1", Direction: "DEBIT"},
			{RoutingNumber: 111, AccountType: "ACCOUNT", AccountId: "111-B", AssetType: "OPTION", AssetId: `{"ticker":"AAPL"}`, Amount: "1", Direction: "CREDIT"},
		},
	})
	if err != nil {
		t.Fatalf("InitiateOutboundTxWithPostings: %v", err)
	}
	if resp.GetTransactionId() == "" {
		t.Errorf("expected transaction_id, got %+v", resp)
	}
	if resp.GetStatus() != "pending" {
		t.Errorf("status: %s", resp.GetStatus())
	}
}

// TestInitiateOutboundTxWithPostings_LocalCommitFailure_LeavesCommitting is the
// saga-pivot regression: after a YES vote, a failing local commit step (here the
// strike/premium settle) must leave the row in the forward-only `committing`
// state — NEVER `pending` (which the cron could max-attempts-COMPENSATE,
// stranding settled money) and never `rolled_back`.
func TestInitiateOutboundTxWithPostings_LocalCommitFailure_LeavesCommitting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var probe map[string]any
		_ = json.NewDecoder(r.Body).Decode(&probe)
		if probe["messageType"] == "NEW_TX" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"vote":"YES"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err := db.AutoMigrate(&model.PeerIdempotenceRecord{}, &model.OutboundPeerTx{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	stub := &stubAccountForHandler{}
	// Force the local DEBIT-leg settle to fail — simulates account-service
	// briefly unavailable during the commit phase.
	stub.settleOutFn = func(ctx context.Context, in *accountpb.SettleOutgoingRequest, opts ...grpc.CallOption) (*accountpb.SettleOutgoingResponse, error) {
		return nil, status.Error(codes.Unavailable, "account-service down")
	}
	idemRepo := repository.NewPeerIdempotenceRepository(db)
	outRepo := repository.NewOutboundPeerTxRepository(db)
	exec := sitx.NewPostingExecutor(stub, 111)
	httpClient := sitx.NewPeerHTTPClient(http.DefaultClient)
	peerLookup := func(ctx context.Context, code string) (*sitx.PeerHTTPTarget, error) {
		return &sitx.PeerHTTPTarget{BankCode: code, BaseURL: srv.URL, APIToken: "tok", OwnRouting: 111, RoutingNumber: 222}, nil
	}
	h := handler.NewPeerTxGRPCHandler(idemRepo, exec, stub, outRepo, httpClient, handler.PeerLookupFunc(peerLookup), 111)

	resp, err := h.InitiateOutboundTxWithPostings(context.Background(), &transactionpb.SiTxInitiateWithPostingsRequest{
		PeerBankCode: "222",
		TxKind:       "otc-accept",
		Postings: []*transactionpb.SiTxPosting{
			{RoutingNumber: 111, AccountType: "ACCOUNT", AccountId: "111-A", AssetType: "MONAS", AssetId: "RSD", Amount: "700", Direction: "DEBIT"},
			{RoutingNumber: 222, AccountType: "ACCOUNT", AccountId: "222-A", AssetType: "MONAS", AssetId: "RSD", Amount: "700", Direction: "CREDIT"},
		},
	})
	if err != nil {
		t.Fatalf("InitiateOutboundTxWithPostings: %v", err)
	}
	row, gerr := outRepo.GetByIdempotenceKey(resp.GetTransactionId())
	if gerr != nil {
		t.Fatalf("get row: %v", gerr)
	}
	if row.Status != "committing" {
		t.Errorf("after YES + local settle failure, row must be `committing` (forward-only), got %q", row.Status)
	}
}

func TestInitiateOutboundTxWithPostings_NoPostings_400(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	_ = db.AutoMigrate(&model.PeerIdempotenceRecord{}, &model.OutboundPeerTx{})
	stub := &stubAccountForHandler{}
	idemRepo := repository.NewPeerIdempotenceRepository(db)
	outRepo := repository.NewOutboundPeerTxRepository(db)
	exec := sitx.NewPostingExecutor(stub, 111)
	httpClient := sitx.NewPeerHTTPClient(http.DefaultClient)
	peerLookup := func(ctx context.Context, code string) (*sitx.PeerHTTPTarget, error) {
		return &sitx.PeerHTTPTarget{BankCode: code, BaseURL: "http://x", APIToken: "tok", OwnRouting: 111, RoutingNumber: 222}, nil
	}
	h := handler.NewPeerTxGRPCHandler(idemRepo, exec, stub, outRepo, httpClient, handler.PeerLookupFunc(peerLookup), 111)

	_, err := h.InitiateOutboundTxWithPostings(context.Background(), &transactionpb.SiTxInitiateWithPostingsRequest{
		PeerBankCode: "222",
		TxKind:       "otc-accept",
	})
	if err == nil {
		t.Fatalf("expected error for empty postings")
	}
}

// TestInitiateOutboundTx_NewTxTransactionId_CommitFreshIdem is the Task 11
// initiator test: the NEW_TX's Transaction.TransactionID.ID equals its own
// idempotence key (L), and the COMMIT envelope carries a DIFFERENT idem (M)
// while correlating to L via its Message.TransactionID.
func TestInitiateOutboundTx_NewTxTransactionId_CommitFreshIdem(t *testing.T) {
	type captured struct {
		messageType string
		idemKey     string
		txID        string
	}
	var seen []captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		mt, _ := raw["messageType"].(string)
		idem, _ := raw["idempotenceKey"].(map[string]any)
		idemKey, _ := idem["locallyGeneratedKey"].(string)
		msg, _ := raw["message"].(map[string]any)
		txid, _ := msg["transactionId"].(map[string]any)
		txID, _ := txid["id"].(string)
		seen = append(seen, captured{messageType: mt, idemKey: idemKey, txID: txID})
		if mt == "NEW_TX" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"vote":"YES"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err := db.AutoMigrate(&model.PeerIdempotenceRecord{}, &model.OutboundPeerTx{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	stub := &stubAccountForHandler{}
	idemRepo := repository.NewPeerIdempotenceRepository(db)
	outRepo := repository.NewOutboundPeerTxRepository(db)
	exec := sitx.NewPostingExecutor(stub, 111)
	httpClient := sitx.NewPeerHTTPClient(http.DefaultClient)
	peerLookup := func(ctx context.Context, code string) (*sitx.PeerHTTPTarget, error) {
		return &sitx.PeerHTTPTarget{BankCode: code, BaseURL: srv.URL, APIToken: "tok", OwnRouting: 111, RoutingNumber: 222}, nil
	}
	h := handler.NewPeerTxGRPCHandler(idemRepo, exec, stub, outRepo, httpClient, handler.PeerLookupFunc(peerLookup), 111)

	resp, err := h.InitiateOutboundTx(context.Background(), &transactionpb.SiTxInitiateRequest{
		FromAccountNumber: "111000000000000001",
		ToAccountNumber:   "222000000000000002",
		Amount:            "100",
		Currency:          "RSD",
	})
	if err != nil {
		t.Fatalf("InitiateOutboundTx: %v", err)
	}
	L := resp.GetTransactionId()
	var newtx, commit *captured
	for i := range seen {
		switch seen[i].messageType {
		case "NEW_TX":
			newtx = &seen[i]
		case "COMMIT_TX":
			commit = &seen[i]
		}
	}
	if newtx == nil || commit == nil {
		t.Fatalf("expected both NEW_TX and COMMIT_TX, got %+v", seen)
	}
	// NEW_TX idem == L; NEW_TX transactionId.id == L.
	if newtx.idemKey != L {
		t.Errorf("NEW_TX idem = %q, want L = %q", newtx.idemKey, L)
	}
	if newtx.txID != L {
		t.Errorf("NEW_TX transactionId.id = %q, want L = %q", newtx.txID, L)
	}
	// COMMIT idem != L (its own unique key); COMMIT transactionId.id == L.
	if commit.idemKey == L || commit.idemKey == "" {
		t.Errorf("COMMIT idem = %q, must be a fresh key distinct from L = %q", commit.idemKey, L)
	}
	if commit.txID != L {
		t.Errorf("COMMIT transactionId.id = %q, want L = %q", commit.txID, L)
	}
}
