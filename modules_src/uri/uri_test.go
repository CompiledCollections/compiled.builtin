package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// testdata/python.json holds what CPython and ansible-core's own uri code
// return for the same inputs; regenerate it with testdata/generate.py.
type golden struct {
	JSON []struct{ In, Out string } `json:"json"`
	Form []struct {
		In    string `json:"in"`
		Out   any    `json:"out"`
		Error string `json:"error"`
		Msg   string `json:"msg"`
	} `json:"form"`
	Multipart []struct {
		In  string `json:"in"`
		Out *struct {
			ContentType string `json:"content_type"`
			Body        string `json:"body"`
		} `json:"out"`
		Error string `json:"error"`
		Msg   string `json:"msg"`
	} `json:"multipart"`
	MaskURL     []struct{ In, Out string } `json:"mask_url"`
	ContentType []struct {
		In          string `json:"in"`
		ContentType string `json:"content_type"`
		SubType     string `json:"sub_type"`
		Charset     string `json:"charset"`
	} `json:"content_type"`
	Disposition []struct {
		In  string  `json:"in"`
		Out *string `json:"out"`
	} `json:"disposition"`
	URLJoin       []struct{ Base, Ref, Out string } `json:"urljoin"`
	RedirectQuote []struct{ In, Out string }        `json:"redirect_quote"`
	Netrc         []struct {
		Host string   `json:"host"`
		Out  []string `json:"out"`
	} `json:"netrc"`
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

func decode(t *testing.T, s string) any {
	t.Helper()
	v, err := decodeOrdered([]byte(s))
	if err != nil {
		t.Fatalf("decodeOrdered(%s): %v", s, err)
	}
	return v
}

func TestPyJSONDumps(t *testing.T) {
	for _, c := range loadGolden(t).JSON {
		if got := pyJSONDumps(decode(t, c.In)); got != c.Out {
			t.Errorf("pyJSONDumps(%s)\n got %s\nwant %s", c.In, got, c.Out)
		}
	}
}

func TestFormURLEncoded(t *testing.T) {
	for _, c := range loadGolden(t).Form {
		got, err := formURLEncoded(decode(t, c.In))
		if c.Error != "" {
			pe, ok := err.(*pyError)
			if !ok || pe.kind != c.Error || pe.msg != c.Msg {
				t.Errorf("formURLEncoded(%s) error = %v, want %s: %s", c.In, err, c.Error, c.Msg)
			}
			continue
		}
		if err != nil || got != c.Out {
			t.Errorf("formURLEncoded(%s) = %q, %v; want %q", c.In, got, err, c.Out)
		}
	}
}

func TestPrepareMultipart(t *testing.T) {
	for _, c := range loadGolden(t).Multipart {
		ct, body, err := prepareMultipart(decode(t, c.In))
		if c.Error != "" {
			pe, ok := err.(*pyError)
			if !ok || pe.kind != c.Error || pe.msg != c.Msg {
				t.Errorf("prepareMultipart(%s) error = %v, want %s: %s", c.In, err, c.Error, c.Msg)
			}
			continue
		}
		if err != nil {
			t.Errorf("prepareMultipart(%s): %v", c.In, err)
			continue
		}
		boundary := strings.TrimSuffix(strings.SplitN(ct, `boundary="`, 2)[1], `"`)
		gotCT := strings.ReplaceAll(ct, boundary, "BOUNDARY")
		gotBody := strings.ReplaceAll(string(body), boundary, "BOUNDARY")
		// The golden body is bytes stored as latin-1 text.
		want := make([]byte, 0, len(c.Out.Body))
		for _, r := range c.Out.Body {
			want = append(want, byte(r))
		}
		if gotCT != c.Out.ContentType || gotBody != string(want) {
			t.Errorf("prepareMultipart(%s)\n got %q %q\nwant %q %q", c.In, gotCT, gotBody, c.Out.ContentType, want)
		}
	}
}

func TestMaskURL(t *testing.T) {
	for _, c := range loadGolden(t).MaskURL {
		if got := maskURL(c.In); got != c.Out {
			t.Errorf("maskURL(%q) = %q, want %q", c.In, got, c.Out)
		}
	}
}

func TestParseContentType(t *testing.T) {
	for _, c := range loadGolden(t).ContentType {
		ct, sub, charset := parseContentType(c.In)
		if ct != c.ContentType || sub != c.SubType || charset != c.Charset {
			t.Errorf("parseContentType(%q) = %q %q %q, want %q %q %q", c.In, ct, sub, charset, c.ContentType, c.SubType, c.Charset)
		}
	}
}

func TestResponseFilename(t *testing.T) {
	for _, c := range loadGolden(t).Disposition {
		r := &response{header: map[string][]string{"Content-Disposition": {c.In}}, url: "http://h/"}
		got := responseFilename(r)
		want := ""
		if c.Out != nil {
			want = *c.Out
		}
		if got != want {
			t.Errorf("responseFilename(%q) = %q, want %q", c.In, got, want)
		}
	}
}

func TestURLJoin(t *testing.T) {
	for _, c := range loadGolden(t).URLJoin {
		if got := urljoin(c.Base, c.Ref); got != c.Out {
			t.Errorf("urljoin(%q, %q) = %q, want %q", c.Base, c.Ref, got, c.Out)
		}
	}
}

func TestRedirectQuote(t *testing.T) {
	for _, c := range loadGolden(t).RedirectQuote {
		if got := quoteRedirect(c.In); got != c.Out {
			t.Errorf("quoteRedirect(%q) = %q, want %q", c.In, got, c.Out)
		}
	}
}

func TestNetrc(t *testing.T) {
	t.Setenv("NETRC", "testdata/netrc")
	for _, c := range loadGolden(t).Netrc {
		login, password, ok := netrcLogin(c.Host)
		var got []string
		if ok {
			got = []string{login, "", password}
		}
		if len(got) != len(c.Out) || len(got) == 3 && (got[0] != c.Out[0] || got[2] != c.Out[2]) {
			t.Errorf("netrcLogin(%q) = %q, want %q", c.Host, got, c.Out)
		}
	}
}

func TestBypassProxy(t *testing.T) {
	cases := []struct {
		host, noProxy string
		want          bool
	}{
		{"example.com", "", false},
		{"example.com", "*", true},
		{"example.com", "example.com", true},
		{"www.example.com", ".example.com", true},
		{"badexample.com", "example.com", false},
		{"127.0.0.1:8080", "127.0.0.1", true},
		{"host:8080", "host:8080", true},
	}
	for _, c := range cases {
		if got := bypassProxy(c.host, c.noProxy); got != c.want {
			t.Errorf("bypassProxy(%q, %q) = %v, want %v", c.host, c.noProxy, got, c.want)
		}
	}
}
