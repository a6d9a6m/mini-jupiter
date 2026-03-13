package task

import (
	"database/sql"
	"errors"

	"mini-jupiter/pkg/mysql"
)

var (
	ErrTaskDuplicate     = errors.New("task duplicate")
	ErrTaskNotFound      = errors.New("task not found")
	ErrTaskNotReplayable = errors.New("task not replayable")
)

type Repository struct {
	db  *sql.DB
	txm *mysql.TxManager
}

func NewRepository(db *sql.DB, txm *mysql.TxManager) *Repository {
	return &Repository{db: db, txm: txm}
}
