# Stock Trading API Surface & Service Scaffold Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Define the complete REST API surface, gRPC contracts, test-app acceptance tests, and service scaffolding for the stock trading feature set (exchanges, securities, listings, orders, portfolio, OTC, actuaries, tax).

**Architecture:** Single `stock-service` (gRPC port 50060, DB port 5440) handles all trading domain logic. Actuary limits extend `user-service` (new `ActuaryService` in `user.proto`). API Gateway exposes REST endpoints following existing patterns. This plan is the first of 6 — it defines the external contract. Plans 2–6 (one per requirement file) implement the internals.

**Tech Stack:** Go 1.26, gRPC/Protobuf, Gin HTTP, GORM/PostgreSQL, Kafka (segmentio/kafka-go), Docker Compose

---

## Architecture Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Service count | 1 (`stock-service`) | User preference; keeps trading domain cohesive |
| Actuary limits | In `user-service` | Extends Employee model; stock-service calls via gRPC |
| ForexPair listings | Approach 2 (listing per exchange) | User decision |
| Forex order execution | Through Order flow, instant execution | Consistent with other order types |
| API structure | Separate endpoints per security type | Different fields/filters per type |
| Data sourcing | External APIs on startup → save locally; testing mode uses cached data | User decision |
| Testing mode toggle | Global (all exchanges) | User decision |
| Roles | Use existing `EmployeeAgent`/`EmployeeSupervisor` + new permissions | No isAgent/isSupervisor flags; role list on employee suffices |

## New Permissions (add to `user-service/internal/service/role_service.go`)

| Code | Description | Category | Roles |
|------|-------------|----------|-------|
| `orders.approve` | Approve/decline stock orders | orders | EmployeeSupervisor, EmployeeAdmin |
| `tax.manage` | Manage capital gains tax collection | tax | EmployeeSupervisor, EmployeeAdmin |
| `exchanges.manage` | Manage exchange settings/testing mode | exchanges | EmployeeSupervisor, EmployeeAdmin |

## Master Endpoint Table

### Exchanges (stock exchanges — not currency exchange rates)

| Method | Path | Auth | Permission | Description |
|--------|------|------|------------|-------------|
| GET | `/api/stock-exchanges` | AnyAuth | — | List stock exchanges |
| GET | `/api/stock-exchanges/:id` | AnyAuth | — | Get exchange detail |
| POST | `/api/stock-exchanges/testing-mode` | Auth | `exchanges.manage` | Toggle global testing mode |
| GET | `/api/stock-exchanges/testing-mode` | Auth | `exchanges.manage` | Get testing mode status |

### Securities

| Method | Path | Auth | Permission | Description |
|--------|------|------|------------|-------------|
| GET | `/api/securities/stocks` | AnyAuth | — | List stocks with listing data |
| GET | `/api/securities/stocks/:id` | AnyAuth | — | Stock detail + options chain |
| GET | `/api/securities/stocks/:id/history` | AnyAuth | — | Stock daily price history |
| GET | `/api/securities/futures` | AnyAuth | — | List futures with listing data |
| GET | `/api/securities/futures/:id` | AnyAuth | — | Futures detail |
| GET | `/api/securities/futures/:id/history` | AnyAuth | — | Futures daily price history |
| GET | `/api/securities/forex` | AnyAuth | — | List forex pairs with listing data |
| GET | `/api/securities/forex/:id` | AnyAuth | — | Forex pair detail |
| GET | `/api/securities/forex/:id/history` | AnyAuth | — | Forex pair daily price history |
| GET | `/api/securities/options` | AnyAuth | — | List options (requires `?stock_id=N`) |
| GET | `/api/securities/options/:id` | AnyAuth | — | Option detail |

### Orders

| Method | Path | Auth | Permission | Description |
|--------|------|------|------------|-------------|
| POST | `/api/me/orders` | AnyAuth | — | Create buy/sell order |
| GET | `/api/me/orders` | AnyAuth | — | List my orders |
| GET | `/api/me/orders/:id` | AnyAuth | — | Get my order detail |
| POST | `/api/me/orders/:id/cancel` | AnyAuth | — | Cancel unfilled order |
| GET | `/api/orders` | Auth | `orders.approve` | List all orders (supervisor) |
| POST | `/api/orders/:id/approve` | Auth | `orders.approve` | Approve order |
| POST | `/api/orders/:id/decline` | Auth | `orders.approve` | Decline order |

### Portfolio

| Method | Path | Auth | Permission | Description |
|--------|------|------|------------|-------------|
| GET | `/api/me/portfolio` | AnyAuth | — | List my holdings |
| GET | `/api/me/portfolio/summary` | AnyAuth | — | Profit + tax summary |
| POST | `/api/me/portfolio/:id/make-public` | AnyAuth | — | Make shares public for OTC |
| POST | `/api/me/portfolio/:id/exercise` | AnyAuth | — | Exercise an option |

### OTC Trading

| Method | Path | Auth | Permission | Description |
|--------|------|------|------------|-------------|
| GET | `/api/otc/offers` | AnyAuth | — | List public OTC offers |
| POST | `/api/otc/offers/:id/buy` | AnyAuth | — | Buy from OTC offer |

### Actuaries (user-service extension)

| Method | Path | Auth | Permission | Description |
|--------|------|------|------------|-------------|
| GET | `/api/actuaries` | Auth | `agents.manage` | List actuaries (agents) |
| PUT | `/api/actuaries/:id/limit` | Auth | `agents.manage` | Set actuary limit |
| POST | `/api/actuaries/:id/reset-limit` | Auth | `agents.manage` | Reset used limit to 0 |
| PUT | `/api/actuaries/:id/approval` | Auth | `agents.manage` | Set need-approval flag |

### Tax

| Method | Path | Auth | Permission | Description |
|--------|------|------|------------|-------------|
| GET | `/api/tax` | Auth | `tax.manage` | List users with tax info |
| POST | `/api/tax/collect` | Auth | `tax.manage` | Trigger tax collection |

## Request/Response Shapes

### Exchanges

**GET /api/stock-exchanges** `?search=NYSE&page=1&page_size=10`
```json
{
  "exchanges": [
    {
      "id": 1,
      "name": "New York Stock Exchange",
      "acronym": "NYSE",
      "mic_code": "XNYS",
      "polity": "United States",
      "currency": "USD",
      "time_zone": "+5",
      "open_time": "09:30",
      "close_time": "16:00",
      "pre_market_open": "04:00",
      "post_market_close": "20:00"
    }
  ],
  "total_count": 90
}
```

**POST /api/stock-exchanges/testing-mode**
Request: `{ "enabled": true }`
Response: `{ "testing_mode": true }`

### Securities — Stocks

**GET /api/securities/stocks** `?search=AAPL&exchange_acronym=NYSE&min_price=10&max_price=500&min_volume=1000&max_volume=999999&sort_by=price&sort_order=asc&page=1&page_size=10`
```json
{
  "stocks": [
    {
      "id": 1,
      "ticker": "AAPL",
      "name": "Apple Inc.",
      "outstanding_shares": 3000000,
      "dividend_yield": "0.052",
      "listing_id": 1,
      "exchange_acronym": "NYSE",
      "price": "165.00",
      "high": "167.50",
      "low": "163.20",
      "change": "-2.30",
      "volume": 50000,
      "initial_margin_cost": "90.75",
      "last_refresh": "2026-03-31T15:30:00Z"
    }
  ],
  "total_count": 100
}
```

**GET /api/securities/stocks/:id** — full detail + options chain
```json
{
  "id": 1,
  "ticker": "AAPL",
  "name": "Apple Inc.",
  "outstanding_shares": 3000000,
  "dividend_yield": "0.052",
  "market_cap": "495000000.00",
  "listing": {
    "id": 1,
    "exchange_id": 1,
    "exchange_acronym": "NYSE",
    "price": "165.00",
    "high": "167.50",
    "low": "163.20",
    "change": "-2.30",
    "change_percent": "-1.37",
    "volume": 50000,
    "initial_margin_cost": "90.75",
    "last_refresh": "2026-03-31T15:30:00Z"
  },
  "options": [
    {
      "id": 1,
      "ticker": "AAPL260927C00165000",
      "option_type": "call",
      "strike_price": "165.00",
      "premium": "5.50",
      "implied_volatility": "0.25",
      "open_interest": 565,
      "settlement_date": "2026-09-27"
    }
  ]
}
```

**GET /api/securities/stocks/:id/history** `?period=month&page=1&page_size=30`
```json
{
  "history": [
    {
      "date": "2026-03-30",
      "price": "163.50",
      "high": "165.00",
      "low": "162.00",
      "change": "-1.50",
      "volume": 45000
    }
  ],
  "total_count": 30
}
```

### Securities — Futures

**GET /api/securities/futures** `?search=CL&exchange_acronym=NYM&settlement_date_from=2026-04-01&settlement_date_to=2026-12-31&sort_by=price&sort_order=desc&page=1&page_size=10`
```json
{
  "futures": [
    {
      "id": 1,
      "ticker": "CLJ26",
      "name": "Crude Oil Futures",
      "contract_size": 5000,
      "contract_unit": "barrel",
      "settlement_date": "2026-04-30",
      "listing_id": 5,
      "exchange_acronym": "NYM",
      "price": "72.50",
      "high": "73.20",
      "low": "71.80",
      "change": "0.70",
      "volume": 120000,
      "initial_margin_cost": "39875.00",
      "last_refresh": "2026-03-31T15:30:00Z"
    }
  ],
  "total_count": 50
}
```

### Securities — Forex

**GET /api/securities/forex** `?search=EUR&base_currency=EUR&quote_currency=USD&liquidity=high&sort_by=price&page=1&page_size=10`
```json
{
  "forex_pairs": [
    {
      "id": 1,
      "ticker": "EUR/USD",
      "name": "Euro to US Dollar",
      "base_currency": "EUR",
      "quote_currency": "USD",
      "exchange_rate": "1.1123",
      "liquidity": "high",
      "contract_size": 1000,
      "listing_id": 100,
      "exchange_acronym": "FOREX",
      "price": "1.1123",
      "high": "1.1150",
      "low": "1.1100",
      "change": "0.0023",
      "volume": 500000,
      "initial_margin_cost": "122.35",
      "last_refresh": "2026-03-31T15:30:00Z"
    }
  ],
  "total_count": 56
}
```

### Securities — Options

**GET /api/securities/options** `?stock_id=1&option_type=call&settlement_date=2026-09-27&min_strike=100&max_strike=200&page=1&page_size=20`
```json
{
  "options": [
    {
      "id": 1,
      "ticker": "AAPL260927C00165000",
      "name": "Apple Call Option Sep 2026",
      "stock_ticker": "AAPL",
      "stock_listing_id": 1,
      "option_type": "call",
      "strike_price": "165.00",
      "implied_volatility": "0.25",
      "premium": "5.50",
      "open_interest": 565,
      "settlement_date": "2026-09-27",
      "contract_size": 100,
      "initial_margin_cost": "9075.00"
    }
  ],
  "total_count": 200
}
```

### Orders

**POST /api/me/orders** — Create Buy Order
```json
{
  "listing_id": 1,
  "direction": "buy",
  "order_type": "market",
  "quantity": 10,
  "limit_value": null,
  "stop_value": null,
  "all_or_none": false,
  "margin": false,
  "account_id": 5
}
```

**POST /api/me/orders** — Create Sell Order
```json
{
  "holding_id": 15,
  "direction": "sell",
  "order_type": "limit",
  "quantity": 5,
  "limit_value": "170.00",
  "stop_value": null,
  "all_or_none": false,
  "margin": false
}
```

**Order Response** (create / get / approve / decline):
```json
{
  "id": 1,
  "user_id": 5,
  "listing_id": 1,
  "holding_id": null,
  "security_type": "stock",
  "ticker": "AAPL",
  "direction": "buy",
  "order_type": "market",
  "quantity": 10,
  "contract_size": 1,
  "price_per_unit": "165.00",
  "approximate_price": "1650.00",
  "commission": "7.00",
  "status": "approved",
  "approved_by": "no need for approval",
  "is_done": false,
  "remaining_portions": 10,
  "after_hours": false,
  "all_or_none": false,
  "margin": false,
  "account_id": 5,
  "last_modification": "2026-03-31T15:30:00Z",
  "created_at": "2026-03-31T15:30:00Z"
}
```

**GET /api/me/orders/:id** — includes transaction history:
```json
{
  "...all order fields above...",
  "transactions": [
    {
      "id": 1,
      "quantity": 3,
      "price_per_unit": "165.20",
      "total_price": "495.60",
      "executed_at": "2026-03-31T15:35:00Z"
    }
  ]
}
```

**GET /api/me/orders** `?status=approved&direction=buy&order_type=market&page=1&page_size=10`
**GET /api/orders** `?status=pending&agent_email=agent@bank.com&page=1&page_size=10`

### Portfolio

**GET /api/me/portfolio** `?security_type=stock&page=1&page_size=10`
```json
{
  "holdings": [
    {
      "id": 1,
      "security_type": "stock",
      "ticker": "AAPL",
      "name": "Apple Inc.",
      "quantity": 30,
      "average_price": "160.00",
      "current_price": "165.00",
      "profit": "150.00",
      "public_quantity": 0,
      "account_id": 5,
      "last_modified": "2026-03-31T15:30:00Z"
    }
  ],
  "total_count": 5
}
```

**GET /api/me/portfolio/summary**
```json
{
  "total_profit": "1500.00",
  "total_profit_rsd": "175500.00",
  "tax_paid_this_year": "225.00",
  "tax_unpaid_this_month": "75.00"
}
```

**POST /api/me/portfolio/:id/make-public**
Request: `{ "quantity": 10 }`
Response: `{ "id": 1, "public_quantity": 10 }`

**POST /api/me/portfolio/:id/exercise**
Response:
```json
{
  "id": 1,
  "option_ticker": "AAPL260927C00165000",
  "exercised_quantity": 2,
  "shares_affected": 200,
  "profit": "700.00"
}
```

### OTC

**GET /api/otc/offers** `?security_type=stock&ticker=AAPL&page=1&page_size=10`
```json
{
  "offers": [
    {
      "id": 1,
      "seller_id": 5,
      "seller_name": "John Doe",
      "security_type": "stock",
      "ticker": "AAPL",
      "name": "Apple Inc.",
      "quantity": 10,
      "price_per_unit": "165.00",
      "created_at": "2026-03-31T15:30:00Z"
    }
  ],
  "total_count": 15
}
```

**POST /api/otc/offers/:id/buy**
Request: `{ "quantity": 5, "account_id": 5 }`
Response:
```json
{
  "id": 1,
  "offer_id": 1,
  "quantity": 5,
  "price_per_unit": "165.00",
  "total_price": "825.00",
  "commission": "7.00"
}
```

### Actuaries

**GET /api/actuaries** `?search=milos&position=agent&page=1&page_size=10`
```json
{
  "actuaries": [
    {
      "id": 1,
      "employee_id": 5,
      "first_name": "Miloš",
      "last_name": "Milošević",
      "email": "milos@bank.com",
      "role": "agent",
      "limit": "100000.00",
      "used_limit": "15000.00",
      "need_approval": true
    }
  ],
  "total_count": 10
}
```

**PUT /api/actuaries/:id/limit** — Request: `{ "limit": "200000.00" }`
**POST /api/actuaries/:id/reset-limit** — No body
**PUT /api/actuaries/:id/approval** — Request: `{ "need_approval": false }`

All three return: `{ "id": 1, "employee_id": 5, "limit": "200000.00", "used_limit": "0.00", "need_approval": false }`

### Tax

**GET /api/tax** `?user_type=client&search=marko&page=1&page_size=10`
```json
{
  "tax_records": [
    {
      "user_id": 5,
      "user_type": "client",
      "first_name": "Marko",
      "last_name": "Marković",
      "total_debt_rsd": "22500.00",
      "last_collection": "2026-02-28T23:59:59Z"
    }
  ],
  "total_count": 30
}
```

**POST /api/tax/collect** — No body
```json
{
  "collected_count": 30,
  "total_collected_rsd": "675000.00",
  "failed_count": 2
}
```

## Enum Values (for gateway validation)

| Field | Allowed Values |
|-------|---------------|
| `direction` | `buy`, `sell` |
| `order_type` | `market`, `limit`, `stop`, `stop_limit` |
| `security_type` | `stock`, `futures`, `forex`, `option` |
| `option_type` | `call`, `put` |
| `order_status` (filter) | `pending`, `approved`, `declined`, `done` |
| `liquidity` | `high`, `medium`, `low` |
| `sort_by` (securities) | `price`, `volume`, `change`, `margin` |
| `sort_order` | `asc`, `desc` |
| `period` (history) | `day`, `week`, `month`, `year`, `5y`, `all` |
| `user_type` (tax filter) | `client`, `actuary` |
| `contract_unit` | `barrel`, `kilogram`, `liter`, `bushel`, `troy_ounce`, `mmbtu` |

---

## File Structure

### New files to create

```
stock-service/
├── cmd/main.go                              # Skeleton with gRPC server startup
├── go.mod
├── internal/
│   ├── config/config.go                     # Env var loading + DSN()
│   ├── model/                               # (empty dir — populated by plans 2–6)
│   ├── repository/                          # (empty dir)
│   ├── service/                             # (empty dir)
│   ├── handler/                             # (empty dir)
│   ├── kafka/
│   │   ├── producer.go                      # (empty skeleton)
│   │   └── topics.go                        # (empty skeleton)
│   └── provider/                            # External API clients (empty dir)
contract/proto/stock/stock.proto             # gRPC service + message definitions
contract/stockpb/                            # Generated Go code (after make proto)
api-gateway/internal/grpc/stock_client.go    # gRPC client constructors
api-gateway/internal/handler/stock_exchange_handler.go
api-gateway/internal/handler/securities_handler.go
api-gateway/internal/handler/stock_order_handler.go
api-gateway/internal/handler/portfolio_handler.go
api-gateway/internal/handler/actuary_handler.go
api-gateway/internal/handler/tax_handler.go
test-app/workflows/stock_exchange_test.go
test-app/workflows/securities_test.go
test-app/workflows/stock_order_test.go
test-app/workflows/portfolio_test.go
test-app/workflows/otc_test.go
test-app/workflows/actuary_test.go
test-app/workflows/tax_test.go
test-app/workflows/stock_helpers_test.go
```

### Files to modify

```
contract/proto/user/user.proto               # Add ActuaryService
go.work                                      # Add ./stock-service
Makefile                                     # Add stock-service to proto/build/tidy/test/clean
docker-compose.yml                           # Add stock-service + stock-db
api-gateway/internal/router/router.go        # Add stock trading routes
api-gateway/cmd/main.go                      # Wire stock-service gRPC clients
user-service/internal/service/role_service.go # Add new permissions
```

---

### Task 1: Create stock-service directory scaffold

**Files:**
- Create: `stock-service/go.mod`
- Create: `stock-service/cmd/main.go`
- Create: `stock-service/internal/config/config.go`
- Create: `stock-service/internal/kafka/producer.go`
- Create: `stock-service/internal/kafka/topics.go`
- Modify: `go.work`

- [ ] **Step 1: Create stock-service/go.mod**

```bash
mkdir -p stock-service/cmd stock-service/internal/{config,model,repository,service,handler,kafka,provider}
```

File `stock-service/go.mod`:
```go
module github.com/exbanka/stock-service

go 1.26.1

require (
	github.com/exbanka/contract v0.0.0
	google.golang.org/grpc v1.72.0
	gorm.io/driver/postgres v1.5.11
	gorm.io/gorm v1.26.1
	github.com/segmentio/kafka-go v0.4.47
	github.com/shopspring/decimal v1.4.0
)

replace github.com/exbanka/contract => ../contract
```

- [ ] **Step 2: Create stock-service/internal/config/config.go**

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

- [ ] **Step 3: Create stock-service/cmd/main.go skeleton**

```go
package main

import (
	"log"
	"net"

	"google.golang.org/grpc"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	shared "github.com/exbanka/contract/shared"
	"github.com/exbanka/stock-service/internal/config"
)

func main() {
	cfg := config.Load()

	db, err := gorm.Open(postgres.Open(cfg.DSN()), &gorm.Config{})
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}

	// AutoMigrate will be added in implementation plans
	_ = db

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	// Register services — will be added in implementation plans

	shared.RegisterHealthServer(grpcServer)
	log.Printf("stock-service listening on %s", cfg.GRPCAddr)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
```

- [ ] **Step 4: Create kafka skeleton files**

File `stock-service/internal/kafka/producer.go`:
```go
package kafka

import (
	"github.com/segmentio/kafka-go"
)

type Producer struct {
	writer *kafka.Writer
}

func NewProducer(brokers string) *Producer {
	return &Producer{
		writer: &kafka.Writer{
			Addr:     kafka.TCP(brokers),
			Balancer: &kafka.LeastBytes{},
		},
	}
}

func (p *Producer) Close() error {
	return p.writer.Close()
}
```

File `stock-service/internal/kafka/topics.go`:
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

- [ ] **Step 5: Add stock-service to go.work**

Add `./stock-service` to the `use` block in `go.work`.

- [ ] **Step 6: Commit**

```bash
git add stock-service/ go.work
git commit -m "feat(stock-service): scaffold directory structure and config"
```

---

### Task 2: Define stock.proto with all gRPC service definitions

**Files:**
- Create: `contract/proto/stock/stock.proto`

- [ ] **Step 1: Write the complete proto file**

```protobuf
syntax = "proto3";
package stock;
option go_package = "github.com/exbanka/contract/stockpb;stockpb";

// ==================== Exchange (Stock Exchanges) ====================

service StockExchangeGRPCService {
  rpc ListExchanges(ListExchangesRequest) returns (ListExchangesResponse);
  rpc GetExchange(GetExchangeRequest) returns (Exchange);
  rpc SetTestingMode(SetTestingModeRequest) returns (SetTestingModeResponse);
  rpc GetTestingMode(GetTestingModeRequest) returns (GetTestingModeResponse);
}

message Exchange {
  uint64 id = 1;
  string name = 2;
  string acronym = 3;
  string mic_code = 4;
  string polity = 5;
  string currency = 6;
  string time_zone = 7;
  string open_time = 8;
  string close_time = 9;
  string pre_market_open = 10;
  string post_market_close = 11;
}

message ListExchangesRequest {
  string search = 1;
  int32 page = 2;
  int32 page_size = 3;
}

message ListExchangesResponse {
  repeated Exchange exchanges = 1;
  int64 total_count = 2;
}

message GetExchangeRequest {
  uint64 id = 1;
}

message SetTestingModeRequest {
  bool enabled = 1;
}

message SetTestingModeResponse {
  bool testing_mode = 1;
}

message GetTestingModeRequest {}

message GetTestingModeResponse {
  bool testing_mode = 1;
}

// ==================== Shared Listing Info ====================

message ListingInfo {
  uint64 id = 1;
  uint64 exchange_id = 2;
  string exchange_acronym = 3;
  string price = 4;
  string high = 5;
  string low = 6;
  string change = 7;
  string change_percent = 8;
  int64 volume = 9;
  string initial_margin_cost = 10;
  string last_refresh = 11;
}

message PriceHistoryEntry {
  string date = 1;
  string price = 2;
  string high = 3;
  string low = 4;
  string change = 5;
  int64 volume = 6;
}

message GetPriceHistoryRequest {
  uint64 id = 1;
  string period = 2;
  int32 page = 3;
  int32 page_size = 4;
}

message PriceHistoryResponse {
  repeated PriceHistoryEntry history = 1;
  int64 total_count = 2;
}

// ==================== Securities ====================

service SecurityGRPCService {
  // Stocks
  rpc ListStocks(ListStocksRequest) returns (ListStocksResponse);
  rpc GetStock(GetStockRequest) returns (StockDetail);
  rpc GetStockHistory(GetPriceHistoryRequest) returns (PriceHistoryResponse);
  // Futures
  rpc ListFutures(ListFuturesRequest) returns (ListFuturesResponse);
  rpc GetFutures(GetFuturesRequest) returns (FuturesDetail);
  rpc GetFuturesHistory(GetPriceHistoryRequest) returns (PriceHistoryResponse);
  // Forex
  rpc ListForexPairs(ListForexPairsRequest) returns (ListForexPairsResponse);
  rpc GetForexPair(GetForexPairRequest) returns (ForexPairDetail);
  rpc GetForexPairHistory(GetPriceHistoryRequest) returns (PriceHistoryResponse);
  // Options
  rpc ListOptions(ListOptionsRequest) returns (ListOptionsResponse);
  rpc GetOption(GetOptionRequest) returns (OptionDetail);
}

// --- Stocks ---

message StockItem {
  uint64 id = 1;
  string ticker = 2;
  string name = 3;
  int64 outstanding_shares = 4;
  string dividend_yield = 5;
  ListingInfo listing = 6;
}

message StockDetail {
  uint64 id = 1;
  string ticker = 2;
  string name = 3;
  int64 outstanding_shares = 4;
  string dividend_yield = 5;
  string market_cap = 6;
  ListingInfo listing = 7;
  repeated OptionItem options = 8;
}

message ListStocksRequest {
  string search = 1;
  string exchange_acronym = 2;
  string min_price = 3;
  string max_price = 4;
  int64 min_volume = 5;
  int64 max_volume = 6;
  string sort_by = 7;
  string sort_order = 8;
  int32 page = 9;
  int32 page_size = 10;
}

message ListStocksResponse {
  repeated StockItem stocks = 1;
  int64 total_count = 2;
}

message GetStockRequest {
  uint64 id = 1;
}

// --- Futures ---

message FuturesItem {
  uint64 id = 1;
  string ticker = 2;
  string name = 3;
  int64 contract_size = 4;
  string contract_unit = 5;
  string settlement_date = 6;
  ListingInfo listing = 7;
}

message FuturesDetail {
  uint64 id = 1;
  string ticker = 2;
  string name = 3;
  int64 contract_size = 4;
  string contract_unit = 5;
  string settlement_date = 6;
  string maintenance_margin = 7;
  ListingInfo listing = 8;
}

message ListFuturesRequest {
  string search = 1;
  string exchange_acronym = 2;
  string min_price = 3;
  string max_price = 4;
  int64 min_volume = 5;
  int64 max_volume = 6;
  string settlement_date_from = 7;
  string settlement_date_to = 8;
  string sort_by = 9;
  string sort_order = 10;
  int32 page = 11;
  int32 page_size = 12;
}

message ListFuturesResponse {
  repeated FuturesItem futures = 1;
  int64 total_count = 2;
}

message GetFuturesRequest {
  uint64 id = 1;
}

// --- Forex ---

message ForexPairItem {
  uint64 id = 1;
  string ticker = 2;
  string name = 3;
  string base_currency = 4;
  string quote_currency = 5;
  string exchange_rate = 6;
  string liquidity = 7;
  int64 contract_size = 8;
  ListingInfo listing = 9;
}

message ForexPairDetail {
  uint64 id = 1;
  string ticker = 2;
  string name = 3;
  string base_currency = 4;
  string quote_currency = 5;
  string exchange_rate = 6;
  string liquidity = 7;
  int64 contract_size = 8;
  string maintenance_margin = 9;
  ListingInfo listing = 10;
}

message ListForexPairsRequest {
  string search = 1;
  string base_currency = 2;
  string quote_currency = 3;
  string liquidity = 4;
  string sort_by = 5;
  string sort_order = 6;
  int32 page = 7;
  int32 page_size = 8;
}

message ListForexPairsResponse {
  repeated ForexPairItem forex_pairs = 1;
  int64 total_count = 2;
}

message GetForexPairRequest {
  uint64 id = 1;
}

// --- Options ---

message OptionItem {
  uint64 id = 1;
  string ticker = 2;
  string name = 3;
  string stock_ticker = 4;
  uint64 stock_listing_id = 5;
  string option_type = 6;
  string strike_price = 7;
  string implied_volatility = 8;
  string premium = 9;
  int64 open_interest = 10;
  string settlement_date = 11;
  int64 contract_size = 12;
  string initial_margin_cost = 13;
}

message OptionDetail {
  uint64 id = 1;
  string ticker = 2;
  string name = 3;
  string stock_ticker = 4;
  uint64 stock_listing_id = 5;
  string option_type = 6;
  string strike_price = 7;
  string implied_volatility = 8;
  string premium = 9;
  int64 open_interest = 10;
  string settlement_date = 11;
  int64 contract_size = 12;
  string maintenance_margin = 13;
  string initial_margin_cost = 14;
}

message ListOptionsRequest {
  uint64 stock_id = 1;
  string option_type = 2;
  string settlement_date = 3;
  string min_strike = 4;
  string max_strike = 5;
  int32 page = 6;
  int32 page_size = 7;
}

message ListOptionsResponse {
  repeated OptionItem options = 1;
  int64 total_count = 2;
}

message GetOptionRequest {
  uint64 id = 1;
}

// ==================== Orders ====================

service OrderGRPCService {
  rpc CreateOrder(CreateOrderRequest) returns (Order);
  rpc GetOrder(GetOrderRequest) returns (OrderDetail);
  rpc ListMyOrders(ListMyOrdersRequest) returns (ListOrdersResponse);
  rpc CancelOrder(CancelOrderRequest) returns (Order);
  rpc ListOrders(ListOrdersRequest) returns (ListOrdersResponse);
  rpc ApproveOrder(ApproveOrderRequest) returns (Order);
  rpc DeclineOrder(DeclineOrderRequest) returns (Order);
}

message Order {
  uint64 id = 1;
  uint64 user_id = 2;
  uint64 listing_id = 3;
  uint64 holding_id = 4;
  string security_type = 5;
  string ticker = 6;
  string direction = 7;
  string order_type = 8;
  int64 quantity = 9;
  int64 contract_size = 10;
  string price_per_unit = 11;
  string approximate_price = 12;
  string commission = 13;
  string status = 14;
  string approved_by = 15;
  bool is_done = 16;
  int64 remaining_portions = 17;
  bool after_hours = 18;
  bool all_or_none = 19;
  bool margin = 20;
  uint64 account_id = 21;
  string last_modification = 22;
  string created_at = 23;
  optional string limit_value = 24;
  optional string stop_value = 25;
}

message OrderTransaction {
  uint64 id = 1;
  int64 quantity = 2;
  string price_per_unit = 3;
  string total_price = 4;
  string executed_at = 5;
}

message OrderDetail {
  Order order = 1;
  repeated OrderTransaction transactions = 2;
}

message CreateOrderRequest {
  uint64 user_id = 1;
  string system_type = 2;
  uint64 listing_id = 3;
  uint64 holding_id = 4;
  string direction = 5;
  string order_type = 6;
  int64 quantity = 7;
  optional string limit_value = 8;
  optional string stop_value = 9;
  bool all_or_none = 10;
  bool margin = 11;
  uint64 account_id = 12;
}

message GetOrderRequest {
  uint64 id = 1;
  uint64 user_id = 2;
}

message ListMyOrdersRequest {
  uint64 user_id = 1;
  string status = 2;
  string direction = 3;
  string order_type = 4;
  int32 page = 5;
  int32 page_size = 6;
}

message ListOrdersRequest {
  string status = 1;
  string agent_email = 2;
  string direction = 3;
  string order_type = 4;
  int32 page = 5;
  int32 page_size = 6;
}

message ListOrdersResponse {
  repeated Order orders = 1;
  int64 total_count = 2;
}

message CancelOrderRequest {
  uint64 id = 1;
  uint64 user_id = 2;
}

message ApproveOrderRequest {
  uint64 id = 1;
  uint64 supervisor_id = 2;
}

message DeclineOrderRequest {
  uint64 id = 1;
  uint64 supervisor_id = 2;
}

// ==================== Portfolio ====================

service PortfolioGRPCService {
  rpc ListHoldings(ListHoldingsRequest) returns (ListHoldingsResponse);
  rpc GetPortfolioSummary(GetPortfolioSummaryRequest) returns (PortfolioSummary);
  rpc MakePublic(MakePublicRequest) returns (Holding);
  rpc ExerciseOption(ExerciseOptionRequest) returns (ExerciseResult);
}

message Holding {
  uint64 id = 1;
  string security_type = 2;
  string ticker = 3;
  string name = 4;
  int64 quantity = 5;
  string average_price = 6;
  string current_price = 7;
  string profit = 8;
  int64 public_quantity = 9;
  uint64 account_id = 10;
  string last_modified = 11;
}

message ListHoldingsRequest {
  uint64 user_id = 1;
  string security_type = 2;
  int32 page = 3;
  int32 page_size = 4;
}

message ListHoldingsResponse {
  repeated Holding holdings = 1;
  int64 total_count = 2;
}

message GetPortfolioSummaryRequest {
  uint64 user_id = 1;
}

message PortfolioSummary {
  string total_profit = 1;
  string total_profit_rsd = 2;
  string tax_paid_this_year = 3;
  string tax_unpaid_this_month = 4;
}

message MakePublicRequest {
  uint64 holding_id = 1;
  uint64 user_id = 2;
  int64 quantity = 3;
}

message ExerciseOptionRequest {
  uint64 holding_id = 1;
  uint64 user_id = 2;
}

message ExerciseResult {
  uint64 id = 1;
  string option_ticker = 2;
  int64 exercised_quantity = 3;
  int64 shares_affected = 4;
  string profit = 5;
}

// ==================== OTC Trading ====================

service OTCGRPCService {
  rpc ListOffers(ListOTCOffersRequest) returns (ListOTCOffersResponse);
  rpc BuyOffer(BuyOTCOfferRequest) returns (OTCTransaction);
}

message OTCOffer {
  uint64 id = 1;
  uint64 seller_id = 2;
  string seller_name = 3;
  string security_type = 4;
  string ticker = 5;
  string name = 6;
  int64 quantity = 7;
  string price_per_unit = 8;
  string created_at = 9;
}

message ListOTCOffersRequest {
  string security_type = 1;
  string ticker = 2;
  int32 page = 3;
  int32 page_size = 4;
}

message ListOTCOffersResponse {
  repeated OTCOffer offers = 1;
  int64 total_count = 2;
}

message BuyOTCOfferRequest {
  uint64 offer_id = 1;
  uint64 buyer_id = 2;
  string system_type = 3;
  int64 quantity = 4;
  uint64 account_id = 5;
}

message OTCTransaction {
  uint64 id = 1;
  uint64 offer_id = 2;
  int64 quantity = 3;
  string price_per_unit = 4;
  string total_price = 5;
  string commission = 6;
}

// ==================== Tax ====================

service TaxGRPCService {
  rpc ListTaxRecords(ListTaxRecordsRequest) returns (ListTaxRecordsResponse);
  rpc CollectTax(CollectTaxRequest) returns (CollectTaxResponse);
}

message TaxRecord {
  uint64 user_id = 1;
  string user_type = 2;
  string first_name = 3;
  string last_name = 4;
  string total_debt_rsd = 5;
  string last_collection = 6;
}

message ListTaxRecordsRequest {
  string user_type = 1;
  string search = 2;
  int32 page = 3;
  int32 page_size = 4;
}

message ListTaxRecordsResponse {
  repeated TaxRecord tax_records = 1;
  int64 total_count = 2;
}

message CollectTaxRequest {}

message CollectTaxResponse {
  int64 collected_count = 1;
  string total_collected_rsd = 2;
  int64 failed_count = 3;
}
```

- [ ] **Step 2: Add stock.proto to Makefile proto target**

In `Makefile`, add `stock/stock.proto` to the `protoc` command and add `contract/stockpb` to the `mkdir`, `mv`, `rmdir`, and `clean` targets.

- [ ] **Step 3: Run make proto**

```bash
make proto
```

Expected: proto compiles, `contract/stockpb/` contains generated `.pb.go` and `_grpc.pb.go` files.

- [ ] **Step 4: Commit**

```bash
git add contract/proto/stock/ contract/stockpb/ Makefile
git commit -m "feat(contract): add stock.proto with all gRPC service definitions"
```

---

### Task 3: Extend user.proto with ActuaryService

**Files:**
- Modify: `contract/proto/user/user.proto`

- [ ] **Step 1: Add ActuaryService to user.proto**

Append after the `EmployeeLimitService` definition:

```protobuf
service ActuaryService {
  rpc ListActuaries(ListActuariesRequest) returns (ListActuariesResponse);
  rpc GetActuaryInfo(GetActuaryInfoRequest) returns (ActuaryInfo);
  rpc SetActuaryLimit(SetActuaryLimitRequest) returns (ActuaryInfo);
  rpc ResetActuaryUsedLimit(ResetActuaryUsedLimitRequest) returns (ActuaryInfo);
  rpc SetNeedApproval(SetNeedApprovalRequest) returns (ActuaryInfo);
  rpc UpdateUsedLimit(UpdateUsedLimitRequest) returns (ActuaryInfo);
}

message ActuaryInfo {
  uint64 id = 1;
  uint64 employee_id = 2;
  string first_name = 3;
  string last_name = 4;
  string email = 5;
  string role = 6;
  string limit = 7;
  string used_limit = 8;
  bool need_approval = 9;
}

message ListActuariesRequest {
  string search = 1;
  string position = 2;
  int32 page = 3;
  int32 page_size = 4;
}

message ListActuariesResponse {
  repeated ActuaryInfo actuaries = 1;
  int64 total_count = 2;
}

message GetActuaryInfoRequest {
  uint64 employee_id = 1;
}

message SetActuaryLimitRequest {
  uint64 id = 1;
  string limit = 2;
}

message ResetActuaryUsedLimitRequest {
  uint64 id = 1;
}

message SetNeedApprovalRequest {
  uint64 id = 1;
  bool need_approval = 2;
}

message UpdateUsedLimitRequest {
  uint64 id = 1;
  string amount = 2;
  string currency = 3;
}
```

- [ ] **Step 2: Run make proto**

```bash
make proto
```

Expected: `contract/userpb/` regenerated with `ActuaryService` client and server interfaces.

- [ ] **Step 3: Commit**

```bash
git add contract/proto/user/user.proto contract/userpb/
git commit -m "feat(contract): add ActuaryService to user.proto"
```

---

### Task 4: Add new permissions to user-service role seeds

**Files:**
- Modify: `user-service/internal/service/role_service.go`

- [ ] **Step 1: Add new permission entries to AllPermissions**

Add after the `// Agent/OTC/Funds` block:

```go
// Stock trading operations
{"orders.approve", "Approve/decline stock trading orders", "orders"},
{"tax.manage", "Manage capital gains tax collection", "tax"},
{"exchanges.manage", "Manage stock exchange settings and testing mode", "exchanges"},
```

- [ ] **Step 2: Add permissions to EmployeeSupervisor and EmployeeAdmin roles**

Add `"orders.approve", "tax.manage", "exchanges.manage"` to both the `EmployeeSupervisor` and `EmployeeAdmin` entries in `DefaultRolePermissions`.

- [ ] **Step 3: Commit**

```bash
git add user-service/internal/service/role_service.go
git commit -m "feat(user-service): add orders.approve, tax.manage, exchanges.manage permissions"
```

---

### Task 5: Create gateway gRPC clients for stock-service

**Files:**
- Create: `api-gateway/internal/grpc/stock_client.go`

- [ ] **Step 1: Create stock_client.go with all client constructors**

```go
package grpc

import (
	stockpb "github.com/exbanka/contract/stockpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func NewStockExchangeClient(addr string) (stockpb.StockExchangeGRPCServiceClient, *grpc.ClientConn, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	return stockpb.NewStockExchangeGRPCServiceClient(conn), conn, nil
}

func NewSecurityClient(addr string) (stockpb.SecurityGRPCServiceClient, *grpc.ClientConn, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	return stockpb.NewSecurityGRPCServiceClient(conn), conn, nil
}

func NewOrderClient(addr string) (stockpb.OrderGRPCServiceClient, *grpc.ClientConn, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	return stockpb.NewOrderGRPCServiceClient(conn), conn, nil
}

func NewPortfolioClient(addr string) (stockpb.PortfolioGRPCServiceClient, *grpc.ClientConn, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	return stockpb.NewPortfolioGRPCServiceClient(conn), conn, nil
}

func NewOTCClient(addr string) (stockpb.OTCGRPCServiceClient, *grpc.ClientConn, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	return stockpb.NewOTCGRPCServiceClient(conn), conn, nil
}

func NewTaxClient(addr string) (stockpb.TaxGRPCServiceClient, *grpc.ClientConn, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	return stockpb.NewTaxGRPCServiceClient(conn), conn, nil
}
```

- [ ] **Step 2: Create actuary client constructor**

Add to `api-gateway/internal/grpc/user_client.go` (or wherever the user clients are defined):

```go
func NewActuaryClient(addr string) (userpb.ActuaryServiceClient, *grpc.ClientConn, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	return userpb.NewActuaryServiceClient(conn), conn, nil
}
```

- [ ] **Step 3: Commit**

```bash
git add api-gateway/internal/grpc/stock_client.go api-gateway/internal/grpc/
git commit -m "feat(api-gateway): add gRPC client constructors for stock-service"
```

---

### Task 6: Create gateway handlers — Stock Exchanges

**Files:**
- Create: `api-gateway/internal/handler/stock_exchange_handler.go`

- [ ] **Step 1: Write the handler**

```go
package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	stockpb "github.com/exbanka/contract/stockpb"
)

type StockExchangeHandler struct {
	client stockpb.StockExchangeGRPCServiceClient
}

func NewStockExchangeHandler(client stockpb.StockExchangeGRPCServiceClient) *StockExchangeHandler {
	return &StockExchangeHandler{client: client}
}

func (h *StockExchangeHandler) ListExchanges(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))
	search := c.Query("search")

	resp, err := h.client.ListExchanges(c.Request.Context(), &stockpb.ListExchangesRequest{
		Search:   search,
		Page:     int32(page),
		PageSize: int32(pageSize),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"exchanges":   resp.Exchanges,
		"total_count": resp.TotalCount,
	})
}

func (h *StockExchangeHandler) GetExchange(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid exchange id")
		return
	}

	resp, err := h.client.GetExchange(c.Request.Context(), &stockpb.GetExchangeRequest{Id: id})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *StockExchangeHandler) SetTestingMode(c *gin.Context) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		apiError(c, 400, ErrValidation, "invalid request body")
		return
	}

	resp, err := h.client.SetTestingMode(c.Request.Context(), &stockpb.SetTestingModeRequest{
		Enabled: req.Enabled,
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"testing_mode": resp.TestingMode})
}

func (h *StockExchangeHandler) GetTestingMode(c *gin.Context) {
	resp, err := h.client.GetTestingMode(c.Request.Context(), &stockpb.GetTestingModeRequest{})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"testing_mode": resp.TestingMode})
}
```

- [ ] **Step 2: Commit**

```bash
git add api-gateway/internal/handler/stock_exchange_handler.go
git commit -m "feat(api-gateway): add stock exchange handler"
```

---

### Task 7: Create gateway handlers — Securities

**Files:**
- Create: `api-gateway/internal/handler/securities_handler.go`

- [ ] **Step 1: Write the handler with all security type methods**

```go
package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	stockpb "github.com/exbanka/contract/stockpb"
)

type SecuritiesHandler struct {
	client stockpb.SecurityGRPCServiceClient
}

func NewSecuritiesHandler(client stockpb.SecurityGRPCServiceClient) *SecuritiesHandler {
	return &SecuritiesHandler{client: client}
}

// --- Stocks ---

func (h *SecuritiesHandler) ListStocks(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))

	sortBy := c.Query("sort_by")
	if sortBy != "" {
		if _, err := oneOf("sort_by", sortBy, "price", "volume", "change", "margin"); err != nil {
			apiError(c, 400, ErrValidation, err.Error())
			return
		}
	}
	sortOrder := c.DefaultQuery("sort_order", "asc")
	if _, err := oneOf("sort_order", sortOrder, "asc", "desc"); err != nil {
		apiError(c, 400, ErrValidation, err.Error())
		return
	}

	resp, err := h.client.ListStocks(c.Request.Context(), &stockpb.ListStocksRequest{
		Search:          c.Query("search"),
		ExchangeAcronym: c.Query("exchange_acronym"),
		MinPrice:        c.Query("min_price"),
		MaxPrice:        c.Query("max_price"),
		MinVolume:       parseInt64Query(c, "min_volume"),
		MaxVolume:       parseInt64Query(c, "max_volume"),
		SortBy:          sortBy,
		SortOrder:       sortOrder,
		Page:            int32(page),
		PageSize:        int32(pageSize),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"stocks": resp.Stocks, "total_count": resp.TotalCount})
}

func (h *SecuritiesHandler) GetStock(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid stock id")
		return
	}
	resp, err := h.client.GetStock(c.Request.Context(), &stockpb.GetStockRequest{Id: id})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *SecuritiesHandler) GetStockHistory(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid stock id")
		return
	}
	period := c.DefaultQuery("period", "month")
	if _, err := oneOf("period", period, "day", "week", "month", "year", "5y", "all"); err != nil {
		apiError(c, 400, ErrValidation, err.Error())
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "30"))

	resp, err := h.client.GetStockHistory(c.Request.Context(), &stockpb.GetPriceHistoryRequest{
		Id: id, Period: period, Page: int32(page), PageSize: int32(pageSize),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"history": resp.History, "total_count": resp.TotalCount})
}

// --- Futures ---

func (h *SecuritiesHandler) ListFutures(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))

	resp, err := h.client.ListFutures(c.Request.Context(), &stockpb.ListFuturesRequest{
		Search:             c.Query("search"),
		ExchangeAcronym:    c.Query("exchange_acronym"),
		MinPrice:           c.Query("min_price"),
		MaxPrice:           c.Query("max_price"),
		MinVolume:          parseInt64Query(c, "min_volume"),
		MaxVolume:          parseInt64Query(c, "max_volume"),
		SettlementDateFrom: c.Query("settlement_date_from"),
		SettlementDateTo:   c.Query("settlement_date_to"),
		SortBy:             c.Query("sort_by"),
		SortOrder:          c.DefaultQuery("sort_order", "asc"),
		Page:               int32(page),
		PageSize:           int32(pageSize),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"futures": resp.Futures, "total_count": resp.TotalCount})
}

func (h *SecuritiesHandler) GetFutures(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid futures id")
		return
	}
	resp, err := h.client.GetFutures(c.Request.Context(), &stockpb.GetFuturesRequest{Id: id})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *SecuritiesHandler) GetFuturesHistory(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid futures id")
		return
	}
	period := c.DefaultQuery("period", "month")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "30"))

	resp, err := h.client.GetFuturesHistory(c.Request.Context(), &stockpb.GetPriceHistoryRequest{
		Id: id, Period: period, Page: int32(page), PageSize: int32(pageSize),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"history": resp.History, "total_count": resp.TotalCount})
}

// --- Forex ---

func (h *SecuritiesHandler) ListForexPairs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))

	liquidity := c.Query("liquidity")
	if liquidity != "" {
		if _, err := oneOf("liquidity", liquidity, "high", "medium", "low"); err != nil {
			apiError(c, 400, ErrValidation, err.Error())
			return
		}
	}

	resp, err := h.client.ListForexPairs(c.Request.Context(), &stockpb.ListForexPairsRequest{
		Search:        c.Query("search"),
		BaseCurrency:  c.Query("base_currency"),
		QuoteCurrency: c.Query("quote_currency"),
		Liquidity:     liquidity,
		SortBy:        c.Query("sort_by"),
		SortOrder:     c.DefaultQuery("sort_order", "asc"),
		Page:          int32(page),
		PageSize:      int32(pageSize),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"forex_pairs": resp.ForexPairs, "total_count": resp.TotalCount})
}

func (h *SecuritiesHandler) GetForexPair(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid forex pair id")
		return
	}
	resp, err := h.client.GetForexPair(c.Request.Context(), &stockpb.GetForexPairRequest{Id: id})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *SecuritiesHandler) GetForexPairHistory(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid forex pair id")
		return
	}
	period := c.DefaultQuery("period", "month")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "30"))

	resp, err := h.client.GetForexPairHistory(c.Request.Context(), &stockpb.GetPriceHistoryRequest{
		Id: id, Period: period, Page: int32(page), PageSize: int32(pageSize),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"history": resp.History, "total_count": resp.TotalCount})
}

// --- Options ---

func (h *SecuritiesHandler) ListOptions(c *gin.Context) {
	stockIDStr := c.Query("stock_id")
	if stockIDStr == "" {
		apiError(c, 400, ErrValidation, "stock_id query parameter is required")
		return
	}
	stockID, err := strconv.ParseUint(stockIDStr, 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid stock_id")
		return
	}

	optionType := c.Query("option_type")
	if optionType != "" {
		if _, err := oneOf("option_type", optionType, "call", "put"); err != nil {
			apiError(c, 400, ErrValidation, err.Error())
			return
		}
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	resp, err := h.client.ListOptions(c.Request.Context(), &stockpb.ListOptionsRequest{
		StockId:        stockID,
		OptionType:     optionType,
		SettlementDate: c.Query("settlement_date"),
		MinStrike:      c.Query("min_strike"),
		MaxStrike:      c.Query("max_strike"),
		Page:           int32(page),
		PageSize:       int32(pageSize),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"options": resp.Options, "total_count": resp.TotalCount})
}

func (h *SecuritiesHandler) GetOption(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid option id")
		return
	}
	resp, err := h.client.GetOption(c.Request.Context(), &stockpb.GetOptionRequest{Id: id})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// parseInt64Query parses a query parameter as int64, returning 0 if absent or invalid.
func parseInt64Query(c *gin.Context, key string) int64 {
	v := c.Query(key)
	if v == "" {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}
```

- [ ] **Step 2: Commit**

```bash
git add api-gateway/internal/handler/securities_handler.go
git commit -m "feat(api-gateway): add securities handler (stocks, futures, forex, options)"
```

---

### Task 8: Create gateway handlers — Orders

**Files:**
- Create: `api-gateway/internal/handler/stock_order_handler.go`

- [ ] **Step 1: Write the handler**

```go
package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	stockpb "github.com/exbanka/contract/stockpb"
)

type StockOrderHandler struct {
	client stockpb.OrderGRPCServiceClient
}

func NewStockOrderHandler(client stockpb.OrderGRPCServiceClient) *StockOrderHandler {
	return &StockOrderHandler{client: client}
}

func (h *StockOrderHandler) CreateOrder(c *gin.Context) {
	var req struct {
		ListingID  uint64  `json:"listing_id"`
		HoldingID  uint64  `json:"holding_id"`
		Direction  string  `json:"direction"`
		OrderType  string  `json:"order_type"`
		Quantity   int64   `json:"quantity"`
		LimitValue *string `json:"limit_value"`
		StopValue  *string `json:"stop_value"`
		AllOrNone  bool    `json:"all_or_none"`
		Margin     bool    `json:"margin"`
		AccountID  uint64  `json:"account_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		apiError(c, 400, ErrValidation, "invalid request body")
		return
	}

	direction, err := oneOf("direction", req.Direction, "buy", "sell")
	if err != nil {
		apiError(c, 400, ErrValidation, err.Error())
		return
	}
	orderType, err := oneOf("order_type", req.OrderType, "market", "limit", "stop", "stop_limit")
	if err != nil {
		apiError(c, 400, ErrValidation, err.Error())
		return
	}
	if req.Quantity <= 0 {
		apiError(c, 400, ErrValidation, "quantity must be positive")
		return
	}
	if direction == "buy" && req.ListingID == 0 {
		apiError(c, 400, ErrValidation, "listing_id is required for buy orders")
		return
	}
	if direction == "buy" && req.AccountID == 0 {
		apiError(c, 400, ErrValidation, "account_id is required for buy orders")
		return
	}
	if direction == "sell" && req.HoldingID == 0 {
		apiError(c, 400, ErrValidation, "holding_id is required for sell orders")
		return
	}
	if (orderType == "limit" || orderType == "stop_limit") && req.LimitValue == nil {
		apiError(c, 400, ErrValidation, "limit_value is required for limit/stop_limit orders")
		return
	}
	if (orderType == "stop" || orderType == "stop_limit") && req.StopValue == nil {
		apiError(c, 400, ErrValidation, "stop_value is required for stop/stop_limit orders")
		return
	}

	userID := c.GetInt64("user_id")
	systemType := c.GetString("system_type")

	grpcReq := &stockpb.CreateOrderRequest{
		UserId:     uint64(userID),
		SystemType: systemType,
		ListingId:  req.ListingID,
		HoldingId:  req.HoldingID,
		Direction:  direction,
		OrderType:  orderType,
		Quantity:   req.Quantity,
		AllOrNone:  req.AllOrNone,
		Margin:     req.Margin,
		AccountId:  req.AccountID,
	}
	if req.LimitValue != nil {
		grpcReq.LimitValue = req.LimitValue
	}
	if req.StopValue != nil {
		grpcReq.StopValue = req.StopValue
	}

	resp, err := h.client.CreateOrder(c.Request.Context(), grpcReq)
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusCreated, resp)
}

func (h *StockOrderHandler) ListMyOrders(c *gin.Context) {
	userID := c.GetInt64("user_id")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))

	resp, err := h.client.ListMyOrders(c.Request.Context(), &stockpb.ListMyOrdersRequest{
		UserId:    uint64(userID),
		Status:    c.Query("status"),
		Direction: c.Query("direction"),
		OrderType: c.Query("order_type"),
		Page:      int32(page),
		PageSize:  int32(pageSize),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"orders": resp.Orders, "total_count": resp.TotalCount})
}

func (h *StockOrderHandler) GetMyOrder(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid order id")
		return
	}
	userID := c.GetInt64("user_id")

	resp, err := h.client.GetOrder(c.Request.Context(), &stockpb.GetOrderRequest{
		Id: id, UserId: uint64(userID),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *StockOrderHandler) CancelOrder(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid order id")
		return
	}
	userID := c.GetInt64("user_id")

	resp, err := h.client.CancelOrder(c.Request.Context(), &stockpb.CancelOrderRequest{
		Id: id, UserId: uint64(userID),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *StockOrderHandler) ListOrders(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))

	resp, err := h.client.ListOrders(c.Request.Context(), &stockpb.ListOrdersRequest{
		Status:     c.Query("status"),
		AgentEmail: c.Query("agent_email"),
		Direction:  c.Query("direction"),
		OrderType:  c.Query("order_type"),
		Page:       int32(page),
		PageSize:   int32(pageSize),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"orders": resp.Orders, "total_count": resp.TotalCount})
}

func (h *StockOrderHandler) ApproveOrder(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid order id")
		return
	}
	supervisorID := c.GetInt64("user_id")

	resp, err := h.client.ApproveOrder(c.Request.Context(), &stockpb.ApproveOrderRequest{
		Id: id, SupervisorId: uint64(supervisorID),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *StockOrderHandler) DeclineOrder(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid order id")
		return
	}
	supervisorID := c.GetInt64("user_id")

	resp, err := h.client.DeclineOrder(c.Request.Context(), &stockpb.DeclineOrderRequest{
		Id: id, SupervisorId: uint64(supervisorID),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}
```

- [ ] **Step 2: Commit**

```bash
git add api-gateway/internal/handler/stock_order_handler.go
git commit -m "feat(api-gateway): add stock order handler (create, list, approve, decline)"
```

---

### Task 9: Create gateway handlers — Portfolio, OTC, Actuary, Tax

**Files:**
- Create: `api-gateway/internal/handler/portfolio_handler.go`
- Create: `api-gateway/internal/handler/actuary_handler.go`
- Create: `api-gateway/internal/handler/tax_handler.go`

- [ ] **Step 1: Write portfolio_handler.go**

```go
package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	stockpb "github.com/exbanka/contract/stockpb"
)

type PortfolioHandler struct {
	portfolioClient stockpb.PortfolioGRPCServiceClient
	otcClient       stockpb.OTCGRPCServiceClient
}

func NewPortfolioHandler(
	portfolioClient stockpb.PortfolioGRPCServiceClient,
	otcClient stockpb.OTCGRPCServiceClient,
) *PortfolioHandler {
	return &PortfolioHandler{portfolioClient: portfolioClient, otcClient: otcClient}
}

func (h *PortfolioHandler) ListHoldings(c *gin.Context) {
	userID := c.GetInt64("user_id")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))

	secType := c.Query("security_type")
	if secType != "" {
		if _, err := oneOf("security_type", secType, "stock", "futures", "option"); err != nil {
			apiError(c, 400, ErrValidation, err.Error())
			return
		}
	}

	resp, err := h.portfolioClient.ListHoldings(c.Request.Context(), &stockpb.ListHoldingsRequest{
		UserId: uint64(userID), SecurityType: secType, Page: int32(page), PageSize: int32(pageSize),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"holdings": resp.Holdings, "total_count": resp.TotalCount})
}

func (h *PortfolioHandler) GetPortfolioSummary(c *gin.Context) {
	userID := c.GetInt64("user_id")
	resp, err := h.portfolioClient.GetPortfolioSummary(c.Request.Context(), &stockpb.GetPortfolioSummaryRequest{
		UserId: uint64(userID),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *PortfolioHandler) MakePublic(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid holding id")
		return
	}
	var req struct {
		Quantity int64 `json:"quantity"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		apiError(c, 400, ErrValidation, "invalid request body")
		return
	}
	if req.Quantity <= 0 {
		apiError(c, 400, ErrValidation, "quantity must be positive")
		return
	}
	userID := c.GetInt64("user_id")

	resp, err := h.portfolioClient.MakePublic(c.Request.Context(), &stockpb.MakePublicRequest{
		HoldingId: id, UserId: uint64(userID), Quantity: req.Quantity,
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *PortfolioHandler) ExerciseOption(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid holding id")
		return
	}
	userID := c.GetInt64("user_id")

	resp, err := h.portfolioClient.ExerciseOption(c.Request.Context(), &stockpb.ExerciseOptionRequest{
		HoldingId: id, UserId: uint64(userID),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// --- OTC ---

func (h *PortfolioHandler) ListOTCOffers(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))

	secType := c.Query("security_type")
	if secType != "" {
		if _, err := oneOf("security_type", secType, "stock", "futures"); err != nil {
			apiError(c, 400, ErrValidation, err.Error())
			return
		}
	}

	resp, err := h.otcClient.ListOffers(c.Request.Context(), &stockpb.ListOTCOffersRequest{
		SecurityType: secType, Ticker: c.Query("ticker"), Page: int32(page), PageSize: int32(pageSize),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"offers": resp.Offers, "total_count": resp.TotalCount})
}

func (h *PortfolioHandler) BuyOTCOffer(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid offer id")
		return
	}
	var req struct {
		Quantity  int64  `json:"quantity"`
		AccountID uint64 `json:"account_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		apiError(c, 400, ErrValidation, "invalid request body")
		return
	}
	if req.Quantity <= 0 {
		apiError(c, 400, ErrValidation, "quantity must be positive")
		return
	}
	if req.AccountID == 0 {
		apiError(c, 400, ErrValidation, "account_id is required")
		return
	}

	userID := c.GetInt64("user_id")
	systemType := c.GetString("system_type")

	resp, err := h.otcClient.BuyOffer(c.Request.Context(), &stockpb.BuyOTCOfferRequest{
		OfferId: id, BuyerId: uint64(userID), SystemType: systemType,
		Quantity: req.Quantity, AccountId: req.AccountID,
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}
```

- [ ] **Step 2: Write actuary_handler.go**

```go
package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	userpb "github.com/exbanka/contract/userpb"
)

type ActuaryHandler struct {
	client userpb.ActuaryServiceClient
}

func NewActuaryHandler(client userpb.ActuaryServiceClient) *ActuaryHandler {
	return &ActuaryHandler{client: client}
}

func (h *ActuaryHandler) ListActuaries(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))

	resp, err := h.client.ListActuaries(c.Request.Context(), &userpb.ListActuariesRequest{
		Search:   c.Query("search"),
		Position: c.Query("position"),
		Page:     int32(page),
		PageSize: int32(pageSize),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"actuaries": resp.Actuaries, "total_count": resp.TotalCount})
}

func (h *ActuaryHandler) SetActuaryLimit(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid actuary id")
		return
	}
	var req struct {
		Limit string `json:"limit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Limit == "" {
		apiError(c, 400, ErrValidation, "limit is required")
		return
	}

	resp, err := h.client.SetActuaryLimit(c.Request.Context(), &userpb.SetActuaryLimitRequest{
		Id: id, Limit: req.Limit,
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *ActuaryHandler) ResetActuaryLimit(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid actuary id")
		return
	}

	resp, err := h.client.ResetActuaryUsedLimit(c.Request.Context(), &userpb.ResetActuaryUsedLimitRequest{Id: id})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *ActuaryHandler) SetNeedApproval(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 400, ErrValidation, "invalid actuary id")
		return
	}
	var req struct {
		NeedApproval bool `json:"need_approval"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		apiError(c, 400, ErrValidation, "invalid request body")
		return
	}

	resp, err := h.client.SetNeedApproval(c.Request.Context(), &userpb.SetNeedApprovalRequest{
		Id: id, NeedApproval: req.NeedApproval,
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}
```

- [ ] **Step 3: Write tax_handler.go**

```go
package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	stockpb "github.com/exbanka/contract/stockpb"
)

type TaxHandler struct {
	client stockpb.TaxGRPCServiceClient
}

func NewTaxHandler(client stockpb.TaxGRPCServiceClient) *TaxHandler {
	return &TaxHandler{client: client}
}

func (h *TaxHandler) ListTaxRecords(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))

	userType := c.Query("user_type")
	if userType != "" {
		if _, err := oneOf("user_type", userType, "client", "actuary"); err != nil {
			apiError(c, 400, ErrValidation, err.Error())
			return
		}
	}

	resp, err := h.client.ListTaxRecords(c.Request.Context(), &stockpb.ListTaxRecordsRequest{
		UserType: userType, Search: c.Query("search"), Page: int32(page), PageSize: int32(pageSize),
	})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"tax_records": resp.TaxRecords, "total_count": resp.TotalCount})
}

func (h *TaxHandler) CollectTax(c *gin.Context) {
	resp, err := h.client.CollectTax(c.Request.Context(), &stockpb.CollectTaxRequest{})
	if err != nil {
		handleGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"collected_count":    resp.CollectedCount,
		"total_collected_rsd": resp.TotalCollectedRsd,
		"failed_count":       resp.FailedCount,
	})
}
```

- [ ] **Step 4: Commit**

```bash
git add api-gateway/internal/handler/portfolio_handler.go \
        api-gateway/internal/handler/actuary_handler.go \
        api-gateway/internal/handler/tax_handler.go
git commit -m "feat(api-gateway): add portfolio, OTC, actuary, and tax handlers"
```

---

### Task 10: Register all stock trading routes in router

**Files:**
- Modify: `api-gateway/internal/router/router.go`
- Modify: `api-gateway/cmd/main.go`

- [ ] **Step 1: Update router.go Setup() signature**

Add new client parameters:

```go
stockExchangeClient stockpb.StockExchangeGRPCServiceClient,
securityClient stockpb.SecurityGRPCServiceClient,
orderClient stockpb.OrderGRPCServiceClient,
portfolioClient stockpb.PortfolioGRPCServiceClient,
otcClient stockpb.OTCGRPCServiceClient,
taxClient stockpb.TaxGRPCServiceClient,
actuaryClient userpb.ActuaryServiceClient,
```

And the corresponding import:
```go
stockpb "github.com/exbanka/contract/stockpb"
```

- [ ] **Step 2: Add handler instantiation**

Inside `Setup()`, add:
```go
stockExchangeHandler := handler.NewStockExchangeHandler(stockExchangeClient)
securitiesHandler := handler.NewSecuritiesHandler(securityClient)
stockOrderHandler := handler.NewStockOrderHandler(orderClient)
portfolioHandler := handler.NewPortfolioHandler(portfolioClient, otcClient)
actuaryHandler := handler.NewActuaryHandler(actuaryClient)
taxHandler := handler.NewTaxHandler(taxClient)
```

- [ ] **Step 3: Add routes under /api/me (AnyAuthMiddleware)**

Inside the existing `me` group:
```go
// Stock orders
me.POST("/orders", stockOrderHandler.CreateOrder)
me.GET("/orders", stockOrderHandler.ListMyOrders)
me.GET("/orders/:id", stockOrderHandler.GetMyOrder)
me.POST("/orders/:id/cancel", stockOrderHandler.CancelOrder)

// Portfolio
me.GET("/portfolio", portfolioHandler.ListHoldings)
me.GET("/portfolio/summary", portfolioHandler.GetPortfolioSummary)
me.POST("/portfolio/:id/make-public", portfolioHandler.MakePublic)
me.POST("/portfolio/:id/exercise", portfolioHandler.ExerciseOption)
```

- [ ] **Step 4: Add routes under /api (AnyAuthMiddleware — separate group)**

```go
// Stock exchanges — accessible to any authenticated user
stockExchanges := api.Group("/stock-exchanges")
stockExchanges.Use(middleware.AnyAuthMiddleware(authClient))
{
    stockExchanges.GET("", stockExchangeHandler.ListExchanges)
    stockExchanges.GET("/:id", stockExchangeHandler.GetExchange)
}

// Securities — accessible to any authenticated user
securities := api.Group("/securities")
securities.Use(middleware.AnyAuthMiddleware(authClient))
{
    securities.GET("/stocks", securitiesHandler.ListStocks)
    securities.GET("/stocks/:id", securitiesHandler.GetStock)
    securities.GET("/stocks/:id/history", securitiesHandler.GetStockHistory)
    securities.GET("/futures", securitiesHandler.ListFutures)
    securities.GET("/futures/:id", securitiesHandler.GetFutures)
    securities.GET("/futures/:id/history", securitiesHandler.GetFuturesHistory)
    securities.GET("/forex", securitiesHandler.ListForexPairs)
    securities.GET("/forex/:id", securitiesHandler.GetForexPair)
    securities.GET("/forex/:id/history", securitiesHandler.GetForexPairHistory)
    securities.GET("/options", securitiesHandler.ListOptions)
    securities.GET("/options/:id", securitiesHandler.GetOption)
}

// OTC — accessible to any authenticated user
otc := api.Group("/otc/offers")
otc.Use(middleware.AnyAuthMiddleware(authClient))
{
    otc.GET("", portfolioHandler.ListOTCOffers)
    otc.POST("/:id/buy", portfolioHandler.BuyOTCOffer)
}
```

- [ ] **Step 5: Add protected (employee) routes**

Inside the existing `protected` block:
```go
// Stock exchange management (supervisor)
stockExchangeAdmin := protected.Group("/stock-exchanges")
stockExchangeAdmin.Use(middleware.RequirePermission("exchanges.manage"))
{
    stockExchangeAdmin.POST("/testing-mode", stockExchangeHandler.SetTestingMode)
    stockExchangeAdmin.GET("/testing-mode", stockExchangeHandler.GetTestingMode)
}

// Order management (supervisor)
ordersAdmin := protected.Group("/orders")
ordersAdmin.Use(middleware.RequirePermission("orders.approve"))
{
    ordersAdmin.GET("", stockOrderHandler.ListOrders)
    ordersAdmin.POST("/:id/approve", stockOrderHandler.ApproveOrder)
    ordersAdmin.POST("/:id/decline", stockOrderHandler.DeclineOrder)
}

// Actuary management (supervisor)
actuariesAdmin := protected.Group("/actuaries")
actuariesAdmin.Use(middleware.RequirePermission("agents.manage"))
{
    actuariesAdmin.GET("", actuaryHandler.ListActuaries)
    actuariesAdmin.PUT("/:id/limit", actuaryHandler.SetActuaryLimit)
    actuariesAdmin.POST("/:id/reset-limit", actuaryHandler.ResetActuaryLimit)
    actuariesAdmin.PUT("/:id/approval", actuaryHandler.SetNeedApproval)
}

// Tax management (supervisor)
taxAdmin := protected.Group("/tax")
taxAdmin.Use(middleware.RequirePermission("tax.manage"))
{
    taxAdmin.GET("", taxHandler.ListTaxRecords)
    taxAdmin.POST("/collect", taxHandler.CollectTax)
}
```

- [ ] **Step 6: Update api-gateway/cmd/main.go to wire stock-service clients**

Add env var `STOCK_GRPC_ADDR` and create all 6 stock-service gRPC clients plus the actuary client (using existing `USER_GRPC_ADDR`). Pass them all to `router.Setup(...)`.

- [ ] **Step 7: Commit**

```bash
git add api-gateway/internal/router/router.go api-gateway/cmd/main.go
git commit -m "feat(api-gateway): register all stock trading routes"
```

---

### Task 11: Update docker-compose.yml and Makefile

**Files:**
- Modify: `docker-compose.yml`
- Modify: `Makefile`

- [ ] **Step 1: Add stock-db and stock-service to docker-compose.yml**

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
    ports:
      - "50060:50060"
    depends_on:
      - stock-db
      - kafka
      - user-service
      - account-service
      - exchange-service
```

Add to api-gateway environment:
```yaml
STOCK_GRPC_ADDR: stock-service:50060
```

Add to api-gateway depends_on:
```yaml
- stock-service
```

Add volume:
```yaml
  stock_db_data:
```

- [ ] **Step 2: Add stock-service to Makefile build/tidy/test/clean targets**

Add `cd stock-service && go build -o bin/stock-service ./cmd` to `build`, `cd stock-service && go mod tidy` to `tidy`, etc.

- [ ] **Step 3: Commit**

```bash
git add docker-compose.yml Makefile
git commit -m "feat(docker): add stock-service and stock-db to docker-compose"
```

---

### Task 12: Write test-app helpers for stock trading

**Files:**
- Create: `test-app/workflows/stock_helpers_test.go`

- [ ] **Step 1: Write stock-specific test helpers**

```go
//go:build integration

package workflows

import (
	"testing"

	"github.com/exbanka/test-app/internal/client"
	"github.com/exbanka/test-app/internal/helpers"
)

// setupAgentEmployee creates an employee with EmployeeAgent role, activates them,
// and returns the employee ID and an authenticated client.
func setupAgentEmployee(t *testing.T, adminC *client.APIClient) (empID int, agentC *client.APIClient, email string) {
	t.Helper()
	email = helpers.RandomEmail()
	password := helpers.RandomPassword()

	createResp, err := adminC.POST("/api/employees", map[string]interface{}{
		"first_name":    helpers.RandomName("Agent"),
		"last_name":     helpers.RandomName("Emp"),
		"date_of_birth": helpers.DateOfBirthUnix(),
		"gender":        "male",
		"email":         email,
		"phone":         helpers.RandomPhone(),
		"address":       "Agent St 1",
		"username":      helpers.RandomName("agent"),
		"position":      "agent",
		"department":    "Trading",
		"role":          "EmployeeAgent",
		"jmbg":          helpers.RandomJMBG(),
	})
	if err != nil {
		t.Fatalf("setupAgentEmployee: create: %v", err)
	}
	helpers.RequireStatus(t, createResp, 201)
	empID = int(helpers.GetNumberField(t, createResp, "id"))

	token := scanKafkaForActivationToken(t, email)
	activateResp, err := newClient().ActivateAccount(token, password)
	if err != nil {
		t.Fatalf("setupAgentEmployee: activate: %v", err)
	}
	helpers.RequireStatus(t, activateResp, 200)

	agentC = newClient()
	loginResp, err := agentC.Login(email, password)
	if err != nil {
		t.Fatalf("setupAgentEmployee: login: %v", err)
	}
	helpers.RequireStatus(t, loginResp, 200)
	return empID, agentC, email
}

// setupSupervisorEmployee creates an employee with EmployeeSupervisor role.
func setupSupervisorEmployee(t *testing.T, adminC *client.APIClient) (empID int, supervisorC *client.APIClient, email string) {
	t.Helper()
	email = helpers.RandomEmail()
	password := helpers.RandomPassword()

	createResp, err := adminC.POST("/api/employees", map[string]interface{}{
		"first_name":    helpers.RandomName("Super"),
		"last_name":     helpers.RandomName("Visor"),
		"date_of_birth": helpers.DateOfBirthUnix(),
		"gender":        "female",
		"email":         email,
		"phone":         helpers.RandomPhone(),
		"address":       "Supervisor Ave 1",
		"username":      helpers.RandomName("super"),
		"position":      "supervisor",
		"department":    "Trading",
		"role":          "EmployeeSupervisor",
		"jmbg":          helpers.RandomJMBG(),
	})
	if err != nil {
		t.Fatalf("setupSupervisorEmployee: create: %v", err)
	}
	helpers.RequireStatus(t, createResp, 201)
	empID = int(helpers.GetNumberField(t, createResp, "id"))

	token := scanKafkaForActivationToken(t, email)
	activateResp, err := newClient().ActivateAccount(token, password)
	if err != nil {
		t.Fatalf("setupSupervisorEmployee: activate: %v", err)
	}
	helpers.RequireStatus(t, activateResp, 200)

	supervisorC = newClient()
	loginResp, err := supervisorC.Login(email, password)
	if err != nil {
		t.Fatalf("setupSupervisorEmployee: login: %v", err)
	}
	helpers.RequireStatus(t, loginResp, 200)
	return empID, supervisorC, email
}

// getFirstStockListingID fetches stocks and returns the first stock's ID and listing_id.
// Assumes stock-service has seeded data.
func getFirstStockListingID(t *testing.T, c *client.APIClient) (stockID uint64, listingID uint64) {
	t.Helper()
	resp, err := c.GET("/api/securities/stocks?page=1&page_size=1")
	if err != nil {
		t.Fatalf("getFirstStockListingID: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)

	stocks, ok := resp.Body["stocks"].([]interface{})
	if !ok || len(stocks) == 0 {
		t.Fatal("getFirstStockListingID: no stocks found")
	}
	stock := stocks[0].(map[string]interface{})
	stockID = uint64(stock["id"].(float64))

	listing := stock["listing"].(map[string]interface{})
	listingID = uint64(listing["id"].(float64))
	return
}
```

- [ ] **Step 2: Commit**

```bash
git add test-app/workflows/stock_helpers_test.go
git commit -m "test(test-app): add stock trading test helpers"
```

---

### Task 13: Write test-app tests — Stock Exchanges

**Files:**
- Create: `test-app/workflows/stock_exchange_test.go`

- [ ] **Step 1: Write exchange tests**

```go
//go:build integration

package workflows

import (
	"testing"

	"github.com/exbanka/test-app/internal/helpers"
)

func TestStockExchange_ListExchanges(t *testing.T) {
	adminC := loginAsAdmin(t)
	resp, err := adminC.GET("/api/stock-exchanges")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
	helpers.RequireField(t, resp, "exchanges")
	helpers.RequireField(t, resp, "total_count")
}

func TestStockExchange_ListExchanges_Unauthenticated(t *testing.T) {
	c := newClient()
	resp, err := c.GET("/api/stock-exchanges")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 401)
}

func TestStockExchange_ListExchanges_SearchFilter(t *testing.T) {
	adminC := loginAsAdmin(t)
	resp, err := adminC.GET("/api/stock-exchanges?search=NYSE")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
	// Result should contain NYSE if seeded
}

func TestStockExchange_GetExchange(t *testing.T) {
	adminC := loginAsAdmin(t)
	// List first to get an ID
	listResp, err := adminC.GET("/api/stock-exchanges?page_size=1")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, listResp, 200)
	exchanges := listResp.Body["exchanges"].([]interface{})
	if len(exchanges) == 0 {
		t.Skip("no exchanges seeded")
	}
	id := exchanges[0].(map[string]interface{})["id"].(float64)

	resp, err := adminC.GET("/api/stock-exchanges/" + helpers.FormatID(int(id)))
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
	helpers.RequireField(t, resp, "name")
	helpers.RequireField(t, resp, "mic_code")
}

func TestStockExchange_GetExchange_NotFound(t *testing.T) {
	adminC := loginAsAdmin(t)
	resp, err := adminC.GET("/api/stock-exchanges/999999")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 404)
}

func TestStockExchange_TestingMode_SetAndGet(t *testing.T) {
	adminC := loginAsAdmin(t)

	// Enable testing mode
	setResp, err := adminC.POST("/api/stock-exchanges/testing-mode", map[string]interface{}{
		"enabled": true,
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, setResp, 200)
	helpers.RequireFieldEquals(t, setResp, "testing_mode", true)

	// Get testing mode
	getResp, err := adminC.GET("/api/stock-exchanges/testing-mode")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, getResp, 200)
	helpers.RequireFieldEquals(t, getResp, "testing_mode", true)

	// Disable testing mode (cleanup)
	_, _ = adminC.POST("/api/stock-exchanges/testing-mode", map[string]interface{}{
		"enabled": false,
	})
}

func TestStockExchange_TestingMode_RequiresSupervisor(t *testing.T) {
	_, agentC, _ := setupAgentEmployee(t, loginAsAdmin(t))
	resp, err := agentC.POST("/api/stock-exchanges/testing-mode", map[string]interface{}{
		"enabled": true,
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 403)
}
```

- [ ] **Step 2: Commit**

```bash
git add test-app/workflows/stock_exchange_test.go
git commit -m "test(test-app): add stock exchange integration tests"
```

---

### Task 14: Write test-app tests — Securities

**Files:**
- Create: `test-app/workflows/securities_test.go`

- [ ] **Step 1: Write securities tests**

```go
//go:build integration

package workflows

import (
	"testing"

	"github.com/exbanka/test-app/internal/helpers"
)

// --- Stocks ---

func TestSecurities_ListStocks(t *testing.T) {
	adminC := loginAsAdmin(t)
	resp, err := adminC.GET("/api/securities/stocks")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
	helpers.RequireField(t, resp, "stocks")
	helpers.RequireField(t, resp, "total_count")
}

func TestSecurities_ListStocks_Unauthenticated(t *testing.T) {
	c := newClient()
	resp, err := c.GET("/api/securities/stocks")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 401)
}

func TestSecurities_ListStocks_SearchByTicker(t *testing.T) {
	adminC := loginAsAdmin(t)
	resp, err := adminC.GET("/api/securities/stocks?search=AAPL")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
}

func TestSecurities_ListStocks_SortByPrice(t *testing.T) {
	adminC := loginAsAdmin(t)
	resp, err := adminC.GET("/api/securities/stocks?sort_by=price&sort_order=desc")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
}

func TestSecurities_ListStocks_InvalidSortBy(t *testing.T) {
	adminC := loginAsAdmin(t)
	resp, err := adminC.GET("/api/securities/stocks?sort_by=invalid")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 400)
}

func TestSecurities_GetStock(t *testing.T) {
	adminC := loginAsAdmin(t)
	stockID, _ := getFirstStockListingID(t, adminC)

	resp, err := adminC.GET("/api/securities/stocks/" + helpers.FormatID(int(stockID)))
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
	helpers.RequireField(t, resp, "ticker")
	helpers.RequireField(t, resp, "listing")
}

func TestSecurities_GetStockHistory(t *testing.T) {
	adminC := loginAsAdmin(t)
	stockID, _ := getFirstStockListingID(t, adminC)

	resp, err := adminC.GET("/api/securities/stocks/" + helpers.FormatID(int(stockID)) + "/history?period=month")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
	helpers.RequireField(t, resp, "history")
}

func TestSecurities_GetStockHistory_InvalidPeriod(t *testing.T) {
	adminC := loginAsAdmin(t)
	resp, err := adminC.GET("/api/securities/stocks/1/history?period=invalid")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 400)
}

// --- Futures ---

func TestSecurities_ListFutures(t *testing.T) {
	adminC := loginAsAdmin(t)
	resp, err := adminC.GET("/api/securities/futures")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
	helpers.RequireField(t, resp, "futures")
	helpers.RequireField(t, resp, "total_count")
}

func TestSecurities_ListFutures_SettlementDateFilter(t *testing.T) {
	adminC := loginAsAdmin(t)
	resp, err := adminC.GET("/api/securities/futures?settlement_date_from=2026-01-01&settlement_date_to=2026-12-31")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
}

// --- Forex ---

func TestSecurities_ListForexPairs(t *testing.T) {
	adminC := loginAsAdmin(t)
	resp, err := adminC.GET("/api/securities/forex")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
	helpers.RequireField(t, resp, "forex_pairs")
}

func TestSecurities_ListForexPairs_LiquidityFilter(t *testing.T) {
	adminC := loginAsAdmin(t)
	resp, err := adminC.GET("/api/securities/forex?liquidity=high")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
}

func TestSecurities_ListForexPairs_InvalidLiquidity(t *testing.T) {
	adminC := loginAsAdmin(t)
	resp, err := adminC.GET("/api/securities/forex?liquidity=invalid")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 400)
}

// --- Options ---

func TestSecurities_ListOptions_RequiresStockID(t *testing.T) {
	adminC := loginAsAdmin(t)
	resp, err := adminC.GET("/api/securities/options")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 400)
}

func TestSecurities_ListOptions_WithStockID(t *testing.T) {
	adminC := loginAsAdmin(t)
	stockID, _ := getFirstStockListingID(t, adminC)

	resp, err := adminC.GET("/api/securities/options?stock_id=" + helpers.FormatID(int(stockID)))
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
	helpers.RequireField(t, resp, "options")
}

func TestSecurities_ListOptions_FilterByType(t *testing.T) {
	adminC := loginAsAdmin(t)
	stockID, _ := getFirstStockListingID(t, adminC)

	resp, err := adminC.GET("/api/securities/options?stock_id=" + helpers.FormatID(int(stockID)) + "&option_type=call")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
}

// --- Client access ---

func TestSecurities_ClientCanViewStocksAndFutures(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, _, clientC, _ := setupActivatedClient(t, adminC)

	// Clients can see stocks
	resp, err := clientC.GET("/api/securities/stocks")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)

	// Clients can see futures
	resp, err = clientC.GET("/api/securities/futures")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
}
```

- [ ] **Step 2: Commit**

```bash
git add test-app/workflows/securities_test.go
git commit -m "test(test-app): add securities integration tests"
```

---

### Task 15: Write test-app tests — Orders

**Files:**
- Create: `test-app/workflows/stock_order_test.go`

- [ ] **Step 1: Write order tests**

```go
//go:build integration

package workflows

import (
	"fmt"
	"testing"

	"github.com/exbanka/test-app/internal/helpers"
)

func TestOrder_CreateMarketBuyOrder(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)
	_, listingID := getFirstStockListingID(t, agentC)

	// Get a bank account to pay from
	bankAcctResp, err := adminC.GET("/api/bank-accounts")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, bankAcctResp, 200)
	accts := bankAcctResp.Body["accounts"].([]interface{})
	acctID := uint64(accts[0].(map[string]interface{})["id"].(float64))

	resp, err := agentC.POST("/api/me/orders", map[string]interface{}{
		"listing_id": listingID,
		"direction":  "buy",
		"order_type": "market",
		"quantity":   1,
		"all_or_none": false,
		"margin":      false,
		"account_id":  acctID,
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 201)
	helpers.RequireField(t, resp, "id")
	helpers.RequireFieldEquals(t, resp, "direction", "buy")
	helpers.RequireFieldEquals(t, resp, "order_type", "market")
}

func TestOrder_CreateOrder_Unauthenticated(t *testing.T) {
	c := newClient()
	resp, err := c.POST("/api/me/orders", map[string]interface{}{
		"listing_id": 1,
		"direction":  "buy",
		"order_type": "market",
		"quantity":   1,
		"account_id": 1,
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 401)
}

func TestOrder_CreateOrder_InvalidDirection(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.POST("/api/me/orders", map[string]interface{}{
		"listing_id": 1,
		"direction":  "invalid",
		"order_type": "market",
		"quantity":   1,
		"account_id": 1,
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 400)
}

func TestOrder_CreateOrder_InvalidOrderType(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.POST("/api/me/orders", map[string]interface{}{
		"listing_id": 1,
		"direction":  "buy",
		"order_type": "invalid",
		"quantity":   1,
		"account_id": 1,
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 400)
}

func TestOrder_CreateOrder_ZeroQuantity(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.POST("/api/me/orders", map[string]interface{}{
		"listing_id": 1,
		"direction":  "buy",
		"order_type": "market",
		"quantity":   0,
		"account_id": 1,
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 400)
}

func TestOrder_CreateLimitOrder_RequiresLimitValue(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.POST("/api/me/orders", map[string]interface{}{
		"listing_id": 1,
		"direction":  "buy",
		"order_type": "limit",
		"quantity":   1,
		"account_id": 1,
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 400)
}

func TestOrder_CreateBuyOrder_RequiresAccountID(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.POST("/api/me/orders", map[string]interface{}{
		"listing_id": 1,
		"direction":  "buy",
		"order_type": "market",
		"quantity":   1,
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 400)
}

func TestOrder_ListMyOrders(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.GET("/api/me/orders")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
	helpers.RequireField(t, resp, "orders")
	helpers.RequireField(t, resp, "total_count")
}

func TestOrder_ListOrders_RequiresSupervisor(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.GET("/api/orders")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 403)
}

func TestOrder_ListOrders_Supervisor(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, supervisorC, _ := setupSupervisorEmployee(t, adminC)

	resp, err := supervisorC.GET("/api/orders")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
	helpers.RequireField(t, resp, "orders")
}

func TestOrder_ApproveOrder_RequiresSupervisor(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.POST("/api/orders/1/approve", nil)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 403)
}

func TestOrder_DeclineOrder_RequiresSupervisor(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.POST("/api/orders/1/decline", nil)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 403)
}

func TestOrder_ClientOrderAutoApproved(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, _, clientC, _ := setupActivatedClient(t, adminC)
	_, listingID := getFirstStockListingID(t, clientC)

	// Create funded account for client
	// (setupActivatedClient already creates one)

	resp, err := clientC.POST("/api/me/orders", map[string]interface{}{
		"listing_id": listingID,
		"direction":  "buy",
		"order_type": "market",
		"quantity":   1,
		"all_or_none": false,
		"margin":      false,
		"account_id":  1, // Will need real account ID from setup
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	// Client orders should be auto-approved (status != "pending")
	if resp.StatusCode == 201 {
		status := fmt.Sprintf("%v", resp.Body["status"])
		if status == "pending" {
			t.Fatal("client order should not be pending — should be auto-approved")
		}
	}
}
```

- [ ] **Step 2: Commit**

```bash
git add test-app/workflows/stock_order_test.go
git commit -m "test(test-app): add stock order integration tests"
```

---

### Task 16: Write test-app tests — Portfolio, OTC, Actuary, Tax

**Files:**
- Create: `test-app/workflows/portfolio_test.go`
- Create: `test-app/workflows/otc_test.go`
- Create: `test-app/workflows/actuary_test.go`
- Create: `test-app/workflows/tax_test.go`

- [ ] **Step 1: Write portfolio_test.go**

```go
//go:build integration

package workflows

import (
	"testing"

	"github.com/exbanka/test-app/internal/helpers"
)

func TestPortfolio_ListHoldings(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.GET("/api/me/portfolio")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
	helpers.RequireField(t, resp, "holdings")
	helpers.RequireField(t, resp, "total_count")
}

func TestPortfolio_ListHoldings_Unauthenticated(t *testing.T) {
	c := newClient()
	resp, err := c.GET("/api/me/portfolio")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 401)
}

func TestPortfolio_ListHoldings_FilterByType(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.GET("/api/me/portfolio?security_type=stock")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
}

func TestPortfolio_ListHoldings_InvalidSecurityType(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.GET("/api/me/portfolio?security_type=invalid")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 400)
}

func TestPortfolio_GetSummary(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.GET("/api/me/portfolio/summary")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
	helpers.RequireField(t, resp, "total_profit")
	helpers.RequireField(t, resp, "tax_paid_this_year")
}

func TestPortfolio_MakePublic_InvalidQuantity(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.POST("/api/me/portfolio/1/make-public", map[string]interface{}{
		"quantity": 0,
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 400)
}
```

- [ ] **Step 2: Write otc_test.go**

```go
//go:build integration

package workflows

import (
	"testing"

	"github.com/exbanka/test-app/internal/helpers"
)

func TestOTC_ListOffers(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.GET("/api/otc/offers")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
	helpers.RequireField(t, resp, "offers")
	helpers.RequireField(t, resp, "total_count")
}

func TestOTC_ListOffers_Unauthenticated(t *testing.T) {
	c := newClient()
	resp, err := c.GET("/api/otc/offers")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 401)
}

func TestOTC_ListOffers_FilterBySecurityType(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.GET("/api/otc/offers?security_type=stock")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
}

func TestOTC_ListOffers_InvalidSecurityType(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.GET("/api/otc/offers?security_type=option")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	// OTC only for stocks and futures
	helpers.RequireStatus(t, resp, 400)
}

func TestOTC_BuyOffer_InvalidQuantity(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.POST("/api/otc/offers/1/buy", map[string]interface{}{
		"quantity":   0,
		"account_id": 1,
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 400)
}

func TestOTC_BuyOffer_MissingAccountID(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.POST("/api/otc/offers/1/buy", map[string]interface{}{
		"quantity": 5,
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 400)
}
```

- [ ] **Step 3: Write actuary_test.go**

```go
//go:build integration

package workflows

import (
	"fmt"
	"testing"

	"github.com/exbanka/test-app/internal/helpers"
)

func TestActuary_ListActuaries(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, supervisorC, _ := setupSupervisorEmployee(t, adminC)

	resp, err := supervisorC.GET("/api/actuaries")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
	helpers.RequireField(t, resp, "actuaries")
	helpers.RequireField(t, resp, "total_count")
}

func TestActuary_ListActuaries_AgentCannot(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.GET("/api/actuaries")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 403)
}

func TestActuary_SetLimit(t *testing.T) {
	adminC := loginAsAdmin(t)
	agentID, _, _ := setupAgentEmployee(t, adminC)
	_, supervisorC, _ := setupSupervisorEmployee(t, adminC)

	resp, err := supervisorC.PUT(fmt.Sprintf("/api/actuaries/%d/limit", agentID), map[string]interface{}{
		"limit": "200000.00",
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
}

func TestActuary_SetLimit_EmptyValue(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, supervisorC, _ := setupSupervisorEmployee(t, adminC)

	resp, err := supervisorC.PUT("/api/actuaries/1/limit", map[string]interface{}{
		"limit": "",
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 400)
}

func TestActuary_ResetLimit(t *testing.T) {
	adminC := loginAsAdmin(t)
	agentID, _, _ := setupAgentEmployee(t, adminC)
	_, supervisorC, _ := setupSupervisorEmployee(t, adminC)

	resp, err := supervisorC.POST(fmt.Sprintf("/api/actuaries/%d/reset-limit", agentID), nil)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
}

func TestActuary_SetNeedApproval(t *testing.T) {
	adminC := loginAsAdmin(t)
	agentID, _, _ := setupAgentEmployee(t, adminC)
	_, supervisorC, _ := setupSupervisorEmployee(t, adminC)

	resp, err := supervisorC.PUT(fmt.Sprintf("/api/actuaries/%d/approval", agentID), map[string]interface{}{
		"need_approval": true,
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
}

func TestActuary_Unauthenticated(t *testing.T) {
	c := newClient()
	resp, err := c.GET("/api/actuaries")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 401)
}
```

- [ ] **Step 4: Write tax_test.go**

```go
//go:build integration

package workflows

import (
	"testing"

	"github.com/exbanka/test-app/internal/helpers"
)

func TestTax_ListTaxRecords(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, supervisorC, _ := setupSupervisorEmployee(t, adminC)

	resp, err := supervisorC.GET("/api/tax")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
	helpers.RequireField(t, resp, "tax_records")
	helpers.RequireField(t, resp, "total_count")
}

func TestTax_ListTaxRecords_FilterByUserType(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, supervisorC, _ := setupSupervisorEmployee(t, adminC)

	resp, err := supervisorC.GET("/api/tax?user_type=client")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
}

func TestTax_ListTaxRecords_InvalidUserType(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, supervisorC, _ := setupSupervisorEmployee(t, adminC)

	resp, err := supervisorC.GET("/api/tax?user_type=invalid")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 400)
}

func TestTax_CollectTax(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, supervisorC, _ := setupSupervisorEmployee(t, adminC)

	resp, err := supervisorC.POST("/api/tax/collect", nil)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 200)
	helpers.RequireField(t, resp, "collected_count")
	helpers.RequireField(t, resp, "total_collected_rsd")
}

func TestTax_CollectTax_AgentCannot(t *testing.T) {
	adminC := loginAsAdmin(t)
	_, agentC, _ := setupAgentEmployee(t, adminC)

	resp, err := agentC.POST("/api/tax/collect", nil)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 403)
}

func TestTax_Unauthenticated(t *testing.T) {
	c := newClient()
	resp, err := c.GET("/api/tax")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	helpers.RequireStatus(t, resp, 401)
}
```

- [ ] **Step 5: Add FormatID helper**

`helpers.FormatID` does not exist yet. Add to `test-app/internal/helpers/random.go`:

```go
import "strconv" // add to existing imports

// FormatID converts an int to its string representation for URL paths.
func FormatID(id int) string {
	return strconv.Itoa(id)
}
```

- [ ] **Step 6: Commit**

```bash
git add test-app/workflows/portfolio_test.go \
        test-app/workflows/otc_test.go \
        test-app/workflows/actuary_test.go \
        test-app/workflows/tax_test.go \
        test-app/internal/helpers/
git commit -m "test(test-app): add portfolio, OTC, actuary, and tax integration tests"
```

---

### Task 17: Documentation placeholder note

**Files:**
- Note for future: `docs/api/REST_API.md`, `Specification.md`

- [ ] **Step 1: Add a note to REST_API.md**

Add a section header at the end of `docs/api/REST_API.md`:

```markdown
## Stock Trading (Coming Soon)

Stock trading endpoints are defined in the implementation plan at
`docs/superpowers/plans/2026-03-31-stock-trading-api-surface.md`.
Full documentation will be added as each feature is implemented.
```

- [ ] **Step 2: Commit**

```bash
git add docs/api/REST_API.md
git commit -m "docs: add stock trading section placeholder to REST_API.md"
```

---

## Plan-to-Requirements Mapping (for subsequent plans)

| Plan | Requirement File | Scope |
|------|-----------------|-------|
| Plan 2 | 31.md | Stock exchanges (CRUD, data sync, testing mode) + Actuary model in user-service |
| Plan 3 | 32.md | Securities entities (stocks, futures, forex, options) + data seeding |
| Plan 4 | 33.md | Listings (security↔exchange pairing, daily price info) + price history |
| Plan 5 | 34.md | Orders (all types, execution engine, approval flow) + OTC trading |
| Plan 6 | 35.md | Portfolio (holdings, profit calc) + Tax (capital gains, monthly collection) |

Each subsequent plan should reference this plan's API Reference section for the exact request/response contract their implementation must fulfill, and the test-app tests as acceptance criteria.
