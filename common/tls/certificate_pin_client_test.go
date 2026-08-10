package tls_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	stdtls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/stretchr/testify/require"
)

func generateTestServerCertificate(t *testing.T, commonName string) stdtls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		DNSNames:     []string{commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	require.NoError(t, err)
	return stdtls.Certificate{Certificate: [][]byte{certificateDER}, PrivateKey: key}
}

func startTLSTestServer(t *testing.T, certificate stdtls.Certificate) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() {
		listener.Close()
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				serverConn := stdtls.Server(conn, &stdtls.Config{
					Certificates: []stdtls.Certificate{certificate},
				})
				if serverConn.HandshakeContext(context.Background()) == nil {
					// Hold the connection open long enough for the client to observe success.
					time.Sleep(100 * time.Millisecond)
				}
			}()
		}
	}()
	return listener.Addr().String()
}

func fingerprintOf(certificate stdtls.Certificate) option.SHA256Fingerprint {
	hashValue := sha256.Sum256(certificate.Certificate[0])
	return hashValue[:]
}

func dialWithTLSOptions(t *testing.T, serverAddress string, options option.OutboundTLSOptions) error {
	t.Helper()
	ctx := context.Background()
	config, err := tls.NewSTDClient(ctx, nil, "localhost", options)
	if err != nil {
		return err
	}
	conn, err := net.Dial("tcp", serverAddress)
	require.NoError(t, err)
	defer conn.Close()
	handshakeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tlsConn, err := tls.ClientHandshake(handshakeCtx, conn, config)
	if err != nil {
		return err
	}
	tlsConn.Close()
	return nil
}

// A self-signed certificate is not trusted by the system pool, so the pin is the only reason
// this handshake can succeed — which is exactly the "fingerprint instead of a CA" use case.
func TestSTDClientHandshakeAcceptsPinnedSelfSignedCertificate(t *testing.T) {
	t.Parallel()

	certificate := generateTestServerCertificate(t, "localhost")
	serverAddress := startTLSTestServer(t, certificate)

	require.NoError(t, dialWithTLSOptions(t, serverAddress, option.OutboundTLSOptions{
		Enabled:           true,
		ServerName:        "localhost",
		CertificateSHA256: badoption.Listable[option.SHA256Fingerprint]{fingerprintOf(certificate)},
	}))
}

// Control for the test above: without the pin the same handshake must fail.
func TestSTDClientHandshakeRejectsUntrustedCertificateWithoutPin(t *testing.T) {
	t.Parallel()

	certificate := generateTestServerCertificate(t, "localhost")
	serverAddress := startTLSTestServer(t, certificate)

	require.Error(t, dialWithTLSOptions(t, serverAddress, option.OutboundTLSOptions{
		Enabled:    true,
		ServerName: "localhost",
	}))
}

func TestSTDClientHandshakeRejectsWrongFingerprint(t *testing.T) {
	t.Parallel()

	certificate := generateTestServerCertificate(t, "localhost")
	serverAddress := startTLSTestServer(t, certificate)

	err := dialWithTLSOptions(t, serverAddress, option.OutboundTLSOptions{
		Enabled:           true,
		ServerName:        "localhost",
		CertificateSHA256: badoption.Listable[option.SHA256Fingerprint]{make([]byte, 32)},
	})
	// Asserting the reason, not just any failure: an untrusted self-signed certificate would
	// fail chain verification anyway, which would mask the pin never being applied.
	require.ErrorContains(t, err, "unrecognized remote certificate")
}

// The pin replaces chain verification, so combining it with a custom CA is a configuration
// mistake and must be reported instead of silently ignoring one of them.
func TestSTDClientRejectsFingerprintCombinedWithCertificate(t *testing.T) {
	t.Parallel()

	certificate := generateTestServerCertificate(t, "localhost")

	// A valid PEM, so the failure can only come from the conflict check rather than from
	// the certificate itself being unparsable.
	_, err := tls.NewSTDClient(context.Background(), nil, "localhost", option.OutboundTLSOptions{
		Enabled:           true,
		ServerName:        "localhost",
		Certificate:       badoption.Listable[string]{fixedCertificatePEM},
		CertificateSHA256: badoption.Listable[option.SHA256Fingerprint]{fingerprintOf(certificate)},
	})
	require.ErrorContains(t, err, "certificate_sha256")
}

// The pinned certificate is a public document — anyone can fetch it with `openssl s_client`.
// Since a pin turns off chain verification, a server may put anything it likes behind the
// leaf, so an attacker holding an unrelated key must not get through by simply appending the
// pinned certificate to its own chain.
func TestSTDClientHandshakeRejectsPinnedCertificateSmuggledIntoTheChain(t *testing.T) {
	t.Parallel()

	pinned := generateTestServerCertificate(t, "localhost")
	attacker := generateTestServerCertificate(t, "localhost")
	attacker.Certificate = append(attacker.Certificate, pinned.Certificate[0])
	serverAddress := startTLSTestServer(t, attacker)

	err := dialWithTLSOptions(t, serverAddress, option.OutboundTLSOptions{
		Enabled:           true,
		ServerName:        "localhost",
		CertificateSHA256: badoption.Listable[option.SHA256Fingerprint]{fingerprintOf(pinned)},
	})
	require.ErrorContains(t, err, "unrecognized remote certificate")
}
