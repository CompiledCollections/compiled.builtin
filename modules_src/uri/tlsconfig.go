package uri

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// buildTLS is module_utils.urls.make_context. Python builds it for every
// request, plain HTTP included, so its errors (missing files, no usable
// cipher) fail any request; they are reported as "Connection failure".
func buildTLS(validate bool, caPath string, ciphers []string, clientCert, clientKey string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if !validate {
		cfg.InsecureSkipVerify = true
	} else if caPath != "" {
		data, err := os.ReadFile(caPath)
		if err != nil {
			return nil, &connFailure{osReason(err)}
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return nil, &connFailure{"[X509: NO_CERTIFICATE_OR_CRL_FOUND] no certificate or crl found"}
		}
		cfg.RootCAs = pool
	} else {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		for _, dir := range extraCADirs() {
			addCADir(pool, dir)
		}
		cfg.RootCAs = pool
	}

	if len(ciphers) > 0 {
		suites, err := cipherSuites(ciphers)
		if err != nil {
			return nil, err
		}
		cfg.CipherSuites = suites
	}

	if clientCert != "" {
		certPEM, err := os.ReadFile(clientCert)
		if err != nil {
			return nil, &connFailure{osReason(err)}
		}
		keyPEM := certPEM
		if clientKey != "" {
			if keyPEM, err = os.ReadFile(clientKey); err != nil {
				return nil, &connFailure{osReason(err)}
			}
		}
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, &connFailure{"[SSL] PEM lib"}
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return cfg, nil
}

// extraCADirs are the directories get_ca_certs adds to the system trust.
func extraCADirs() []string {
	dirs := []string{}
	switch runtime.GOOS {
	case "linux":
		dirs = append(dirs, "/etc/pki/ca-trust/extracted/pem", "/etc/pki/tls/certs", "/usr/share/ca-certificates/cacert.org")
	case "freebsd":
		dirs = append(dirs, "/usr/local/share/certs")
	case "openbsd":
		dirs = append(dirs, "/etc/ssl")
	case "netbsd":
		dirs = append(dirs, "/etc/openssl/certs")
	case "solaris":
		dirs = append(dirs, "/opt/local/etc/openssl/certs")
	case "aix":
		dirs = append(dirs, "/var/ssl/certs", "/opt/freeware/etc/ssl/certs")
	case "darwin":
		dirs = append(dirs, "/usr/local/etc/openssl")
	}
	return append(dirs, "/etc/ansible")
}

func addCADir(pool *x509.CertPool, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		switch filepath.Ext(e.Name()) {
		case ".pem", ".cer", ".crt":
		default:
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for {
			var block *pem.Block
			block, data = pem.Decode(data)
			if block == nil {
				break
			}
			if block.Type != "CERTIFICATE" {
				continue
			}
			if c, err := x509.ParseCertificate(block.Bytes); err == nil {
				pool.AddCert(c)
			}
		}
	}
}

// opensslCiphers maps OpenSSL cipher names, which the ciphers option takes
// as Python does, to Go's TLS 1.2 suites. IANA names work too. TLS 1.3
// suites can't be restricted, in Python or Go.
var opensslCiphers = map[string]uint16{
	"ECDHE-ECDSA-AES128-GCM-SHA256": tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	"ECDHE-RSA-AES128-GCM-SHA256":   tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	"ECDHE-ECDSA-AES256-GCM-SHA384": tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	"ECDHE-RSA-AES256-GCM-SHA384":   tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	"ECDHE-ECDSA-CHACHA20-POLY1305": tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	"ECDHE-RSA-CHACHA20-POLY1305":   tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
	"ECDHE-ECDSA-AES128-SHA256":     tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256,
	"ECDHE-RSA-AES128-SHA256":       tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256,
	"ECDHE-ECDSA-AES128-SHA":        tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
	"ECDHE-RSA-AES128-SHA":          tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
	"ECDHE-ECDSA-AES256-SHA":        tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
	"ECDHE-RSA-AES256-SHA":          tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
	"AES128-GCM-SHA256":             tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
	"AES256-GCM-SHA384":             tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
	"AES128-SHA256":                 tls.TLS_RSA_WITH_AES_128_CBC_SHA256,
	"AES128-SHA":                    tls.TLS_RSA_WITH_AES_128_CBC_SHA,
	"AES256-SHA":                    tls.TLS_RSA_WITH_AES_256_CBC_SHA,
}

// cipherSuites resolves the ciphers option, joined with ':' as Python does.
// OpenSSL keywords and exclusions (HIGH, DEFAULT, !aNULL...) have no Go
// equivalent and are reported as such.
func cipherSuites(ciphers []string) ([]uint16, error) {
	iana := map[string]uint16{}
	for _, s := range append(tls.CipherSuites(), tls.InsecureCipherSuites()...) {
		iana[s.Name] = s.ID
	}
	var out []uint16
	for _, name := range strings.Split(strings.Join(ciphers, ":"), ":") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if id, ok := opensslCiphers[name]; ok {
			out = append(out, id)
		} else if id, ok := iana[name]; ok {
			out = append(out, id)
		} else if strings.ContainsAny(name[:1], "!+-") || strings.Contains(name, "@") || !strings.Contains(name, "-") {
			// Operators (!x, +x, -x, @STRENGTH) and keywords (HIGH, DEFAULT, aNULL...)
			return nil, &unsupportedError{fmt.Sprintf("the cipher string %q uses OpenSSL keywords or operators, which compiled.builtin.uri does not support; list cipher names instead", name)}
		}
		// Like OpenSSL, unknown cipher names are skipped.
	}
	if len(out) == 0 {
		return nil, &connFailure{"('No cipher can be selected.',)"}
	}
	return out, nil
}

// unsupportedError is a feature of the Python module this one can't offer.
type unsupportedError struct{ msg string }

func (e *unsupportedError) Error() string { return e.msg }
