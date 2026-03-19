package claim

import (
	"context"
	"database/sql"
	"time"

	"mini-jupiter/examples/Quan/internal/adjudication/hotpath"
	"mini-jupiter/examples/Quan/internal/adjudication/reservation"
	claimmodel "mini-jupiter/examples/Quan/internal/claim/model"
	claimrepo "mini-jupiter/examples/Quan/internal/claim/repository"
	claimservice "mini-jupiter/examples/Quan/internal/claim/service"
	"mini-jupiter/examples/Quan/internal/outbox"
	"mini-jupiter/examples/Quan/internal/sideeffect"
	"mini-jupiter/examples/Quan/internal/task"
	"mini-jupiter/pkg/mysql"
	"mini-jupiter/pkg/redis"
)

type ClaimRecord = claimmodel.Record
type Repository = claimrepo.Repository
type Service = claimservice.Service
type Adjudicator = hotpath.Adjudicator
type ReservationReconciler = reservation.ReservationReconciler
type ReservationReconcilerConfig = reservation.ReservationReconcilerConfig
type SideEffectDispatchConfig = sideeffect.DispatchConfig

type ClaimSideEffect = sideeffect.Record
type ClaimSideEffectPayload = sideeffect.Payload
type CreateClaimSideEffectParams = sideeffect.CreateParams

type campaignSnapshot = hotpath.CampaignSnapshot
type reservationLease = hotpath.ReservationLease

type SideEffectRepository struct {
	*sideeffect.Repository
}

type SideEffectDispatcher struct {
	*sideeffect.Dispatcher
}

type claimSideEffectRecorder interface {
	StageClaimCreatedTx(ctx context.Context, tx *sql.Tx, claimID, couponID, userID int64, traceID string) error
}

type reservationReconcilerRepository interface {
	FindClaimByIdempotency(ctx context.Context, couponID, userID int64, idemKey string) (claimmodel.Record, bool, error)
}

type reservationReconcilerAdjudicator interface {
	ListExpiredReservations(ctx context.Context, now time.Time, limit int) ([]hotpath.ReservationLease, error)
	Finalize(ctx context.Context, couponID, userID int64, idemKey, reservationID string, claimID int64) error
	Rollback(ctx context.Context, couponID, userID int64, idemKey, reservationID string) error
}

type sideEffectDispatchRepository interface {
	RecoverStaleProcessing(ctx context.Context, staleBefore time.Time, limit int) (int64, error)
	ListDispatchable(ctx context.Context, limit int) ([]sideeffect.Record, error)
	TryMarkProcessing(ctx context.Context, sideEffectID int64) (bool, error)
	MarkSuspended(ctx context.Context, sideEffectID int64, lastErr string) error
	MarkRetry(ctx context.Context, sideEffectID int64, delay time.Duration, lastErr string) error
	MarkDone(ctx context.Context, sideEffectID, asyncTaskID, outboxEventID int64) error
}

type sideEffectTaskRepository interface {
	Create(ctx context.Context, p task.CreateTaskParams) (task.AsyncTask, error)
	GetByTypeBiz(ctx context.Context, taskType, bizID string) (task.AsyncTask, error)
}

type sideEffectOutboxRepository interface {
	FindByAggregate(ctx context.Context, eventType, aggregateType, aggregateID string) (outbox.Event, bool, error)
	Create(ctx context.Context, p outbox.CreateEventParams) (outbox.Event, error)
}

var (
	ErrCampaignNotFound  = claimrepo.ErrCampaignNotFound
	ErrCampaignInactive  = claimrepo.ErrCampaignInactive
	ErrSoldOut           = claimrepo.ErrSoldOut
	ErrAlreadyClaimed    = claimrepo.ErrAlreadyClaimed
	ErrClaimLimitReached = claimrepo.ErrClaimLimitReached

	ErrClaimSideEffectDuplicate = sideeffect.ErrDuplicate
)

const (
	decisionCodeAdmitted     = hotpath.DecisionCodeAdmitted
	decisionCodeIdemHit      = hotpath.DecisionCodeIdemHit
	decisionCodePending      = hotpath.DecisionCodePending
	decisionCodeAlready      = hotpath.DecisionCodeAlready
	decisionCodeLimit        = hotpath.DecisionCodeLimit
	decisionCodeSoldOut      = hotpath.DecisionCodeSoldOut
	decisionCodeInactive     = hotpath.DecisionCodeInactive
	decisionCodeCampaignMiss = hotpath.DecisionCodeCampaignMiss

	ClaimSideEffectTypeClaimCreated = sideeffect.TypeClaimCreated

	ClaimSideEffectStatusPending    = sideeffect.StatusPending
	ClaimSideEffectStatusProcessing = sideeffect.StatusProcessing
	ClaimSideEffectStatusDone       = sideeffect.StatusDone
	ClaimSideEffectStatusSuspended  = sideeffect.StatusSuspended
)

func NewRepository(db *sql.DB, txm *mysql.TxManager, sideEffectRepo claimSideEffectRecorder) *Repository {
	if sideEffectRepo == nil {
		return claimrepo.NewRepository(db, txm, nil)
	}
	return claimrepo.NewRepository(db, txm, sideEffectRepo)
}

func NewService(repo *Repository, cache *redis.Client, idemTTL time.Duration) *Service {
	return claimservice.NewService(repo, cache, idemTTL)
}

func NewServiceWithAdjudicator(repo *Repository, adjudicator *Adjudicator, idemTTL time.Duration) *Service {
	return claimservice.NewServiceWithAdjudicator(repo, adjudicator, idemTTL)
}

func NewAdjudicator(client *redis.Client) *Adjudicator {
	return hotpath.NewAdjudicator(client)
}

func NewReservationReconciler(repo reservationReconcilerRepository, adjudicator reservationReconcilerAdjudicator, cfg ReservationReconcilerConfig) (*ReservationReconciler, error) {
	return reservation.NewReservationReconciler(repo, adjudicator, cfg)
}

func NewSideEffectRepository(db *sql.DB) *SideEffectRepository {
	return &SideEffectRepository{Repository: sideeffect.NewRepository(db)}
}

func NewSideEffectDispatcher(sideEffectRepo sideEffectDispatchRepository, taskRepo sideEffectTaskRepository, outboxRepo sideEffectOutboxRepository, cfg SideEffectDispatchConfig) (*SideEffectDispatcher, error) {
	dispatcher, err := sideeffect.NewDispatcher(sideEffectRepo, taskRepo, outboxRepo, cfg)
	if err != nil {
		return nil, err
	}
	return &SideEffectDispatcher{Dispatcher: dispatcher}, nil
}

func ClaimReservationID() string {
	return claimservice.ClaimReservationID()
}

func claimReservationID() string {
	return ClaimReservationID()
}

func ClaimCacheKey(couponID, userID int64) string {
	return claimservice.ClaimCacheKey(couponID, userID)
}

func claimCacheKey(couponID, userID int64) string {
	return ClaimCacheKey(couponID, userID)
}

func campaignMetaKey(couponID int64) string {
	return hotpath.CampaignMetaKey(couponID)
}

func campaignStockKey(couponID int64) string {
	return hotpath.CampaignStockKey(couponID)
}

func campaignUserCountKey(couponID int64) string {
	return hotpath.CampaignUserCountKey(couponID)
}

func idemDecisionKey(couponID, userID int64, idemKey string) string {
	return hotpath.IdemDecisionKey(couponID, userID, idemKey)
}

func reservationLeaseKey(reservationID string) string {
	return hotpath.ReservationLeaseKey(reservationID)
}

func MarshalClaimSideEffectPayload(payload ClaimSideEffectPayload) ([]byte, error) {
	return sideeffect.MarshalPayload(payload)
}

func ParseClaimSideEffectPayload(payload []byte) (ClaimSideEffectPayload, error) {
	return sideeffect.ParsePayload(payload)
}

func scanClaimSideEffect(row interface{ Scan(dest ...any) error }) (ClaimSideEffect, error) {
	return sideeffect.ScanRecord(row)
}

func (d *SideEffectDispatcher) recoverAndDispatchOnce(ctx context.Context) error {
	if d == nil || d.Dispatcher == nil {
		return nil
	}
	return d.Dispatcher.RecoverAndDispatchOnce(ctx)
}
