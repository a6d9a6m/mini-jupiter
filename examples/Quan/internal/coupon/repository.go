package coupon

import (
	"database/sql"
	"errors"

	"mini-jupiter/examples/Quan/internal/outbox"
	"mini-jupiter/examples/Quan/internal/task"
	"mini-jupiter/pkg/mysql"
)

var (
	ErrCampaignNotFound  = errors.New("campaign not found")
	ErrCampaignInactive  = errors.New("campaign not active")
	ErrSoldOut           = errors.New("coupon sold out")
	ErrAlreadyClaimed    = errors.New("already claimed")
	ErrClaimLimitReached = errors.New("claim limit reached")
)

type Repository struct {
	db                  *sql.DB
	txm                 *mysql.TxManager
	taskRepo            *task.Repository
	outboxRepo          *outbox.Repository
	defaultTaskMaxRetry int
}

func NewRepository(
	db *sql.DB,
	txm *mysql.TxManager,
	taskRepo *task.Repository,
	outboxRepo *outbox.Repository,
	defaultTaskMaxRetry int,
) *Repository {
	if defaultTaskMaxRetry <= 0 {
		defaultTaskMaxRetry = 5
	}
	if txm == nil {
		created, _ := mysql.NewTxManager(db)
		txm = created
	}
	return &Repository{
		db:                  db,
		txm:                 txm,
		taskRepo:            taskRepo,
		outboxRepo:          outboxRepo,
		defaultTaskMaxRetry: defaultTaskMaxRetry,
	}
}
