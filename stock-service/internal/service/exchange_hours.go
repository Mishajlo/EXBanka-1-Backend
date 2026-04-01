package service

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/exbanka/stock-service/internal/model"
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
