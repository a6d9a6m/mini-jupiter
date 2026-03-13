package task

import (
	"errors"
	"time"

	mysqlerr "github.com/go-sql-driver/mysql"
)

func isDuplicateKey(err error) bool {
	var me *mysqlerr.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1062
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func taskBackoff(retryCount int, base time.Duration) time.Duration {
	if base <= 0 {
		base = 1 * time.Second
	}
	if retryCount < 0 {
		retryCount = 0
	}
	if retryCount > 8 {
		retryCount = 8
	}
	return base * time.Duration(1<<retryCount)
}
