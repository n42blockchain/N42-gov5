package olddb

import "errors"

var (
	errNestedTransactionsUnsupported = errors.New("nested transactions are not supported")
	errMutationBeginUnsupported      = errors.New("pending mutations cannot start a new transaction")
	errMutationDBUnavailable         = errors.New("pending mutations require an attached database transaction")
)
