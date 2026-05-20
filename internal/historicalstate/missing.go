package historicalstate

import (
	"errors"
	"io/fs"
)

var errFileNotExist = fs.ErrNotExist

func isENOENT(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}
