package repository

import (
	"testing"
	"time"

	"github.com/exbanka/stock-service/internal/model"
	"github.com/shopspring/decimal"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newRemoteOfferDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.RemoteOTCOffer{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func sampleRemote(routing int64, fid string) *model.RemoteOTCOffer {
	return &model.RemoteOTCOffer{
		PeerRoutingNumber: routing, ForeignOfferID: fid, BankCode: "111",
		SellerID: "employee-1", Direction: "sell_initiated", Ticker: "BAC", Amount: 7,
		StrikePrice: decimal.RequireFromString("100"), StrikeCurrency: "USD",
		Premium: decimal.RequireFromString("10"), PremiumCurrency: "USD",
		SettlementDate: "2026-06-11T00:00:00Z", PeerCreatedAt: "2026-06-04T18:02:16Z",
	}
}

func TestRemoteOffer_UpsertIsIdempotentAndStableID(t *testing.T) {
	db := newRemoteOfferDB(t)
	r := NewRemoteOTCOfferRepository(db)
	now := time.Now().UTC()

	id1, err := r.Upsert(sampleRemote(111, "1"), now)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	o := sampleRemote(111, "1")
	o.Premium = decimal.RequireFromString("12")
	id2, err := r.Upsert(o, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("surrogate id changed across upserts: %d != %d", id1, id2)
	}
	got, err := r.GetByID(id1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Premium.Equal(decimal.RequireFromString("12")) {
		t.Fatalf("premium not updated: %s", got.Premium)
	}
	if got.Status != "open" {
		t.Fatalf("status = %q, want open", got.Status)
	}
}

func TestRemoteOffer_ReconcileCancelsOnlyNotSeen(t *testing.T) {
	db := newRemoteOfferDB(t)
	r := NewRemoteOTCOfferRepository(db)
	now := time.Now().UTC()
	idA, _ := r.Upsert(sampleRemote(111, "A"), now)
	_, _ = r.Upsert(sampleRemote(111, "B"), now)
	_, _ = r.Upsert(sampleRemote(222, "A"), now)

	n, err := r.ReconcilePeerNotSeen(111, []string{"A"})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("cancelled %d rows, want 1", n)
	}
	a, _ := r.GetByID(idA)
	if a.Status != "open" {
		t.Fatalf("seen offer A flipped to %q", a.Status)
	}
	var b model.RemoteOTCOffer
	db.Where("peer_routing_number = ? AND foreign_offer_id = ?", 111, "B").First(&b)
	if b.Status != "cancelled" {
		t.Fatalf("unseen offer B = %q, want cancelled", b.Status)
	}
	var other model.RemoteOTCOffer
	db.Where("peer_routing_number = ? AND foreign_offer_id = ?", 222, "A").First(&other)
	if other.Status != "open" {
		t.Fatalf("other peer's offer flipped to %q", other.Status)
	}
}

func TestRemoteOffer_ReconcileEmptySeenCancelsAllForPeer(t *testing.T) {
	db := newRemoteOfferDB(t)
	r := NewRemoteOTCOfferRepository(db)
	now := time.Now().UTC()
	_, _ = r.Upsert(sampleRemote(111, "A"), now)
	_, _ = r.Upsert(sampleRemote(111, "B"), now)
	n, err := r.ReconcilePeerNotSeen(111, nil)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 2 {
		t.Fatalf("cancelled %d, want 2", n)
	}
}

func TestRemoteOffer_ReappearReopens(t *testing.T) {
	db := newRemoteOfferDB(t)
	r := NewRemoteOTCOfferRepository(db)
	now := time.Now().UTC()
	id, _ := r.Upsert(sampleRemote(111, "A"), now)
	_, _ = r.ReconcilePeerNotSeen(111, nil)
	if _, err := r.Upsert(sampleRemote(111, "A"), now.Add(time.Hour)); err != nil {
		t.Fatalf("reopen upsert: %v", err)
	}
	got, _ := r.GetByID(id)
	if got.Status != "open" {
		t.Fatalf("reappeared offer = %q, want open", got.Status)
	}
}
