package claimrepo

import (
	"database/sql"
	"errors"

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
	db  *sql.DB
	txm *mysql.TxManager
}

func NewRepository(db *sql.DB, txm *mysql.TxManager) *Repository {
	if txm == nil {
		created, _ := mysql.NewTxManager(db)
		txm = created
	}
	return &Repository{
		db:  db,
		txm: txm,
	}
}
