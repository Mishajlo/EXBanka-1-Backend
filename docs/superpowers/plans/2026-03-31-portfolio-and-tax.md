# Portfolio, OTC & Tax Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement portfolio holdings tracking, full OTC trading (replacing Plan 5 stubs), option exercise, capital gains tax calculation and collection, and the state company entity for tax deposits.

**Architecture:** When orders fill (via Plan 5's execution engine), portfolio holdings are created/updated. Sell fills record capital gains. OTC trading transfers shares between users via public holdings. Tax is computed monthly (15% of realized capital gains) and collected automatically or by supervisor trigger. The "State" is a Company with an RSD-only account that receives tax payments.

**Tech Stack:** Go 1.26, gRPC/Protobuf, GORM/PostgreSQL, Kafka (segmentio/kafka-go), `shopspring/decimal`

**Depends on:**
- Plan 1 (API surface) — `PortfolioGRPCService`, `OTCGRPCService`, `TaxGRPCService` proto definitions
- Plan 2 (exchanges) — Exchange model, `SettingRepo` for testing mode
- Plan 3 (securities) — Security models (Stock, Option), repositories
- Plan 4 (listings) — `Listing` model, `ListingRepo` for current prices
- Plan 5 (orders) — `Order`, `OrderTransaction`, `OrderExecutionEngine`

---

## Design Decisions

### Holdings Aggregation

When a user buys the same security multiple times on the same account, the holding is **upserted** (not duplicated). The `AveragePrice` is a weighted average:

```
new_avg = (old_avg × old_qty + fill_price × fill_qty) / (old_qty + fill_qty)
```

Unique constraint: `(user_id, security_type, security_id, account_id)`.

### Capital Gains Tax

- Tax is **15% of realized capital gains** from stock sales (exchange and OTC).
- Gains are recorded per-fill: `gain = (sell_price - average_buy_price) × quantity`.
- Tax is deducted from the **same account** where the gain was realized (per spec).
- Foreign currency gains are converted to RSD via exchange-service `Convert` RPC (no commission).
- Tax payments are credited to the **State's RSD account**.

### Tax Collection Flow

At month end (or supervisor trigger):
1. For each user, group `CapitalGain` records by `(account_id, currency)` for the current month.
2. For each group with positive total gain: `tax = total_gain × 0.15`.
3. Convert tax to RSD if the account is foreign currency.
4. Debit user's account (via account-service `UpdateBalance`).
5. Credit State's RSD account (via account-service `UpdateBalance`).
6. Record a `TaxCollection` entry.

### OTC Trading

OTC offers are **not a separate model** — they are Holding records with `public_quantity > 0`. The "offer ID" in the API is the holding ID. Only **stocks** can be made public.

OTC buy flow:
1. Validate: seller's `public_quantity >= requested_quantity`.
2. Compute price: `current_listing_price × quantity`.
3. Compute commission: `min(14% × total_price, $7)` (same as Market order).
4. Debit buyer's account: `total_price + commission`.
5. Credit seller's account: `total_price`.
6. Credit bank's account: `commission`.
7. Transfer holding: decrease seller's quantity & public_quantity, create/update buyer's holding.
8. Record `CapitalGain` for seller: `(listing_price - seller_avg_price) × quantity`.

### Option Exercise

Only **actuaries** (employees with EmployeeAgent role) can exercise options:
- **CALL**: Stock price > strike → user acquires `quantity × 100` shares at strike price. Debit account by `strike × shares`. Create/update stock holding.
- **PUT**: Stock price < strike → user sells `quantity × 100` shares at strike price. Must hold enough stock. Credit account by `strike × shares`. Decrease stock holding. Record capital gain.
- Option holding is **deleted** after exercise.
- Settlement date must not have passed.

### Account Balance Integration

stock-service calls account-service via gRPC for all debit/credit operations:
1. `GetAccount(id)` → retrieve account_number and currency.
2. `UpdateBalance(account_number, amount, update_available=true)` → debit (negative) or credit (positive).

### User Name Resolution

Holding stores denormalized `UserFirstName` and `UserLastName`, looked up once at creation from user-service or client-service. This avoids per-request gRPC calls when listing OTC offers.

---

## File Structure

### New files to create

```
stock-service/
├── internal/
│   ├── model/
│   │   ├── holding.go                 # Portfolio holding entity
│   │   ├── capital_gain.go            # Realized capital gain per sell fill
│   │   └── tax_collection.go          # Monthly tax collection record
│   ├── repository/
│   │   ├── holding_repository.go      # Holding CRUD + OTC queries
│   │   ├── capital_gain_repository.go # Capital gain tracking
│   │   └── tax_collection_repository.go # Tax collection records
│   ├── service/
│   │   ├── portfolio_service.go       # Holdings, exercise, fill processing
│   │   ├── tax_service.go            # Tax computation + collection
│   │   └── tax_cron.go              # Monthly auto-collection cron
│   └── handler/
│       ├── portfolio_handler.go       # PortfolioGRPCService implementation
│       └── tax_handler.go           # TaxGRPCService implementation
```

### Files to modify

```
stock-service/internal/service/interfaces.go       # Add Holding, CapitalGain, TaxCollection repo interfaces
stock-service/internal/service/otc_service.go       # Replace stub with full implementation
stock-service/internal/service/order_execution.go   # Add FillHandler callback
stock-service/internal/handler/otc_handler.go       # Replace stub with full implementation
stock-service/internal/config/config.go             # Add ClientGRPCAddr, StateAccountNumber
stock-service/internal/kafka/producer.go            # Add portfolio/tax event publishing
stock-service/cmd/main.go                           # Wire portfolio, OTC, tax models, repos, services, handlers
contract/kafka/messages.go                          # Add portfolio/tax event topics
docker-compose.yml                                  # Add CLIENT_GRPC_ADDR, STATE_ACCOUNT_NUMBER to stock-service
account-service/cmd/main.go                         # Seed state company + RSD account
```

---

## Task 1: Define Holding model

**Files:**
- Create: `stock-service/internal/model/holding.go`

- [ ] **Step 1: Write the model**

```go
package model

import (
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Holding represents a user's ownership of a security.
// Holdings are aggregated per (user_id, security_type, security_id, account_id).
type Holding struct {
	ID             uint64          `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID         uint64          `gorm:"not null;index:idx_holding_user" json:"user_id"`
	SystemType     string          `gorm:"size:10;not null" json:"system_type"` // "employee" or "client"
	UserFirstName  string          `gorm:"size:100;not null" json:"user_first_name"`
	UserLastName   string          `gorm:"size:100;not null" json:"user_last_name"`
	SecurityType   string          `gorm:"size:10;not null" json:"security_type"` // "stock", "futures", "forex", "option"
	SecurityID     uint64          `gorm:"not null" json:"security_id"`
	ListingID      uint64          `gorm:"not null;index" json:"listing_id"`
	Ticker         string          `gorm:"size:30;not null" json:"ticker"`
	Name           string          `gorm:"size:200;not null" json:"name"`
	Quantity       int64           `gorm:"not null" json:"quantity"`
	AveragePrice   decimal.Decimal `gorm:"type:numeric(18,4);not null" json:"average_price"`
	PublicQuantity int64           `gorm:"not null;default:0" json:"public_quantity"` // OTC: shares available for purchase
	AccountID      uint64          `gorm:"not null" json:"account_id"`
	Version        int64           `gorm:"not null;default:1" json:"-"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

func (h *Holding) BeforeUpdate(tx *gorm.DB) error {
	tx.Statement.Where("version = ?", h.Version)
	h.Version++
	return nil
}
```

- [ ] **Step 2: Commit**

```bash
git add stock-service/internal/model/holding.go
git commit -m "feat(stock-service): add Holding model for portfolio ownership tracking"
```

---

## Task 2: Define CapitalGain and TaxCollection models

**Files:**
- Create: `stock-service/internal/model/capital_gain.go`
- Create: `stock-service/internal/model/tax_collection.go`

- [ ] **Step 1: Write CapitalGain model**

```go
package model

import (
	"time"

	"github.com/shopspring/decimal"
)

// CapitalGain records a realized gain or loss from a sell transaction.
// Used for computing monthly capital gains tax (15%).
type CapitalGain struct {
	ID                 uint64          `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID             uint64          `gorm:"not null;index:idx_cg_user_month" json:"user_id"`
	SystemType         string          `gorm:"size:10;not null" json:"system_type"`
	OrderTransactionID uint64          `gorm:"not null" json:"order_transaction_id"` // 0 for OTC
	OTC                bool            `gorm:"not null;default:false" json:"otc"`
	SecurityType       string          `gorm:"size:10;not null" json:"security_type"`
	Ticker             string          `gorm:"size:30;not null" json:"ticker"`
	Quantity           int64           `gorm:"not null" json:"quantity"`
	BuyPricePerUnit    decimal.Decimal `gorm:"type:numeric(18,4);not null" json:"buy_price_per_unit"`
	SellPricePerUnit   decimal.Decimal `gorm:"type:numeric(18,4);not null" json:"sell_price_per_unit"`
	TotalGain          decimal.Decimal `gorm:"type:numeric(18,4);not null" json:"total_gain"` // can be negative
	Currency           string          `gorm:"size:3;not null" json:"currency"`
	AccountID          uint64          `gorm:"not null" json:"account_id"`
	TaxYear            int             `gorm:"not null;index:idx_cg_user_month" json:"tax_year"`
	TaxMonth           int             `gorm:"not null;index:idx_cg_user_month" json:"tax_month"`
	CreatedAt          time.Time       `json:"created_at"`
}
```

- [ ] **Step 2: Write TaxCollection model**

```go
package model

import (
	"time"

	"github.com/shopspring/decimal"
)

// TaxCollection records a tax collection event for a user for a given month.
// One record per user per month per account.
type TaxCollection struct {
	ID              uint64          `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID          uint64          `gorm:"not null;index:idx_tax_user_year" json:"user_id"`
	SystemType      string          `gorm:"size:10;not null" json:"system_type"`
	Year            int             `gorm:"not null;index:idx_tax_user_year" json:"year"`
	Month           int             `gorm:"not null" json:"month"`
	AccountID       uint64          `gorm:"not null" json:"account_id"`
	Currency        string          `gorm:"size:3;not null" json:"currency"`
	TotalGain       decimal.Decimal `gorm:"type:numeric(18,4);not null" json:"total_gain"`
	TaxAmount       decimal.Decimal `gorm:"type:numeric(18,4);not null" json:"tax_amount"` // in original currency
	TaxAmountRSD    decimal.Decimal `gorm:"type:numeric(18,4);not null" json:"tax_amount_rsd"`
	CollectedAt     time.Time       `gorm:"not null" json:"collected_at"`
	CreatedAt       time.Time       `json:"created_at"`
}
```

- [ ] **Step 3: Commit**

```bash
git add stock-service/internal/model/capital_gain.go stock-service/internal/model/tax_collection.go
git commit -m "feat(stock-service): add CapitalGain and TaxCollection models for tax tracking"
```

---

## Task 3: Add repository interfaces

**Files:**
- Modify: `stock-service/internal/service/interfaces.go`

- [ ] **Step 1: Add holding, capital gain, and tax collection interfaces**

Append to `stock-service/internal/service/interfaces.go`:

```go
// --- Portfolio ---

type HoldingRepo interface {
	Upsert(holding *model.Holding) error // INSERT or UPDATE (weighted average)
	GetByID(id uint64) (*model.Holding, error)
	Update(holding *model.Holding) error
	Delete(id uint64) error
	GetByUserAndSecurity(userID uint64, securityType string, securityID uint64, accountID uint64) (*model.Holding, error)
	ListByUser(userID uint64, filter HoldingFilter) ([]model.Holding, int64, error)
	ListPublicOffers(filter OTCFilter) ([]model.Holding, int64, error)
}

type HoldingFilter struct {
	SecurityType string
	Page         int
	PageSize     int
}

type OTCFilter struct {
	SecurityType string
	Ticker       string
	Page         int
	PageSize     int
}

// --- Tax ---

type CapitalGainRepo interface {
	Create(gain *model.CapitalGain) error
	SumByUserMonth(userID uint64, year, month int) ([]AccountGainSummary, error) // grouped by account_id, currency
	SumByUserYear(userID uint64, year int) ([]AccountGainSummary, error)
}

type AccountGainSummary struct {
	AccountID uint64
	Currency  string
	TotalGain decimal.Decimal
}

type TaxCollectionRepo interface {
	Create(collection *model.TaxCollection) error
	SumByUserYear(userID uint64, year int) (decimal.Decimal, error) // total RSD collected
	SumByUserMonth(userID uint64, year, month int) (decimal.Decimal, error)
	GetLastCollection(userID uint64) (*model.TaxCollection, error)
	ListUsersWithGains(year, month int, filter TaxFilter) ([]TaxUserSummary, int64, error)
}

type TaxFilter struct {
	UserType string // "client", "actuary"
	Search   string
	Page     int
	PageSize int
}

type TaxUserSummary struct {
	UserID         uint64
	SystemType     string
	UserFirstName  string
	UserLastName   string
	TotalDebtRSD   decimal.Decimal
	LastCollection *time.Time
}

// --- Fill Handler (for order execution integration) ---

type FillHandler interface {
	ProcessBuyFill(order *model.Order, txn *model.OrderTransaction) error
	ProcessSellFill(order *model.Order, txn *model.OrderTransaction) error
}

// --- Name Resolver (for user name lookup) ---

type UserNameResolver func(userID uint64, systemType string) (firstName, lastName string, err error)
```

Add the necessary imports at the top (`time`, `decimal`):

```go
import (
	"time"

	"github.com/shopspring/decimal"
	"github.com/exbanka/stock-service/internal/model"
)
```

- [ ] **Step 2: Commit**

```bash
git add stock-service/internal/service/interfaces.go
git commit -m "feat(stock-service): add Holding, CapitalGain, TaxCollection repo interfaces"
```

---

## Task 4: Implement HoldingRepository

**Files:**
- Create: `stock-service/internal/repository/holding_repository.go`

- [ ] **Step 1: Write the repository**

```go
package repository

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/exbanka/stock-service/internal/model"
	"github.com/exbanka/stock-service/internal/service"
	"github.com/shopspring/decimal"
)

type HoldingRepository struct {
	db *gorm.DB
}

func NewHoldingRepository(db *gorm.DB) *HoldingRepository {
	return &HoldingRepository{db: db}
}

// Upsert creates a new holding or updates an existing one with weighted average price.
// Uses SELECT FOR UPDATE to prevent race conditions on concurrent fills.
func (r *HoldingRepository) Upsert(holding *model.Holding) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var existing model.Holding
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND security_type = ? AND security_id = ? AND account_id = ?",
				holding.UserID, holding.SecurityType, holding.SecurityID, holding.AccountID).
			First(&existing).Error

		if err == gorm.ErrRecordNotFound {
			return tx.Create(holding).Error
		}
		if err != nil {
			return err
		}

		// Weighted average price
		oldTotal := existing.AveragePrice.Mul(decimal.NewFromInt(existing.Quantity))
		newTotal := holding.AveragePrice.Mul(decimal.NewFromInt(holding.Quantity))
		totalQty := existing.Quantity + holding.Quantity

		if totalQty > 0 {
			existing.AveragePrice = oldTotal.Add(newTotal).Div(decimal.NewFromInt(totalQty))
		}
		existing.Quantity = totalQty
		existing.ListingID = holding.ListingID
		existing.Ticker = holding.Ticker
		existing.Name = holding.Name

		result := tx.Save(&existing)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrOptimisticLock
		}

		*holding = existing
		return nil
	})
}

func (r *HoldingRepository) GetByID(id uint64) (*model.Holding, error) {
	var holding model.Holding
	if err := r.db.First(&holding, id).Error; err != nil {
		return nil, err
	}
	return &holding, nil
}

func (r *HoldingRepository) Update(holding *model.Holding) error {
	result := r.db.Save(holding)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrOptimisticLock
	}
	return nil
}

func (r *HoldingRepository) Delete(id uint64) error {
	return r.db.Delete(&model.Holding{}, id).Error
}

func (r *HoldingRepository) GetByUserAndSecurity(userID uint64, securityType string, securityID uint64, accountID uint64) (*model.Holding, error) {
	var holding model.Holding
	err := r.db.Where("user_id = ? AND security_type = ? AND security_id = ? AND account_id = ?",
		userID, securityType, securityID, accountID).
		First(&holding).Error
	if err != nil {
		return nil, err
	}
	return &holding, nil
}

func (r *HoldingRepository) ListByUser(userID uint64, filter service.HoldingFilter) ([]model.Holding, int64, error) {
	var holdings []model.Holding
	var total int64

	q := r.db.Model(&model.Holding{}).Where("user_id = ? AND quantity > 0", userID)
	if filter.SecurityType != "" {
		q = q.Where("security_type = ?", filter.SecurityType)
	}

	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	q = applyPagination(q, filter.Page, filter.PageSize)

	if err := q.Order("updated_at DESC").Find(&holdings).Error; err != nil {
		return nil, 0, err
	}
	return holdings, total, nil
}

// ListPublicOffers returns holdings with public_quantity > 0 (for OTC).
func (r *HoldingRepository) ListPublicOffers(filter service.OTCFilter) ([]model.Holding, int64, error) {
	var holdings []model.Holding
	var total int64

	q := r.db.Model(&model.Holding{}).Where("public_quantity > 0 AND security_type = 'stock'")
	if filter.SecurityType != "" {
		q = q.Where("security_type = ?", filter.SecurityType)
	}
	if filter.Ticker != "" {
		q = q.Where("ticker ILIKE ?", "%"+filter.Ticker+"%")
	}

	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	q = applyPagination(q, filter.Page, filter.PageSize)

	if err := q.Order("updated_at DESC").Find(&holdings).Error; err != nil {
		return nil, 0, err
	}
	return holdings, total, nil
}
```

- [ ] **Step 2: Commit**

```bash
git add stock-service/internal/repository/holding_repository.go
git commit -m "feat(stock-service): add HoldingRepository with upsert and OTC queries"
```

---

## Task 5: Implement CapitalGain and TaxCollection repositories

**Files:**
- Create: `stock-service/internal/repository/capital_gain_repository.go`
- Create: `stock-service/internal/repository/tax_collection_repository.go`

- [ ] **Step 1: Write CapitalGainRepository**

```go
package repository

import (
	"gorm.io/gorm"

	"github.com/exbanka/stock-service/internal/model"
	"github.com/exbanka/stock-service/internal/service"
	"github.com/shopspring/decimal"
)

type CapitalGainRepository struct {
	db *gorm.DB
}

func NewCapitalGainRepository(db *gorm.DB) *CapitalGainRepository {
	return &CapitalGainRepository{db: db}
}

func (r *CapitalGainRepository) Create(gain *model.CapitalGain) error {
	return r.db.Create(gain).Error
}

// SumByUserMonth returns capital gains grouped by (account_id, currency) for a month.
func (r *CapitalGainRepository) SumByUserMonth(userID uint64, year, month int) ([]service.AccountGainSummary, error) {
	var results []service.AccountGainSummary
	err := r.db.Model(&model.CapitalGain{}).
		Select("account_id, currency, SUM(total_gain) as total_gain").
		Where("user_id = ? AND tax_year = ? AND tax_month = ?", userID, year, month).
		Group("account_id, currency").
		Find(&results).Error
	return results, err
}

// SumByUserYear returns capital gains grouped by (account_id, currency) for a year.
func (r *CapitalGainRepository) SumByUserYear(userID uint64, year int) ([]service.AccountGainSummary, error) {
	var results []service.AccountGainSummary
	err := r.db.Model(&model.CapitalGain{}).
		Select("account_id, currency, SUM(total_gain) as total_gain").
		Where("user_id = ? AND tax_year = ?", userID, year).
		Group("account_id, currency").
		Find(&results).Error
	return results, err
}
```

- [ ] **Step 2: Write TaxCollectionRepository**

```go
package repository

import (
	"time"

	"gorm.io/gorm"

	"github.com/exbanka/stock-service/internal/model"
	"github.com/exbanka/stock-service/internal/service"
	"github.com/shopspring/decimal"
)

type TaxCollectionRepository struct {
	db *gorm.DB
}

func NewTaxCollectionRepository(db *gorm.DB) *TaxCollectionRepository {
	return &TaxCollectionRepository{db: db}
}

func (r *TaxCollectionRepository) Create(collection *model.TaxCollection) error {
	return r.db.Create(collection).Error
}

// SumByUserYear returns total RSD tax collected for a user in a given year.
func (r *TaxCollectionRepository) SumByUserYear(userID uint64, year int) (decimal.Decimal, error) {
	var result struct {
		Total decimal.Decimal
	}
	err := r.db.Model(&model.TaxCollection{}).
		Select("COALESCE(SUM(tax_amount_rsd), 0) as total").
		Where("user_id = ? AND year = ?", userID, year).
		Scan(&result).Error
	return result.Total, err
}

// SumByUserMonth returns total RSD tax collected for a user in a given month.
func (r *TaxCollectionRepository) SumByUserMonth(userID uint64, year, month int) (decimal.Decimal, error) {
	var result struct {
		Total decimal.Decimal
	}
	err := r.db.Model(&model.TaxCollection{}).
		Select("COALESCE(SUM(tax_amount_rsd), 0) as total").
		Where("user_id = ? AND year = ? AND month = ?", userID, year, month).
		Scan(&result).Error
	return result.Total, err
}

func (r *TaxCollectionRepository) GetLastCollection(userID uint64) (*model.TaxCollection, error) {
	var tc model.TaxCollection
	err := r.db.Where("user_id = ?", userID).
		Order("collected_at DESC").
		First(&tc).Error
	if err != nil {
		return nil, err
	}
	return &tc, nil
}

// ListUsersWithGains returns users who have capital gains in the given month,
// along with their uncollected tax debt in RSD.
func (r *TaxCollectionRepository) ListUsersWithGains(year, month int, filter service.TaxFilter) ([]service.TaxUserSummary, int64, error) {
	// Subquery: sum capital gains per user for the month, join with holdings for name
	// We use a raw query for the complex aggregation
	type rawResult struct {
		UserID        uint64
		SystemType    string
		UserFirstName string
		UserLastName  string
		TotalGainRSD  decimal.Decimal
		LastCollected *time.Time
	}

	var results []rawResult
	var total int64

	// Base query: users with capital gains this month
	baseQuery := r.db.Table("capital_gains cg").
		Select(`
			cg.user_id,
			cg.system_type,
			h.user_first_name,
			h.user_last_name,
			SUM(cg.total_gain) as total_gain_rsd,
			(SELECT MAX(tc.collected_at) FROM tax_collections tc WHERE tc.user_id = cg.user_id) as last_collected
		`).
		Joins("LEFT JOIN holdings h ON h.user_id = cg.user_id AND h.id = (SELECT MIN(id) FROM holdings WHERE user_id = cg.user_id)").
		Where("cg.tax_year = ? AND cg.tax_month = ?", year, month)

	if filter.UserType != "" {
		if filter.UserType == "actuary" {
			baseQuery = baseQuery.Where("cg.system_type = 'employee'")
		} else {
			baseQuery = baseQuery.Where("cg.system_type = ?", filter.UserType)
		}
	}
	if filter.Search != "" {
		baseQuery = baseQuery.Where("(h.user_first_name ILIKE ? OR h.user_last_name ILIKE ?)",
			"%"+filter.Search+"%", "%"+filter.Search+"%")
	}

	baseQuery = baseQuery.Group("cg.user_id, cg.system_type, h.user_first_name, h.user_last_name")

	// Count distinct users
	countQuery := r.db.Table("(?) as sub", baseQuery).Select("COUNT(*)")
	if err := countQuery.Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	// Paginate
	q := baseQuery.Order("total_gain_rsd DESC")
	if filter.PageSize > 0 {
		q = q.Limit(filter.PageSize)
		if filter.Page > 1 {
			q = q.Offset((filter.Page - 1) * filter.PageSize)
		}
	}

	if err := q.Find(&results).Error; err != nil {
		return nil, 0, err
	}

	// Convert to TaxUserSummary
	summaries := make([]service.TaxUserSummary, len(results))
	taxRate := decimal.NewFromFloat(0.15)
	for i, r := range results {
		debt := decimal.Zero
		if r.TotalGainRSD.IsPositive() {
			debt = r.TotalGainRSD.Mul(taxRate).Round(2)
		}
		// Subtract already-collected tax for this month
		// (the caller handles this if needed, or we subtract here)
		summaries[i] = service.TaxUserSummary{
			UserID:         r.UserID,
			SystemType:     r.SystemType,
			UserFirstName:  r.UserFirstName,
			UserLastName:   r.UserLastName,
			TotalDebtRSD:   debt,
			LastCollection: r.LastCollected,
		}
	}

	return summaries, total, nil
}
```

- [ ] **Step 3: Commit**

```bash
git add stock-service/internal/repository/capital_gain_repository.go stock-service/internal/repository/tax_collection_repository.go
git commit -m "feat(stock-service): add CapitalGain and TaxCollection repositories"
```

---

## Task 6: Create PortfolioService

**Files:**
- Create: `stock-service/internal/service/portfolio_service.go`

This service handles holdings management, profit calculation, making shares public, option exercise, and processing order fills (implements `FillHandler` interface).

- [ ] **Step 1: Write the service**

```go
package service

import (
	"context"
	"errors"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	accountpb "github.com/exbanka/contract/accountpb"
	"github.com/exbanka/stock-service/internal/model"
)

type PortfolioService struct {
	holdingRepo    HoldingRepo
	capitalGainRepo CapitalGainRepo
	listingRepo    ListingRepo
	stockRepo      StockRepo
	optionRepo     OptionRepo
	accountClient  accountpb.AccountGRPCServiceClient
	nameResolver   UserNameResolver
	stateAccountNo string
}

func NewPortfolioService(
	holdingRepo HoldingRepo,
	capitalGainRepo CapitalGainRepo,
	listingRepo ListingRepo,
	stockRepo StockRepo,
	optionRepo OptionRepo,
	accountClient accountpb.AccountGRPCServiceClient,
	nameResolver UserNameResolver,
	stateAccountNo string,
) *PortfolioService {
	return &PortfolioService{
		holdingRepo:     holdingRepo,
		capitalGainRepo: capitalGainRepo,
		listingRepo:     listingRepo,
		stockRepo:       stockRepo,
		optionRepo:      optionRepo,
		accountClient:   accountClient,
		nameResolver:    nameResolver,
		stateAccountNo:  stateAccountNo,
	}
}

// ProcessBuyFill handles a buy order fill: creates/updates holding, debits account.
func (s *PortfolioService) ProcessBuyFill(order *model.Order, txn *model.OrderTransaction) error {
	// Look up user name for new holdings
	firstName, lastName := "", ""
	if s.nameResolver != nil {
		fn, ln, err := s.nameResolver(order.UserID, order.SystemType)
		if err == nil {
			firstName, lastName = fn, ln
		}
	}

	// Look up listing for security info
	listing, err := s.listingRepo.GetByID(order.ListingID)
	if err != nil {
		return err
	}

	holding := &model.Holding{
		UserID:        order.UserID,
		SystemType:    order.SystemType,
		UserFirstName: firstName,
		UserLastName:  lastName,
		SecurityType:  order.SecurityType,
		SecurityID:    listing.SecurityID,
		ListingID:     order.ListingID,
		Ticker:        order.Ticker,
		Name:          listing.SecurityName,
		Quantity:      txn.Quantity,
		AveragePrice:  txn.PricePerUnit,
		AccountID:     order.AccountID,
	}

	if err := s.holdingRepo.Upsert(holding); err != nil {
		return err
	}

	// Debit buyer's account: total_price + proportional commission
	proportionalCommission := order.Commission.Mul(
		decimal.NewFromInt(txn.Quantity),
	).Div(decimal.NewFromInt(order.Quantity))
	debitAmount := txn.TotalPrice.Add(proportionalCommission)

	if err := s.debitAccount(order.AccountID, debitAmount); err != nil {
		return err
	}

	// Credit bank account with commission
	if err := s.creditBankCommission(order.AccountID, proportionalCommission); err != nil {
		return err
	}

	return nil
}

// ProcessSellFill handles a sell order fill: decreases holding, credits account, records gain.
func (s *PortfolioService) ProcessSellFill(order *model.Order, txn *model.OrderTransaction) error {
	// Find the holding
	listing, err := s.listingRepo.GetByID(order.ListingID)
	if err != nil {
		return err
	}

	holding, err := s.holdingRepo.GetByUserAndSecurity(
		order.UserID, order.SecurityType, listing.SecurityID, order.AccountID,
	)
	if err != nil {
		return errors.New("holding not found for sell order")
	}

	if holding.Quantity < txn.Quantity {
		return errors.New("insufficient holding quantity for sell")
	}

	// Record capital gain
	gain := txn.PricePerUnit.Sub(holding.AveragePrice).Mul(decimal.NewFromInt(txn.Quantity))
	capitalGain := &model.CapitalGain{
		UserID:             order.UserID,
		SystemType:         order.SystemType,
		OrderTransactionID: txn.ID,
		OTC:                false,
		SecurityType:       order.SecurityType,
		Ticker:             order.Ticker,
		Quantity:           txn.Quantity,
		BuyPricePerUnit:    holding.AveragePrice,
		SellPricePerUnit:   txn.PricePerUnit,
		TotalGain:          gain,
		Currency:           listing.Currency,
		AccountID:          order.AccountID,
		TaxYear:            time.Now().Year(),
		TaxMonth:           int(time.Now().Month()),
	}
	if err := s.capitalGainRepo.Create(capitalGain); err != nil {
		return err
	}

	// Decrease holding
	holding.Quantity -= txn.Quantity
	if holding.PublicQuantity > holding.Quantity {
		holding.PublicQuantity = holding.Quantity
	}

	if holding.Quantity == 0 {
		if err := s.holdingRepo.Delete(holding.ID); err != nil {
			return err
		}
	} else {
		if err := s.holdingRepo.Update(holding); err != nil {
			return err
		}
	}

	// Credit seller's account: total_price - proportional commission
	proportionalCommission := order.Commission.Mul(
		decimal.NewFromInt(txn.Quantity),
	).Div(decimal.NewFromInt(order.Quantity))
	creditAmount := txn.TotalPrice.Sub(proportionalCommission)

	if err := s.creditAccount(order.AccountID, creditAmount); err != nil {
		return err
	}

	// Credit bank with commission
	if err := s.creditBankCommission(order.AccountID, proportionalCommission); err != nil {
		return err
	}

	return nil
}

// ListHoldings returns a user's holdings with computed profit.
func (s *PortfolioService) ListHoldings(userID uint64, filter HoldingFilter) ([]model.Holding, int64, error) {
	return s.holdingRepo.ListByUser(userID, filter)
}

// GetCurrentPrice retrieves current listing price for a holding.
func (s *PortfolioService) GetCurrentPrice(listingID uint64) (decimal.Decimal, error) {
	listing, err := s.listingRepo.GetByID(listingID)
	if err != nil {
		return decimal.Zero, err
	}
	return listing.Price, nil
}

// MakePublic sets a number of shares as publicly available for OTC trading.
func (s *PortfolioService) MakePublic(holdingID, userID uint64, quantity int64) (*model.Holding, error) {
	holding, err := s.holdingRepo.GetByID(holdingID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("holding not found")
		}
		return nil, err
	}
	if holding.UserID != userID {
		return nil, errors.New("holding does not belong to user")
	}
	if holding.SecurityType != "stock" {
		return nil, errors.New("only stocks can be made public for OTC trading")
	}
	if quantity < 0 || quantity > holding.Quantity {
		return nil, errors.New("invalid public quantity")
	}

	holding.PublicQuantity = quantity

	if err := s.holdingRepo.Update(holding); err != nil {
		return nil, err
	}
	return holding, nil
}

// ExerciseOption exercises an option holding if it's in the money and not expired.
func (s *PortfolioService) ExerciseOption(holdingID, userID uint64) (*ExerciseResult, error) {
	holding, err := s.holdingRepo.GetByID(holdingID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("holding not found")
		}
		return nil, err
	}
	if holding.UserID != userID {
		return nil, errors.New("holding does not belong to user")
	}
	if holding.SecurityType != "option" {
		return nil, errors.New("holding is not an option")
	}

	// Look up the option to get strike, type, settlement, stock info
	option, err := s.optionRepo.GetByID(holding.SecurityID)
	if err != nil {
		return nil, errors.New("option not found")
	}

	// Check settlement date hasn't passed
	if time.Now().After(option.SettlementDate) {
		return nil, errors.New("option has expired (settlement date passed)")
	}

	// Look up current stock price via listing
	stockListing, err := s.listingRepo.GetBySecurityAndType(option.StockID, "stock")
	if err != nil {
		return nil, errors.New("stock listing not found for option's underlying")
	}

	sharesAffected := holding.Quantity * 100 // 1 option = 100 shares
	var profit decimal.Decimal

	if option.OptionType == "call" {
		// CALL: stock price must be > strike price
		if stockListing.Price.LessThanOrEqual(option.StrikePrice) {
			return nil, errors.New("call option is not in the money")
		}
		profit = stockListing.Price.Sub(option.StrikePrice).Mul(decimal.NewFromInt(sharesAffected))

		// Debit account by strike × shares (buying stock at strike price)
		debitAmount := option.StrikePrice.Mul(decimal.NewFromInt(sharesAffected))
		if err := s.debitAccount(holding.AccountID, debitAmount); err != nil {
			return nil, err
		}

		// Create/update stock holding at strike price
		stock, err := s.stockRepo.GetByID(option.StockID)
		if err != nil {
			return nil, err
		}
		stockHolding := &model.Holding{
			UserID:        userID,
			SystemType:    holding.SystemType,
			UserFirstName: holding.UserFirstName,
			UserLastName:  holding.UserLastName,
			SecurityType:  "stock",
			SecurityID:    option.StockID,
			ListingID:     stockListing.ID,
			Ticker:        stock.Ticker,
			Name:          stock.Name,
			Quantity:      sharesAffected,
			AveragePrice:  option.StrikePrice,
			AccountID:     holding.AccountID,
		}
		if err := s.holdingRepo.Upsert(stockHolding); err != nil {
			return nil, err
		}

	} else { // "put"
		// PUT: stock price must be < strike price
		if stockListing.Price.GreaterThanOrEqual(option.StrikePrice) {
			return nil, errors.New("put option is not in the money")
		}
		profit = option.StrikePrice.Sub(stockListing.Price).Mul(decimal.NewFromInt(sharesAffected))

		// User must hold enough stock
		stock, err := s.stockRepo.GetByID(option.StockID)
		if err != nil {
			return nil, err
		}
		stockHolding, err := s.holdingRepo.GetByUserAndSecurity(userID, "stock", option.StockID, holding.AccountID)
		if err != nil || stockHolding.Quantity < sharesAffected {
			return nil, errors.New("insufficient stock holdings to exercise put option")
		}

		// Credit account by strike × shares (selling stock at strike price)
		creditAmount := option.StrikePrice.Mul(decimal.NewFromInt(sharesAffected))
		if err := s.creditAccount(holding.AccountID, creditAmount); err != nil {
			return nil, err
		}

		// Decrease stock holding
		stockHolding.Quantity -= sharesAffected
		if stockHolding.PublicQuantity > stockHolding.Quantity {
			stockHolding.PublicQuantity = stockHolding.Quantity
		}

		if stockHolding.Quantity == 0 {
			if err := s.holdingRepo.Delete(stockHolding.ID); err != nil {
				return nil, err
			}
		} else {
			if err := s.holdingRepo.Update(stockHolding); err != nil {
				return nil, err
			}
		}

		// Record capital gain for the stock sale
		gain := option.StrikePrice.Sub(stockHolding.AveragePrice).Mul(decimal.NewFromInt(sharesAffected))
		capitalGain := &model.CapitalGain{
			UserID:       userID,
			SystemType:   holding.SystemType,
			SecurityType: "stock",
			Ticker:       stock.Ticker,
			Quantity:     sharesAffected,
			BuyPricePerUnit:  stockHolding.AveragePrice,
			SellPricePerUnit: option.StrikePrice,
			TotalGain:        gain,
			Currency:         stockListing.Currency,
			AccountID:        holding.AccountID,
			TaxYear:          time.Now().Year(),
			TaxMonth:         int(time.Now().Month()),
		}
		if err := s.capitalGainRepo.Create(capitalGain); err != nil {
			return nil, err
		}
	}

	// Delete option holding
	if err := s.holdingRepo.Delete(holdingID); err != nil {
		return nil, err
	}

	return &ExerciseResult{
		ID:                holdingID,
		OptionTicker:      holding.Ticker,
		ExercisedQuantity: holding.Quantity,
		SharesAffected:    sharesAffected,
		Profit:            profit,
	}, nil
}

// --- Account helpers ---

func (s *PortfolioService) debitAccount(accountID uint64, amount decimal.Decimal) error {
	// Look up account number
	acctResp, err := s.accountClient.GetAccount(context.Background(), &accountpb.GetAccountRequest{Id: accountID})
	if err != nil {
		return err
	}
	_, err = s.accountClient.UpdateBalance(context.Background(), &accountpb.UpdateBalanceRequest{
		AccountNumber:   acctResp.AccountNumber,
		Amount:          amount.Neg().StringFixed(4), // negative for debit
		UpdateAvailable: true,
	})
	return err
}

func (s *PortfolioService) creditAccount(accountID uint64, amount decimal.Decimal) error {
	acctResp, err := s.accountClient.GetAccount(context.Background(), &accountpb.GetAccountRequest{Id: accountID})
	if err != nil {
		return err
	}
	_, err = s.accountClient.UpdateBalance(context.Background(), &accountpb.UpdateBalanceRequest{
		AccountNumber:   acctResp.AccountNumber,
		Amount:          amount.StringFixed(4), // positive for credit
		UpdateAvailable: true,
	})
	return err
}

func (s *PortfolioService) creditBankCommission(sourceAccountID uint64, commission decimal.Decimal) error {
	if commission.IsZero() {
		return nil
	}
	// Look up source account to get currency, then find bank's account in that currency
	// For simplicity, credit the bank's RSD account (commission is small)
	// If the bank has a same-currency account, use that instead
	_, err := s.accountClient.UpdateBalance(context.Background(), &accountpb.UpdateBalanceRequest{
		AccountNumber:   s.stateAccountNo, // reuse state account for simplicity; in production use bank account
		Amount:          commission.StringFixed(4),
		UpdateAvailable: true,
	})
	return err
}

// --- Types ---

type ExerciseResult struct {
	ID                uint64
	OptionTicker      string
	ExercisedQuantity int64
	SharesAffected    int64
	Profit            decimal.Decimal
}
```

- [ ] **Step 2: Commit**

```bash
git add stock-service/internal/service/portfolio_service.go
git commit -m "feat(stock-service): add PortfolioService with fill processing, OTC, and option exercise"
```

---

## Task 7: Replace OTCService with full implementation

**Files:**
- Modify: `stock-service/internal/service/otc_service.go` (replace entire file)

- [ ] **Step 1: Replace the stub with full implementation**

```go
package service

import (
	"context"
	"errors"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	accountpb "github.com/exbanka/contract/accountpb"
	"github.com/exbanka/stock-service/internal/model"
)

// OTCService handles over-the-counter trading.
// OTC offers are Holding records with public_quantity > 0.
type OTCService struct {
	holdingRepo     HoldingRepo
	capitalGainRepo CapitalGainRepo
	listingRepo     ListingRepo
	accountClient   accountpb.AccountGRPCServiceClient
	nameResolver    UserNameResolver
}

func NewOTCService(
	holdingRepo HoldingRepo,
	capitalGainRepo CapitalGainRepo,
	listingRepo ListingRepo,
	accountClient accountpb.AccountGRPCServiceClient,
	nameResolver UserNameResolver,
) *OTCService {
	return &OTCService{
		holdingRepo:     holdingRepo,
		capitalGainRepo: capitalGainRepo,
		listingRepo:     listingRepo,
		accountClient:   accountClient,
		nameResolver:    nameResolver,
	}
}

// ListOffers returns public stock holdings available for OTC purchase.
func (s *OTCService) ListOffers(filter OTCFilter) ([]model.Holding, int64, error) {
	return s.holdingRepo.ListPublicOffers(filter)
}

// BuyOffer purchases shares from an OTC offer (a public holding).
func (s *OTCService) BuyOffer(
	offerID uint64, // holding ID of the seller
	buyerID uint64,
	buyerSystemType string,
	quantity int64,
	buyerAccountID uint64,
) (*OTCBuyResult, error) {
	// Get seller's holding
	sellerHolding, err := s.holdingRepo.GetByID(offerID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("OTC offer not found")
		}
		return nil, err
	}
	if sellerHolding.PublicQuantity < quantity {
		return nil, errors.New("insufficient public quantity for OTC purchase")
	}
	if sellerHolding.UserID == buyerID {
		return nil, errors.New("cannot buy your own OTC offer")
	}

	// Get current market price from listing
	listing, err := s.listingRepo.GetByID(sellerHolding.ListingID)
	if err != nil {
		return nil, errors.New("listing not found for OTC offer")
	}

	pricePerUnit := listing.Price
	totalPrice := pricePerUnit.Mul(decimal.NewFromInt(quantity))

	// Commission: same as Market order = min(14% × total, $7)
	commission := totalPrice.Mul(decimal.NewFromFloat(0.14))
	cap := decimal.NewFromFloat(7)
	if commission.GreaterThan(cap) {
		commission = cap
	}
	commission = commission.Round(2)

	// Debit buyer's account: total + commission
	buyerAcct, err := s.accountClient.GetAccount(context.Background(), &accountpb.GetAccountRequest{Id: buyerAccountID})
	if err != nil {
		return nil, errors.New("buyer account not found")
	}
	_, err = s.accountClient.UpdateBalance(context.Background(), &accountpb.UpdateBalanceRequest{
		AccountNumber:   buyerAcct.AccountNumber,
		Amount:          totalPrice.Add(commission).Neg().StringFixed(4),
		UpdateAvailable: true,
	})
	if err != nil {
		return nil, errors.New("failed to debit buyer account: " + err.Error())
	}

	// Credit seller's account: total
	sellerAcct, err := s.accountClient.GetAccount(context.Background(), &accountpb.GetAccountRequest{Id: sellerHolding.AccountID})
	if err != nil {
		return nil, errors.New("seller account not found")
	}
	_, err = s.accountClient.UpdateBalance(context.Background(), &accountpb.UpdateBalanceRequest{
		AccountNumber:   sellerAcct.AccountNumber,
		Amount:          totalPrice.StringFixed(4),
		UpdateAvailable: true,
	})
	if err != nil {
		return nil, errors.New("failed to credit seller account: " + err.Error())
	}

	// Record capital gain for seller
	gain := pricePerUnit.Sub(sellerHolding.AveragePrice).Mul(decimal.NewFromInt(quantity))
	capitalGain := &model.CapitalGain{
		UserID:           sellerHolding.UserID,
		SystemType:       sellerHolding.SystemType,
		OTC:              true,
		SecurityType:     sellerHolding.SecurityType,
		Ticker:           sellerHolding.Ticker,
		Quantity:         quantity,
		BuyPricePerUnit:  sellerHolding.AveragePrice,
		SellPricePerUnit: pricePerUnit,
		TotalGain:        gain,
		Currency:         listing.Currency,
		AccountID:        sellerHolding.AccountID,
		TaxYear:          time.Now().Year(),
		TaxMonth:         int(time.Now().Month()),
	}
	if err := s.capitalGainRepo.Create(capitalGain); err != nil {
		return nil, err
	}

	// Decrease seller's holding
	sellerHolding.Quantity -= quantity
	sellerHolding.PublicQuantity -= quantity
	if sellerHolding.Quantity == 0 {
		if err := s.holdingRepo.Delete(sellerHolding.ID); err != nil {
			return nil, err
		}
	} else {
		if err := s.holdingRepo.Update(sellerHolding); err != nil {
			return nil, err
		}
	}

	// Create/update buyer's holding
	buyerFirstName, buyerLastName := "", ""
	if s.nameResolver != nil {
		fn, ln, resolveErr := s.nameResolver(buyerID, buyerSystemType)
		if resolveErr == nil {
			buyerFirstName, buyerLastName = fn, ln
		}
	}

	buyerHolding := &model.Holding{
		UserID:        buyerID,
		SystemType:    buyerSystemType,
		UserFirstName: buyerFirstName,
		UserLastName:  buyerLastName,
		SecurityType:  sellerHolding.SecurityType,
		SecurityID:    listing.SecurityID,
		ListingID:     sellerHolding.ListingID,
		Ticker:        sellerHolding.Ticker,
		Name:          sellerHolding.Name,
		Quantity:      quantity,
		AveragePrice:  pricePerUnit,
		AccountID:     buyerAccountID,
	}
	if err := s.holdingRepo.Upsert(buyerHolding); err != nil {
		return nil, err
	}

	return &OTCBuyResult{
		ID:           buyerHolding.ID,
		OfferID:      offerID,
		Quantity:     quantity,
		PricePerUnit: pricePerUnit,
		TotalPrice:   totalPrice,
		Commission:   commission,
	}, nil
}

type OTCBuyResult struct {
	ID           uint64
	OfferID      uint64
	Quantity     int64
	PricePerUnit decimal.Decimal
	TotalPrice   decimal.Decimal
	Commission   decimal.Decimal
}
```

- [ ] **Step 2: Commit**

```bash
git add stock-service/internal/service/otc_service.go
git commit -m "feat(stock-service): replace OTC stub with full implementation"
```

---

## Task 8: Create TaxService

**Files:**
- Create: `stock-service/internal/service/tax_service.go`

- [ ] **Step 1: Write the service**

```go
package service

import (
	"context"
	"log"
	"time"

	"github.com/shopspring/decimal"

	accountpb "github.com/exbanka/contract/accountpb"
	exchangepb "github.com/exbanka/contract/exchangepb"
	"github.com/exbanka/stock-service/internal/model"
)

type TaxService struct {
	capitalGainRepo   CapitalGainRepo
	taxCollectionRepo TaxCollectionRepo
	holdingRepo       HoldingRepo
	accountClient     accountpb.AccountGRPCServiceClient
	exchangeClient    exchangepb.ExchangeGRPCServiceClient
	stateAccountNo    string
}

func NewTaxService(
	capitalGainRepo CapitalGainRepo,
	taxCollectionRepo TaxCollectionRepo,
	holdingRepo HoldingRepo,
	accountClient accountpb.AccountGRPCServiceClient,
	exchangeClient exchangepb.ExchangeGRPCServiceClient,
	stateAccountNo string,
) *TaxService {
	return &TaxService{
		capitalGainRepo:   capitalGainRepo,
		taxCollectionRepo: taxCollectionRepo,
		holdingRepo:       holdingRepo,
		accountClient:     accountClient,
		exchangeClient:    exchangeClient,
		stateAccountNo:    stateAccountNo,
	}
}

// ListTaxRecords returns users with their tax debt for supervisor portal.
func (s *TaxService) ListTaxRecords(year, month int, filter TaxFilter) ([]TaxUserSummary, int64, error) {
	return s.taxCollectionRepo.ListUsersWithGains(year, month, filter)
}

// GetUserTaxSummary returns tax summary for a user's portfolio page.
func (s *TaxService) GetUserTaxSummary(userID uint64) (taxPaidThisYear, taxUnpaidThisMonth decimal.Decimal, err error) {
	now := time.Now()
	year := now.Year()
	month := int(now.Month())

	// Tax paid this year
	taxPaidThisYear, err = s.taxCollectionRepo.SumByUserYear(userID, year)
	if err != nil {
		return decimal.Zero, decimal.Zero, err
	}

	// Tax owed this month (uncollected)
	gainSummaries, err := s.capitalGainRepo.SumByUserMonth(userID, year, month)
	if err != nil {
		return decimal.Zero, decimal.Zero, err
	}

	// Already collected this month
	collectedThisMonth, err := s.taxCollectionRepo.SumByUserMonth(userID, year, month)
	if err != nil {
		return decimal.Zero, decimal.Zero, err
	}

	// Compute uncollected tax in RSD
	taxRate := decimal.NewFromFloat(0.15)
	totalUnpaidRSD := decimal.Zero
	for _, gs := range gainSummaries {
		if gs.TotalGain.IsPositive() {
			taxInCurrency := gs.TotalGain.Mul(taxRate)
			taxInRSD := taxInCurrency
			if gs.Currency != "RSD" {
				converted, convErr := s.convertToRSD(gs.Currency, taxInCurrency)
				if convErr == nil {
					taxInRSD = converted
				}
			}
			totalUnpaidRSD = totalUnpaidRSD.Add(taxInRSD)
		}
	}
	taxUnpaidThisMonth = totalUnpaidRSD.Sub(collectedThisMonth).Round(2)
	if taxUnpaidThisMonth.IsNegative() {
		taxUnpaidThisMonth = decimal.Zero
	}

	return taxPaidThisYear, taxUnpaidThisMonth, nil
}

// CollectTax collects tax from all users for the given month.
// Returns the number of users collected, total RSD, and failures.
func (s *TaxService) CollectTax(year, month int) (collectedCount int64, totalRSD decimal.Decimal, failedCount int64, err error) {
	// Get all users with gains this month (broad filter, all pages)
	summaries, _, err := s.taxCollectionRepo.ListUsersWithGains(year, month, TaxFilter{
		Page:     1,
		PageSize: 10000, // collect all
	})
	if err != nil {
		return 0, decimal.Zero, 0, err
	}

	taxRate := decimal.NewFromFloat(0.15)

	for _, summary := range summaries {
		if summary.TotalDebtRSD.IsZero() || summary.TotalDebtRSD.IsNegative() {
			continue
		}

		// Get detailed gains per account
		gainSummaries, gainErr := s.capitalGainRepo.SumByUserMonth(summary.UserID, year, month)
		if gainErr != nil {
			failedCount++
			log.Printf("WARN: tax: failed to get gains for user %d: %v", summary.UserID, gainErr)
			continue
		}

		userFailed := false
		for _, gs := range gainSummaries {
			if !gs.TotalGain.IsPositive() {
				continue
			}

			taxInCurrency := gs.TotalGain.Mul(taxRate).Round(4)
			taxInRSD := taxInCurrency

			// Convert to RSD if needed
			if gs.Currency != "RSD" {
				converted, convErr := s.convertToRSD(gs.Currency, taxInCurrency)
				if convErr != nil {
					log.Printf("WARN: tax: currency conversion failed for user %d: %v", summary.UserID, convErr)
					userFailed = true
					continue
				}
				taxInRSD = converted
			}

			// Debit user's account
			acctResp, acctErr := s.accountClient.GetAccount(context.Background(), &accountpb.GetAccountRequest{Id: gs.AccountID})
			if acctErr != nil {
				log.Printf("WARN: tax: account lookup failed for user %d account %d: %v", summary.UserID, gs.AccountID, acctErr)
				userFailed = true
				continue
			}

			_, debitErr := s.accountClient.UpdateBalance(context.Background(), &accountpb.UpdateBalanceRequest{
				AccountNumber:   acctResp.AccountNumber,
				Amount:          taxInCurrency.Neg().StringFixed(4),
				UpdateAvailable: true,
			})
			if debitErr != nil {
				log.Printf("WARN: tax: debit failed for user %d account %s: %v", summary.UserID, acctResp.AccountNumber, debitErr)
				userFailed = true
				continue
			}

			// Credit state's RSD account
			_, creditErr := s.accountClient.UpdateBalance(context.Background(), &accountpb.UpdateBalanceRequest{
				AccountNumber:   s.stateAccountNo,
				Amount:          taxInRSD.StringFixed(4),
				UpdateAvailable: true,
			})
			if creditErr != nil {
				log.Printf("WARN: tax: credit state account failed for user %d: %v", summary.UserID, creditErr)
				// Don't fail entirely — the debit already happened
			}

			// Record collection
			collection := &model.TaxCollection{
				UserID:       summary.UserID,
				SystemType:   summary.SystemType,
				Year:         year,
				Month:        month,
				AccountID:    gs.AccountID,
				Currency:     gs.Currency,
				TotalGain:    gs.TotalGain,
				TaxAmount:    taxInCurrency,
				TaxAmountRSD: taxInRSD,
				CollectedAt:  time.Now(),
			}
			if createErr := s.taxCollectionRepo.Create(collection); createErr != nil {
				log.Printf("WARN: tax: failed to record collection for user %d: %v", summary.UserID, createErr)
			}

			totalRSD = totalRSD.Add(taxInRSD)
		}

		if userFailed {
			failedCount++
		} else {
			collectedCount++
		}
	}

	return collectedCount, totalRSD.Round(2), failedCount, nil
}

// convertToRSD converts an amount from a foreign currency to RSD.
func (s *TaxService) convertToRSD(fromCurrency string, amount decimal.Decimal) (decimal.Decimal, error) {
	resp, err := s.exchangeClient.Convert(context.Background(), &exchangepb.ConvertRequest{
		FromCurrency: fromCurrency,
		ToCurrency:   "RSD",
		Amount:       amount.StringFixed(4),
	})
	if err != nil {
		return decimal.Zero, err
	}
	converted, err := decimal.NewFromString(resp.ConvertedAmount)
	if err != nil {
		return decimal.Zero, err
	}
	return converted, nil
}
```

- [ ] **Step 2: Commit**

```bash
git add stock-service/internal/service/tax_service.go
git commit -m "feat(stock-service): add TaxService with collection, computation, and RSD conversion"
```

---

## Task 9: Create TaxCronService

**Files:**
- Create: `stock-service/internal/service/tax_cron.go`

Monthly automatic tax collection cron that runs at the end of each month.

- [ ] **Step 1: Write the cron service**

```go
package service

import (
	"context"
	"log"
	"time"
)

type TaxCronService struct {
	taxSvc *TaxService
}

func NewTaxCronService(taxSvc *TaxService) *TaxCronService {
	return &TaxCronService{taxSvc: taxSvc}
}

// StartMonthlyCron starts a background goroutine that triggers tax collection
// at the end of each month (last day, 23:59).
func (s *TaxCronService) StartMonthlyCron(ctx context.Context) {
	go func() {
		for {
			now := time.Now()
			// Calculate next month boundary
			nextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location())
			// Trigger 1 minute before month ends
			triggerTime := nextMonth.Add(-1 * time.Minute)

			if triggerTime.Before(now) {
				// Already past trigger time this month, wait for next month
				triggerTime = time.Date(now.Year(), now.Month()+2, 1, 0, 0, 0, 0, now.Location()).Add(-1 * time.Minute)
			}

			waitDuration := triggerTime.Sub(now)
			log.Printf("tax cron: next collection scheduled for %s (in %s)", triggerTime.Format(time.RFC3339), waitDuration)

			select {
			case <-time.After(waitDuration):
				s.runCollection()
			case <-ctx.Done():
				log.Println("tax cron: shutting down")
				return
			}
		}
	}()
}

func (s *TaxCronService) runCollection() {
	now := time.Now()
	year := now.Year()
	month := int(now.Month())

	log.Printf("tax cron: starting monthly collection for %d-%02d", year, month)

	collected, totalRSD, failed, err := s.taxSvc.CollectTax(year, month)
	if err != nil {
		log.Printf("ERROR: tax cron: collection failed: %v", err)
		return
	}

	log.Printf("tax cron: collection complete — collected=%d total_rsd=%s failed=%d",
		collected, totalRSD.StringFixed(2), failed)
}
```

- [ ] **Step 2: Commit**

```bash
git add stock-service/internal/service/tax_cron.go
git commit -m "feat(stock-service): add TaxCronService for automatic monthly tax collection"
```

---

## Task 10: Create PortfolioGRPCService handler

**Files:**
- Create: `stock-service/internal/handler/portfolio_handler.go`

- [ ] **Step 1: Write the handler**

```go
package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/shopspring/decimal"

	pb "github.com/exbanka/contract/stockpb"
	"github.com/exbanka/stock-service/internal/service"
)

type PortfolioHandler struct {
	pb.UnimplementedPortfolioGRPCServiceServer
	portfolioSvc *service.PortfolioService
	taxSvc       *service.TaxService
}

func NewPortfolioHandler(portfolioSvc *service.PortfolioService, taxSvc *service.TaxService) *PortfolioHandler {
	return &PortfolioHandler{portfolioSvc: portfolioSvc, taxSvc: taxSvc}
}

func (h *PortfolioHandler) ListHoldings(ctx context.Context, req *pb.ListHoldingsRequest) (*pb.ListHoldingsResponse, error) {
	filter := service.HoldingFilter{
		SecurityType: req.SecurityType,
		Page:         int(req.Page),
		PageSize:     int(req.PageSize),
	}

	holdings, total, err := h.portfolioSvc.ListHoldings(req.UserId, filter)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	pbHoldings := make([]*pb.Holding, len(holdings))
	for i, h := range holdings {
		// Compute current price and profit
		currentPrice, _ := h.portfolioSvc.GetCurrentPrice(h.ListingID)
		profit := currentPrice.Sub(h.AveragePrice).Mul(decimal.NewFromInt(h.Quantity))

		pbHoldings[i] = &pb.Holding{
			Id:             h.ID,
			SecurityType:   h.SecurityType,
			Ticker:         h.Ticker,
			Name:           h.Name,
			Quantity:       h.Quantity,
			AveragePrice:   h.AveragePrice.StringFixed(2),
			CurrentPrice:   currentPrice.StringFixed(2),
			Profit:         profit.StringFixed(2),
			PublicQuantity: h.PublicQuantity,
			AccountId:      h.AccountID,
			LastModified:   h.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
	}

	return &pb.ListHoldingsResponse{
		Holdings:   pbHoldings,
		TotalCount: total,
	}, nil
}

func (h *PortfolioHandler) GetPortfolioSummary(ctx context.Context, req *pb.GetPortfolioSummaryRequest) (*pb.PortfolioSummary, error) {
	// Compute total unrealized profit across all holdings
	allHoldings, _, err := h.portfolioSvc.ListHoldings(req.UserId, service.HoldingFilter{
		Page: 1, PageSize: 10000,
	})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	totalProfit := decimal.Zero
	for _, holding := range allHoldings {
		currentPrice, priceErr := h.portfolioSvc.GetCurrentPrice(holding.ListingID)
		if priceErr != nil {
			continue
		}
		profit := currentPrice.Sub(holding.AveragePrice).Mul(decimal.NewFromInt(holding.Quantity))
		totalProfit = totalProfit.Add(profit)
	}

	// Get tax info
	taxPaidYear, taxUnpaidMonth, err := h.taxSvc.GetUserTaxSummary(req.UserId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &pb.PortfolioSummary{
		TotalProfit:        totalProfit.StringFixed(2),
		TotalProfitRsd:     totalProfit.StringFixed(2), // TODO: convert via exchange-service if multi-currency
		TaxPaidThisYear:    taxPaidYear.StringFixed(2),
		TaxUnpaidThisMonth: taxUnpaidMonth.StringFixed(2),
	}, nil
}

func (h *PortfolioHandler) MakePublic(ctx context.Context, req *pb.MakePublicRequest) (*pb.Holding, error) {
	holding, err := h.portfolioSvc.MakePublic(req.HoldingId, req.UserId, req.Quantity)
	if err != nil {
		return nil, mapPortfolioError(err)
	}

	return &pb.Holding{
		Id:             holding.ID,
		SecurityType:   holding.SecurityType,
		Ticker:         holding.Ticker,
		Name:           holding.Name,
		Quantity:       holding.Quantity,
		AveragePrice:   holding.AveragePrice.StringFixed(2),
		PublicQuantity: holding.PublicQuantity,
		AccountId:      holding.AccountID,
		LastModified:   holding.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}, nil
}

func (h *PortfolioHandler) ExerciseOption(ctx context.Context, req *pb.ExerciseOptionRequest) (*pb.ExerciseResult, error) {
	result, err := h.portfolioSvc.ExerciseOption(req.HoldingId, req.UserId)
	if err != nil {
		return nil, mapPortfolioError(err)
	}

	return &pb.ExerciseResult{
		Id:                result.ID,
		OptionTicker:      result.OptionTicker,
		ExercisedQuantity: result.ExercisedQuantity,
		SharesAffected:    result.SharesAffected,
		Profit:            result.Profit.StringFixed(2),
	}, nil
}

func mapPortfolioError(err error) error {
	switch err.Error() {
	case "holding not found", "option not found", "stock listing not found for option's underlying":
		return status.Error(codes.NotFound, err.Error())
	case "holding does not belong to user":
		return status.Error(codes.PermissionDenied, err.Error())
	case "only stocks can be made public for OTC trading",
		"invalid public quantity",
		"holding is not an option",
		"option has expired (settlement date passed)",
		"call option is not in the money",
		"put option is not in the money",
		"insufficient stock holdings to exercise put option":
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
```

**Note:** The `h.portfolioSvc` reference inside `ListHoldings` loop uses the outer handler's `h` — rename the loop variable to avoid shadowing:

```go
	for i, hld := range holdings {
		currentPrice, _ := h.portfolioSvc.GetCurrentPrice(hld.ListingID)
		profit := currentPrice.Sub(hld.AveragePrice).Mul(decimal.NewFromInt(hld.Quantity))

		pbHoldings[i] = &pb.Holding{
			Id:             hld.ID,
			SecurityType:   hld.SecurityType,
			Ticker:         hld.Ticker,
			Name:           hld.Name,
			Quantity:       hld.Quantity,
			AveragePrice:   hld.AveragePrice.StringFixed(2),
			CurrentPrice:   currentPrice.StringFixed(2),
			Profit:         profit.StringFixed(2),
			PublicQuantity: hld.PublicQuantity,
			AccountId:      hld.AccountID,
			LastModified:   hld.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
	}
```

Use the corrected loop variable `hld` in the actual implementation.

- [ ] **Step 2: Commit**

```bash
git add stock-service/internal/handler/portfolio_handler.go
git commit -m "feat(stock-service): add PortfolioGRPCService handler with holdings, summary, exercise"
```

---

## Task 11: Replace OTCGRPCService handler

**Files:**
- Modify: `stock-service/internal/handler/otc_handler.go` (replace entire file)

- [ ] **Step 1: Replace stub with full implementation**

```go
package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/exbanka/contract/stockpb"
	"github.com/exbanka/stock-service/internal/service"
)

type OTCHandler struct {
	pb.UnimplementedOTCGRPCServiceServer
	otcSvc *service.OTCService
}

func NewOTCHandler(otcSvc *service.OTCService) *OTCHandler {
	return &OTCHandler{otcSvc: otcSvc}
}

func (h *OTCHandler) ListOffers(ctx context.Context, req *pb.ListOTCOffersRequest) (*pb.ListOTCOffersResponse, error) {
	filter := service.OTCFilter{
		SecurityType: req.SecurityType,
		Ticker:       req.Ticker,
		Page:         int(req.Page),
		PageSize:     int(req.PageSize),
	}

	holdings, total, err := h.otcSvc.ListOffers(filter)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	offers := make([]*pb.OTCOffer, len(holdings))
	for i, h := range holdings {
		offers[i] = &pb.OTCOffer{
			Id:           h.ID,
			SellerId:     h.UserID,
			SellerName:   h.UserFirstName + " " + h.UserLastName,
			SecurityType: h.SecurityType,
			Ticker:       h.Ticker,
			Name:         h.Name,
			Quantity:     h.PublicQuantity,
			PricePerUnit: h.AveragePrice.StringFixed(2), // current price filled by listing in handler
			CreatedAt:    h.CreatedAt.Format("2006-01-02T15:04:05Z"),
		}
	}

	return &pb.ListOTCOffersResponse{
		Offers:     offers,
		TotalCount: total,
	}, nil
}

func (h *OTCHandler) BuyOffer(ctx context.Context, req *pb.BuyOTCOfferRequest) (*pb.OTCTransaction, error) {
	result, err := h.otcSvc.BuyOffer(
		req.OfferId,
		req.BuyerId,
		req.SystemType,
		req.Quantity,
		req.AccountId,
	)
	if err != nil {
		return nil, mapOTCError(err)
	}

	return &pb.OTCTransaction{
		Id:           result.ID,
		OfferId:      result.OfferID,
		Quantity:     result.Quantity,
		PricePerUnit: result.PricePerUnit.StringFixed(2),
		TotalPrice:   result.TotalPrice.StringFixed(2),
		Commission:   result.Commission.StringFixed(2),
	}, nil
}

func mapOTCError(err error) error {
	switch err.Error() {
	case "OTC offer not found":
		return status.Error(codes.NotFound, err.Error())
	case "cannot buy your own OTC offer":
		return status.Error(codes.PermissionDenied, err.Error())
	case "insufficient public quantity for OTC purchase",
		"buyer account not found", "seller account not found":
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
```

- [ ] **Step 2: Commit**

```bash
git add stock-service/internal/handler/otc_handler.go
git commit -m "feat(stock-service): replace OTC handler stub with full implementation"
```

---

## Task 12: Create TaxGRPCService handler

**Files:**
- Create: `stock-service/internal/handler/tax_handler.go`

- [ ] **Step 1: Write the handler**

```go
package handler

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/exbanka/contract/stockpb"
	"github.com/exbanka/stock-service/internal/service"
)

type TaxHandler struct {
	pb.UnimplementedTaxGRPCServiceServer
	taxSvc *service.TaxService
}

func NewTaxHandler(taxSvc *service.TaxService) *TaxHandler {
	return &TaxHandler{taxSvc: taxSvc}
}

func (h *TaxHandler) ListTaxRecords(ctx context.Context, req *pb.ListTaxRecordsRequest) (*pb.ListTaxRecordsResponse, error) {
	now := time.Now()
	filter := service.TaxFilter{
		UserType: req.UserType,
		Search:   req.Search,
		Page:     int(req.Page),
		PageSize: int(req.PageSize),
	}

	summaries, total, err := h.taxSvc.ListTaxRecords(now.Year(), int(now.Month()), filter)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	records := make([]*pb.TaxRecord, len(summaries))
	for i, s := range summaries {
		lastCollection := ""
		if s.LastCollection != nil {
			lastCollection = s.LastCollection.Format("2006-01-02T15:04:05Z")
		}
		records[i] = &pb.TaxRecord{
			UserId:         s.UserID,
			UserType:       s.SystemType,
			FirstName:      s.UserFirstName,
			LastName:       s.UserLastName,
			TotalDebtRsd:   s.TotalDebtRSD.StringFixed(2),
			LastCollection: lastCollection,
		}
	}

	return &pb.ListTaxRecordsResponse{
		TaxRecords: records,
		TotalCount: total,
	}, nil
}

func (h *TaxHandler) CollectTax(ctx context.Context, req *pb.CollectTaxRequest) (*pb.CollectTaxResponse, error) {
	now := time.Now()
	collected, totalRSD, failed, err := h.taxSvc.CollectTax(now.Year(), int(now.Month()))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &pb.CollectTaxResponse{
		CollectedCount:   collected,
		TotalCollectedRsd: totalRSD.StringFixed(2),
		FailedCount:      failed,
	}, nil
}
```

- [ ] **Step 2: Commit**

```bash
git add stock-service/internal/handler/tax_handler.go
git commit -m "feat(stock-service): add TaxGRPCService handler with list and collect"
```

---

## Task 13: Integrate order fills with holdings and account balances

**Files:**
- Modify: `stock-service/internal/service/order_execution.go`

The execution engine needs to call PortfolioService when a fill happens to update holdings and debit/credit accounts.

- [ ] **Step 1: Add FillHandler field to OrderExecutionEngine**

Add a `fillHandler FillHandler` field to the `OrderExecutionEngine` struct:

```go
type OrderExecutionEngine struct {
	orderRepo   OrderRepo
	txRepo      OrderTransactionRepo
	listingRepo ListingRepo
	settingRepo SettingRepo
	fillHandler FillHandler // NEW: processes fills (holdings + account)
	mu          sync.Mutex
	activeJobs  map[uint64]context.CancelFunc
}
```

Update the constructor:

```go
func NewOrderExecutionEngine(
	orderRepo OrderRepo,
	txRepo OrderTransactionRepo,
	listingRepo ListingRepo,
	settingRepo SettingRepo,
	fillHandler FillHandler, // NEW parameter
) *OrderExecutionEngine {
	return &OrderExecutionEngine{
		orderRepo:   orderRepo,
		txRepo:      txRepo,
		listingRepo: listingRepo,
		settingRepo: settingRepo,
		fillHandler: fillHandler,
		activeJobs:  make(map[uint64]context.CancelFunc),
	}
}
```

- [ ] **Step 2: Add fill processing after each OrderTransaction creation**

In the `executeOrder` method, after `e.txRepo.Create(txn)` succeeds and before updating `order.RemainingPortions`, add:

```go
		if err := e.txRepo.Create(txn); err != nil {
			log.Printf("WARN: order engine: failed to record txn for order %d: %v", orderID, err)
			continue
		}

		// Process fill: update holdings and account balance
		if e.fillHandler != nil {
			if order.Direction == "buy" {
				if fillErr := e.fillHandler.ProcessBuyFill(order, txn); fillErr != nil {
					log.Printf("WARN: order engine: buy fill processing failed for order %d: %v", orderID, fillErr)
				}
			} else {
				if fillErr := e.fillHandler.ProcessSellFill(order, txn); fillErr != nil {
					log.Printf("WARN: order engine: sell fill processing failed for order %d: %v", orderID, fillErr)
				}
			}
		}

		// Update order (existing code continues below)
		order.RemainingPortions -= portionSize
```

- [ ] **Step 3: Commit**

```bash
git add stock-service/internal/service/order_execution.go
git commit -m "feat(stock-service): integrate order fills with portfolio holdings and account balance"
```

---

## Task 14: Seed state company and account in account-service

**Files:**
- Modify: `account-service/cmd/main.go` (or the seed function)

The "State" is a Company entity with an RSD-only account. It receives tax payments.

- [ ] **Step 1: Add state company seeding**

Find the existing bank account seed in `account-service/cmd/main.go` (the section that seeds bank-owned accounts with `is_bank_account = true` and `owner_id = 1_000_000_000`). Add after the bank seed:

```go
	// Seed state company and RSD account (for capital gains tax deposits)
	const stateOwnerID = uint64(2_000_000_000)
	var stateCompany model.Company
	if err := db.Where("company_id = ?", stateOwnerID).First(&stateCompany).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			stateCompany = model.Company{
				CompanyID:          stateOwnerID,
				Name:               "State of Serbia",
				RegistrationNumber: "000000000",
				TaxNumber:          "000000000",
				ActivityCode:       "8411",
				Address:            "Nemanjina 11, Belgrade",
			}
			if err := db.Create(&stateCompany).Error; err != nil {
				log.Printf("WARN: failed to seed state company: %v", err)
			} else {
				log.Println("seeded state company: State of Serbia")
			}
		}
	}

	// Seed state RSD account
	stateAccountNumber := "0000000000000099"
	var stateAccount model.Account
	if err := db.Where("account_number = ?", stateAccountNumber).First(&stateAccount).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			stateAccount = model.Account{
				AccountNumber:    stateAccountNumber,
				OwnerID:          stateOwnerID,
				Currency:         "RSD",
				AccountKind:      "current",
				Balance:          decimal.Zero,
				AvailableBalance: decimal.Zero,
				Status:           "active",
				IsBankAccount:    true,
			}
			if err := db.Create(&stateAccount).Error; err != nil {
				log.Printf("WARN: failed to seed state RSD account: %v", err)
			} else {
				log.Printf("seeded state account: %s (RSD)", stateAccountNumber)
			}
		}
	}
```

- [ ] **Step 2: Commit**

```bash
git add account-service/cmd/main.go
git commit -m "feat(account-service): seed State of Serbia company and RSD account for tax deposits"
```

---

## Task 15: Add Kafka events, config, and docker-compose updates

**Files:**
- Modify: `contract/kafka/messages.go`
- Modify: `stock-service/internal/kafka/producer.go`
- Modify: `stock-service/internal/config/config.go`
- Modify: `docker-compose.yml`

- [ ] **Step 1: Add portfolio and tax event topics and messages**

In `contract/kafka/messages.go`:

```go
const (
	// ... existing topics ...
	TopicHoldingUpdated   = "stock.holding-updated"
	TopicOTCTradeExecuted = "stock.otc-trade-executed"
	TopicTaxCollected     = "stock.tax-collected"
	TopicOptionExercised  = "stock.option-exercised"
)

type HoldingUpdatedMessage struct {
	HoldingID    uint64 `json:"holding_id"`
	UserID       uint64 `json:"user_id"`
	SecurityType string `json:"security_type"`
	Ticker       string `json:"ticker"`
	Quantity     int64  `json:"quantity"`
	Direction    string `json:"direction"` // "buy" or "sell"
	Timestamp    int64  `json:"timestamp"`
}

type OTCTradeMessage struct {
	SellerID     uint64 `json:"seller_id"`
	BuyerID      uint64 `json:"buyer_id"`
	Ticker       string `json:"ticker"`
	Quantity     int64  `json:"quantity"`
	PricePerUnit string `json:"price_per_unit"`
	TotalPrice   string `json:"total_price"`
	Timestamp    int64  `json:"timestamp"`
}

type TaxCollectedMessage struct {
	UserID       uint64 `json:"user_id"`
	Year         int    `json:"year"`
	Month        int    `json:"month"`
	TaxAmountRSD string `json:"tax_amount_rsd"`
	Timestamp    int64  `json:"timestamp"`
}

type OptionExercisedMessage struct {
	UserID        uint64 `json:"user_id"`
	OptionTicker  string `json:"option_ticker"`
	OptionType    string `json:"option_type"` // "call" or "put"
	Quantity      int64  `json:"quantity"`
	Profit        string `json:"profit"`
	Timestamp     int64  `json:"timestamp"`
}
```

- [ ] **Step 2: Add publish methods to producer**

In `stock-service/internal/kafka/producer.go`:

```go
func (p *Producer) PublishHoldingUpdated(ctx context.Context, msg contract.HoldingUpdatedMessage) error {
	return p.publish(ctx, contract.TopicHoldingUpdated, msg)
}

func (p *Producer) PublishOTCTradeExecuted(ctx context.Context, msg contract.OTCTradeMessage) error {
	return p.publish(ctx, contract.TopicOTCTradeExecuted, msg)
}

func (p *Producer) PublishTaxCollected(ctx context.Context, msg contract.TaxCollectedMessage) error {
	return p.publish(ctx, contract.TopicTaxCollected, msg)
}

func (p *Producer) PublishOptionExercised(ctx context.Context, msg contract.OptionExercisedMessage) error {
	return p.publish(ctx, contract.TopicOptionExercised, msg)
}
```

- [ ] **Step 3: Update EnsureTopics**

```go
kafkaprod.EnsureTopics(cfg.KafkaBrokers,
	"stock.security-synced", "stock.listing-updated",
	"stock.order-created", "stock.order-approved", "stock.order-declined",
	"stock.order-filled", "stock.order-cancelled",
	"stock.holding-updated", "stock.otc-trade-executed",
	"stock.tax-collected", "stock.option-exercised",
)
```

- [ ] **Step 4: Update config.go with new env vars**

Add to `stock-service/internal/config/config.go`:

```go
type Config struct {
	// ... existing fields ...
	ClientGRPCAddr   string
	StateAccountNo   string // State's RSD account number for tax deposits
}

func Load() *Config {
	return &Config{
		// ... existing ...
		ClientGRPCAddr:   getEnv("CLIENT_GRPC_ADDR", "localhost:50054"),
		StateAccountNo:   getEnv("STATE_ACCOUNT_NUMBER", "0000000000000099"),
	}
}
```

- [ ] **Step 5: Update docker-compose.yml**

Add to the `stock-service` environment section:

```yaml
      CLIENT_GRPC_ADDR: client-service:50054
      STATE_ACCOUNT_NUMBER: "0000000000000099"
```

Add `client-service` to stock-service's `depends_on:` if not already present.

- [ ] **Step 6: Commit**

```bash
git add contract/kafka/messages.go stock-service/internal/kafka/producer.go stock-service/internal/config/config.go docker-compose.yml
git commit -m "feat(stock-service): add Kafka events, config, docker-compose for portfolio/OTC/tax"
```

---

## Task 16: Wire portfolio, OTC, and tax into main.go

**Files:**
- Modify: `stock-service/cmd/main.go`

- [ ] **Step 1: Add AutoMigrate for new models**

```go
if err := db.AutoMigrate(
	// ... existing models ...
	&model.Holding{},
	&model.CapitalGain{},
	&model.TaxCollection{},
); err != nil {
	log.Fatalf("auto-migrate failed: %v", err)
}
```

- [ ] **Step 2: Add composite unique index for holdings**

After AutoMigrate:

```go
// Unique constraint: one holding per user per security per account
db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_holding_unique ON holdings (user_id, security_type, security_id, account_id)")
```

- [ ] **Step 3: Create gRPC client connections**

```go
// Account service client (for debit/credit)
accountConn, err := grpc.NewClient(cfg.AccountGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
if err != nil {
	log.Fatalf("failed to connect to account-service: %v", err)
}
defer accountConn.Close()
accountClient := accountpb.NewAccountGRPCServiceClient(accountConn)

// Exchange service client (for currency conversion)
exchangeConn, err := grpc.NewClient(cfg.ExchangeGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
if err != nil {
	log.Fatalf("failed to connect to exchange-service: %v", err)
}
defer exchangeConn.Close()
exchangeClient := exchangepb.NewExchangeGRPCServiceClient(exchangeConn)

// User service client (for name resolution)
userConn, err := grpc.NewClient(cfg.UserGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
if err != nil {
	log.Fatalf("failed to connect to user-service: %v", err)
}
defer userConn.Close()
userClient := userpb.NewEmployeeServiceClient(userConn)

// Client service client (for name resolution)
clientConn, err := grpc.NewClient(cfg.ClientGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
if err != nil {
	log.Fatalf("failed to connect to client-service: %v", err)
}
defer clientConn.Close()
clientClient := clientpb.NewClientServiceClient(clientConn)
```

- [ ] **Step 4: Create name resolver function**

```go
nameResolver := func(userID uint64, systemType string) (string, string, error) {
	if systemType == "client" {
		resp, err := clientClient.GetClient(context.Background(), &clientpb.GetClientRequest{Id: userID})
		if err != nil {
			return "", "", err
		}
		return resp.FirstName, resp.LastName, nil
	}
	resp, err := userClient.GetEmployee(context.Background(), &userpb.GetEmployeeRequest{Id: userID})
	if err != nil {
		return "", "", err
	}
	return resp.FirstName, resp.LastName, nil
}
```

- [ ] **Step 5: Create portfolio, OTC, and tax repos and services**

```go
// Repositories
holdingRepo := repository.NewHoldingRepository(db)
capitalGainRepo := repository.NewCapitalGainRepository(db)
taxCollectionRepo := repository.NewTaxCollectionRepository(db)

// Services
portfolioSvc := service.NewPortfolioService(
	holdingRepo, capitalGainRepo, listingRepo,
	stockRepo, optionRepo,
	accountClient, nameResolver, cfg.StateAccountNo,
)

otcSvc := service.NewOTCService(
	holdingRepo, capitalGainRepo, listingRepo,
	accountClient, nameResolver,
)

taxSvc := service.NewTaxService(
	capitalGainRepo, taxCollectionRepo, holdingRepo,
	accountClient, exchangeClient, cfg.StateAccountNo,
)

taxCronSvc := service.NewTaxCronService(taxSvc)
```

- [ ] **Step 6: Update execution engine to use portfolio as fill handler**

Replace the `NewOrderExecutionEngine` call to include `portfolioSvc`:

```go
execEngine := service.NewOrderExecutionEngine(orderRepo, orderTxRepo, listingRepo, settingRepo, portfolioSvc)
```

- [ ] **Step 7: Register gRPC services and start cron**

```go
// Portfolio handler
portfolioHandler := handler.NewPortfolioHandler(portfolioSvc, taxSvc)
pb.RegisterPortfolioGRPCServiceServer(grpcServer, portfolioHandler)

// OTC handler (replaces stub)
otcHandler := handler.NewOTCHandler(otcSvc)
pb.RegisterOTCGRPCServiceServer(grpcServer, otcHandler)

// Tax handler
taxHandler := handler.NewTaxHandler(taxSvc)
pb.RegisterTaxGRPCServiceServer(grpcServer, taxHandler)

// Start tax collection cron
taxCronSvc.StartMonthlyCron(ctx)
```

- [ ] **Step 8: Commit**

```bash
git add stock-service/cmd/main.go
git commit -m "feat(stock-service): wire portfolio, OTC, tax models, repos, services, handlers into main"
```

---

## Task 17: Verify build

**Files:** None (verification only)

- [ ] **Step 1: Run go build**

```bash
cd stock-service && go build ./cmd
```

Expected: BUILD SUCCESS.

- [ ] **Step 2: Run tidy**

```bash
cd stock-service && go mod tidy
```

- [ ] **Step 3: Verify all services build**

```bash
make build
```

Expected: All services compile without errors.

- [ ] **Step 4: Commit if tidy made changes**

```bash
git add stock-service/go.mod stock-service/go.sum
git commit -m "chore(stock-service): tidy go modules after portfolio/tax additions"
```
