package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
)

// mapError turns a Go transport error into what urllib raises: a URLError
// while connecting or sending the request, an OSError ("Connection
// failure") while waiting for the response.
func (o *options) mapError(err error, wrote bool) error {
	var ue *unixSocketError
	if errors.As(err, &ue) {
		return &urlError{ue.Error()}
	}
	reason := pythonReason(err)
	if wrote {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			reason = "Remote end closed connection without response"
		}
		return &connFailure{reason}
	}
	return &urlError{reason}
}

// pythonReason is how Python prints the underlying error.
func pythonReason(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return "[Errno -2] Name or service not known"
		}
		return "[Errno -3] Temporary failure in name resolution"
	}
	if isTimeout(err) {
		return "timed out"
	}
	if msg, ok := sslReason(err); ok {
		return msg
	}
	return osReason(err)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, os.ErrDeadlineExceeded) || errors.As(err, &ne) && ne.Timeout()
}

// sslReason is the text of Python's SSL errors for certificate and TLS
// alert failures (without the "(_ssl.c:NNNN)" location Python adds).
func sslReason(err error) (string, bool) {
	verify := "[SSL: CERTIFICATE_VERIFY_FAILED] certificate verify failed: "
	var unknown x509.UnknownAuthorityError
	if errors.As(err, &unknown) {
		if c := unknown.Cert; c != nil && c.Issuer.String() == c.Subject.String() {
			return verify + "self-signed certificate", true
		}
		return verify + "unable to get local issuer certificate", true
	}
	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) {
		return verify + fmt.Sprintf("Hostname mismatch, certificate is not valid for '%s'.", hostErr.Host), true
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) {
		switch invalid.Reason {
		case x509.Expired:
			return verify + "certificate has expired", true
		default:
			return verify + invalid.Error(), true
		}
	}
	var verr *tls.CertificateVerificationError
	if errors.As(err, &verr) {
		return verify + verr.Err.Error(), true
	}
	var alert tls.AlertError
	if errors.As(err, &alert) || strings.Contains(err.Error(), "remote error: tls: ") {
		text := err.Error()
		if i := strings.Index(text, "remote error: tls: "); i >= 0 {
			text = text[i+len("remote error: tls: "):]
		}
		switch text {
		case "certificate required":
			return "[SSL: TLSV13_ALERT_CERTIFICATE_REQUIRED] tlsv13 alert certificate required", true
		case "handshake failure":
			return "[SSL: SSLV3_ALERT_HANDSHAKE_FAILURE] sslv3 alert handshake failure", true
		case "bad certificate":
			return "[SSL: SSLV3_ALERT_BAD_CERTIFICATE] sslv3 alert bad certificate", true
		case "unknown certificate authority":
			return "[SSL: TLSV1_ALERT_UNKNOWN_CA] tlsv1 alert unknown ca", true
		case "protocol version not supported":
			return "[SSL: TLSV1_ALERT_PROTOCOL_VERSION] tlsv1 alert protocol version", true
		}
		return "[SSL] " + text, true
	}
	return "", false
}

// osReason formats an error like Python's OSError: "[Errno 111] Connection refused".
func osReason(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		msg := errno.Error()
		if msg != "" {
			msg = strings.ToUpper(msg[:1]) + msg[1:]
		}
		return fmt.Sprintf("[Errno %d] %s", int(errno), msg)
	}
	if isTimeout(err) {
		return "timed out"
	}
	return err.Error()
}

// osErrorText is str(OSError) for a file path Python opened as bytes.
func osErrorText(err error, path string) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		msg := errno.Error()
		if msg != "" {
			msg = strings.ToUpper(msg[:1]) + msg[1:]
		}
		return fmt.Sprintf("[Errno %d] %s: b'%s'", int(errno), msg, path)
	}
	return err.Error()
}
