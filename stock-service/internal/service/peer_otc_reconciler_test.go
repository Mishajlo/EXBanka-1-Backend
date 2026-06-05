package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	contractkafka "github.com/exbanka/contract/kafka"
	"github.com/exbanka/stock-service/internal/model"
)

// ---------------------------------------------------------------------------
// Fake repo
// ---------------------------------------------------------------------------

type fakeNegRepo struct {
	mu      sync.Mutex
	rows    []model.PeerOtcNegotiation
	updates []updateCall
}

type updateCall struct {
	peerCode  string
	foreignID string
	status    string
}

func (f *fakeNegRepo) ListOngoing() ([]model.PeerOtcNegotiation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.PeerOtcNegotiation, len(f.rows))
	copy(out, f.rows)
	return out, nil
}

func (f *fakeNegRepo) UpdateStatus(peerCode, foreignID, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, updateCall{peerCode, foreignID, status})
	return nil
}

func (f *fakeNegRepo) getUpdates() []updateCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]updateCall, len(f.updates))
	copy(out, f.updates)
	return out
}

// ---------------------------------------------------------------------------
// Fake notifier
// ---------------------------------------------------------------------------

type fakeReconcilerNotifier struct {
	mu   sync.Mutex
	msgs []contractkafka.GeneralNotificationMessage
}

func (n *fakeReconcilerNotifier) PublishGeneralNotification(_ context.Context, m contractkafka.GeneralNotificationMessage) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msgs = append(n.msgs, m)
	return nil
}

func (n *fakeReconcilerNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.msgs)
}

// ---------------------------------------------------------------------------
// Helper: build a reconciler with injected fakes (no gRPC, no HTTP)
// ---------------------------------------------------------------------------

func newTestReconciler(
	repo peerOtcNegRepo,
	fetcher PeerNegStatusFetcher,
	ownRouting int64,
) *PeerOTCNegotiationReconciler {
	r := &PeerOTCNegotiationReconciler{
		repo:       repo,
		peerAdmin:  nil, // won't be called — peerMap is bypassed in reconcileRow tests
		fetcher:    fetcher,
		ownRouting: ownRouting,
		interval:   time.Hour, // irrelevant for unit tests
	}
	return r
}

// ---------------------------------------------------------------------------
// Test: Run() returns promptly when ctx is cancelled
// ---------------------------------------------------------------------------

func TestPeerOTCReconciler_RunCancelsCleanly(t *testing.T) {
	repo := &fakeNegRepo{}
	fetcher := func(_ context.Context, _, _, _, _ string) (bool, error) {
		return true, nil // never called in this test
	}

	r := &PeerOTCNegotiationReconciler{
		repo:       repo,
		peerAdmin:  nil,
		fetcher:    fetcher,
		ownRouting: 111,
		interval:   10 * time.Millisecond, // very short to prove the loop respects cancel
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	// Cancel immediately and ensure Run exits within 1 second.
	cancel()
	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Fatal("Run() did not return within 1s after ctx cancel")
	}
}

// ---------------------------------------------------------------------------
// Test: fetcher reports "cancelled" → row is flipped
// ---------------------------------------------------------------------------

func TestPeerOTCReconciler_ReconcileRow_FetcherReportsCancelled_FlipsStatus(t *testing.T) {
	const (
		ownRouting  int64  = 111
		peerRouting int64  = 222
		peerCode           = "222"
		foreignID          = "neg-abc-123"
		buyerID            = "client-99" // on peer bank (routing 222)
		sellerID           = "client-42" // on our bank (routing 111)
	)

	repo := &fakeNegRepo{
		rows: []model.PeerOtcNegotiation{
			{
				ID:                 1,
				PeerBankCode:       peerCode,
				ForeignID:          foreignID,
				BuyerRoutingNumber: peerRouting,
				BuyerID:            buyerID,
				SellerRoutingNumber: ownRouting,
				SellerID:           sellerID,
				Status:             "ongoing",
			},
		},
	}

	// Fetcher always says not-ongoing (terminal state on peer).
	fetcher := func(_ context.Context, _, _, _, _ string) (bool, error) {
		return false, nil
	}

	r := newTestReconciler(repo, fetcher, ownRouting)

	peerMap := map[string]peerEntry{
		peerCode: {baseURL: "http://peer:8080/api/v3", apiKey: "secret"},
	}

	if err := r.reconcileRow(context.Background(), &repo.rows[0], peerMap); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updates := repo.getUpdates()
	if len(updates) != 1 {
		t.Fatalf("expected 1 UpdateStatus call, got %d", len(updates))
	}
	u := updates[0]
	if u.peerCode != peerCode || u.foreignID != foreignID || u.status != "cancelled" {
		t.Errorf("unexpected update: %+v", u)
	}
}

// ---------------------------------------------------------------------------
// Test: fetcher returns error → row stays ongoing (false-cancel guard)
// ---------------------------------------------------------------------------

func TestPeerOTCReconciler_ReconcileRow_FetcherError_NoStatusChange(t *testing.T) {
	const (
		ownRouting  int64 = 111
		peerRouting int64 = 222
		peerCode         = "222"
		foreignID        = "neg-xyz-456"
	)

	repo := &fakeNegRepo{
		rows: []model.PeerOtcNegotiation{
			{
				ID:                  2,
				PeerBankCode:        peerCode,
				ForeignID:           foreignID,
				BuyerRoutingNumber:  peerRouting,
				BuyerID:             "client-7",
				SellerRoutingNumber: ownRouting,
				SellerID:            "client-5",
				Status:              "ongoing",
			},
		},
	}

	// Fetcher returns a transport error.
	fetcher := func(_ context.Context, _, _, _, _ string) (bool, error) {
		return false, errors.New("connection refused")
	}

	r := newTestReconciler(repo, fetcher, ownRouting)

	peerMap := map[string]peerEntry{
		peerCode: {baseURL: "http://peer:8080/api/v3", apiKey: "secret"},
	}

	// reconcileRow should return the error (caller logs and skips).
	err := r.reconcileRow(context.Background(), &repo.rows[0], peerMap)
	if err == nil {
		t.Fatal("expected error from reconcileRow when fetcher fails")
	}

	// No UpdateStatus should have been called.
	if got := repo.getUpdates(); len(got) != 0 {
		t.Errorf("expected 0 UpdateStatus calls on fetcher error, got %d: %+v", len(got), got)
	}
}

// ---------------------------------------------------------------------------
// Test: fetcher reports "ongoing" → no change
// ---------------------------------------------------------------------------

func TestPeerOTCReconciler_ReconcileRow_FetcherReportsOngoing_NoChange(t *testing.T) {
	const (
		ownRouting  int64 = 111
		peerRouting int64 = 222
		peerCode         = "222"
		foreignID        = "neg-still-going"
	)

	repo := &fakeNegRepo{
		rows: []model.PeerOtcNegotiation{
			{
				ID:                  3,
				PeerBankCode:        peerCode,
				ForeignID:           foreignID,
				BuyerRoutingNumber:  peerRouting,
				BuyerID:             "client-10",
				SellerRoutingNumber: ownRouting,
				SellerID:            "client-20",
				Status:              "ongoing",
			},
		},
	}

	// Fetcher says still ongoing.
	fetcher := func(_ context.Context, _, _, _, _ string) (bool, error) {
		return true, nil
	}

	r := newTestReconciler(repo, fetcher, ownRouting)

	peerMap := map[string]peerEntry{
		peerCode: {baseURL: "http://peer:8080/api/v3", apiKey: "secret"},
	}

	if err := r.reconcileRow(context.Background(), &repo.rows[0], peerMap); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := repo.getUpdates(); len(got) != 0 {
		t.Errorf("expected 0 UpdateStatus calls when peer reports ongoing, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// Test: intra-bank row (both routings == ownRouting) → skipped
// ---------------------------------------------------------------------------

func TestPeerOTCReconciler_ReconcileRow_IntraBank_Skipped(t *testing.T) {
	const ownRouting int64 = 111

	repo := &fakeNegRepo{
		rows: []model.PeerOtcNegotiation{
			{
				ID:                  4,
				PeerBankCode:        "111",
				ForeignID:           "neg-intra",
				BuyerRoutingNumber:  ownRouting,
				BuyerID:             "client-1",
				SellerRoutingNumber: ownRouting,
				SellerID:            "client-2",
				Status:              "ongoing",
			},
		},
	}

	fetcherCalled := false
	fetcher := func(_ context.Context, _, _, _, _ string) (bool, error) {
		fetcherCalled = true
		return false, nil
	}

	r := newTestReconciler(repo, fetcher, ownRouting)

	peerMap := map[string]peerEntry{
		"111": {baseURL: "http://self:8080/api/v3", apiKey: "key"},
	}

	if err := r.reconcileRow(context.Background(), &repo.rows[0], peerMap); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fetcherCalled {
		t.Error("fetcher should NOT be called for intra-bank rows (peerRoutingForRow returns 0)")
	}
	if got := repo.getUpdates(); len(got) != 0 {
		t.Errorf("expected 0 updates for intra-bank row, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// Test: notification is sent when local party can be resolved
// ---------------------------------------------------------------------------

func TestPeerOTCReconciler_ReconcileRow_NotifiesLocalParty(t *testing.T) {
	const (
		ownRouting  int64 = 111
		peerRouting int64 = 222
		peerCode         = "222"
		foreignID        = "neg-notif-test"
		sellerID         = "client-42" // local seller on ownRouting
	)

	repo := &fakeNegRepo{
		rows: []model.PeerOtcNegotiation{
			{
				ID:                  5,
				PeerBankCode:        peerCode,
				ForeignID:           foreignID,
				BuyerRoutingNumber:  peerRouting,
				BuyerID:             "client-7",
				SellerRoutingNumber: ownRouting,
				SellerID:            sellerID,
				Status:              "ongoing",
			},
		},
	}

	fetcher := func(_ context.Context, _, _, _, _ string) (bool, error) {
		return false, nil // peer reports terminal
	}

	notifier := &fakeReconcilerNotifier{}
	r := newTestReconciler(repo, fetcher, ownRouting)
	r.notifier = notifier

	peerMap := map[string]peerEntry{
		peerCode: {baseURL: "http://peer:8080/api/v3", apiKey: "secret"},
	}

	if err := r.reconcileRow(context.Background(), &repo.rows[0], peerMap); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if notifier.count() != 1 {
		t.Errorf("expected 1 notification, got %d", notifier.count())
	}
}

// ---------------------------------------------------------------------------
// Test: peerRoutingForRow picks the non-own routing
// ---------------------------------------------------------------------------

func TestPeerRoutingForRow(t *testing.T) {
	r := &PeerOTCNegotiationReconciler{ownRouting: 111}

	tests := []struct {
		name            string
		buyerRouting    int64
		sellerRouting   int64
		expectedRouting int64
	}{
		{"buyer is peer", 222, 111, 222},
		{"seller is peer", 111, 333, 333},
		{"both own (intra-bank)", 111, 111, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := &model.PeerOtcNegotiation{
				BuyerRoutingNumber:  tc.buyerRouting,
				SellerRoutingNumber: tc.sellerRouting,
			}
			got := r.peerRoutingForRow(row)
			if got != tc.expectedRouting {
				t.Errorf("got %d, want %d", got, tc.expectedRouting)
			}
		})
	}
}
