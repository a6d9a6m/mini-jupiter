package request

import (
	"context"
	"fmt"
	"testing"
	"time"

	"mini-jupiter/examples/Quan/internal/testutil/quanenv"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestRabbitMQPublisher_PublishAcceptedRecordsMetricAndEnqueuesMessage(t *testing.T) {
	rabbitClient := quanenv.OpenIntegrationRabbitMQ(t)
	brokerCfg := testRabbitMQConfig(fmt.Sprintf("itest:publisher:%d", time.Now().UnixNano()))
	publisher, err := NewRabbitMQPublisher(rabbitClient, brokerCfg)
	if err != nil {
		t.Fatalf("new publisher failed: %v", err)
	}
	metrics := &publishMetricsRecorder{}
	publisher.SetMetrics(metrics)

	if err := publisher.PublishAccepted(context.Background(), Request{
		ID:             "req-rabbit-publish",
		CouponID:       1401,
		UserID:         2401,
		IdempotencyKey: "idem-rabbit-publish",
	}); err != nil {
		t.Fatalf("publish accepted failed: %v", err)
	}

	if len(metrics.results) != 1 || metrics.results[0] != "published" {
		t.Fatalf("expected published metric, got %+v", metrics.results)
	}

	count, err := queueMessageCount(rabbitClient, brokerCfg.Queue)
	if err != nil {
		t.Fatalf("inspect queue failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one queued message, got %d", count)
	}
}

func TestRabbitMQConsumerComponent_AcksMalformedPayloadAndDrainsQueue(t *testing.T) {
	rabbitClient := quanenv.OpenIntegrationRabbitMQ(t)
	brokerCfg := testRabbitMQConfig(fmt.Sprintf("itest:consumer-malformed:%d", time.Now().UnixNano()))
	ch, err := rabbitClient.Channel()
	if err != nil {
		t.Fatalf("open channel failed: %v", err)
	}
	defer ch.Close()
	if err := declareClaimRequestTopology(ch, brokerCfg); err != nil {
		t.Fatalf("declare topology failed: %v", err)
	}
	if err := ch.PublishWithContext(context.Background(), brokerCfg.Exchange, brokerCfg.RoutingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    "bad-json",
		Body:         []byte("{not-json"),
	}); err != nil {
		t.Fatalf("publish malformed payload failed: %v", err)
	}

	consumerComp, err := NewRabbitMQConsumerComponent(rabbitClient, NewConsumer(newFakeRequestStore(), &countingClaimWriter{}, &fakeHotPath{}), brokerCfg)
	if err != nil {
		t.Fatalf("new consumer component failed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := consumerComp.Start(ctx); err != nil {
		t.Fatalf("start consumer component failed: %v", err)
	}
	defer func() {
		_ = consumerComp.Stop(context.Background())
	}()

	if err := waitForQueueMessageCount(rabbitClient, brokerCfg.Queue, 0, 3*time.Second); err != nil {
		t.Fatalf("expected malformed payload to be acked and drained: %v", err)
	}
}

type publishMetricsRecorder struct {
	results   []string
	durations []time.Duration
}

func (r *publishMetricsRecorder) ObserveClaimRequestPublish(result string, duration time.Duration) {
	r.results = append(r.results, result)
	r.durations = append(r.durations, duration)
}

func queueMessageCount(client interface{ Channel() (*amqp.Channel, error) }, queue string) (int, error) {
	ch, err := client.Channel()
	if err != nil {
		return 0, err
	}
	defer ch.Close()
	state, err := ch.QueueDeclarePassive(queue, true, false, false, false, amqp.Table{
		"x-queue-type": "classic",
	})
	if err != nil {
		return 0, err
	}
	return state.Messages, nil
}

func waitForQueueMessageCount(client interface{ Channel() (*amqp.Channel, error) }, queue string, want int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		count, err := queueMessageCount(client, queue)
		if err == nil && count == want {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("queue %s did not reach message count %d within %s", queue, want, timeout)
}
