package ansible

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// testdata/python.json holds what CPython returns for the same inputs;
// regenerate it with testdata/generate.py.
type golden struct {
	Shlex []struct {
		In  string   `json:"in"`
		Out []string `json:"out"`
		Err string   `json:"err"`
	} `json:"shlex"`
	Quote []struct {
		In  string `json:"in"`
		Out string `json:"out"`
	} `json:"quote"`
	Env        map[string]string `json:"env"`
	Expandvars []struct {
		In  string `json:"in"`
		Out string `json:"out"`
	} `json:"expandvars"`
	Expanduser []struct {
		In  string `json:"in"`
		Out string `json:"out"`
	} `json:"expanduser"`
	Timedelta []struct {
		Us  int64  `json:"us"`
		Out string `json:"out"`
	} `json:"timedelta"`
	Datetime []struct {
		In  []int  `json:"in"`
		Out string `json:"out"`
	} `json:"datetime"`
	Regex []struct {
		Re     string    `json:"re"`
		S      string    `json:"s"`
		Match  bool      `json:"match"`
		Groups []*string `json:"groups"`
	} `json:"regex"`
	Expand []struct {
		Re  string `json:"re"`
		S   string `json:"s"`
		T   string `json:"t"`
		Out string `json:"out"`
		Err string `json:"err"`
	} `json:"expand"`
	Fnmatch []struct {
		P   string `json:"p"`
		S   string `json:"s"`
		Out bool   `json:"out"`
	} `json:"fnmatch"`
	StrptimeTZ string `json:"strptime_tz"`
	Strptime   []struct {
		In  string  `json:"in"`
		Fmt string  `json:"fmt"`
		Out float64 `json:"out"`
		Err string  `json:"err"`
	} `json:"strptime"`
	PyFloat []struct {
		In  float64 `json:"in"`
		Out string  `json:"out"`
	} `json:"pyfloat"`
	Percent []struct {
		In  string `json:"in"`
		Out string `json:"out"`
		Err string `json:"err"`
	} `json:"percent"`
	SplitLines []struct {
		In  string   `json:"in"`
		Out []string `json:"out"`
	} `json:"splitlines"`
	Multiline []struct {
		Re        string `json:"re"`
		Multiline bool   `json:"multiline"`
		Err       string `json:"err"`
	} `json:"multiline"`
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	data, err := os.ReadFile("testdata/python.json")
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestShlexSplit(t *testing.T) {
	for _, c := range loadGolden(t).Shlex {
		got, err := ShlexSplit(c.In)
		if c.Err != "" {
			if err == nil || err.Error() != c.Err {
				t.Errorf("ShlexSplit(%q) error = %v, want %q", c.In, err, c.Err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.Out) && !(len(got) == 0 && len(c.Out) == 0) {
			t.Errorf("ShlexSplit(%q) = %q, %v; want %q", c.In, got, err, c.Out)
		}
	}
}

func TestShlexQuote(t *testing.T) {
	for _, c := range loadGolden(t).Quote {
		if got := ShlexQuote(c.In); got != c.Out {
			t.Errorf("ShlexQuote(%q) = %q, want %q", c.In, got, c.Out)
		}
	}
}

func TestExpandVars(t *testing.T) {
	g := loadGolden(t)
	os.Clearenv()
	for k, v := range g.Env {
		t.Setenv(k, v)
	}
	for _, c := range g.Expandvars {
		if got := ExpandVars(c.In); got != c.Out {
			t.Errorf("ExpandVars(%q) = %q, want %q", c.In, got, c.Out)
		}
	}
	for _, c := range g.Expanduser {
		if got := ExpandUser(c.In); got != c.Out {
			t.Errorf("ExpandUser(%q) = %q, want %q", c.In, got, c.Out)
		}
	}
}

func TestPyTime(t *testing.T) {
	g := loadGolden(t)
	for _, c := range g.Timedelta {
		if got := PyTimedelta(time.Duration(c.Us) * time.Microsecond); got != c.Out {
			t.Errorf("PyTimedelta(%dus) = %q, want %q", c.Us, got, c.Out)
		}
	}
	for _, c := range g.Datetime {
		d := c.In
		tm := time.Date(d[0], time.Month(d[1]), d[2], d[3], d[4], d[5], d[6]*1000, time.Local)
		if got := PyDatetime(tm); got != c.Out {
			t.Errorf("PyDatetime(%v) = %q, want %q", d, got, c.Out)
		}
	}
}

func TestCompilePython(t *testing.T) {
	for _, c := range loadGolden(t).Regex {
		re, err := CompilePython(c.Re)
		if err != nil {
			t.Errorf("CompilePython(%q): %v", c.Re, err)
			continue
		}
		loc := re.FindStringSubmatchIndex(c.S)
		if (loc != nil) != c.Match {
			t.Errorf("CompilePython(%q) on %q: match = %v, want %v", c.Re, c.S, loc != nil, c.Match)
			continue
		}
		if loc == nil {
			continue
		}
		var groups []*string
		for g := 1; g <= re.NumSubexp(); g++ {
			if loc[2*g] < 0 {
				groups = append(groups, nil)
			} else {
				s := c.S[loc[2*g]:loc[2*g+1]]
				groups = append(groups, &s)
			}
		}
		if len(groups) != len(c.Groups) {
			t.Errorf("CompilePython(%q) on %q: %d groups, want %d", c.Re, c.S, len(groups), len(c.Groups))
			continue
		}
		for i := range groups {
			if (groups[i] == nil) != (c.Groups[i] == nil) || groups[i] != nil && *groups[i] != *c.Groups[i] {
				t.Errorf("CompilePython(%q) on %q: group %d = %v, want %v", c.Re, c.S, i+1, groups[i], c.Groups[i])
			}
		}
	}
}

func TestCompilePythonUnsupported(t *testing.T) {
	for _, pat := range []string{`a(?=b)`, `a(?!b)`, `(?<=a)b`, `(?<!a)b`, `(a)\1`, `(?P<n>a)(?P=n)`, `(?x)a b`, `(?>a)`, `(a)?(?(1)b|c)`} {
		if _, err := CompilePython(pat); err == nil {
			t.Errorf("CompilePython(%q) succeeded, want an error", pat)
		}
	}
}

func TestExpandPython(t *testing.T) {
	for _, c := range loadGolden(t).Expand {
		re, err := CompilePython(c.Re)
		if err != nil {
			t.Fatalf("CompilePython(%q): %v", c.Re, err)
		}
		got, err := ExpandPython(re, c.T, c.S, re.FindStringSubmatchIndex(c.S))
		if c.Err != "" {
			if err == nil || err.Error() != c.Err {
				t.Errorf("ExpandPython(%q, %q) error = %v, want %q", c.Re, c.T, err, c.Err)
			}
			continue
		}
		if err != nil || got != c.Out {
			t.Errorf("ExpandPython(%q, %q) = %q, %v; want %q", c.Re, c.T, got, err, c.Out)
		}
	}
}

func TestFnmatch(t *testing.T) {
	for _, c := range loadGolden(t).Fnmatch {
		if got := fnmatchRegexp(c.P).MatchString(c.S); got != c.Out {
			t.Errorf("fnmatch(%q, %q) = %v, want %v", c.S, c.P, got, c.Out)
		}
	}
}

func TestSurrogateEscaped(t *testing.T) {
	cases := map[string]string{
		"plain":            `"plain"`,
		"é✓":               `"é✓"`,
		"a\x00\"\\\n":      `"a\u0000\"\\\n"`,
		"\xff\xfeok\x80":   `"\udcff\udcfeok\udc80"`,
		"\xe2\x9c":         `"\udce2\udc9c"`,
		"\xf0\x9d\x84\x9e": `"𝄞"`,
	}
	for in, want := range cases {
		if got := string(SurrogateEscaped([]byte(in))); got != want {
			t.Errorf("SurrogateEscaped(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestStrptime(t *testing.T) {
	g := loadGolden(t)
	loc, err := time.LoadLocation(g.StrptimeTZ)
	if err != nil {
		t.Skip("no time zone data:", err)
	}
	saved := time.Local
	time.Local = loc
	defer func() { time.Local = saved }()
	for _, c := range g.Strptime {
		got, err := Strptime(c.In, c.Fmt)
		switch {
		case c.Err != "" && (err == nil || err.Error() != c.Err):
			t.Errorf("Strptime(%q, %q) error = %v, want %q", c.In, c.Fmt, err, c.Err)
		case c.Err == "" && (err != nil || got != c.Out):
			t.Errorf("Strptime(%q, %q) = %v, %v, want %v", c.In, c.Fmt, got, err, c.Out)
		}
	}
}

func TestPyFloat(t *testing.T) {
	for _, c := range loadGolden(t).PyFloat {
		if got := PyFloat(c.In).String(); got != c.Out {
			t.Errorf("PyFloat(%v) = %s, want %s", c.In, got, c.Out)
		}
	}
}

func TestPercentFormat(t *testing.T) {
	for _, c := range loadGolden(t).Percent {
		got, err := PercentFormat(c.In, "X")
		switch {
		case c.Err != "" && (err == nil || err.Error() != c.Err):
			t.Errorf("PercentFormat(%q) error = %v, want %q", c.In, err, c.Err)
		case c.Err == "" && (err != nil || got != c.Out):
			t.Errorf("PercentFormat(%q) = %q, %v, want %q", c.In, got, err, c.Out)
		}
	}
}

func TestSplitLines(t *testing.T) {
	for _, c := range loadGolden(t).SplitLines {
		got := SplitLines(c.In)
		if len(got) == 0 && len(c.Out) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.Out) {
			t.Errorf("SplitLines(%q) = %q, Python %q", c.In, got, c.Out)
		}
	}
}

func TestPythonGlobalFlags(t *testing.T) {
	for _, c := range loadGolden(t).Multiline {
		if c.Err != "" {
			continue // Python rejects global flags after the start
		}
		if got := strings.ContainsRune(PythonGlobalFlags(c.Re), 'm'); got != c.Multiline {
			t.Errorf("PythonGlobalFlags(%q) multiline = %v, Python %v", c.Re, got, c.Multiline)
		}
	}
}
