package mdbx

import (
	"os"
	"strconv"
)

var earlyWritebackEnabled = func() bool {
	enabled, _ := strconv.ParseBool(os.Getenv("N42_MDBX_EARLY_WRITEBACK"))
	return enabled
}()

// EarlyWritebackEnabled reports whether block writes should request data-page
// writeback before Commit. This never changes Commit's synchronization flags.
func EarlyWritebackEnabled() bool { return earlyWritebackEnabled }
