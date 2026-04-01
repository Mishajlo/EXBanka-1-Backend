package service

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/exbanka/stock-service/internal/model"
)

type StockRepo interface {
	Create(stock *model.Stock) error
	GetByID(id uint64) (*model.Stock, error)
	GetByTicker(ticker string) (*model.Stock, error)
	Update(stock *model.Stock) error
	UpsertByTicker(stock *model.Stock) error
	List(filter StockFilter) ([]model.Stock, int64, error)
}

type StockFilter struct {
	Search          string
	ExchangeAcronym string
	MinPrice        *decimal.Decimal
	MaxPrice        *decimal.Decimal
	MinVolume       *int64
	MaxVolume       *int64
	SortBy          string // "price", "volume", "change", "margin"
	SortOrder       string // "asc", "desc"
	Page            int
	PageSize        int
}

type FuturesRepo interface {
	Create(f *model.FuturesContract) error
	GetByID(id uint64) (*model.FuturesContract, error)
	GetByTicker(ticker string) (*model.FuturesContract, error)
	Update(f *model.FuturesContract) error
	UpsertByTicker(f *model.FuturesContract) error
	List(filter FuturesFilter) ([]model.FuturesContract, int64, error)
}

type FuturesFilter struct {
	Search             string
	ExchangeAcronym    string
	MinPrice           *decimal.Decimal
	MaxPrice           *decimal.Decimal
	MinVolume          *int64
	MaxVolume          *int64
	SettlementDateFrom *time.Time
	SettlementDateTo   *time.Time
	SortBy             string
	SortOrder          string
	Page               int
	PageSize           int
}

type ForexPairRepo interface {
	Create(fp *model.ForexPair) error
	GetByID(id uint64) (*model.ForexPair, error)
	GetByTicker(ticker string) (*model.ForexPair, error)
	Update(fp *model.ForexPair) error
	UpsertByTicker(fp *model.ForexPair) error
	List(filter ForexFilter) ([]model.ForexPair, int64, error)
}

type ForexFilter struct {
	Search        string
	BaseCurrency  string
	QuoteCurrency string
	Liquidity     string
	SortBy        string
	SortOrder     string
	Page          int
	PageSize      int
}

type OptionRepo interface {
	Create(o *model.Option) error
	GetByID(id uint64) (*model.Option, error)
	GetByTicker(ticker string) (*model.Option, error)
	Update(o *model.Option) error
	UpsertByTicker(o *model.Option) error
	List(filter OptionFilter) ([]model.Option, int64, error)
	DeleteExpiredBefore(cutoff time.Time) (int64, error)
}

type OptionFilter struct {
	StockID        *uint64
	OptionType     string // "call", "put", "" (both)
	SettlementDate *time.Time
	MinStrike      *decimal.Decimal
	MaxStrike      *decimal.Decimal
	Page           int
	PageSize       int
}

// ExchangeRepo is the exchange repository from Plan 2 (already defined).
// Re-declared here as an interface so security_service can depend on it.
type ExchangeRepo interface {
	GetByID(id uint64) (*model.StockExchange, error)
	GetByAcronym(acronym string) (*model.StockExchange, error)
	List(search string, page, pageSize int) ([]model.StockExchange, int64, error)
}

// SettingRepo is the system setting repository from Plan 2.
type SettingRepo interface {
	Get(key string) (string, error)
	Set(key, value string) error
}
