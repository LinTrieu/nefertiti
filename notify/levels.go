package notify

import (
	"strconv"

	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/flag"
)

const (
	LEVEL_NOTHING = 0
	LEVEL_ERRORS  = 1
	LEVEL_DEFAULT = 2
	LEVEL_VERBOSE = 3
)

type Notification int

const (
	ERROR Notification = iota
	INFO
	FILLED
	OPENED
	CANCELLED
)

// --level=[0..3]
func Level() (int64, error) {
	var (
		err error
		out int64 = LEVEL_DEFAULT
	)
	arg := flag.Get("notify")
	if !arg.Exists {
		flag.Set("notify", strconv.FormatInt(out, 10))
	} else {
		if out, err = arg.Int64(); err != nil {
			return out, errors.Errorf("notify %v is invalid", arg)
		}
		if out < 0 || out > 3 {
			return out, errors.Errorf("notify %v is not in the 0..3 range", arg)
		}
	}
	return out, nil
}

func CanSend(level int64, notification Notification) bool {
	switch level {
	case LEVEL_NOTHING:
		return false
	case LEVEL_ERRORS:
		return notification == ERROR
	case LEVEL_VERBOSE:
		return true
	}
	return notification == ERROR || notification == INFO || notification == FILLED
}
