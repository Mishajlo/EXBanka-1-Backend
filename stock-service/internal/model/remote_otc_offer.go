package model

import (
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// RemoteOTCOffer is the persistent mirror of an OTC option listing
// discovered on a peer bank via GET /cross-bank-protocol/public-option-offers.
//
// The option cache (otccache.OptionRefresher) upserts one row per remote
// listing on every successful peer poll. The autoincrement ID is the
// STABLE local surrogate id surfaced to the frontend as the unified
// offer id, so the same peer listing keeps the same id across cache
// rebuilds. Reconciliation flips Status open->cancelled when a peer
// stops listing an offer (see RemoteOTCOfferRepository.ReconcilePeerNotSeen).
//
// Natural key: (PeerRoutingNumber, ForeignOfferID).
type RemoteOTCOffer struct {
	ID                uint64 `gorm:"primaryKey;autoIncrement"`
	PeerRoutingNumber int64  `gorm:"uniqueIndex:ux_remote_offer,priority:1;not null"`
	ForeignOfferID    string `gorm:"uniqueIndex:ux_remote_offer,priority:2;size:128;not null"`

	BankCode        string `gorm:"size:8;not null"`
	SellerID        string `gorm:"size:128"` // SI-TX wire id: "client-<N>" | "employee-<N>" | legacy "bank"
	Direction       string `gorm:"size:24"`  // sell_initiated | buy_initiated
	Ticker          string `gorm:"size:32"`
	Amount          int64
	StrikePrice     decimal.Decimal `gorm:"type:numeric(20,8)"`
	StrikeCurrency  string          `gorm:"size:8"`
	Premium         decimal.Decimal `gorm:"type:numeric(20,8)"`
	PremiumCurrency string          `gorm:"size:8"`
	SettlementDate  string          `gorm:"size:64"` // RFC3339 UTC as published by the peer
	PeerCreatedAt   string          `gorm:"size:64"`

	Status     string    `gorm:"size:24;index;not null;default:'open'"` // open | cancelled
	LastSeenAt time.Time `gorm:"index"`                                 // last successful peer poll that listed it

	CreatedAt time.Time
	UpdatedAt time.Time
	Version   int64 `gorm:"not null;default:0"`
}

// BeforeUpdate enforces optimistic locking per the Concurrency requirement.
func (m *RemoteOTCOffer) BeforeUpdate(tx *gorm.DB) error {
	if tx != nil {
		tx.Statement.Where("version = ?", m.Version)
	}
	m.Version++
	return nil
}
