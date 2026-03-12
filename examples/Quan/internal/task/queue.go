package task

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"mini-jupiter/pkg/redis"

	goredis "github.com/redis/go-redis/v9"
)

type QueueConfig struct {
	ReadyKey string `mapstructure:"ready_key" yaml:"ready_key"`
	RetryKey string `mapstructure:"retry_key" yaml:"retry_key"`
	DLQKey   string `mapstructure:"dlq_key" yaml:"dlq_key"`
}

func (c QueueConfig) withDefaults() QueueConfig {
	if c.ReadyKey == "" {
		c.ReadyKey = "queue:task:ready"
	}
	if c.RetryKey == "" {
		c.RetryKey = "queue:task:retry"
	}
	if c.DLQKey == "" {
		c.DLQKey = "queue:task:dlq"
	}
	return c
}

type Queue struct {
	rdb *goredis.Client
	cfg QueueConfig
}

func NewQueue(client *redis.Client, cfg QueueConfig) (*Queue, error) {
	if client == nil {
		return nil, fmt.Errorf("task queue redis client is nil")
	}
	return &Queue{
		rdb: client.Raw(),
		cfg: cfg.withDefaults(),
	}, nil
}

func (q *Queue) PublishReady(ctx context.Context, taskID int64) error {
	return q.rdb.RPush(ctx, q.cfg.ReadyKey, strconv.FormatInt(taskID, 10)).Err()
}

func (q *Queue) PushDLQ(ctx context.Context, taskID int64) error {
	return q.rdb.RPush(ctx, q.cfg.DLQKey, strconv.FormatInt(taskID, 10)).Err()
}

func (q *Queue) ScheduleRetry(ctx context.Context, taskID int64, retryAt time.Time) error {
	score := float64(retryAt.UnixMilli())
	return q.rdb.ZAdd(ctx, q.cfg.RetryKey, goredis.Z{
		Score:  score,
		Member: strconv.FormatInt(taskID, 10),
	}).Err()
}

func (q *Queue) PopReady(ctx context.Context, timeout time.Duration) (int64, bool, error) {
	res, err := q.rdb.BLPop(ctx, timeout, q.cfg.ReadyKey).Result()
	if err != nil {
		if err == goredis.Nil {
			return 0, false, nil
		}
		return 0, false, err
	}
	if len(res) != 2 {
		return 0, false, fmt.Errorf("invalid BLPOP result")
	}
	taskID, convErr := strconv.ParseInt(res[1], 10, 64)
	if convErr != nil {
		return 0, false, fmt.Errorf("invalid task id in ready queue: %w", convErr)
	}
	return taskID, true, nil
}

func (q *Queue) MoveDueRetryToReady(ctx context.Context, batch int) (int, error) {
	if batch <= 0 {
		batch = 100
	}
	nowScore := strconv.FormatInt(time.Now().UTC().UnixMilli(), 10)
	ids, err := q.rdb.ZRangeByScore(ctx, q.cfg.RetryKey, &goredis.ZRangeBy{
		Min:    "-inf",
		Max:    nowScore,
		Offset: 0,
		Count:  int64(batch),
	}).Result()
	if err != nil {
		return 0, err
	}
	moved := 0
	for _, id := range ids {
		removed, err := q.rdb.ZRem(ctx, q.cfg.RetryKey, id).Result()
		if err != nil {
			return moved, err
		}
		if removed == 0 {
			continue
		}
		if err := q.rdb.RPush(ctx, q.cfg.ReadyKey, id).Err(); err != nil {
			_ = q.rdb.ZAdd(ctx, q.cfg.RetryKey, goredis.Z{
				Score:  float64(time.Now().UTC().UnixMilli()),
				Member: id,
			}).Err()
			return moved, err
		}
		moved++
	}
	return moved, nil
}
