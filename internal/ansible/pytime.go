package ansible

import (
	"fmt"
	"time"
)

// PyDatetime formats t like str() of a Python datetime: microseconds are
// left out when they are zero.
func PyDatetime(t time.Time) string {
	if t.Nanosecond()/1000 == 0 {
		return t.Format("2006-01-02 15:04:05")
	}
	return t.Format("2006-01-02 15:04:05.000000")
}

// PyTimedelta formats d like str() of a Python timedelta, such as 0:00:01.000123.
func PyTimedelta(d time.Duration) string {
	us := d.Microseconds()
	days := us / (86400 * 1e6)
	us -= days * 86400 * 1e6
	h, m, s, frac := us/3600e6, us/60e6%60, us/1e6%60, us%1e6
	out := fmt.Sprintf("%d:%02d:%02d", h, m, s)
	if frac != 0 {
		out += fmt.Sprintf(".%06d", frac)
	}
	switch {
	case days == 1:
		out = "1 day, " + out
	case days > 1:
		out = fmt.Sprintf("%d days, %s", days, out)
	}
	return out
}
