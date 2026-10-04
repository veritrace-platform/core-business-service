package outbox

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/twmb/franz-go/pkg/kgo"
)

// KafkaPublisher writes messages through an idempotent producer that waits for every in-sync replica
// (messaging.md §2), so a retried batch neither reorders nor duplicates records within a partition.
type KafkaPublisher struct {
	client *kgo.Client
}

// NewKafkaPublisher returns a publisher for the brokers. It connects on first use.
func NewKafkaPublisher(brokers []string) (*KafkaPublisher, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.RequiredAcks(kgo.AllISRAcks()),
	)
	if err != nil {
		return nil, fmt.Errorf("create Kafka client: %w", err)
	}
	return &KafkaPublisher{client: client}, nil
}

// Publish writes the messages and waits until every one is acknowledged.
func (p *KafkaPublisher) Publish(ctx context.Context, messages []Message) error {
	records := make([]*kgo.Record, len(messages))
	for i, m := range messages {
		records[i] = &kgo.Record{Topic: m.Topic, Key: []byte(m.Key), Value: m.Payload}
		for _, name := range slices.Sorted(maps.Keys(m.Headers)) {
			records[i].Headers = append(records[i].Headers, kgo.RecordHeader{Key: name, Value: []byte(m.Headers[name])})
		}
	}
	if err := p.client.ProduceSync(ctx, records...).FirstErr(); err != nil {
		return fmt.Errorf("produce: %w", err)
	}
	return nil
}

// Close closes the connections to the brokers.
func (p *KafkaPublisher) Close() {
	p.client.Close()
}
