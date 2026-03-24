package main

import (
	_ "github.com/exbanka/contract/exchangepb"
	_ "github.com/segmentio/kafka-go"
	_ "github.com/shopspring/decimal"
	_ "google.golang.org/grpc"
	_ "gorm.io/driver/postgres"
	_ "gorm.io/gorm"
)

func main() {}
