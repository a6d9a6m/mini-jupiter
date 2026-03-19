package reservation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"mini-jupiter/examples/Quan/internal/adjudication/hotpath"
	claimmodel "mini-jupiter/examples/Quan/internal/claim/model"
	applog "mini-jupiter/pkg/log"

	"go.uber.org/zap"
)

type ReservationReconcilerConfig struct {
	Enabled      bool          `mapstructure:"enabled" yaml:"enabled"`
	PollInterval time.Duration `mapstructure:"poll_interval" yaml:"poll_interval"`
	BatchSize    int           `mapstructure:"batch_size" yaml:"batch_size"`
}

func (c ReservationReconcilerConfig) withDefaults() ReservationReconcilerConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = 2 * time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 100
	}
	return c
}

type ReservationReconciler struct {
	cfg         ReservationReconcilerConfig
	repo        reservationReconcilerRepository
	adjudicator reservationReconcilerAdjudicator

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type reservationReconcilerRepository interface {
	FindClaimByIdempotency(ctx context.Context, couponID, userID int64, idemKey string) (claimmodel.Record, bool, error)
}

type reservationReconcilerAdjudicator interface {
	ListExpiredReservations(ctx context.Context, now time.Time, limit int) ([]hotpath.ReservationLease, error)
	Finalize(ctx context.Context, couponID, userID int64, idemKey, reservationID string, claimID int64) error
	Rollback(ctx context.Context, couponID, userID int64, idemKey, reservationID string) error
}

func NewReservationReconciler(repo reservationReconcilerRepository, adjudicator reservationReconcilerAdjudicator, cfg ReservationReconcilerConfig) (*ReservationReconciler, error) {
	cfg = cfg.withDefaults()
	if !cfg.Enabled {
		return &ReservationReconciler{cfg: cfg}, nil
	}
	if repo == nil {
		return nil, fmt.Errorf("coupon reservation reconciler repository is nil")
	}
	if adjudicator == nil {
		return nil, fmt.Errorf("coupon reservation reconciler adjudicator is nil")
	}
	return &ReservationReconciler{
		cfg:         cfg,
		repo:        repo,
		adjudicator: adjudicator,
	}, nil
}

func (r *ReservationReconciler) Start(ctx context.Context) error {
	if r == nil || !r.cfg.Enabled {
		return nil
	}
	loopCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.run(loopCtx)
	}()
	return nil
}

func (r *ReservationReconciler) Stop(_ context.Context) error {
	if r == nil || !r.cfg.Enabled {
		return nil
	}
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	return nil
}

func (r *ReservationReconciler) run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if err := r.ReconcileOnce(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			applog.L(ctx).Error("coupon reservation reconcile failed", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *ReservationReconciler) ReconcileOnce(ctx context.Context, now time.Time) error {
	if r == nil || r.adjudicator == nil {
		return nil
	}
	leases, err := r.adjudicator.ListExpiredReservations(ctx, now, r.cfg.BatchSize)
	if err != nil {
		return err
	}
	for _, lease := range leases {
		rec, found, findErr := r.repo.FindClaimByIdempotency(ctx, lease.CouponID, lease.UserID, lease.IdemKey)
		if findErr != nil {
			applog.L(ctx).Warn("find claim by idempotency during reservation reconcile failed",
				zap.String("reservation_id", lease.ReservationID),
				zap.Int64("coupon_id", lease.CouponID),
				zap.Int64("user_id", lease.UserID),
				zap.Error(findErr),
			)
			continue
		}
		if found {
			if finErr := r.adjudicator.Finalize(ctx, lease.CouponID, lease.UserID, lease.IdemKey, lease.ReservationID, rec.ID); finErr != nil {
				applog.L(ctx).Warn("finalize reservation reconcile failed",
					zap.String("reservation_id", lease.ReservationID),
					zap.Int64("coupon_id", lease.CouponID),
					zap.Int64("user_id", lease.UserID),
					zap.Int64("claim_id", rec.ID),
					zap.Error(finErr),
				)
				continue
			}
			applog.L(ctx).Info("finalized stale coupon reservation from persisted claim",
				zap.String("reservation_id", lease.ReservationID),
				zap.Int64("coupon_id", lease.CouponID),
				zap.Int64("user_id", lease.UserID),
				zap.Int64("claim_id", rec.ID),
			)
			continue
		}
		if rbErr := r.adjudicator.Rollback(ctx, lease.CouponID, lease.UserID, lease.IdemKey, lease.ReservationID); rbErr != nil {
			applog.L(ctx).Warn("rollback reservation reconcile failed",
				zap.String("reservation_id", lease.ReservationID),
				zap.Int64("coupon_id", lease.CouponID),
				zap.Int64("user_id", lease.UserID),
				zap.Error(rbErr),
			)
			continue
		}
		applog.L(ctx).Info("rolled back stale coupon reservation without persisted claim",
			zap.String("reservation_id", lease.ReservationID),
			zap.Int64("coupon_id", lease.CouponID),
			zap.Int64("user_id", lease.UserID),
		)
	}
	return nil
}
