//go:build darwin && cgo

package tls

import (
	"crypto/sha256"
	stdtls "crypto/tls"
	"strings"
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

func appleTestFingerprint(certificate stdtls.Certificate) option.SHA256Fingerprint {
	hashValue := sha256.Sum256(certificate.Certificate[0])
	return hashValue[:]
}

func TestAppleClientHandshakeAcceptsPinnedSelfSignedCertificate(t *testing.T) {
	serverCertificate, _ := newAppleTestCertificate(t, "localhost")
	_, serverAddress := startAppleTLSTestServer(t, &stdtls.Config{
		Certificates: []stdtls.Certificate{serverCertificate},
	})

	clientConn, err := newAppleTestClientConn(t, serverAddress, option.OutboundTLSOptions{
		Enabled:           true,
		Engine:            C.TLSEngineApple,
		ServerName:        "localhost",
		CertificateSHA256: badoption.Listable[option.SHA256Fingerprint]{appleTestFingerprint(serverCertificate)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()
}

func TestAppleClientHandshakeRejectsWrongFingerprint(t *testing.T) {
	serverCertificate, _ := newAppleTestCertificate(t, "localhost")
	_, serverAddress := startAppleTLSTestServer(t, &stdtls.Config{
		Certificates: []stdtls.Certificate{serverCertificate},
	})

	clientConn, err := newAppleTestClientConn(t, serverAddress, option.OutboundTLSOptions{
		Enabled:           true,
		Engine:            C.TLSEngineApple,
		ServerName:        "localhost",
		CertificateSHA256: badoption.Listable[option.SHA256Fingerprint]{make([]byte, 32)},
	})
	if err == nil {
		clientConn.Close()
		t.Fatal("expected certificate fingerprint mismatch to fail")
	}
	// Asserting the reason, not just any failure: an untrusted self-signed certificate would
	// fail chain verification anyway, which would mask the pin never being applied.
	if !strings.Contains(err.Error(), "unrecognized remote certificate") {
		t.Fatalf("expected fingerprint mismatch error, got: %v", err)
	}
}
