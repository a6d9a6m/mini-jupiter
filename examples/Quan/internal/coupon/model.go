package coupon

import "time"

type ClaimRecord struct {
	ID             int64
	CouponID       int64
	UserID         int64
	Status         string
	IdempotencyKey string
	CreatedAt      time.Time
}
