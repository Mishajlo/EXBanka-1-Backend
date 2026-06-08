package consumer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/exbanka/card-service/internal/model"
	kafkamsg "github.com/exbanka/contract/kafka"
)

type fakeReplicaRepo struct {
	last  model.ClientReplica
	calls int
}

func (f *fakeReplicaRepo) Upsert(_ context.Context, in model.ClientReplica) error {
	f.last = in
	f.calls++
	return nil
}

func TestHandleClientEvent_UpsertsReplica(t *testing.T) {
	repo := &fakeReplicaRepo{}
	c := &ClientReplicaConsumer{repo: repo}
	payload, _ := json.Marshal(kafkamsg.ClientCreatedMessage{
		ClientID: 42, Email: "x@y.com", FirstName: "X", LastName: "Y", JMBG: "9999999999999", Version: 3,
	})
	if err := c.handle(context.Background(), payload); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if repo.calls != 1 || repo.last.ID != 42 || repo.last.Email != "x@y.com" || repo.last.JMBG != "9999999999999" || repo.last.Version != 3 {
		t.Fatalf("bad upsert: %+v calls=%d", repo.last, repo.calls)
	}
}

func TestHandleClientEvent_BadJSON(t *testing.T) {
	repo := &fakeReplicaRepo{}
	c := &ClientReplicaConsumer{repo: repo}
	if err := c.handle(context.Background(), []byte("{not json")); err == nil {
		t.Fatalf("expected error on malformed json")
	}
	if repo.calls != 0 {
		t.Fatalf("repo should not be called on bad json, got %d calls", repo.calls)
	}
}
