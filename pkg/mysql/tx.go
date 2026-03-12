package mysql

import (
	"context"
	"database/sql"
	"fmt"
)

type TxManager struct {
	db *sql.DB
}

func NewTxManager(db *sql.DB) (*TxManager, error) {
	if db == nil {
		return nil, fmt.Errorf("tx manager db is nil")
	}
	return &TxManager{db: db}, nil
}

func (m *TxManager) WithinTx(ctx context.Context, opts *sql.TxOptions, fn func(ctx context.Context, tx *sql.Tx) error) (err error) {
	if fn == nil {
		return fmt.Errorf("tx callback is nil")
	}
	tx, err := m.db.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
			return
		}
		if commitErr := tx.Commit(); commitErr != nil {
			err = fmt.Errorf("commit tx: %w", commitErr)
		}
	}()
	err = fn(ctx, tx)
	return err
}
