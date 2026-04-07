package request

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mini-jupiter/examples/Quan/internal/testutil/quanenv"
	apprabbit "mini-jupiter/pkg/rabbitmq"
)

func TestAsyncFlow_RabbitMQReconcilerRecoversFinalizeFailureAfterConsumerRestart(t *testing.T) {
	redisClient := quanenv.OpenIntegrationRedis(t, 7)
	rabbitClient := quanenv.OpenIntegrationRabbitMQ(t)
	prefix := fmt.Sprintf("itest:claimrequest:%d", time.Now().UnixNano())

	store, err := NewRedisRequestStore(redisClient, RequestStoreConfig{
		Prefix:             prefix,
		TTL:                time.Hour,
		SkipWaitOnStatuses: []Status{StatusEnqueued},
	})
	if err != nil {
		t.Fatalf("new redis request store failed: %v", err)
	}
	brokerCfg := testRabbitMQConfig(prefix)
	publisher, err := NewRabbitMQPublisher(rabbitClient, brokerCfg)
	if err != nil {
		t.Fatalf("new rabbitmq publisher failed: %v", err)
	}

	hotpath := &fakeHotPath{
		decision: Decision{
			Code:      DecisionCodeAdmitted,
			RequestID: "req-it-001",
		},
		finalizeErr: fmt.Errorf("redis finalize timeout"),
	}
	writer := &fakeClaimWriter{claimID: 7001, inserted: true}

	consumerComp, err := NewRabbitMQConsumerComponent(rabbitClient, NewConsumer(store, writer, hotpath), brokerCfg)
	if err != nil {
		t.Fatalf("new consumer component failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := consumerComp.Start(ctx); err != nil {
		t.Fatalf("start consumer component failed: %v", err)
	}
	t.Cleanup(func() {
		_ = consumerComp.Stop(context.Background())
	})

	acceptSvc := NewAcceptService(hotpath, store, publisher)
	accepted, err := acceptSvc.Accept(context.Background(), AcceptRequest{
		CouponID:       5001,
		UserID:         6001,
		IdempotencyKey: "idem-it-001",
	})
	if err != nil {
		t.Fatalf("accept failed: %v", err)
	}
	if accepted.RequestID != "req-it-001" {
		t.Fatalf("expected request id req-it-001, got %q", accepted.RequestID)
	}

	if err := waitForRequestStatus(context.Background(), store, accepted.RequestID, StatusProcessing, 2*time.Second); err != nil {
		t.Fatalf("request did not reach processing after finalize failure: %v", err)
	}

	if err := consumerComp.Stop(context.Background()); err != nil {
		t.Fatalf("stop consumer component failed: %v", err)
	}

	hotpath.finalizeErr = nil
	reconciler := NewReconciler(store, publisher, hotpath, &fakeClaimLookup{
		claims: map[string]int64{
			accepted.RequestID: 7001,
		},
	}, ReconcilePolicy{
		PublishStaleAfter:    20 * time.Millisecond,
		ProcessingStaleAfter: 20 * time.Millisecond,
	})
	reconcilerComp, err := NewReconcilerComponent(reconciler, ReconcilerConfig{
		PollInterval: 20 * time.Millisecond,
		BatchSize:    10,
	})
	if err != nil {
		t.Fatalf("new reconciler component failed: %v", err)
	}
	if err := reconcilerComp.Start(ctx); err != nil {
		t.Fatalf("start reconciler component failed: %v", err)
	}
	defer func() {
		_ = reconcilerComp.Stop(context.Background())
	}()

	if err := waitForRequestStatus(context.Background(), store, accepted.RequestID, StatusSucceeded, 2*time.Second); err != nil {
		t.Fatalf("request did not converge to succeeded: %v", err)
	}

	req, found, err := store.Get(context.Background(), accepted.RequestID)
	if err != nil {
		t.Fatalf("load request failed: %v", err)
	}
	if !found {
		t.Fatal("expected request to remain in redis")
	}
	if req.ClaimID != 7001 {
		t.Fatalf("expected claim id 7001, got %d", req.ClaimID)
	}
	if len(hotpath.finalized) == 0 {
		t.Fatal("expected finalize to be retried by reconciler")
	}
}

func TestAsyncFlow_RabbitMQConsumerCanDrainDurableQueueAfterDelayedStart(t *testing.T) {
	redisClient := quanenv.OpenIntegrationRedis(t, 8)
	rabbitClient := quanenv.OpenIntegrationRabbitMQ(t)
	prefix := fmt.Sprintf("itest:claimrequest:%d", time.Now().UnixNano())

	store, err := NewRedisRequestStore(redisClient, RequestStoreConfig{
		Prefix:             prefix,
		TTL:                time.Hour,
		SkipWaitOnStatuses: []Status{StatusEnqueued},
	})
	if err != nil {
		t.Fatalf("new redis request store failed: %v", err)
	}
	brokerCfg := testRabbitMQConfig(prefix)
	publisher, err := NewRabbitMQPublisher(rabbitClient, brokerCfg)
	if err != nil {
		t.Fatalf("new rabbitmq publisher failed: %v", err)
	}

	hotpath := &fakeHotPath{
		decision: Decision{
			Code:      DecisionCodeAdmitted,
			RequestID: "req-it-002",
		},
	}
	acceptSvc := NewAcceptService(hotpath, store, publisher)
	accepted, err := acceptSvc.Accept(context.Background(), AcceptRequest{
		CouponID:       5002,
		UserID:         6002,
		IdempotencyKey: "idem-it-002",
	})
	if err != nil {
		t.Fatalf("accept failed: %v", err)
	}
	if accepted.Status != StatusEnqueued {
		t.Fatalf("expected enqueued request, got %s", accepted.Status)
	}

	req, found, err := store.Get(context.Background(), accepted.RequestID)
	if err != nil {
		t.Fatalf("load request failed: %v", err)
	}
	if !found || req.Status != StatusEnqueued {
		t.Fatalf("expected request to stay enqueued before consumer starts, got %+v found=%v", req, found)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consumerComp, err := NewRabbitMQConsumerComponent(rabbitClient, NewConsumer(store, &fakeClaimWriter{
		claimID:  7002,
		inserted: true,
	}, hotpath), brokerCfg)
	if err != nil {
		t.Fatalf("new consumer component failed: %v", err)
	}
	if err := consumerComp.Start(ctx); err != nil {
		t.Fatalf("start consumer component failed: %v", err)
	}
	defer func() {
		_ = consumerComp.Stop(context.Background())
	}()

	if err := waitForRequestStatus(context.Background(), store, accepted.RequestID, StatusSucceeded, 2*time.Second); err != nil {
		t.Fatalf("delayed consumer did not drain durable queue: %v", err)
	}
}

func TestAsyncFlow_RabbitMQRequeuesTransientConsumerFailure(t *testing.T) {
	redisClient := quanenv.OpenIntegrationRedis(t, 9)
	rabbitClient := quanenv.OpenIntegrationRabbitMQ(t)
	prefix := fmt.Sprintf("itest:claimrequest:%d", time.Now().UnixNano())

	store, err := NewRedisRequestStore(redisClient, RequestStoreConfig{
		Prefix:             prefix,
		TTL:                time.Hour,
		SkipWaitOnStatuses: []Status{StatusEnqueued},
	})
	if err != nil {
		t.Fatalf("new redis request store failed: %v", err)
	}
	brokerCfg := testRabbitMQConfig(prefix)
	publisher, err := NewRabbitMQPublisher(rabbitClient, brokerCfg)
	if err != nil {
		t.Fatalf("new rabbitmq publisher failed: %v", err)
	}

	hotpath := &fakeHotPath{
		decision: Decision{
			Code:      DecisionCodeAdmitted,
			RequestID: "req-it-003",
		},
	}
	writer := &flakyClaimWriter{
		failuresLeft: 1,
		claimID:      7003,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consumerComp, err := NewRabbitMQConsumerComponent(rabbitClient, NewConsumer(store, writer, hotpath), brokerCfg)
	if err != nil {
		t.Fatalf("new consumer component failed: %v", err)
	}
	if err := consumerComp.Start(ctx); err != nil {
		t.Fatalf("start consumer component failed: %v", err)
	}
	defer func() {
		_ = consumerComp.Stop(context.Background())
	}()

	acceptSvc := NewAcceptService(hotpath, store, publisher)
	accepted, err := acceptSvc.Accept(context.Background(), AcceptRequest{
		CouponID:       5003,
		UserID:         6003,
		IdempotencyKey: "idem-it-003",
	})
	if err != nil {
		t.Fatalf("accept failed: %v", err)
	}
	if err := waitForRequestStatus(context.Background(), store, accepted.RequestID, StatusSucceeded, 3*time.Second); err != nil {
		t.Fatalf("request did not converge after transient consumer failure: %v", err)
	}
	if attempts := writer.Attempts(); attempts < 2 {
		t.Fatalf("expected rabbitmq redelivery after first failure, got %d attempts", attempts)
	}
}

func TestAsyncFlow_RabbitMQRecoversAfterBrokerRestart(t *testing.T) {
	requireDockerRabbitMQContainer(t)

	redisClient := quanenv.OpenIntegrationRedis(t, 10)
	rabbitClient := quanenv.OpenIntegrationRabbitMQ(t)
	prefix := fmt.Sprintf("itest:claimrequest:%d", time.Now().UnixNano())

	store, err := NewRedisRequestStore(redisClient, RequestStoreConfig{
		Prefix:             prefix,
		TTL:                time.Hour,
		SkipWaitOnStatuses: []Status{StatusEnqueued},
	})
	if err != nil {
		t.Fatalf("new redis request store failed: %v", err)
	}
	brokerCfg := testRabbitMQConfig(prefix)
	brokerCfg.ReconnectDelay = 200 * time.Millisecond
	publisher, err := NewRabbitMQPublisher(rabbitClient, brokerCfg)
	if err != nil {
		t.Fatalf("new rabbitmq publisher failed: %v", err)
	}

	hotpath := &fakeHotPath{
		decision: Decision{
			Code:      DecisionCodeAdmitted,
			RequestID: "req-it-004",
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consumerComp, err := NewRabbitMQConsumerComponent(rabbitClient, NewConsumer(store, &fakeClaimWriter{
		claimID:  7004,
		inserted: true,
	}, hotpath), brokerCfg)
	if err != nil {
		t.Fatalf("new consumer component failed: %v", err)
	}
	if err := consumerComp.Start(ctx); err != nil {
		t.Fatalf("start consumer component failed: %v", err)
	}
	defer func() {
		_ = consumerComp.Stop(context.Background())
	}()
	reconcilerComp, err := NewReconcilerComponent(NewReconciler(
		store,
		publisher,
		hotpath,
		&fakeClaimLookup{
			claims: map[string]int64{
				"req-it-004": 7004,
				"req-it-005": 7004,
			},
		},
		ReconcilePolicy{
			PublishStaleAfter:    20 * time.Millisecond,
			ProcessingStaleAfter: 20 * time.Millisecond,
		},
	), ReconcilerConfig{
		PollInterval: 20 * time.Millisecond,
		BatchSize:    20,
	})
	if err != nil {
		t.Fatalf("new reconciler component failed: %v", err)
	}
	if err := reconcilerComp.Start(ctx); err != nil {
		t.Fatalf("start reconciler component failed: %v", err)
	}
	defer func() {
		_ = reconcilerComp.Stop(context.Background())
	}()

	acceptSvc := NewAcceptService(hotpath, store, publisher)
	first, err := acceptSvc.Accept(context.Background(), AcceptRequest{
		CouponID:       5004,
		UserID:         6004,
		IdempotencyKey: "idem-it-004",
	})
	if err != nil {
		t.Fatalf("first accept failed: %v", err)
	}
	if err := waitForRequestStatus(context.Background(), store, first.RequestID, StatusSucceeded, 3*time.Second); err != nil {
		t.Fatalf("first request did not converge before rabbitmq restart: %v", err)
	}

	if err := restartDockerContainer(context.Background(), rabbitmqContainerName()); err != nil {
		t.Fatalf("restart rabbitmq container failed: %v", err)
	}

	hotpath.decision = Decision{
		Code:      DecisionCodeAdmitted,
		RequestID: "req-it-005",
	}
	second, err := acceptSvc.Accept(context.Background(), AcceptRequest{
		CouponID:       5005,
		UserID:         6005,
		IdempotencyKey: "idem-it-005",
	})
	if err != nil {
		t.Fatalf("second accept failed after rabbitmq restart: %v", err)
	}
	if second.Status != StatusPublishing && second.Status != StatusEnqueued {
		t.Fatalf("expected second request to be publishing or enqueued after restart, got %s", second.Status)
	}
	if err := waitForRabbitMQReady(rabbitMQTestURL(), 40*time.Second); err != nil {
		t.Fatalf("rabbitmq did not become ready after restart: %v", err)
	}
	if err := waitForRequestStatus(context.Background(), store, second.RequestID, StatusSucceeded, 8*time.Second); err != nil {
		t.Fatalf("second request did not converge after rabbitmq restart: %v", err)
	}
}

func waitForRequestStatus(ctx context.Context, store RequestStore, requestID string, want Status, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, found, err := store.Get(ctx, requestID)
		if err != nil {
			return err
		}
		if found && req.Status == want {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("request %s did not reach status %s within %s", requestID, want, timeout)
}

func testRabbitMQConfig(prefix string) RabbitMQConfig {
	return RabbitMQConfig{
		Exchange:          prefix + ".exchange",
		Queue:             prefix + ".queue",
		RoutingKey:        prefix + ".routing",
		ConsumerTag:       prefix + ".consumer",
		PublisherChannels: 1,
		ConsumerWorkers:   1,
		Prefetch:          8,
		ConfirmTimeout:    time.Second,
		ReconnectDelay:    200 * time.Millisecond,
	}
}

type flakyClaimWriter struct {
	failuresLeft int32
	claimID      int64
	attempts     atomic.Int32
	mu           sync.Mutex
}

func (w *flakyClaimWriter) PersistClaim(_ context.Context, _ Request) (int64, bool, error) {
	w.attempts.Add(1)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failuresLeft > 0 {
		w.failuresLeft--
		return 0, false, RetryableError{Err: fmt.Errorf("transient sql failure")}
	}
	return w.claimID, true, nil
}

func (w *flakyClaimWriter) Attempts() int32 {
	return w.attempts.Load()
}

func requireDockerRabbitMQContainer(t *testing.T) {
	t.Helper()
	container := rabbitmqContainerName()
	cmd := exec.Command("docker", "inspect", container)
	if err := cmd.Run(); err != nil {
		t.Skipf("skip docker fault test: rabbitmq container %s is unavailable: %v", container, err)
	}
}

func rabbitmqContainerName() string {
	if v := os.Getenv("QUAN_TEST_RABBITMQ_CONTAINER"); v != "" {
		return v
	}
	return "mini-jupiter-rabbitmq"
}

func rabbitMQTestURL() string {
	if v := os.Getenv(quanenv.TestRabbitMQURLEnv); v != "" {
		return v
	}
	return quanenv.DefaultTestRabbitMQURL
}

func restartDockerContainer(ctx context.Context, container string) error {
	cmd := exec.CommandContext(ctx, "docker", "restart", container)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker restart %s failed: %w: %s", container, err, string(out))
	}
	return nil
}

func waitForTCP(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("tcp %s not reachable within %s", addr, timeout)
}

func waitForRabbitMQReady(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		client, err := apprabbit.NewClient(apprabbit.Config{
			URL:         url,
			DialTimeout: 2 * time.Second,
			Heartbeat:   5 * time.Second,
		})
		if err == nil {
			_ = client.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("rabbitmq %s not ready within %s", url, timeout)
}
