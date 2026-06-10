package repository

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/exbanka/stock-service/internal/model"
)

// TestMergeDuplicateOpenOffers_SumsAndConsumes verifies the startup migration
// that collapses pre-existing duplicate OPEN LOCAL offers sharing
// (initiator_owner_id, ticker, direction) into the oldest row (summing
// quantity) and marks the rest consumed — the precondition for the partial
// unique index ux_otc_offer_open_owner_ticker_dir.
func TestMergeDuplicateOpenOffers_SumsAndConsumes(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.OTCOffer{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := NewOTCOfferRepository(db)
	uid := uint64(7)
	mk := func(qty int64) *model.OTCOffer {
		return &model.OTCOffer{
			InitiatorOwnerType: model.OwnerClient, InitiatorOwnerID: &uid,
			Direction: model.OTCDirectionSellInitiated, StockID: 1, Ticker: "OPK",
			Quantity: decimal.NewFromInt(qty), Status: model.OTCOfferStatusOpen, Local: true,
			LastModifiedByPrincipalType: "client", LastModifiedByPrincipalID: 7,
		}
	}
	require.NoError(t, db.Create(mk(5)).Error)
	require.NoError(t, db.Create(mk(70)).Error)

	n, err := repo.MergeDuplicateOpenOffers()
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	var open []model.OTCOffer
	require.NoError(t, db.Where("status = ?", model.OTCOfferStatusOpen).Find(&open).Error)
	require.Len(t, open, 1)
	require.True(t, open[0].Quantity.Equal(decimal.NewFromInt(75)), "merged qty = 5+70")

	// Re-read the kept row directly to confirm the column update actually
	// persisted (RowsAffected == 1 path, no silent version-WHERE no-op).
	kept, err := repo.GetByID(open[0].ID)
	require.NoError(t, err)
	require.True(t, kept.Quantity.Equal(decimal.NewFromInt(75)))
	require.Equal(t, model.OTCOfferStatusOpen, kept.Status)

	// Idempotent: a second run is a no-op.
	n2, err := repo.MergeDuplicateOpenOffers()
	require.NoError(t, err)
	require.Equal(t, int64(0), n2)
}
