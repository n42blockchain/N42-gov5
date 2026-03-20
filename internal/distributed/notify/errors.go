package notify

import "errors"

var ErrMaxSubscribers = errors.New("notify: max subscribers reached")
