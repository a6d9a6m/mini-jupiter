package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	_ "github.com/go-sql-driver/mysql"
	goredis "github.com/redis/go-redis/v9"
)

type prepResult struct {
	CouponID       int64  `json:"coupon_id"`
	Stock          int    `json:"stock"`
	PerUserLimit   int    `json:"per_user_limit"`
	KeepHistorical bool   `json:"keep_historical"`
	UpdatedAt      string `json:"updated_at"`
}

func main() {
	var (
		dsn            = flag.String("dsn", "", "mysql dsn, e.g. root:root@tcp(127.0.0.1:3306)/mini_jupiter?parseTime=true&loc=Local&charset=utf8mb4")
		redisAddr      = flag.String("redis-addr", "127.0.0.1:6379", "redis addr for claim hot-path cleanup")
		couponID       = flag.Int64("coupon-id", 9001, "coupon campaign id")
		stock          = flag.Int("stock", 30000, "campaign total and available stock")
		perUserLimit   = flag.Int("per-user-limit", 1, "campaign per user claim limit")
		campaignName   = flag.String("campaign-name", "phase3_perf_coupon", "campaign name")
		keepHistorical = flag.Bool("keep-historical", false, "keep existing async_tasks and outbox_events rows")
	)
	flag.Parse()

	if *dsn == "" {
		fail("missing required -dsn")
	}
	if *redisAddr == "" {
		fail("missing required -redis-addr")
	}
	if *couponID <= 0 {
		fail("invalid -coupon-id, must be > 0")
	}
	if *stock <= 0 {
		fail("invalid -stock, must be > 0")
	}
	if *perUserLimit <= 0 {
		fail("invalid -per-user-limit, must be > 0")
	}

	db, err := sql.Open("mysql", *dsn)
	if err != nil {
		fail("open mysql failed: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		fail("ping mysql failed: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		fail("begin tx failed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if !*keepHistorical {
		if _, err := tx.ExecContext(ctx, `DELETE FROM outbox_events`); err != nil {
			fail("cleanup outbox_events failed: %v", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM async_tasks`); err != nil {
			fail("cleanup async_tasks failed: %v", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM claim_side_effects
WHERE claim_id IN (
	SELECT claim_id
	FROM coupon_claims
	WHERE coupon_id = ?
)
`, *couponID); err != nil {
		fail("cleanup claim_side_effects failed: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM coupon_claims WHERE coupon_id = ?`, *couponID); err != nil {
		fail("cleanup coupon_claims failed: %v", err)
	}

	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `
INSERT INTO coupon_campaigns
	(coupon_id, name, total_stock, available_stock, per_user_limit, status, start_at, end_at, version, created_at, updated_at)
VALUES
	(?, ?, ?, ?, ?, 'ACTIVE', DATE_SUB(CURRENT_TIMESTAMP(3), INTERVAL 1 HOUR), DATE_ADD(CURRENT_TIMESTAMP(3), INTERVAL 1 DAY), 0, CURRENT_TIMESTAMP(3), CURRENT_TIMESTAMP(3))
ON DUPLICATE KEY UPDATE
	name = VALUES(name),
	total_stock = VALUES(total_stock),
	available_stock = VALUES(available_stock),
	per_user_limit = VALUES(per_user_limit),
	status = 'ACTIVE',
	start_at = VALUES(start_at),
	end_at = VALUES(end_at),
	updated_at = VALUES(updated_at)
`, *couponID, *campaignName, *stock, *stock, *perUserLimit)
	if err != nil {
		fail("upsert coupon campaign failed: %v", err)
	}

	if err := tx.Commit(); err != nil {
		fail("commit tx failed: %v", err)
	}

	if err := cleanupRedisClaimState(ctx, *redisAddr, *couponID); err != nil {
		fail("cleanup redis claim state failed: %v", err)
	}

	out := prepResult{
		CouponID:       *couponID,
		Stock:          *stock,
		PerUserLimit:   *perUserLimit,
		KeepHistorical: *keepHistorical,
		UpdatedAt:      now.Format(time.RFC3339),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

func fail(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func cleanupRedisClaimState(ctx context.Context, addr string, couponID int64) error {
	client := goredis.NewClient(&goredis.Options{
		Addr:        addr,
		DB:          0,
		DialTimeout: 2 * time.Second,
	})
	defer client.Close()
	if err := client.Ping(ctx).Err(); err != nil {
		return err
	}

	keys := []string{
		fmt.Sprintf("quan:claim:campaign:%d:stock", couponID),
		fmt.Sprintf("quan:claim:campaign:%d:meta", couponID),
		fmt.Sprintf("quan:claim:campaign:%d:user_count", couponID),
	}
	if err := client.Del(ctx, keys...).Err(); err != nil {
		return err
	}

	patterns := []string{
		fmt.Sprintf("quan:claim:coupon:%d:user:*:idem:*", couponID),
		fmt.Sprintf("quan:claim:coupon:%d:user:*:claim", couponID),
	}
	for _, pattern := range patterns {
		if err := deleteByPattern(ctx, client, pattern); err != nil {
			return err
		}
	}

	reservationIDs, err := client.ZRange(ctx, "quan:claim:reservation:index", 0, -1).Result()
	if err != nil {
		return err
	}
	for _, reservationID := range reservationIDs {
		reservationKey := fmt.Sprintf("quan:claim:reservation:%s", reservationID)
		cid, getErr := client.HGet(ctx, reservationKey, "coupon_id").Result()
		if getErr != nil {
			if getErr == goredis.Nil {
				_ = client.ZRem(ctx, "quan:claim:reservation:index", reservationID).Err()
				continue
			}
			return getErr
		}
		if cid != strconv.FormatInt(couponID, 10) {
			continue
		}
		if err := client.Del(ctx, reservationKey).Err(); err != nil {
			return err
		}
		if err := client.ZRem(ctx, "quan:claim:reservation:index", reservationID).Err(); err != nil {
			return err
		}
	}

	return nil
}

func deleteByPattern(ctx context.Context, client *goredis.Client, pattern string) error {
	var cursor uint64
	for {
		keys, next, err := client.Scan(ctx, cursor, pattern, 200).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := client.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}
