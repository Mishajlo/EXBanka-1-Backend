# Stock Exchanges & Actuaries Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement stock exchange data management (models, external data sync, testing mode toggle) in `stock-service` and actuary limit management (model, CRUD, daily reset cron) in `user-service`.

**Architecture:** Two workstreams that can be parallelized:
1. `stock-service`: Exchange entity with CSV-seeded data, testing mode toggle, exchange hours logic
2. `user-service`: ActuaryLimit entity extending Employee, with limit/usedLimit/needApproval, daily reset cron, gRPC ActuaryService

**Tech Stack:** Go 1.26, gRPC/Protobuf, GORM/PostgreSQL, Kafka (segmentio/kafka-go), CSV parsing

**Depends on:** Plan 1 (API surface plan) for proto definitions and gateway handlers. This plan creates the actual implementations that those gateway handlers call.

---

## Workstream A: Stock Exchanges in stock-service

### Task A1: Create stock-service module and directory scaffold

**Files:**
- Create: `stock-service/go.mod`
- Create: `stock-service/cmd/main.go`
- Create: `stock-service/internal/config/config.go`
- Create: `stock-service/internal/kafka/producer.go`
- Create: `stock-service/internal/kafka/topics.go`
- Modify: `go.work` — add `./stock-service`

- [ ] **Step 1: Write the failing build test**

```bash
cd stock-service && go build ./cmd
```

Expected: FAIL — directory doesn't exist yet.

- [ ] **Step 2: Create directories**

```bash
mkdir -p stock-service/cmd \
  stock-service/internal/{config,model,repository,service,handler,kafka,provider} \
  stock-service/data
```

- [ ] **Step 3: Create stock-service/go.mod**

```go
module github.com/exbanka/stock-service

go 1.26.1

require (
	github.com/exbanka/contract v0.0.0
	github.com/segmentio/kafka-go v0.4.47
	github.com/shopspring/decimal v1.4.0
	google.golang.org/grpc v1.72.0
	gorm.io/driver/postgres v1.5.11
	gorm.io/gorm v1.26.1
)

replace github.com/exbanka/contract => ../contract
```

- [ ] **Step 4: Create stock-service/internal/config/config.go**

```go
package config

import (
	"fmt"
	"os"
)

type Config struct {
	DBHost       string
	DBPort       string
	DBUser       string
	DBPassword   string
	DBName       string
	DBSslmode    string
	GRPCAddr     string
	KafkaBrokers string
	// Dependencies
	UserGRPCAddr     string
	AccountGRPCAddr  string
	ExchangeGRPCAddr string
}

func Load() *Config {
	return &Config{
		DBHost:           getEnv("STOCK_DB_HOST", "localhost"),
		DBPort:           getEnv("STOCK_DB_PORT", "5440"),
		DBUser:           getEnv("STOCK_DB_USER", "postgres"),
		DBPassword:       getEnv("STOCK_DB_PASSWORD", "postgres"),
		DBName:           getEnv("STOCK_DB_NAME", "stock_db"),
		DBSslmode:        getEnv("STOCK_DB_SSLMODE", "disable"),
		GRPCAddr:         getEnv("STOCK_GRPC_ADDR", ":50060"),
		KafkaBrokers:     getEnv("KAFKA_BROKERS", "localhost:9092"),
		UserGRPCAddr:     getEnv("USER_GRPC_ADDR", "localhost:50052"),
		AccountGRPCAddr:  getEnv("ACCOUNT_GRPC_ADDR", "localhost:50055"),
		ExchangeGRPCAddr: getEnv("EXCHANGE_GRPC_ADDR", "localhost:50059"),
	}
}

func (c *Config) DSN() string {
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		c.DBHost, c.DBPort, c.DBUser, c.DBPassword, c.DBName, c.DBSslmode)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

- [ ] **Step 5: Create stock-service/internal/kafka/producer.go**

```go
package kafka

import (
	"context"
	"encoding/json"

	kafkago "github.com/segmentio/kafka-go"
)

type Producer struct {
	writer *kafkago.Writer
}

func NewProducer(brokers string) *Producer {
	return &Producer{
		writer: &kafkago.Writer{
			Addr:     kafkago.TCP(brokers),
			Balancer: &kafkago.LeastBytes{},
		},
	}
}

func (p *Producer) Close() error {
	return p.writer.Close()
}

func (p *Producer) publish(ctx context.Context, topic string, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return p.writer.WriteMessages(ctx, kafkago.Message{
		Topic: topic,
		Value: data,
	})
}
```

- [ ] **Step 6: Create stock-service/internal/kafka/topics.go**

```go
package kafka

import (
	"log"
	"net"
	"strconv"
	"time"

	kafkalib "github.com/segmentio/kafka-go"
)

func EnsureTopics(broker string, topics ...string) {
	var conn *kafkalib.Conn
	var err error
	for i := 0; i < 10; i++ {
		conn, err = kafkalib.Dial("tcp", broker)
		if err == nil {
			break
		}
		log.Printf("kafka dial attempt %d: %v", i+1, err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		log.Printf("WARN: could not connect to kafka to create topics: %v", err)
		return
	}
	defer conn.Close()

	controller, err := conn.Controller()
	if err != nil {
		log.Printf("WARN: could not get kafka controller: %v", err)
		return
	}
	addr := net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port))
	controllerConn, err := kafkalib.Dial("tcp", addr)
	if err != nil {
		log.Printf("WARN: could not connect to kafka controller: %v", err)
		return
	}
	defer controllerConn.Close()

	topicConfigs := make([]kafkalib.TopicConfig, len(topics))
	for i, t := range topics {
		topicConfigs[i] = kafkalib.TopicConfig{
			Topic:             t,
			NumPartitions:     1,
			ReplicationFactor: 1,
		}
	}
	if err := controllerConn.CreateTopics(topicConfigs...); err != nil {
		log.Printf("WARN: create topics: %v", err)
	}
}
```

- [ ] **Step 7: Create stub stock-service/cmd/main.go**

```go
package main

import (
	"log"
	"net"

	"google.golang.org/grpc"

	shared "github.com/exbanka/contract/shared"
	"github.com/exbanka/stock-service/internal/config"
)

func main() {
	cfg := config.Load()
	_ = cfg // will wire DB in next tasks

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	shared.RegisterHealthCheck(s, "stock-service")

	log.Printf("stock-service listening on %s", cfg.GRPCAddr)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("gRPC server failed: %v", err)
	}
}
```

- [ ] **Step 8: Add stock-service to go.work**

Add `./stock-service` to the `use` block in `go.work`.

- [ ] **Step 9: Run go mod tidy and verify build**

```bash
cd stock-service && go mod tidy && go build ./cmd
```

Expected: BUILD SUCCESS.

- [ ] **Step 10: Commit**

```bash
git add stock-service/ go.work
git commit -m "feat(stock-service): scaffold module with config, kafka, and stub main"
```

---

### Task A2: Define stock exchange model

**Files:**
- Create: `stock-service/internal/model/stock_exchange.go`

- [ ] **Step 1: Write the model**

```go
package model

import (
	"time"
)

// StockExchange represents a stock exchange (e.g., NYSE, NASDAQ).
type StockExchange struct {
	ID              uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	Name            string    `gorm:"not null" json:"name"`
	Acronym         string    `gorm:"uniqueIndex;not null" json:"acronym"`
	MICCode         string    `gorm:"uniqueIndex;column:mic_code;not null" json:"mic_code"`
	Polity          string    `gorm:"not null" json:"polity"`
	Currency        string    `gorm:"size:3;not null" json:"currency"`
	TimeZone        string    `gorm:"not null" json:"time_zone"`
	OpenTime        string    `gorm:"size:5;not null" json:"open_time"`
	CloseTime       string    `gorm:"size:5;not null" json:"close_time"`
	PreMarketOpen   string    `gorm:"size:5" json:"pre_market_open"`
	PostMarketClose string    `gorm:"size:5" json:"post_market_close"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
```

- [ ] **Step 2: Commit**

```bash
git add stock-service/internal/model/stock_exchange.go
git commit -m "feat(stock-service): add StockExchange model"
```

---

### Task A3: Define testing mode model

**Files:**
- Create: `stock-service/internal/model/system_setting.go`

- [ ] **Step 1: Write the model**

A simple key-value model for system-wide settings. Testing mode is stored as `key=testing_mode`, `value=true/false`.

```go
package model

// SystemSetting stores global key-value configuration.
type SystemSetting struct {
	Key   string `gorm:"primaryKey;size:64" json:"key"`
	Value string `gorm:"not null" json:"value"`
}
```

- [ ] **Step 2: Commit**

```bash
git add stock-service/internal/model/system_setting.go
git commit -m "feat(stock-service): add SystemSetting model for testing mode"
```

---

### Task A4: Create exchange repository

**Files:**
- Create: `stock-service/internal/repository/exchange_repository.go`

- [ ] **Step 1: Write the repository**

```go
package repository

import (
	"github.com/exbanka/stock-service/internal/model"
	"gorm.io/gorm"
)

type ExchangeRepository struct {
	db *gorm.DB
}

func NewExchangeRepository(db *gorm.DB) *ExchangeRepository {
	return &ExchangeRepository{db: db}
}

func (r *ExchangeRepository) Create(exchange *model.StockExchange) error {
	return r.db.Create(exchange).Error
}

func (r *ExchangeRepository) GetByID(id uint64) (*model.StockExchange, error) {
	var exchange model.StockExchange
	if err := r.db.First(&exchange, id).Error; err != nil {
		return nil, err
	}
	return &exchange, nil
}

func (r *ExchangeRepository) GetByMICCode(mic string) (*model.StockExchange, error) {
	var exchange model.StockExchange
	if err := r.db.Where("mic_code = ?", mic).First(&exchange).Error; err != nil {
		return nil, err
	}
	return &exchange, nil
}

func (r *ExchangeRepository) GetByAcronym(acronym string) (*model.StockExchange, error) {
	var exchange model.StockExchange
	if err := r.db.Where("acronym = ?", acronym).First(&exchange).Error; err != nil {
		return nil, err
	}
	return &exchange, nil
}

func (r *ExchangeRepository) List(search string, page, pageSize int) ([]model.StockExchange, int64, error) {
	var exchanges []model.StockExchange
	var total int64

	q := r.db.Model(&model.StockExchange{})
	if search != "" {
		like := "%" + search + "%"
		q = q.Where("name ILIKE ? OR acronym ILIKE ? OR mic_code ILIKE ?", like, like, like)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	if err := q.Offset((page - 1) * pageSize).Limit(pageSize).Order("name ASC").Find(&exchanges).Error; err != nil {
		return nil, 0, err
	}
	return exchanges, total, nil
}

func (r *ExchangeRepository) UpsertByMICCode(exchange *model.StockExchange) error {
	existing, err := r.GetByMICCode(exchange.MICCode)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return r.db.Create(exchange).Error
		}
		return err
	}
	existing.Name = exchange.Name
	existing.Acronym = exchange.Acronym
	existing.Polity = exchange.Polity
	existing.Currency = exchange.Currency
	existing.TimeZone = exchange.TimeZone
	existing.OpenTime = exchange.OpenTime
	existing.CloseTime = exchange.CloseTime
	existing.PreMarketOpen = exchange.PreMarketOpen
	existing.PostMarketClose = exchange.PostMarketClose
	return r.db.Save(existing).Error
}
```

- [ ] **Step 2: Commit**

```bash
git add stock-service/internal/repository/exchange_repository.go
git commit -m "feat(stock-service): add exchange repository with List, UpsertByMICCode"
```

---

### Task A5: Create system setting repository

**Files:**
- Create: `stock-service/internal/repository/system_setting_repository.go`

- [ ] **Step 1: Write the repository**

```go
package repository

import (
	"github.com/exbanka/stock-service/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SystemSettingRepository struct {
	db *gorm.DB
}

func NewSystemSettingRepository(db *gorm.DB) *SystemSettingRepository {
	return &SystemSettingRepository{db: db}
}

func (r *SystemSettingRepository) Get(key string) (string, error) {
	var setting model.SystemSetting
	if err := r.db.Where("key = ?", key).First(&setting).Error; err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *SystemSettingRepository) Set(key, value string) error {
	setting := model.SystemSetting{Key: key, Value: value}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&setting).Error
}
```

- [ ] **Step 2: Commit**

```bash
git add stock-service/internal/repository/system_setting_repository.go
git commit -m "feat(stock-service): add system setting repository with Get/Set"
```

---

### Task A6: Seed exchange data from CSV

**Files:**
- Create: `stock-service/data/exchanges.csv`
- Create: `stock-service/internal/provider/exchange_csv_loader.go`

- [ ] **Step 1: Create the CSV seed file**

Create `stock-service/data/exchanges.csv` with the key exchanges the system needs. This is the "testing mode" data that is always available locally. Format:

```csv
name,acronym,mic_code,polity,currency,time_zone,open_time,close_time,pre_market_open,post_market_close
New York Stock Exchange,NYSE,XNYS,United States,USD,-5,09:30,16:00,04:00,20:00
NASDAQ,NASDAQ,XNAS,United States,USD,-5,09:30,16:00,04:00,20:00
London Stock Exchange,LSE,XLON,United Kingdom,GBP,0,08:00,16:30,,
Tokyo Stock Exchange,TSE,XJPX,Japan,JPY,+9,09:00,15:00,,
Shanghai Stock Exchange,SSE,XSHG,China,CNY,+8,09:30,15:00,,
Hong Kong Stock Exchange,HKEX,XHKG,Hong Kong,HKD,+8,09:30,16:00,,
Toronto Stock Exchange,TSX,XTSE,Canada,CAD,-5,09:30,16:00,,
Australian Securities Exchange,ASX,XASX,Australia,AUD,+10,10:00,16:00,,
SIX Swiss Exchange,SIX,XSWX,Switzerland,CHF,+1,09:00,17:30,,
Euronext Paris,ENX,XPAR,France,EUR,+1,09:00,17:30,,
Frankfurt Stock Exchange,FRA,XFRA,Germany,EUR,+1,08:00,22:00,,
New York Mercantile Exchange,NYMEX,XNYM,United States,USD,-5,09:00,14:30,,
Chicago Board of Trade,CBOT,XCBT,United States,USD,-6,08:30,13:20,,
Chicago Mercantile Exchange,CME,XCME,United States,USD,-6,08:30,15:15,,
Borsa Italiana,BIT,XMIL,Italy,EUR,+1,09:00,17:30,,
```

- [ ] **Step 2: Write the CSV loader**

```go
package provider

import (
	"embed"
	"encoding/csv"
	"fmt"
	"io"
	"strings"

	"github.com/exbanka/stock-service/internal/model"
)

//go:embed ../../../data/exchanges.csv
var exchangeCSVData embed.FS

// LoadExchangesFromCSV reads exchanges from the embedded CSV file.
// This is the local/testing-mode data source.
func LoadExchangesFromCSV() ([]model.StockExchange, error) {
	f, err := exchangeCSVData.Open("data/exchanges.csv")
	if err != nil {
		return nil, fmt.Errorf("open embedded csv: %w", err)
	}
	defer f.Close()
	return parseExchangeCSV(f)
}

// LoadExchangesFromCSVBytes parses exchanges from raw CSV bytes.
func LoadExchangesFromCSVBytes(data []byte) ([]model.StockExchange, error) {
	return parseExchangeCSV(strings.NewReader(string(data)))
}

func parseExchangeCSV(r io.Reader) ([]model.StockExchange, error) {
	reader := csv.NewReader(r)
	// Skip header
	if _, err := reader.Read(); err != nil {
		return nil, fmt.Errorf("read csv header: %w", err)
	}

	var exchanges []model.StockExchange
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read csv row: %w", err)
		}
		if len(record) < 10 {
			continue
		}
		exchanges = append(exchanges, model.StockExchange{
			Name:            strings.TrimSpace(record[0]),
			Acronym:         strings.TrimSpace(record[1]),
			MICCode:         strings.TrimSpace(record[2]),
			Polity:          strings.TrimSpace(record[3]),
			Currency:        strings.TrimSpace(record[4]),
			TimeZone:        strings.TrimSpace(record[5]),
			OpenTime:        strings.TrimSpace(record[6]),
			CloseTime:       strings.TrimSpace(record[7]),
			PreMarketOpen:   strings.TrimSpace(record[8]),
			PostMarketClose: strings.TrimSpace(record[9]),
		})
	}
	return exchanges, nil
}
```

**Note:** The `embed` path depends on the file's location relative to the package. If `provider/` is at `stock-service/internal/provider/`, we need to embed from the `stock-service/data/` directory. Since Go `embed` only works with paths relative to the source file, we'll instead pass the CSV path at startup and read it via `os.ReadFile`. Revised approach:

```go
package provider

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/exbanka/stock-service/internal/model"
)

// LoadExchangesFromCSVFile reads exchanges from a CSV file on disk.
func LoadExchangesFromCSVFile(path string) ([]model.StockExchange, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open csv file: %w", err)
	}
	defer f.Close()
	return parseExchangeCSV(f)
}

func parseExchangeCSV(r io.Reader) ([]model.StockExchange, error) {
	reader := csv.NewReader(r)
	// Skip header
	if _, err := reader.Read(); err != nil {
		return nil, fmt.Errorf("read csv header: %w", err)
	}

	var exchanges []model.StockExchange
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read csv row: %w", err)
		}
		if len(record) < 10 {
			continue
		}
		exchanges = append(exchanges, model.StockExchange{
			Name:            strings.TrimSpace(record[0]),
			Acronym:         strings.TrimSpace(record[1]),
			MICCode:         strings.TrimSpace(record[2]),
			Polity:          strings.TrimSpace(record[3]),
			Currency:        strings.TrimSpace(record[4]),
			TimeZone:        strings.TrimSpace(record[5]),
			OpenTime:        strings.TrimSpace(record[6]),
			CloseTime:       strings.TrimSpace(record[7]),
			PreMarketOpen:   strings.TrimSpace(record[8]),
			PostMarketClose: strings.TrimSpace(record[9]),
		})
	}
	return exchanges, nil
}
```

- [ ] **Step 3: Commit**

```bash
git add stock-service/data/exchanges.csv stock-service/internal/provider/exchange_csv_loader.go
git commit -m "feat(stock-service): add CSV exchange data loader with seed data"
```

---

### Task A7: Create exchange service

**Files:**
- Create: `stock-service/internal/service/exchange_service.go`

- [ ] **Step 1: Write the service**

```go
package service

import (
	"errors"
	"log"

	"gorm.io/gorm"

	"github.com/exbanka/stock-service/internal/model"
	"github.com/exbanka/stock-service/internal/provider"
	"github.com/exbanka/stock-service/internal/repository"
)

type ExchangeService struct {
	exchangeRepo *repository.ExchangeRepository
	settingRepo  *repository.SystemSettingRepository
}

func NewExchangeService(
	exchangeRepo *repository.ExchangeRepository,
	settingRepo *repository.SystemSettingRepository,
) *ExchangeService {
	return &ExchangeService{
		exchangeRepo: exchangeRepo,
		settingRepo:  settingRepo,
	}
}

// SeedExchanges loads exchange data from CSV and upserts into the database.
func (s *ExchangeService) SeedExchanges(csvPath string) error {
	exchanges, err := provider.LoadExchangesFromCSVFile(csvPath)
	if err != nil {
		return err
	}
	for _, ex := range exchanges {
		ex := ex
		if err := s.exchangeRepo.UpsertByMICCode(&ex); err != nil {
			log.Printf("WARN: failed to upsert exchange %s: %v", ex.MICCode, err)
		}
	}
	log.Printf("seeded %d exchanges from CSV", len(exchanges))
	return nil
}

func (s *ExchangeService) GetExchange(id uint64) (*model.StockExchange, error) {
	ex, err := s.exchangeRepo.GetByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("exchange not found")
		}
		return nil, err
	}
	return ex, nil
}

func (s *ExchangeService) ListExchanges(search string, page, pageSize int) ([]model.StockExchange, int64, error) {
	return s.exchangeRepo.List(search, page, pageSize)
}

// SetTestingMode toggles global testing mode. When enabled, exchange hours are
// ignored (all exchanges treated as open).
func (s *ExchangeService) SetTestingMode(enabled bool) error {
	val := "false"
	if enabled {
		val = "true"
	}
	return s.settingRepo.Set("testing_mode", val)
}

// GetTestingMode returns whether testing mode is currently enabled.
func (s *ExchangeService) GetTestingMode() bool {
	val, err := s.settingRepo.Get("testing_mode")
	if err != nil {
		return false // default: not in testing mode
	}
	return val == "true"
}

// IsExchangeOpen checks if the given exchange is currently open for trading.
// When testing mode is enabled, always returns true.
func (s *ExchangeService) IsExchangeOpen(exchangeID uint64) (bool, error) {
	if s.GetTestingMode() {
		return true, nil
	}
	ex, err := s.exchangeRepo.GetByID(exchangeID)
	if err != nil {
		return false, err
	}
	return isWithinTradingHours(ex), nil
}
```

- [ ] **Step 2: Write the trading hours helper in same file**

```go
package service

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// isWithinTradingHours checks if the current moment falls within the exchange's
// open and close times, accounting for its time zone offset.
func isWithinTradingHours(ex *model.StockExchange) bool {
	offset, err := parseTimezoneOffset(ex.TimeZone)
	if err != nil {
		return false
	}
	loc := time.FixedZone(ex.Acronym, offset*3600)
	now := time.Now().In(loc)

	openH, openM := parseTime(ex.OpenTime)
	closeH, closeM := parseTime(ex.CloseTime)

	nowMinutes := now.Hour()*60 + now.Minute()
	openMinutes := openH*60 + openM
	closeMinutes := closeH*60 + closeM

	return nowMinutes >= openMinutes && nowMinutes < closeMinutes
}

// parseTimezoneOffset parses "+5", "-5", "+9", "+10" etc. to integer hours.
func parseTimezoneOffset(tz string) (int, error) {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return 0, fmt.Errorf("empty timezone")
	}
	return strconv.Atoi(tz)
}

// parseTime parses "09:30" to (9, 30).
func parseTime(t string) (int, int) {
	parts := strings.Split(t, ":")
	if len(parts) != 2 {
		return 0, 0
	}
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	return h, m
}
```

**Note:** Put the helper functions in a separate file `stock-service/internal/service/exchange_hours.go` for clarity.

- [ ] **Step 3: Commit**

```bash
git add stock-service/internal/service/exchange_service.go \
        stock-service/internal/service/exchange_hours.go
git commit -m "feat(stock-service): add exchange service with seeding, testing mode, and hours check"
```

---

### Task A8: Create exchange gRPC handler

**Files:**
- Create: `stock-service/internal/handler/exchange_handler.go`

**Depends on:** Plan 1, Task 2 (stock.proto must be generated). If proto files are not yet generated, run `make proto` first.

- [ ] **Step 1: Write the handler**

```go
package handler

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/exbanka/contract/stockpb"
	"github.com/exbanka/stock-service/internal/service"
)

// mapServiceError maps service-layer error messages to gRPC status codes.
func mapServiceError(err error) codes.Code {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not found"):
		return codes.NotFound
	case strings.Contains(msg, "must be"), strings.Contains(msg, "invalid"),
		strings.Contains(msg, "must not"), strings.Contains(msg, "required"):
		return codes.InvalidArgument
	case strings.Contains(msg, "already exists"), strings.Contains(msg, "duplicate"):
		return codes.AlreadyExists
	case strings.Contains(msg, "insufficient"), strings.Contains(msg, "limit exceeded"),
		strings.Contains(msg, "not enough"):
		return codes.FailedPrecondition
	case strings.Contains(msg, "permission"), strings.Contains(msg, "forbidden"):
		return codes.PermissionDenied
	default:
		return codes.Internal
	}
}

type ExchangeGRPCHandler struct {
	pb.UnimplementedStockExchangeGRPCServiceServer
	svc *service.ExchangeService
}

func NewExchangeGRPCHandler(svc *service.ExchangeService) *ExchangeGRPCHandler {
	return &ExchangeGRPCHandler{svc: svc}
}

func (h *ExchangeGRPCHandler) ListExchanges(ctx context.Context, req *pb.ListExchangesRequest) (*pb.ListExchangesResponse, error) {
	exchanges, total, err := h.svc.ListExchanges(req.Search, int(req.Page), int(req.PageSize))
	if err != nil {
		return nil, status.Errorf(mapServiceError(err), "failed to list exchanges: %v", err)
	}

	resp := &pb.ListExchangesResponse{TotalCount: total}
	for _, ex := range exchanges {
		resp.Exchanges = append(resp.Exchanges, toExchangeProto(&ex))
	}
	return resp, nil
}

func (h *ExchangeGRPCHandler) GetExchange(ctx context.Context, req *pb.GetExchangeRequest) (*pb.Exchange, error) {
	ex, err := h.svc.GetExchange(req.Id)
	if err != nil {
		return nil, status.Errorf(mapServiceError(err), "exchange not found: %v", err)
	}
	return toExchangeProto(ex), nil
}

func (h *ExchangeGRPCHandler) SetTestingMode(ctx context.Context, req *pb.SetTestingModeRequest) (*pb.SetTestingModeResponse, error) {
	if err := h.svc.SetTestingMode(req.Enabled); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to set testing mode: %v", err)
	}
	return &pb.SetTestingModeResponse{TestingMode: req.Enabled}, nil
}

func (h *ExchangeGRPCHandler) GetTestingMode(ctx context.Context, req *pb.GetTestingModeRequest) (*pb.GetTestingModeResponse, error) {
	enabled := h.svc.GetTestingMode()
	return &pb.GetTestingModeResponse{TestingMode: enabled}, nil
}

func toExchangeProto(ex *model.StockExchange) *pb.Exchange {
	return &pb.Exchange{
		Id:              ex.ID,
		Name:            ex.Name,
		Acronym:         ex.Acronym,
		MicCode:         ex.MICCode,
		Polity:          ex.Polity,
		Currency:        ex.Currency,
		TimeZone:        ex.TimeZone,
		OpenTime:        ex.OpenTime,
		CloseTime:       ex.CloseTime,
		PreMarketOpen:   ex.PreMarketOpen,
		PostMarketClose: ex.PostMarketClose,
	}
}
```

**Fix import:** Add `"github.com/exbanka/stock-service/internal/model"` to imports.

- [ ] **Step 2: Commit**

```bash
git add stock-service/internal/handler/exchange_handler.go
git commit -m "feat(stock-service): add exchange gRPC handler"
```

---

### Task A9: Wire exchange service into main.go

**Files:**
- Modify: `stock-service/cmd/main.go`

- [ ] **Step 1: Update main.go with full wiring**

```go
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	pb "github.com/exbanka/contract/stockpb"
	shared "github.com/exbanka/contract/shared"
	"github.com/exbanka/stock-service/internal/config"
	"github.com/exbanka/stock-service/internal/handler"
	kafkaprod "github.com/exbanka/stock-service/internal/kafka"
	"github.com/exbanka/stock-service/internal/model"
	"github.com/exbanka/stock-service/internal/repository"
	"github.com/exbanka/stock-service/internal/service"
)

func main() {
	cfg := config.Load()

	db, err := gorm.Open(postgres.Open(cfg.DSN()), &gorm.Config{})
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	if err := db.AutoMigrate(
		&model.StockExchange{},
		&model.SystemSetting{},
	); err != nil {
		log.Fatalf("failed to migrate: %v", err)
	}

	producer := kafkaprod.NewProducer(cfg.KafkaBrokers)
	defer producer.Close()

	kafkaprod.EnsureTopics(cfg.KafkaBrokers,
		"stock.exchange-synced",
	)

	_ = producer // will be used by future services

	exchangeRepo := repository.NewExchangeRepository(db)
	settingRepo := repository.NewSystemSettingRepository(db)

	exchangeSvc := service.NewExchangeService(exchangeRepo, settingRepo)

	// Seed exchanges from CSV on startup
	csvPath := getEnv("EXCHANGE_CSV_PATH", "data/exchanges.csv")
	if err := exchangeSvc.SeedExchanges(csvPath); err != nil {
		log.Printf("WARN: failed to seed exchanges from CSV: %v", err)
	}

	exchangeHandler := handler.NewExchangeGRPCHandler(exchangeSvc)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterStockExchangeGRPCServiceServer(s, exchangeHandler)
	shared.RegisterHealthCheck(s, "stock-service")

	go func() {
		fmt.Printf("stock-service listening on %s\n", cfg.GRPCAddr)
		if err := s.Serve(lis); err != nil {
			log.Fatalf("gRPC server failed: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down stock-service...")
	s.GracefulStop()
	log.Println("stock-service stopped")
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

- [ ] **Step 2: Verify build**

```bash
cd stock-service && go build ./cmd
```

Expected: BUILD SUCCESS (proto must be generated first via `make proto`).

- [ ] **Step 3: Commit**

```bash
git add stock-service/cmd/main.go
git commit -m "feat(stock-service): wire exchange service into main with CSV seeding"
```

---

### Task A10: Add stock.proto to Makefile and generate

**Files:**
- Modify: `Makefile`

- [ ] **Step 1: Add stock/stock.proto to the proto target**

In the `proto:` target, add `stock/stock.proto` to the `protoc` command. Add `contract/stockpb` to the `mkdir`, `mv`, `rmdir`, and `clean` targets.

Also add `stock-service` to the `build`, `tidy`, `test`, and `clean` targets.

- [ ] **Step 2: Run make proto**

```bash
make proto
```

Expected: `contract/stockpb/*.pb.go` files generated successfully.

- [ ] **Step 3: Run make tidy**

```bash
make tidy
```

- [ ] **Step 4: Verify full build**

```bash
cd stock-service && go build ./cmd
```

Expected: BUILD SUCCESS.

- [ ] **Step 5: Commit**

```bash
git add Makefile contract/proto/stock/ contract/stockpb/
git commit -m "feat(contract): add stock.proto and update Makefile for stock-service"
```

---

### Task A11: Add stock-service to docker-compose.yml

**Files:**
- Modify: `docker-compose.yml`

- [ ] **Step 1: Add stock-db service**

Add under `services:`:

```yaml
  stock-db:
    image: postgres:17
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: postgres
      POSTGRES_DB: stock_db
    ports:
      - "5440:5432"
    volumes:
      - stock_db_data:/var/lib/postgresql/data
```

- [ ] **Step 2: Add stock-service**

```yaml
  stock-service:
    build:
      context: .
      dockerfile: stock-service/Dockerfile
    environment:
      STOCK_DB_HOST: stock-db
      STOCK_DB_PORT: "5432"
      STOCK_DB_USER: postgres
      STOCK_DB_PASSWORD: postgres
      STOCK_DB_NAME: stock_db
      STOCK_DB_SSLMODE: disable
      STOCK_GRPC_ADDR: ":50060"
      KAFKA_BROKERS: kafka:9092
      USER_GRPC_ADDR: user-service:50052
      ACCOUNT_GRPC_ADDR: account-service:50055
      EXCHANGE_GRPC_ADDR: exchange-service:50059
      EXCHANGE_CSV_PATH: data/exchanges.csv
    ports:
      - "50060:50060"
    depends_on:
      - stock-db
      - kafka
      - user-service
      - account-service
      - exchange-service
```

- [ ] **Step 3: Add STOCK_GRPC_ADDR to api-gateway environment**

```yaml
STOCK_GRPC_ADDR: stock-service:50060
```

- [ ] **Step 4: Add stock-service to api-gateway depends_on**

```yaml
- stock-service
```

- [ ] **Step 5: Add volume**

```yaml
  stock_db_data:
```

- [ ] **Step 6: Create stock-service/Dockerfile**

```dockerfile
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.work go.work.sum ./
COPY contract/ contract/
COPY stock-service/ stock-service/
WORKDIR /app/stock-service
RUN go build -o /bin/stock-service ./cmd

FROM alpine:3.20
COPY --from=builder /bin/stock-service /bin/stock-service
COPY stock-service/data/ /app/data/
WORKDIR /app
ENTRYPOINT ["/bin/stock-service"]
```

- [ ] **Step 7: Commit**

```bash
git add docker-compose.yml stock-service/Dockerfile
git commit -m "feat(docker): add stock-service and stock-db to docker-compose"
```

---

## Workstream B: Actuary Management in user-service

### Task B1: Create ActuaryLimit model

**Files:**
- Create: `user-service/internal/model/actuary_limit.go`

- [ ] **Step 1: Write the model**

```go
package model

import (
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// ActuaryLimit stores trading limits for employees with Agent or Supervisor roles.
// Supervisors always have NeedApproval=false and Limit is ignored for them.
type ActuaryLimit struct {
	ID           int64           `gorm:"primaryKey;autoIncrement" json:"id"`
	EmployeeID   int64           `gorm:"uniqueIndex;not null" json:"employee_id"`
	Limit        decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0" json:"limit"`
	UsedLimit    decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0" json:"used_limit"`
	NeedApproval bool            `gorm:"not null;default:false" json:"need_approval"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	Version      int64           `gorm:"not null;default:1" json:"version"`
}

func (a *ActuaryLimit) BeforeUpdate(tx *gorm.DB) error {
	tx.Statement.Where("version = ?", a.Version)
	a.Version++
	return nil
}
```

- [ ] **Step 2: Add to AutoMigrate in user-service/cmd/main.go**

Add `&model.ActuaryLimit{}` to the `db.AutoMigrate(...)` call.

- [ ] **Step 3: Commit**

```bash
git add user-service/internal/model/actuary_limit.go user-service/cmd/main.go
git commit -m "feat(user-service): add ActuaryLimit model with optimistic locking"
```

---

### Task B2: Create actuary repository

**Files:**
- Create: `user-service/internal/repository/actuary_repository.go`

- [ ] **Step 1: Write the repository**

```go
package repository

import (
	"github.com/exbanka/user-service/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ActuaryRepository struct {
	db *gorm.DB
}

func NewActuaryRepository(db *gorm.DB) *ActuaryRepository {
	return &ActuaryRepository{db: db}
}

func (r *ActuaryRepository) Create(limit *model.ActuaryLimit) error {
	return r.db.Create(limit).Error
}

func (r *ActuaryRepository) GetByID(id int64) (*model.ActuaryLimit, error) {
	var limit model.ActuaryLimit
	if err := r.db.First(&limit, id).Error; err != nil {
		return nil, err
	}
	return &limit, nil
}

func (r *ActuaryRepository) GetByEmployeeID(employeeID int64) (*model.ActuaryLimit, error) {
	var limit model.ActuaryLimit
	if err := r.db.Where("employee_id = ?", employeeID).First(&limit).Error; err != nil {
		return nil, err
	}
	return &limit, nil
}

func (r *ActuaryRepository) Upsert(limit *model.ActuaryLimit) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "employee_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"limit", "need_approval", "updated_at"}),
	}).Create(limit).Error
}

func (r *ActuaryRepository) Save(limit *model.ActuaryLimit) error {
	result := r.db.Save(limit)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound // optimistic lock conflict
	}
	return nil
}

// ListActuaries returns employees who have EmployeeAgent or EmployeeSupervisor
// role, along with their actuary limits. Filters by name/email/position.
func (r *ActuaryRepository) ListActuaries(search, position string, page, pageSize int) ([]ActuaryRow, int64, error) {
	var rows []ActuaryRow
	var total int64

	q := r.db.Table("employees").
		Select(`employees.id as employee_id, employees.first_name, employees.last_name,
			employees.email, employees.position,
			COALESCE(actuary_limits."limit", 0) as "limit",
			COALESCE(actuary_limits.used_limit, 0) as used_limit,
			COALESCE(actuary_limits.need_approval, false) as need_approval,
			actuary_limits.id as actuary_limit_id`).
		Joins("INNER JOIN employee_roles ON employee_roles.employee_id = employees.id").
		Joins("INNER JOIN roles ON roles.id = employee_roles.role_id").
		Joins("LEFT JOIN actuary_limits ON actuary_limits.employee_id = employees.id").
		Where("roles.name IN ?", []string{"EmployeeAgent", "EmployeeSupervisor"}).
		Group("employees.id, actuary_limits.id")

	if search != "" {
		like := "%" + search + "%"
		q = q.Where("employees.email ILIKE ? OR employees.first_name ILIKE ? OR employees.last_name ILIKE ?", like, like, like)
	}
	if position != "" {
		q = q.Where("employees.position ILIKE ?", "%"+position+"%")
	}

	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	if err := q.Offset((page - 1) * pageSize).Limit(pageSize).
		Order("employees.last_name ASC").Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ResetAllUsedLimits resets used_limit to 0 for all actuary limits.
// Used by the daily cron job. Skips optimistic locking.
func (r *ActuaryRepository) ResetAllUsedLimits() error {
	return r.db.Session(&gorm.Session{SkipHooks: true}).
		Model(&model.ActuaryLimit{}).
		Where("used_limit > 0").
		Update("used_limit", 0).Error
}

// ActuaryRow is the result of the ListActuaries join query.
type ActuaryRow struct {
	EmployeeID     int64   `gorm:"column:employee_id"`
	FirstName      string  `gorm:"column:first_name"`
	LastName       string  `gorm:"column:last_name"`
	Email          string  `gorm:"column:email"`
	Position       string  `gorm:"column:position"`
	Limit          float64 `gorm:"column:limit"`
	UsedLimit      float64 `gorm:"column:used_limit"`
	NeedApproval   bool    `gorm:"column:need_approval"`
	ActuaryLimitID *int64  `gorm:"column:actuary_limit_id"`
}
```

- [ ] **Step 2: Add interface to user-service/internal/service/interfaces.go**

```go
type ActuaryRepo interface {
	Create(limit *model.ActuaryLimit) error
	GetByID(id int64) (*model.ActuaryLimit, error)
	GetByEmployeeID(employeeID int64) (*model.ActuaryLimit, error)
	Upsert(limit *model.ActuaryLimit) error
	Save(limit *model.ActuaryLimit) error
	ListActuaries(search, position string, page, pageSize int) ([]repository.ActuaryRow, int64, error)
	ResetAllUsedLimits() error
}
```

**Note:** The interface references `repository.ActuaryRow`. To avoid a cyclic import, define the `ActuaryRow` struct in the `model` package instead, then both `repository` and `service` can reference it. Move `ActuaryRow` to `model/actuary_limit.go`.

- [ ] **Step 3: Commit**

```bash
git add user-service/internal/repository/actuary_repository.go \
        user-service/internal/model/actuary_limit.go \
        user-service/internal/service/interfaces.go
git commit -m "feat(user-service): add actuary repository with ListActuaries and ResetAllUsedLimits"
```

---

### Task B3: Create actuary service

**Files:**
- Create: `user-service/internal/service/actuary_service.go`

- [ ] **Step 1: Write the service**

```go
package service

import (
	"context"
	"errors"
	"log"

	"gorm.io/gorm"

	kafkamsg "github.com/exbanka/contract/kafka"
	kafkaprod "github.com/exbanka/user-service/internal/kafka"
	"github.com/exbanka/user-service/internal/model"
	"github.com/shopspring/decimal"
)

type ActuaryService struct {
	actuaryRepo ActuaryRepo
	empRepo     EmployeeRepo
	producer    *kafkaprod.Producer
}

func NewActuaryService(actuaryRepo ActuaryRepo, empRepo EmployeeRepo, producer *kafkaprod.Producer) *ActuaryService {
	return &ActuaryService{
		actuaryRepo: actuaryRepo,
		empRepo:     empRepo,
		producer:    producer,
	}
}

func (s *ActuaryService) ListActuaries(search, position string, page, pageSize int) ([]model.ActuaryRow, int64, error) {
	return s.actuaryRepo.ListActuaries(search, position, page, pageSize)
}

func (s *ActuaryService) GetActuaryInfo(employeeID int64) (*model.ActuaryLimit, *model.Employee, error) {
	emp, err := s.empRepo.GetByIDWithRoles(employeeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errors.New("employee not found")
		}
		return nil, nil, err
	}
	if !isActuary(emp) {
		return nil, nil, errors.New("employee is not an actuary (must have EmployeeAgent or EmployeeSupervisor role)")
	}

	limit, err := s.actuaryRepo.GetByEmployeeID(employeeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Auto-create default actuary limit
			limit = &model.ActuaryLimit{
				EmployeeID:   employeeID,
				Limit:        decimal.NewFromInt(0),
				UsedLimit:    decimal.NewFromInt(0),
				NeedApproval: !isSupervisor(emp), // agents default to need approval
			}
			if err := s.actuaryRepo.Create(limit); err != nil {
				return nil, nil, err
			}
		} else {
			return nil, nil, err
		}
	}
	return limit, emp, nil
}

func (s *ActuaryService) SetActuaryLimit(ctx context.Context, id int64, limitAmount decimal.Decimal) (*model.ActuaryLimit, error) {
	if limitAmount.IsNegative() {
		return nil, errors.New("limit must not be negative")
	}
	limit, err := s.actuaryRepo.GetByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("actuary limit not found")
		}
		return nil, err
	}
	limit.Limit = limitAmount
	if err := s.actuaryRepo.Save(limit); err != nil {
		return nil, err
	}
	s.publishActuaryEvent(ctx, limit.EmployeeID, "limit_set")
	return limit, nil
}

func (s *ActuaryService) ResetUsedLimit(ctx context.Context, id int64) (*model.ActuaryLimit, error) {
	limit, err := s.actuaryRepo.GetByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("actuary limit not found")
		}
		return nil, err
	}
	limit.UsedLimit = decimal.NewFromInt(0)
	if err := s.actuaryRepo.Save(limit); err != nil {
		return nil, err
	}
	s.publishActuaryEvent(ctx, limit.EmployeeID, "used_limit_reset")
	return limit, nil
}

func (s *ActuaryService) SetNeedApproval(ctx context.Context, id int64, needApproval bool) (*model.ActuaryLimit, error) {
	limit, err := s.actuaryRepo.GetByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("actuary limit not found")
		}
		return nil, err
	}
	limit.NeedApproval = needApproval
	if err := s.actuaryRepo.Save(limit); err != nil {
		return nil, err
	}
	s.publishActuaryEvent(ctx, limit.EmployeeID, "need_approval_changed")
	return limit, nil
}

// UpdateUsedLimit atomically adds the given RSD amount to the actuary's used limit.
// Called by stock-service after an order is placed.
func (s *ActuaryService) UpdateUsedLimit(ctx context.Context, id int64, amountRSD decimal.Decimal) (*model.ActuaryLimit, error) {
	limit, err := s.actuaryRepo.GetByID(id)
	if err != nil {
		return nil, err
	}
	limit.UsedLimit = limit.UsedLimit.Add(amountRSD)
	if err := s.actuaryRepo.Save(limit); err != nil {
		return nil, err
	}
	return limit, nil
}

func (s *ActuaryService) publishActuaryEvent(ctx context.Context, employeeID int64, action string) {
	if s.producer == nil {
		return
	}
	msg := kafkamsg.ActuaryLimitUpdatedMessage{
		EmployeeID: employeeID,
		Action:     action,
	}
	if err := s.producer.PublishActuaryLimitUpdated(ctx, msg); err != nil {
		log.Printf("warn: failed to publish actuary-limit-updated event: %v", err)
	}
}

func isActuary(emp *model.Employee) bool {
	for _, r := range emp.Roles {
		if r.Name == "EmployeeAgent" || r.Name == "EmployeeSupervisor" || r.Name == "EmployeeAdmin" {
			return true
		}
	}
	return false
}

func isSupervisor(emp *model.Employee) bool {
	for _, r := range emp.Roles {
		if r.Name == "EmployeeSupervisor" || r.Name == "EmployeeAdmin" {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Commit**

```bash
git add user-service/internal/service/actuary_service.go
git commit -m "feat(user-service): add actuary service with limit management"
```

---

### Task B4: Add Kafka messages and producer methods for actuary events

**Files:**
- Modify: `contract/kafka/messages.go`
- Modify: `user-service/internal/kafka/producer.go`

- [ ] **Step 1: Add message types to contract/kafka/messages.go**

```go
// Actuary events
const (
	TopicActuaryLimitUpdated = "user.actuary-limit-updated"
)

type ActuaryLimitUpdatedMessage struct {
	EmployeeID int64  `json:"employee_id"`
	Action     string `json:"action"` // limit_set, used_limit_reset, need_approval_changed
}
```

- [ ] **Step 2: Add publish method to user-service/internal/kafka/producer.go**

```go
func (p *Producer) PublishActuaryLimitUpdated(ctx context.Context, msg kafkamsg.ActuaryLimitUpdatedMessage) error {
	return p.publish(ctx, kafkamsg.TopicActuaryLimitUpdated, msg)
}
```

- [ ] **Step 3: Add topic to EnsureTopics in user-service/cmd/main.go**

Add `"user.actuary-limit-updated"` to the `kafkaprod.EnsureTopics(...)` call.

- [ ] **Step 4: Commit**

```bash
git add contract/kafka/messages.go user-service/internal/kafka/producer.go user-service/cmd/main.go
git commit -m "feat(contract): add actuary limit Kafka events and producer method"
```

---

### Task B5: Create actuary gRPC handler

**Files:**
- Create: `user-service/internal/handler/actuary_handler.go`

**Depends on:** Plan 1, Task 3 (ActuaryService added to user.proto and generated).

- [ ] **Step 1: Write the handler**

```go
package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/exbanka/contract/userpb"
	"github.com/exbanka/user-service/internal/service"
	"github.com/shopspring/decimal"
)

type ActuaryGRPCHandler struct {
	pb.UnimplementedActuaryServiceServer
	svc *service.ActuaryService
}

func NewActuaryGRPCHandler(svc *service.ActuaryService) *ActuaryGRPCHandler {
	return &ActuaryGRPCHandler{svc: svc}
}

func (h *ActuaryGRPCHandler) ListActuaries(ctx context.Context, req *pb.ListActuariesRequest) (*pb.ListActuariesResponse, error) {
	rows, total, err := h.svc.ListActuaries(req.Search, req.Position, int(req.Page), int(req.PageSize))
	if err != nil {
		return nil, status.Errorf(mapServiceError(err), "failed to list actuaries: %v", err)
	}

	resp := &pb.ListActuariesResponse{TotalCount: total}
	for _, r := range rows {
		role := "agent"
		// Determination of agent vs supervisor based on position or query
		resp.Actuaries = append(resp.Actuaries, &pb.ActuaryInfo{
			Id:           r.ActuaryLimitID(),
			EmployeeId:   uint64(r.EmployeeID),
			FirstName:    r.FirstName,
			LastName:     r.LastName,
			Email:        r.Email,
			Role:         role,
			Limit:        decimal.NewFromFloat(r.Limit).String(),
			UsedLimit:    decimal.NewFromFloat(r.UsedLimit).String(),
			NeedApproval: r.NeedApproval,
		})
	}
	return resp, nil
}

func (h *ActuaryGRPCHandler) GetActuaryInfo(ctx context.Context, req *pb.GetActuaryInfoRequest) (*pb.ActuaryInfo, error) {
	limit, emp, err := h.svc.GetActuaryInfo(int64(req.EmployeeId))
	if err != nil {
		return nil, status.Errorf(mapServiceError(err), "%v", err)
	}
	role := "agent"
	for _, r := range emp.Roles {
		if r.Name == "EmployeeSupervisor" || r.Name == "EmployeeAdmin" {
			role = "supervisor"
			break
		}
	}
	return &pb.ActuaryInfo{
		Id:           uint64(limit.ID),
		EmployeeId:   uint64(limit.EmployeeID),
		FirstName:    emp.FirstName,
		LastName:     emp.LastName,
		Email:        emp.Email,
		Role:         role,
		Limit:        limit.Limit.String(),
		UsedLimit:    limit.UsedLimit.String(),
		NeedApproval: limit.NeedApproval,
	}, nil
}

func (h *ActuaryGRPCHandler) SetActuaryLimit(ctx context.Context, req *pb.SetActuaryLimitRequest) (*pb.ActuaryInfo, error) {
	limitVal, err := decimal.NewFromString(req.Limit)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid limit value: %v", err)
	}
	result, err := h.svc.SetActuaryLimit(ctx, int64(req.Id), limitVal)
	if err != nil {
		return nil, status.Errorf(mapServiceError(err), "%v", err)
	}
	return toActuaryInfoFromLimit(result), nil
}

func (h *ActuaryGRPCHandler) ResetActuaryUsedLimit(ctx context.Context, req *pb.ResetActuaryUsedLimitRequest) (*pb.ActuaryInfo, error) {
	result, err := h.svc.ResetUsedLimit(ctx, int64(req.Id))
	if err != nil {
		return nil, status.Errorf(mapServiceError(err), "%v", err)
	}
	return toActuaryInfoFromLimit(result), nil
}

func (h *ActuaryGRPCHandler) SetNeedApproval(ctx context.Context, req *pb.SetNeedApprovalRequest) (*pb.ActuaryInfo, error) {
	result, err := h.svc.SetNeedApproval(ctx, int64(req.Id), req.NeedApproval)
	if err != nil {
		return nil, status.Errorf(mapServiceError(err), "%v", err)
	}
	return toActuaryInfoFromLimit(result), nil
}

func (h *ActuaryGRPCHandler) UpdateUsedLimit(ctx context.Context, req *pb.UpdateUsedLimitRequest) (*pb.ActuaryInfo, error) {
	amount, err := decimal.NewFromString(req.Amount)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid amount: %v", err)
	}
	result, err := h.svc.UpdateUsedLimit(ctx, int64(req.Id), amount)
	if err != nil {
		return nil, status.Errorf(mapServiceError(err), "%v", err)
	}
	return toActuaryInfoFromLimit(result), nil
}

// toActuaryInfoFromLimit builds a minimal ActuaryInfo from just the limit record.
// Employee name/email fields are empty — callers that need full info should use GetActuaryInfo.
func toActuaryInfoFromLimit(limit *model.ActuaryLimit) *pb.ActuaryInfo {
	return &pb.ActuaryInfo{
		Id:           uint64(limit.ID),
		EmployeeId:   uint64(limit.EmployeeID),
		Limit:        limit.Limit.String(),
		UsedLimit:    limit.UsedLimit.String(),
		NeedApproval: limit.NeedApproval,
	}
}
```

**Note:** Add `"github.com/exbanka/user-service/internal/model"` to imports for `toActuaryInfoFromLimit`. The `ActuaryRow.ActuaryLimitID()` helper method needs to be defined on the model. Add this method to `model/actuary_limit.go`:

```go
// ActuaryLimitID returns the actuary_limit_id or 0 if nil.
func (r ActuaryRow) ActuaryLimitID() uint64 {
	if r.ActuaryLimitIDPtr != nil {
		return uint64(*r.ActuaryLimitIDPtr)
	}
	return 0
}
```

And rename the struct field from `ActuaryLimitID` to `ActuaryLimitIDPtr` to avoid the method name conflict:

```go
type ActuaryRow struct {
	EmployeeID        int64   `gorm:"column:employee_id"`
	FirstName         string  `gorm:"column:first_name"`
	LastName          string  `gorm:"column:last_name"`
	Email             string  `gorm:"column:email"`
	Position          string  `gorm:"column:position"`
	Limit             float64 `gorm:"column:limit"`
	UsedLimit         float64 `gorm:"column:used_limit"`
	NeedApproval      bool    `gorm:"column:need_approval"`
	ActuaryLimitIDPtr *int64  `gorm:"column:actuary_limit_id"`
}
```

- [ ] **Step 2: Commit**

```bash
git add user-service/internal/handler/actuary_handler.go \
        user-service/internal/model/actuary_limit.go
git commit -m "feat(user-service): add actuary gRPC handler"
```

---

### Task B6: Create actuary daily reset cron job

**Files:**
- Create: `user-service/internal/service/actuary_cron.go`

- [ ] **Step 1: Write the cron service**

```go
package service

import (
	"context"
	"log"
	"time"
)

// ActuaryCronService runs the daily actuary used_limit reset at 23:59.
type ActuaryCronService struct {
	repo ActuaryRepo
}

func NewActuaryCronService(repo ActuaryRepo) *ActuaryCronService {
	return &ActuaryCronService{repo: repo}
}

func (s *ActuaryCronService) Start(ctx context.Context) {
	go s.runDailyReset(ctx)
}

func (s *ActuaryCronService) runDailyReset(ctx context.Context) {
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 0, 0, now.Location())
		if !now.Before(next) {
			next = next.Add(24 * time.Hour)
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := s.repo.ResetAllUsedLimits(); err != nil {
			log.Printf("error resetting actuary daily used limits: %v", err)
		} else {
			log.Println("actuary daily used limits reset completed")
		}
	}
}
```

- [ ] **Step 2: Commit**

```bash
git add user-service/internal/service/actuary_cron.go
git commit -m "feat(user-service): add actuary daily used_limit reset cron job"
```

---

### Task B7: Wire actuary service into user-service main.go

**Files:**
- Modify: `user-service/cmd/main.go`

- [ ] **Step 1: Add actuary wiring**

After the existing `limitCron.Start(ctx)` line, add:

```go
actuaryRepo := repository.NewActuaryRepository(db)
actuarySvc := service.NewActuaryService(actuaryRepo, repo, producer)
actuaryHandler := handler.NewActuaryGRPCHandler(actuarySvc)

actuaryCron := service.NewActuaryCronService(actuaryRepo)
actuaryCron.Start(ctx)
```

After `pb.RegisterEmployeeLimitServiceServer(s, limitHandler)`, add:

```go
pb.RegisterActuaryServiceServer(s, actuaryHandler)
```

- [ ] **Step 2: Verify build**

```bash
cd user-service && go build ./cmd
```

Expected: BUILD SUCCESS (proto must be regenerated first with ActuaryService).

- [ ] **Step 3: Commit**

```bash
git add user-service/cmd/main.go
git commit -m "feat(user-service): wire actuary service, handler, and cron into main"
```

---

### Task B8: Add new permissions to role seeds

**Files:**
- Modify: `user-service/internal/service/role_service.go`

- [ ] **Step 1: Add permission entries**

Add to `AllPermissions` after `// Agent/OTC/Funds` block:

```go
// Stock trading operations
{"orders.approve", "Approve/decline stock trading orders", "orders"},
{"tax.manage", "Manage capital gains tax collection", "tax"},
{"exchanges.manage", "Manage stock exchange settings and testing mode", "exchanges"},
```

- [ ] **Step 2: Add to EmployeeSupervisor and EmployeeAdmin roles**

Add `"orders.approve", "tax.manage", "exchanges.manage"` to `DefaultRolePermissions["EmployeeSupervisor"]` and `DefaultRolePermissions["EmployeeAdmin"]`.

- [ ] **Step 3: Verify build**

```bash
cd user-service && go build ./cmd
```

- [ ] **Step 4: Commit**

```bash
git add user-service/internal/service/role_service.go
git commit -m "feat(user-service): add orders.approve, tax.manage, exchanges.manage permissions"
```

---

## Workstream C: Integration (depends on A + B)

### Task C1: Wire gateway clients and routes

**Files:**
- Create: `api-gateway/internal/grpc/stock_client.go`
- Create: `api-gateway/internal/handler/stock_exchange_handler.go`
- Create: `api-gateway/internal/handler/actuary_handler.go`
- Modify: `api-gateway/internal/router/router.go`
- Modify: `api-gateway/cmd/main.go`

This task implements the gateway side from Plan 1 Tasks 5, 6, 9 (actuary part), and 10. The exact handler code is already defined in Plan 1 — copy it verbatim.

- [ ] **Step 1: Create stock_client.go**

Create `api-gateway/internal/grpc/stock_client.go` with `NewStockExchangeClient` and all other constructors from Plan 1, Task 5.

- [ ] **Step 2: Create stock_exchange_handler.go**

Copy from Plan 1, Task 6. This handler calls `stockpb.StockExchangeGRPCServiceClient`.

- [ ] **Step 3: Create actuary_handler.go**

Copy from Plan 1, Task 9 Step 2. This handler calls `userpb.ActuaryServiceClient`.

- [ ] **Step 4: Update router.go**

Add to the `Setup()` function signature:
```go
stockExchangeClient stockpb.StockExchangeGRPCServiceClient,
actuaryClient userpb.ActuaryServiceClient,
```

Add handler instantiation and route registration:
- `/api/stock-exchanges` group with AnyAuthMiddleware
- `/api/stock-exchanges/testing-mode` with AuthMiddleware + `exchanges.manage`
- `/api/actuaries` group with AuthMiddleware + `agents.manage`

(Full route definitions are in Plan 1, Task 10.)

- [ ] **Step 5: Update api-gateway/cmd/main.go**

Add `STOCK_GRPC_ADDR` env var, create gRPC clients, pass to `router.Setup(...)`.

- [ ] **Step 6: Verify build**

```bash
cd api-gateway && go build ./cmd
```

- [ ] **Step 7: Commit**

```bash
git add api-gateway/internal/grpc/stock_client.go \
        api-gateway/internal/handler/stock_exchange_handler.go \
        api-gateway/internal/handler/actuary_handler.go \
        api-gateway/internal/router/router.go \
        api-gateway/cmd/main.go
git commit -m "feat(api-gateway): wire stock exchange and actuary routes"
```

---

### Task C2: Write exchange integration tests

**Files:**
- Create: `test-app/workflows/stock_exchange_test.go`

- [ ] **Step 1: Write exchange tests**

Copy the full test file from Plan 1, Task 13. It includes:
- `TestStockExchange_ListExchanges` — verifies 200 + response shape
- `TestStockExchange_ListExchanges_Unauthenticated` — verifies 401
- `TestStockExchange_ListExchanges_SearchFilter` — search by "NYSE"
- `TestStockExchange_GetExchange` — get by ID from listed exchanges
- `TestStockExchange_GetExchange_NotFound` — 404 for nonexistent ID
- `TestStockExchange_TestingMode_SetAndGet` — toggle on/off
- `TestStockExchange_TestingMode_RequiresSupervisor` — agent gets 403

- [ ] **Step 2: Commit**

```bash
git add test-app/workflows/stock_exchange_test.go
git commit -m "test(test-app): add stock exchange integration tests"
```

---

### Task C3: Write actuary integration tests

**Files:**
- Create: `test-app/workflows/actuary_test.go`
- Create: `test-app/workflows/stock_helpers_test.go`

- [ ] **Step 1: Write stock helpers**

Copy `setupAgentEmployee`, `setupSupervisorEmployee` from Plan 1, Task 12.

- [ ] **Step 2: Write actuary tests**

Copy from Plan 1, Task 16 Step 3. Tests include:
- `TestActuary_ListActuaries` — supervisor can list
- `TestActuary_ListActuaries_AgentCannot` — agent gets 403
- `TestActuary_SetLimit` — supervisor sets agent limit
- `TestActuary_SetLimit_EmptyValue` — 400 for empty limit
- `TestActuary_ResetLimit` — supervisor resets agent used limit
- `TestActuary_SetNeedApproval` — supervisor toggles approval flag
- `TestActuary_Unauthenticated` — 401 for no token

- [ ] **Step 3: Add FormatID helper to test-app**

Add to `test-app/internal/helpers/random.go`:
```go
import "strconv"

func FormatID(id int) string {
	return strconv.Itoa(id)
}
```

- [ ] **Step 4: Commit**

```bash
git add test-app/workflows/stock_helpers_test.go \
        test-app/workflows/actuary_test.go \
        test-app/internal/helpers/random.go
git commit -m "test(test-app): add actuary integration tests and stock helpers"
```

---

### Task C4: Add proto definitions (if not done in Plan 1)

This task ensures the proto files exist and are generated. If Plan 1 tasks have already been executed, skip this.

**Files:**
- Create: `contract/proto/stock/stock.proto` (exchange-related messages only, or full file from Plan 1)
- Modify: `contract/proto/user/user.proto` (add ActuaryService)

- [ ] **Step 1: Create stock.proto**

Copy the full proto file from Plan 1, Task 2 Step 1.

- [ ] **Step 2: Add ActuaryService to user.proto**

Append the ActuaryService definition from Plan 1, Task 3 Step 1.

- [ ] **Step 3: Run make proto**

```bash
make proto
```

Expected: All proto files compile. `contract/stockpb/` and `contract/userpb/` updated.

- [ ] **Step 4: Run make tidy**

```bash
make tidy
```

- [ ] **Step 5: Commit**

```bash
git add contract/proto/ contract/stockpb/ contract/userpb/
git commit -m "feat(contract): generate stock.proto and ActuaryService proto code"
```

---

## Execution Order

Tasks can be parallelized as follows:

```
A1 → A2 → A3 → A4 → A5 → A6 → A7 → A8 → A9  (stock-service)
                                                  \
B1 → B2 → B3 → B4 → B5 → B6 → B7 → B8          → C4 → C1 → C2 + C3
                                                  /
A10 (Makefile + proto gen)  ─────────────────────/
A11 (docker-compose)  ──────────────────────────/
```

**Critical path:** C4 (proto gen) must complete before C1 (gateway wiring) can build. A10 must complete before any code that imports `stockpb` can compile.

**Recommended execution:** Run A1–A6 and B1–B4 in parallel, then A7–A9 and B5–B8 in parallel, then A10+A11+C4, then C1–C3.
