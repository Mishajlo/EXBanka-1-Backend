package consumer

import (
	"context"
	"log"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// MessageHandler processes one message's value. Returning nil means success
// (the offset is committed); returning an error triggers a bounded retry, then
// dead-lettering. Handlers must make non-retryable failures (bad payload,
// unknown template) return nil — only TRANSIENT failures (DB/SMTP hiccup)
// should return an error.
type MessageHandler func(ctx context.Context, value []byte) error

// DeadLetterWriter sinks messages that exhausted their retries so they are
// preserved rather than dropped. *kafka.Producer satisfies it.
type DeadLetterWriter interface {
	WriteDeadLetter(ctx context.Context, source string, value []byte, cause string) error
}

const (
	maxProcessAttempts = 5
	baseRetryBackoff   = 200 * time.Millisecond
)

// runConsumer drives a consumer-group reader with MANUAL offset commits: each
// message is processed with bounded retry and the offset is committed ONLY
// after success (or after the message is safely dead-lettered). This replaces
// the previous ReadMessage auto-commit loop, which committed BEFORE processing
// and so silently dropped events on any transient DB/SMTP failure.
//
// Semantics:
//   - success            → commit, advance.
//   - retryable error    → retry up to maxProcessAttempts with linear backoff.
//   - retries exhausted  → write to the dead-letter topic, then commit (a poison
//     message never stalls the partition).
//   - dead-letter write itself fails → do NOT commit; the message is redelivered
//     (retry the whole thing) rather than lost.
//   - ctx cancelled      → return (graceful shutdown).
//
// It blocks; callers run it in a goroutine.
func runConsumer(ctx context.Context, name string, reader *kafkago.Reader, dlq DeadLetterWriter, handle MessageHandler) {
	log.Printf("%s consumer started", name)
	for {
		msg, err := reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				log.Printf("%s consumer shutting down", name)
				return
			}
			log.Printf("%s consumer: fetch error: %v", name, err)
			continue
		}

		procErr := processWithRetry(ctx, name, handle, msg.Value)
		if ctx.Err() != nil {
			return
		}
		if procErr != nil {
			if dlq == nil {
				log.Printf("%s consumer: no dead-letter sink — DROPPING after %d attempts: %v", name, maxProcessAttempts, procErr)
			} else if dErr := dlq.WriteDeadLetter(ctx, name, msg.Value, procErr.Error()); dErr != nil {
				log.Printf("%s consumer: dead-letter write failed, message will be redelivered: %v", name, dErr)
				continue // do not commit — prefer redelivery over loss
			} else {
				log.Printf("%s consumer: dead-lettered after %d attempts: %v", name, maxProcessAttempts, procErr)
			}
		}

		if err := reader.CommitMessages(ctx, msg); err != nil && ctx.Err() == nil {
			log.Printf("%s consumer: commit error: %v", name, err)
		}
	}
}

func processWithRetry(ctx context.Context, name string, handle MessageHandler, value []byte) error {
	var err error
	for attempt := 1; attempt <= maxProcessAttempts; attempt++ {
		if err = handle(ctx, value); err == nil {
			return nil
		}
		log.Printf("%s consumer: process attempt %d/%d failed: %v", name, attempt, maxProcessAttempts, err)
		if attempt == maxProcessAttempts {
			break
		}
		select {
		case <-time.After(baseRetryBackoff * time.Duration(attempt)):
		case <-ctx.Done():
			return err
		}
	}
	return err
}
