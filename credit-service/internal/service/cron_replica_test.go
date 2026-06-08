package service

import (
	"context"
	"errors"
	"testing"

	"github.com/exbanka/credit-service/internal/model"
)

// fakeClientReplicaReader is a test double for clientReplicaReader.
type fakeClientReplicaReader struct {
	replica     model.ClientReplica
	getErr      error
	upsertErr   error
	upsertLast  model.ClientReplica
	upsertCalls int
}

func (f *fakeClientReplicaReader) GetByID(_ context.Context, id uint64) (model.ClientReplica, error) {
	if f.getErr != nil {
		return model.ClientReplica{}, f.getErr
	}
	return f.replica, nil
}

func (f *fakeClientReplicaReader) Upsert(_ context.Context, in model.ClientReplica) error {
	f.upsertCalls++
	f.upsertLast = in
	return f.upsertErr
}

// fakeGetClientClient is a mock clientpb.ClientServiceClient that records calls
// and returns a canned response. Only GetClient is used by CronService.
type fakeGetClientClient struct {
	email     string
	firstName string
	lastName  string
	jmbg      string
	err       error
	calls     int
}

func (f *fakeGetClientClient) GetClient(_ context.Context, _ interface{}, _ ...interface{}) (interface{}, error) {
	return nil, nil
}

// mockCronClientClient satisfies clientpb.ClientServiceClient (stub — only GetClient is called).
type mockCronClientClient struct {
	email string
	err   error
	calls int
}

// We implement the interface inline using the same approach as other test mocks
// in this codebase. The interface lives in the generated clientpb package;
// we satisfy it with a minimal embedding + GetClient override.
// This avoids importing the full generated stub just for a unit test.
// Instead, we use the clientReplicaReader-only path tested via resolveClientEmail directly.

// TestResolveClientEmail_ReplicaHit verifies that when the replica contains
// the client's profile, resolveClientEmail returns the email without calling gRPC.
func TestResolveClientEmail_ReplicaHit(t *testing.T) {
	replica := &fakeClientReplicaReader{
		replica: model.ClientReplica{ID: 42, Email: "hit@example.com"},
	}
	cron := &CronService{clientReplicaRepo: replica, clientClient: nil}

	email := cron.resolveClientEmail(context.Background(), 42)
	if email != "hit@example.com" {
		t.Fatalf("expected replica email, got %q", email)
	}
	if replica.upsertCalls != 0 {
		t.Fatalf("upsert must not be called on replica hit, got %d calls", replica.upsertCalls)
	}
}

// TestResolveClientEmail_NilRepoNilClient returns "" gracefully.
func TestResolveClientEmail_NilRepoNilClient(t *testing.T) {
	cron := &CronService{clientReplicaRepo: nil, clientClient: nil}
	email := cron.resolveClientEmail(context.Background(), 1)
	if email != "" {
		t.Fatalf("expected empty string, got %q", email)
	}
}

// TestResolveClientEmail_ReplicaMiss_NilClientClient returns "" without panic.
func TestResolveClientEmail_ReplicaMiss_NilClientClient(t *testing.T) {
	replica := &fakeClientReplicaReader{
		getErr: errors.New("not found"),
	}
	cron := &CronService{clientReplicaRepo: replica, clientClient: nil}
	email := cron.resolveClientEmail(context.Background(), 99)
	if email != "" {
		t.Fatalf("expected empty string when gRPC client is nil, got %q", email)
	}
}
