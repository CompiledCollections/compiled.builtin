package ansible

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// PyFloat is a float written the way Python writes it (repr): 1609459200.0
// rather than Go's 1609459200, 1e-05 rather than 1e-5. Results carrying
// timestamps need it to read back the same as a Python module's.
type PyFloat float64

func (f PyFloat) MarshalJSON() ([]byte, error) { return []byte(f.String()), nil }

func (f PyFloat) String() string {
	v := float64(f)
	sci := strconv.FormatFloat(v, 'e', -1, 64)
	exp, _ := strconv.Atoi(sci[strings.IndexByte(sci, 'e')+1:])
	if exp < -4 || exp >= 16 {
		return sci
	}
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.ContainsAny(s, ".") {
		s += ".0"
	}
	return s
}

// strptime directives Python's _strptime supports here, as regexps.
var strptimeDirectives = map[byte]string{
	'd': `(3[01]|[12]\d|0[1-9]|[1-9]| [1-9])`,
	'f': `([0-9]{1,6})`,
	'H': `(2[0-3]|[0-1]\d|\d)`,
	'I': `(1[0-2]|0[1-9]|[1-9])`,
	'j': `(36[0-6]|3[0-5]\d|[12]\d\d|0[1-9]\d|00[1-9]|[1-9]\d|0[1-9]|[1-9])`,
	'm': `(1[0-2]|0[1-9]|[1-9])`,
	'M': `([0-5]\d|\d)`,
	'S': `(6[0-1]|[0-5]\d|\d)`,
	'y': `(\d\d)`,
	'Y': `(\d\d\d\d)`,
	'b': `(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)`,
	'B': `(january|february|march|april|may|june|july|august|september|october|november|december)`,
	'a': `(mon|tue|wed|thu|fri|sat|sun)`,
	'A': `(monday|tuesday|wednesday|thursday|friday|saturday|sunday)`,
	'p': `(am|pm)`,
	'%': `(%)`,
}

// Strptime is time.strptime then time.mktime: value parsed with a Python
// format, as local time, in seconds since the epoch. Errors carry
// Python's messages.
func Strptime(value, format string) (float64, error) {
	var re strings.Builder
	var kinds []byte
	re.WriteString(`(?i)^`)
	for i := 0; i < len(format); i++ {
		c := format[i]
		switch {
		case c == '%' && i+1 < len(format):
			i++
			d, ok := strptimeDirectives[format[i]]
			if !ok {
				return 0, fmt.Errorf("'%c' is a bad directive in format '%s'", format[i], format)
			}
			re.WriteString(d)
			kinds = append(kinds, format[i])
		case c == '%':
			return 0, fmt.Errorf("stray %% in format '%s'", format)
		case c == ' ' || c == '\t' || c == '\n':
			re.WriteString(`\s+`)
			for i+1 < len(format) && strings.IndexByte(" \t\n", format[i+1]) >= 0 {
				i++
			}
		default:
			re.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	m := regexp.MustCompile(re.String()).FindStringSubmatchIndex(value)
	if m == nil {
		return 0, fmt.Errorf("time data '%s' does not match format '%s'", value, format)
	}
	if m[1] != len(value) {
		return 0, fmt.Errorf("unconverted data remains: %s", value[m[1]:])
	}

	year, month, day, hour, minute, second, nsec := 1900, 1, 1, 0, 0, 0, 0
	yday, pm, hour12 := -1, -1, -1
	months := []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	for k, kind := range kinds {
		s := value[m[2+2*k]:m[3+2*k]]
		n, _ := strconv.Atoi(strings.TrimSpace(s))
		switch kind {
		case 'd':
			day = n
		case 'f':
			s += strings.Repeat("0", 6-len(s))
			us, _ := strconv.Atoi(s)
			nsec = us * 1000
		case 'H':
			hour = n
		case 'I':
			hour12 = n
		case 'j':
			yday = n
		case 'm':
			month = n
		case 'M':
			minute = n
		case 'S':
			second = n
		case 'y':
			if n <= 68 {
				year = 2000 + n
			} else {
				year = 1900 + n
			}
		case 'Y':
			year = n
		case 'b', 'B':
			for i, name := range months {
				if strings.HasPrefix(strings.ToLower(s), name) {
					month = i + 1
				}
			}
		case 'p':
			if strings.EqualFold(s, "pm") {
				pm = 1
			} else {
				pm = 0
			}
		}
	}
	if hour12 >= 0 {
		hour = hour12 % 12
		if pm == 1 {
			hour += 12
		}
	}
	if yday <= 0 {
		if last := daysIn(year, month); day > last {
			return 0, fmt.Errorf("day %d must be in range 1..%d for month %d in year %d", day, last, month, year)
		}
	}
	t := time.Date(year, time.Month(month), day, hour, minute, second, nsec, time.Local)
	if yday > 0 {
		t = time.Date(year, 1, yday, hour, minute, second, nsec, time.Local)
	}
	// mktime ignores the fraction of a second.
	return float64(t.Unix()), nil
}

// daysIn is the number of days in a month of a year.
func daysIn(year, month int) int {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
