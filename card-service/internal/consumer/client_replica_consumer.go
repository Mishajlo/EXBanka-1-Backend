package consumer

import (
	"context"
	"encoding/json"
	"log"

	"github.com/segmentio/kafka-go"

	"github.com/exbanka/card-service/internal/model"
	kafkamsg "github.com/exbanka/contract/kafka"
)

// replicaUpserter is the subset of ClientReplicaRepository the consumer needs.
type replicaUpserter interface {
	Upsert(ctx context.Context, in model.ClientReplica) error
}

// ClientReplicaConsumer maintains card-service's local client_replica from
// client.created / client.updated events (SP-1). Both topics carry the full
// client snapshot (ClientCreatedMessage), so a single handler serves both.
type ClientReplicaConsumer struct {
	reader *kafka.Reader
	repo   replicaUpserter
}

// NewClientReplicaConsumer creates a consumer that subscribes to both
// client.created and client.updated topics via a single consumer group.
func NewClientReplicaConsumer(brokers string, repo replicaUpserter) *ClientReplicaConsumer {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:     []string{brokers},
		GroupTopics: []string{kafkamsg.TopicClientCreated, kafkamsg.TopicClientUpdated},
		GroupID:     "card-service-client-replica",
	})
	return &ClientReplicaConsumer{reader: r, repo: repo}
}

// handle parses one event payload and upserts the replica. Separated from the
// read loop so it can be unit-tested without Kafka.
func (c *ClientReplicaConsumer) handle(ctx context.Context, value []byte) error {
	var evt kafkamsg.ClientCreatedMessage
	if err := json.Unmarshal(value, &evt); err != nil {
		return err
	}
	return c.repo.Upsert(ctx, model.ClientReplica{
		ID:        evt.ClientID,
		Email:     evt.Email,
		FirstName: evt.FirstName,
		LastName:  evt.LastName,
		JMBG:      evt.JMBG,
		Version:   evt.Version,
	})
}

// Start consumes messages in a background goroutine until ctx is cancelled.
func (c *ClientReplicaConsumer) Start(ctx context.Context) {
	go func() {
		for {
			msg, err := c.reader.ReadMessage(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("client-replica consumer read error: %v", err)
				continue
			}
			if err := c.handle(ctx, msg.Value); err != nil {
				log.Printf("client-replica consumer handle error (offset %d): %v", msg.Offset, err)
			}
		}
	}()
}

// Close shuts down the Kafka reader.
func (c *ClientReplicaConsumer) Close() {
	if err := c.reader.Close(); err != nil {
		log.Printf("client-replica consumer close error: %v", err)
	}
}
