package claimrepo

import (
	"errors"

	mysqlerr "github.com/go-sql-driver/mysql"
)

func isDuplicateKey(err error) bool {
	var me *mysqlerr.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1062
	}
	return false
}
