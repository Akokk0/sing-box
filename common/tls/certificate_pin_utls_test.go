//go:build with_utls

package tls_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/stretchr/testify/require"
)

func dialWithUTLSOptions(t *testing.T, serverAddress string, options option.OutboundTLSOptions) error {
	t.Helper()
	options.UTLS = &option.OutboundUTLSOptions{Enabled: true}
	ctx := context.Background()
	config, err := tls.NewUTLSClient(ctx, nil, "localhost", options)
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

func TestUTLSClientHandshakeAcceptsPinnedSelfSignedCertificate(t *testing.T) {
	t.Parallel()

	certificate := generateTestServerCertificate(t, "localhost")
	serverAddress := startTLSTestServer(t, certificate)

	require.NoError(t, dialWithUTLSOptions(t, serverAddress, option.OutboundTLSOptions{
		Enabled:           true,
		ServerName:        "localhost",
		CertificateSHA256: badoption.Listable[option.SHA256Fingerprint]{fingerprintOf(certificate)},
	}))
}

func TestUTLSClientHandshakeRejectsWrongFingerprint(t *testing.T) {
	t.Parallel()

	certificate := generateTestServerCertificate(t, "localhost")
	serverAddress := startTLSTestServer(t, certificate)

	err := dialWithUTLSOptions(t, serverAddress, option.OutboundTLSOptions{
		Enabled:           true,
		ServerName:        "localhost",
		CertificateSHA256: badoption.Listable[option.SHA256Fingerprint]{make([]byte, 32)},
	})
	require.ErrorContains(t, err, "unrecognized remote certificate")
}

func TestUTLSClientRejectsFingerprintCombinedWithCertificate(t *testing.T) {
	t.Parallel()

	certificate := generateTestServerCertificate(t, "localhost")

	err := dialWithUTLSOptions(t, "", option.OutboundTLSOptions{
		Enabled:           true,
		ServerName:        "localhost",
		Certificate:       badoption.Listable[string]{fixedCertificatePEM},
		CertificateSHA256: badoption.Listable[option.SHA256Fingerprint]{fingerprintOf(certificate)},
	})
	require.ErrorContains(t, err, "certificate_sha256")
}
