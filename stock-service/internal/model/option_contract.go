package model

import (
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	OptionContractStatusActive    = "ACTIVE"
	OptionContractStatusExercised = "EXERCISED"
	OptionContractStatusExpired   = "EXPIRED"
	OptionContractStatusFailed    = "FAILED"
)

// OptionContract is the executed option that the premium-payment saga
// produces from an OTCOffer. quantity, strike_price, and settlement_date are
// snapshotted from the final accepted revision.
type OptionContract struct {
	ID uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// RoutingNumber is the bank that owns this row; stamped to OwnRouting on
	// local create by BeforeCreate. (routing_number, native_id) is the
	// bank-scoped natural key. Local-vs-remote is `RoutingNumber == OwnRouting()`.
	RoutingNumber int64   `gorm:"not null;default:0;uniqueIndex:ux_oc_native,priority:1" json:"routing_number"`
	NativeID      *string `gorm:"size:128;uniqueIndex:ux_oc_native,priority:2" json:"native_id,omitempty"`
	// OfferID is the local OTCOffer this contract was minted from. Nullable:
	// remote contracts (added later) have no local offer and store NULL here.
	// Postgres treats NULLs as distinct under a unique index, so the
	// one-contract-per-offer invariant holds for local rows while remote rows
	// (NULL) never collide.
	OfferID         *uint64         `gorm:"uniqueIndex" json:"offer_id,omitempty"`
	BuyerOwnerType  OwnerType       `gorm:"size:8;not null;index:ix_oc_buyer,priority:1;check:buyer_owner_type IN ('client','bank')" json:"buyer_owner_type"`
	BuyerOwnerID    *uint64         `gorm:"index:ix_oc_buyer,priority:2" json:"buyer_owner_id,omitempty"`
	BuyerBankCode   *string         `gorm:"size:32" json:"buyer_bank_code,omitempty"`
	SellerOwnerType OwnerType       `gorm:"size:8;not null;index:ix_oc_seller,priority:1;check:seller_owner_type IN ('client','bank')" json:"seller_owner_type"`
	SellerOwnerID   *uint64         `gorm:"index:ix_oc_seller,priority:2" json:"seller_owner_id,omitempty"`
	SellerBankCode  *string         `gorm:"size:32" json:"seller_bank_code,omitempty"`
	StockID         uint64          `gorm:"not null;index" json:"stock_id"`
	Ticker          string          `gorm:"size:16;not null;default:''" json:"ticker"`
	Quantity        decimal.Decimal `gorm:"type:numeric(20,8);not null" json:"quantity"`
	StrikePrice     decimal.Decimal `gorm:"type:numeric(20,8);not null" json:"strike_price"`
	PremiumPaid     decimal.Decimal `gorm:"type:numeric(20,8);not null" json:"premium_paid"`
	PremiumCurrency string          `gorm:"size:8;not null" json:"premium_currency"`
	StrikeCurrency  string          `gorm:"size:8;not null" json:"strike_currency"`
	SettlementDate  time.Time       `gorm:"type:date;not null;index:ix_oc_settle" json:"settlement_date"`
	// Accounts bound at accept time: buyer's pays the premium/strike, seller's
	// receives them. Read straight off the contract on exercise.
	BuyerAccountID  uint64     `gorm:"not null;default:0" json:"buyer_account_id"`
	SellerAccountID uint64     `gorm:"not null;default:0" json:"seller_account_id"`
	Status          string     `gorm:"size:16;not null;index:ix_oc_buyer,priority:3;index:ix_oc_seller,priority:3" json:"status"`
	SagaID          string     `gorm:"size:64;not null" json:"saga_id"`
	PremiumPaidAt   time.Time  `gorm:"not null" json:"premium_paid_at"`
	ExercisedAt     *time.Time `json:"exercised_at,omitempty"`
	ExpiredAt       *time.Time `json:"expired_at,omitempty"`
	// Cross-bank saga linkage (Spec 4 / Celina 5). CrossbankTxID is set on
	// accept; CrossbankExerciseTxID is set on cross-bank exercise.
	CrossbankTxID         *string `gorm:"size:36;index" json:"crossbank_tx_id,omitempty"`
	CrossbankExerciseTxID *string `gorm:"size:36;index" json:"crossbank_exercise_tx_id,omitempty"`
	// OnBehalfOfFundID, when non-nil, records that the BUYER side of this
	// contract was placed on behalf of an investment fund (E2, Plan E).
	// The fund's manager is the acting employee. On exercise, the acquired
	// shares are credited to fund_holdings instead of the buyer's personal
	// holdings.
	OnBehalfOfFundID *uint64   `gorm:"index" json:"on_behalf_of_fund_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Version          int64     `gorm:"not null;default:0" json:"-"`
}

// BeforeCreate stamps the own routing number on local rows. Remote rows
// (added in later tasks) arrive with RoutingNumber already set to the peer's
// routing and are left untouched. Tolerates a nil tx (only touches the struct).
func (c *OptionContract) BeforeCreate(tx *gorm.DB) error {
	if c.RoutingNumber == 0 {
		c.RoutingNumber = OwnRouting()
	}
	return nil
}

func (c *OptionContract) BeforeSave(tx *gorm.DB) error {
	if err := ValidateOwner(c.BuyerOwnerType, c.BuyerOwnerID); err != nil {
		return err
	}
	return ValidateOwner(c.SellerOwnerType, c.SellerOwnerID)
}

func (c *OptionContract) BeforeUpdate(tx *gorm.DB) error {
	if tx != nil {
		tx.Statement.Where("version = ?", c.Version)
	}
	c.Version++
	return nil
}

// IsCrossBank reports whether buyer and seller live on different banks.
// Empty bank codes mean same-bank (legacy / pre-Spec-4 rows).
func (c *OptionContract) IsCrossBank() bool {
	bb, sb := "", ""
	if c.BuyerBankCode != nil {
		bb = *c.BuyerBankCode
	}
	if c.SellerBankCode != nil {
		sb = *c.SellerBankCode
	}
	return bb != "" && sb != "" && bb != sb
}

func (c *OptionContract) IsTerminal() bool {
	switch c.Status {
	case OptionContractStatusExercised, OptionContractStatusExpired, OptionContractStatusFailed:
		return true
	}
	return false
}
