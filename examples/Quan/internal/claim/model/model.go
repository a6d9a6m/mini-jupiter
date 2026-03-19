package claimmodel

import "time"

type Record struct {
	ID             int64
	CouponID       int64
	UserID         int64
	Status         string
	IdempotencyKey string
	CreatedAt      time.Time
}
