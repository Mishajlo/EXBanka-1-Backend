package repository

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/exbanka/card-service/internal/model"
)

func newReplicaDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.ClientReplica{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestClientReplicaRepo_UpsertAndGet(t *testing.T) {
	repo := NewClientReplicaRepository(newReplicaDB(t))
	ctx := context.Background()
	if err := repo.Upsert(ctx, model.ClientReplica{ID: 1, Email: "v1@b.com", FirstName: "A", LastName: "B", Version: 1}); err != nil {
		t.Fatalf("upsert1: %v", err)
	}
	got, err := repo.GetByID(ctx, 1)
	if err != nil || got.Email != "v1@b.com" {
		t.Fatalf("get1: %+v err=%v", got, err)
	}
	if err := repo.Upsert(ctx, model.ClientReplica{ID: 1, Email: "v2@b.com", Version: 2}); err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	got, _ = repo.GetByID(ctx, 1)
	if got.Email != "v2@b.com" {
		t.Fatalf("expected v2, got %+v", got)
	}
	if err := repo.Upsert(ctx, model.ClientReplica{ID: 1, Email: "stale@b.com", Version: 1}); err != nil {
		t.Fatalf("upsert-stale: %v", err)
	}
	got, _ = repo.GetByID(ctx, 1)
	if got.Email != "v2@b.com" {
		t.Fatalf("stale event overwrote newer state: %+v", got)
	}
}

func TestClientReplicaRepo_GetMissing(t *testing.T) {
	repo := NewClientReplicaRepository(newReplicaDB(t))
	_, err := repo.GetByID(context.Background(), 999)
	if err == nil {
		t.Fatalf("expected error for missing replica")
	}
}
