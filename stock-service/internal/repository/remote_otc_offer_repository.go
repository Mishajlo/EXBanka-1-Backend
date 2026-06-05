package repository

import (
	"time"

	"github.com/exbanka/stock-service/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RemoteOTCOfferRepository owns the persistent mirror of peer OTC option
// listings. It is the source of stable local surrogate ids for remote
// offers and the reconciliation point for peer-side cancels.
type RemoteOTCOfferRepository struct{ db *gorm.DB }

func NewRemoteOTCOfferRepository(db *gorm.DB) *RemoteOTCOfferRepository {
	return &RemoteOTCOfferRepository{db: db}
}

// Upsert inserts or refreshes the mirror row for (PeerRoutingNumber,
// ForeignOfferID), stamping LastSeenAt and (re)opening the row. Returns
// the stable surrogate id. Uses ON CONFLICT (never SELECT-then-INSERT)
// per the Concurrency requirement; the conflict target is the natural key.
func (r *RemoteOTCOfferRepository) Upsert(o *model.RemoteOTCOffer, seenAt time.Time) (uint64, error) {
	o.LastSeenAt = seenAt
	o.Status = "open"
	err := r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "peer_routing_number"}, {Name: "foreign_offer_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"bank_code", "seller_id", "direction", "ticker", "amount",
			"strike_price", "strike_currency", "premium", "premium_currency",
			"settlement_date", "peer_created_at", "status", "last_seen_at", "updated_at",
		}),
	}).Create(o).Error
	if err != nil {
		return 0, err
	}
	// Defensive only: current GORM populates o.ID on the DO-UPDATE path
	// (Postgres RETURNING / SQLite last_insert_rowid), so this rarely fires.
	// Kept as a guard against driver behavior changes.
	if o.ID == 0 {
		var row model.RemoteOTCOffer
		if e := r.db.Select("id").
			Where("peer_routing_number = ? AND foreign_offer_id = ?", o.PeerRoutingNumber, o.ForeignOfferID).
			First(&row).Error; e != nil {
			return 0, e
		}
		o.ID = row.ID
	}
	return o.ID, nil
}

// GetByID returns the mirror row by surrogate id, or gorm.ErrRecordNotFound.
func (r *RemoteOTCOfferRepository) GetByID(id uint64) (*model.RemoteOTCOffer, error) {
	var o model.RemoteOTCOffer
	if err := r.db.First(&o, id).Error; err != nil {
		return nil, err
	}
	return &o, nil
}

// ReconcilePeerNotSeen flips every open mirror row for peerRouting whose
// ForeignOfferID is NOT in seenForeignIDs to "cancelled", and returns the
// count flipped. MUST be called only after a SUCCESSFUL poll of that peer.
// A nil/empty seen slice means the peer listed nothing -> cancel all open
// rows for that peer. Bulk update with SkipHooks (intentional non-versioned
// mass flip per the Concurrency requirement).
func (r *RemoteOTCOfferRepository) ReconcilePeerNotSeen(peerRouting int64, seenForeignIDs []string) (int64, error) {
	q := r.db.Session(&gorm.Session{SkipHooks: true}).
		Model(&model.RemoteOTCOffer{}).
		Where("peer_routing_number = ? AND status = ?", peerRouting, "open")
	// peer offer counts are O(tens) in this domain; NOT IN (...) is acceptable.
	if len(seenForeignIDs) > 0 {
		q = q.Where("foreign_offer_id NOT IN ?", seenForeignIDs)
	}
	res := q.Updates(map[string]any{"status": "cancelled", "updated_at": time.Now().UTC()})
	return res.RowsAffected, res.Error
}
