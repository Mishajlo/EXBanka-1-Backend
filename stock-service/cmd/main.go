package main

import (
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
