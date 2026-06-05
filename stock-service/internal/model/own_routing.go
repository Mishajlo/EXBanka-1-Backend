package model

import (
	"strconv"
	"sync/atomic"
)

// ownRouting is this bank's routing number, set once at startup from
// OWN_BANK_CODE. Local OTC rows are stamped with it by BeforeCreate hooks
// so local-vs-remote is `routing_number == ownRouting`.
var ownRouting atomic.Int64

// SetOwnRouting is called once at startup (cmd/main.go) with OWN_BANK_CODE.
func SetOwnRouting(bankCode string) {
	if n, err := strconv.ParseInt(bankCode, 10, 64); err == nil {
		ownRouting.Store(n)
	}
}

// OwnRouting returns the configured own routing number.
func OwnRouting() int64 { return ownRouting.Load() }
