package request

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	applog "mini-jupiter/pkg/log"
	apprabbit "mini-jupiter/pkg/rabbitmq"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

type RabbitMQConfig struct {
	Exchange          string        `mapstructure:"exchange" yaml:"exchange"`
	Queue             string        `mapstructure:"queue" yaml:"queue"`
	RoutingKey        string        `mapstructure:"routing_key" yaml:"routing_key"`
	ConsumerTag       string        `mapstructure:"consumer_tag" yaml:"consumer_tag"`
	PublisherChannels int           `mapstructure:"publisher_channels" yaml:"publisher_channels"`
	ConsumerWorkers   int           `mapstructure:"consumer_workers" yaml:"consumer_workers"`
	Prefetch          int           `mapstructure:"prefetch" yaml:"prefetch"`
	ConfirmTimeout    time.Duration `mapstructure:"confirm_timeout" yaml:"confirm_timeout"`
	RequeueDelay      time.Duration `mapstructure:"requeue_delay" yaml:"requeue_delay"`
	ReconnectDelay    time.Duration `mapstructure:"reconnect_delay" yaml:"reconnect_delay"`
}

func (c RabbitMQConfig) withDefaults() RabbitMQConfig {
	if c.Exchange == "" {
		c.Exchange = "quan.claim.request"
	}
	if c.Queue == "" {
		c.Queue = "quan.claim.request.accepted"
	}
	if c.RoutingKey == "" {
		c.RoutingKey = "claim.request.accepted"
	}
	if c.ConsumerTag == "" {
		c.ConsumerTag = "claim-request-consumer"
	}
	if c.PublisherChannels <= 0 {
		c.PublisherChannels = 8
	}
	if c.ConsumerWorkers <= 0 {
		c.ConsumerWorkers = 4
	}
	if c.Prefetch <= 0 {
		c.Prefetch = 16
	}
	if c.ConfirmTimeout <= 0 {
		c.ConfirmTimeout = 3 * time.Second
	}
	if c.ReconnectDelay <= 0 {
		c.ReconnectDelay = time.Second
	}
	return c
}

type acceptedMessage struct {
	RequestID      string `json:"request_id"`
	CouponID       int64  `json:"coupon_id"`
	UserID         int64  `json:"user_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

type RabbitMQPublisher struct {
	client  *apprabbit.Client
	cfg     RabbitMQConfig
	idle    chan *publisherChannel
	permits chan struct{}
	metrics publishMetrics
}

type publisherChannel struct {
	ch            *amqp.Channel
	confirmations <-chan amqp.Confirmation
}

func NewRabbitMQPublisher(client *apprabbit.Client, cfg RabbitMQConfig) (*RabbitMQPublisher, error) {
	if client == nil || client.Raw() == nil {
		return nil, fmt.Errorf("rabbitmq publisher client is nil")
	}
	cfg = cfg.withDefaults()
	publisher := &RabbitMQPublisher{
		client:  client,
		cfg:     cfg,
		idle:    make(chan *publisherChannel, cfg.PublisherChannels),
		permits: make(chan struct{}, cfg.PublisherChannels),
		metrics: noopPublishMetrics{},
	}
	for i := 0; i < cfg.PublisherChannels; i++ {
		slot, err := publisher.newPublisherChannel()
		if err != nil {
			publisher.closeIdleChannels()
			return nil, err
		}
		publisher.idle <- slot
	}
	return publisher, nil
}

type publishMetrics interface {
	ObserveClaimRequestPublish(result string, duration time.Duration)
}

type noopPublishMetrics struct{}

func (noopPublishMetrics) ObserveClaimRequestPublish(string, time.Duration) {}

func (p *RabbitMQPublisher) SetMetrics(metrics publishMetrics) {
	if p == nil || metrics == nil {
		return
	}
	p.metrics = metrics
}

func (p *RabbitMQPublisher) PublishAccepted(ctx context.Context, req Request) error {
	startedAt := time.Now()
	result := "unknown"
	defer func() {
		p.metrics.ObserveClaimRequestPublish(result, time.Since(startedAt))
	}()
	var (
		acquireChannelDur  time.Duration
		declareTopologyDur time.Duration
		sendPublishDur     time.Duration
		waitConfirmDur     time.Duration
		stageStart         time.Time
	)

	body, err := json.Marshal(acceptedMessage{
		RequestID:      req.ID,
		CouponID:       req.CouponID,
		UserID:         req.UserID,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		result = "marshal_error"
		recordPublishTiming(ctx, "marshal_error", acquireChannelDur, declareTopologyDur, sendPublishDur, waitConfirmDur, time.Since(startedAt))
		return err
	}

	stageStart = time.Now()
	slot, err := p.acquirePublisherChannel(ctx)
	acquireChannelDur = time.Since(stageStart)
	if err != nil {
		result = "acquire_channel_error"
		recordPublishTiming(ctx, "acquire_channel_error", acquireChannelDur, declareTopologyDur, sendPublishDur, waitConfirmDur, time.Since(startedAt))
		return err
	}
	healthy := true
	defer func() {
		p.releasePublisherChannel(slot, healthy)
	}()

	stageStart = time.Now()
	if err := slot.ch.PublishWithContext(ctx, p.cfg.Exchange, p.cfg.RoutingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    req.ID,
		Timestamp:    time.Now().UTC(),
		Body:         body,
	}); err != nil {
		sendPublishDur = time.Since(stageStart)
		healthy = false
		result = "publish_error"
		recordPublishTiming(ctx, "publish_error", acquireChannelDur, declareTopologyDur, sendPublishDur, waitConfirmDur, time.Since(startedAt))
		return err
	}
	sendPublishDur = time.Since(stageStart)

	waitCtx, cancel := withConfirmTimeout(ctx, p.cfg.ConfirmTimeout)
	defer cancel()
	stageStart = time.Now()
	select {
	case confirm, ok := <-slot.confirmations:
		waitConfirmDur = time.Since(stageStart)
		if !ok {
			healthy = false
			result = "confirm_channel_closed"
			recordPublishTiming(ctx, "confirm_channel_closed", acquireChannelDur, declareTopologyDur, sendPublishDur, waitConfirmDur, time.Since(startedAt))
			return fmt.Errorf("rabbitmq publish confirmation channel closed")
		}
		if !confirm.Ack {
			result = "publish_nack"
			recordPublishTiming(ctx, "publish_nack", acquireChannelDur, declareTopologyDur, sendPublishDur, waitConfirmDur, time.Since(startedAt))
			return fmt.Errorf("rabbitmq publish was nacked")
		}
		result = "published"
		recordPublishTiming(ctx, "published", acquireChannelDur, declareTopologyDur, sendPublishDur, waitConfirmDur, time.Since(startedAt))
		return nil
	case <-waitCtx.Done():
		waitConfirmDur = time.Since(stageStart)
		result = "confirm_timeout"
		recordPublishTiming(ctx, "confirm_timeout", acquireChannelDur, declareTopologyDur, sendPublishDur, waitConfirmDur, time.Since(startedAt))
		return waitCtx.Err()
	}
}

func (p *RabbitMQPublisher) acquirePublisherChannel(ctx context.Context) (*publisherChannel, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case p.permits <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case slot := <-p.idle:
		return slot, nil
	default:
		slot, err := p.newPublisherChannel()
		if err != nil {
			<-p.permits
			return nil, err
		}
		return slot, nil
	}
}

func (p *RabbitMQPublisher) releasePublisherChannel(slot *publisherChannel, healthy bool) {
	defer func() {
		select {
		case <-p.permits:
		default:
		}
	}()
	if slot == nil {
		return
	}
	if !healthy {
		_ = slot.ch.Close()
		return
	}
	select {
	case p.idle <- slot:
	default:
		_ = slot.ch.Close()
	}
}

func (p *RabbitMQPublisher) newPublisherChannel() (*publisherChannel, error) {
	ch, err := p.client.Channel()
	if err != nil {
		return nil, err
	}
	if err := declareClaimRequestTopology(ch, p.cfg); err != nil {
		_ = ch.Close()
		return nil, err
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, err
	}
	return &publisherChannel{
		ch:            ch,
		confirmations: ch.NotifyPublish(make(chan amqp.Confirmation, 1)),
	}, nil
}

func (p *RabbitMQPublisher) closeIdleChannels() {
	for {
		select {
		case slot := <-p.idle:
			if slot != nil && slot.ch != nil {
				_ = slot.ch.Close()
			}
		default:
			return
		}
	}
}

type RabbitMQConsumerComponent struct {
	client   *apprabbit.Client
	consumer *Consumer
	cfg      RabbitMQConfig

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewRabbitMQConsumerComponent(client *apprabbit.Client, consumer *Consumer, cfg RabbitMQConfig) (*RabbitMQConsumerComponent, error) {
	if client == nil || client.Raw() == nil {
		return nil, fmt.Errorf("rabbitmq consumer client is nil")
	}
	if consumer == nil {
		return nil, fmt.Errorf("rabbitmq consumer handler is nil")
	}
	return &RabbitMQConsumerComponent{
		client:   client,
		consumer: consumer,
		cfg:      cfg.withDefaults(),
	}, nil
}

func (c *RabbitMQConsumerComponent) Start(ctx context.Context) error {
	loopCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	for worker := 0; worker < c.cfg.ConsumerWorkers; worker++ {
		workerID := worker
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.run(loopCtx, workerID)
		}()
	}
	return nil
}

func (c *RabbitMQConsumerComponent) Stop(_ context.Context) error {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	return nil
}

func (c *RabbitMQConsumerComponent) run(ctx context.Context, workerID int) {
	for {
		ch, deliveries, err := c.openConsumer(ctx, workerID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			applog.L(ctx).Warn("claim request rabbitmq open consumer failed", zap.Error(err))
			if !sleepOrDone(ctx, c.cfg.ReconnectDelay) {
				return
			}
			continue
		}
		err = c.consumeLoop(ctx, deliveries)
		_ = ch.Close()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			applog.L(ctx).Warn("claim request rabbitmq consumer loop restarting", zap.Error(err))
		}
		if !sleepOrDone(ctx, c.cfg.ReconnectDelay) {
			return
		}
	}
}

func (c *RabbitMQConsumerComponent) openConsumer(ctx context.Context, workerID int) (*amqp.Channel, <-chan amqp.Delivery, error) {
	ch, err := c.client.Channel()
	if err != nil {
		return nil, nil, err
	}
	if err := declareClaimRequestTopology(ch, c.cfg); err != nil {
		_ = ch.Close()
		return nil, nil, err
	}
	if err := ch.Qos(c.cfg.Prefetch, 0, false); err != nil {
		_ = ch.Close()
		return nil, nil, err
	}
	consumerTag := c.cfg.ConsumerTag
	if c.cfg.ConsumerWorkers > 1 {
		consumerTag = fmt.Sprintf("%s-%d", consumerTag, workerID+1)
	}
	deliveries, err := ch.Consume(
		c.cfg.Queue,
		consumerTag,
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		_ = ch.Close()
		return nil, nil, err
	}
	return ch, deliveries, nil
}

func (c *RabbitMQConsumerComponent) consumeLoop(ctx context.Context, deliveries <-chan amqp.Delivery) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("rabbitmq deliveries channel closed")
			}
			var payload acceptedMessage
			if err := json.Unmarshal(msg.Body, &payload); err != nil {
				applog.L(ctx).Warn("claim request rabbitmq payload decode failed",
					zap.String("message_id", msg.MessageId),
					zap.Error(err),
				)
				_ = msg.Ack(false)
				continue
			}
			if payload.RequestID == "" {
				_ = msg.Ack(false)
				continue
			}
			if err := c.consumer.ConsumeAccepted(ctx, payload.RequestID); err != nil {
				applog.L(ctx).Warn("claim request rabbitmq consume failed",
					zap.String("request_id", payload.RequestID),
					zap.Bool("redelivered", msg.Redelivered),
					zap.Error(err),
				)
				if nackErr := msg.Nack(false, true); nackErr != nil {
					return nackErr
				}
				if c.cfg.RequeueDelay > 0 && !sleepOrDone(ctx, c.cfg.RequeueDelay) {
					return nil
				}
				continue
			}
			if err := msg.Ack(false); err != nil {
				return err
			}
		}
	}
}

func declareClaimRequestTopology(ch *amqp.Channel, cfg RabbitMQConfig) error {
	if err := ch.ExchangeDeclare(cfg.Exchange, "direct", true, false, false, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(cfg.Queue, true, false, false, false, nil); err != nil {
		return err
	}
	return ch.QueueBind(cfg.Queue, cfg.RoutingKey, cfg.Exchange, false, nil)
}

func withConfirmTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
