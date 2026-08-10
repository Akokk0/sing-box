package tls_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/tls"

	"github.com/stretchr/testify/require"
)

// fixedCertificatePEM is a self-signed certificate kept verbatim so its fingerprints can be
// stated as literals instead of being recomputed by the test.
//
//	openssl x509 -in cert.pem -outform der | openssl dgst -sha256 -hex
//	openssl x509 -in cert.pem -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256 -hex
const fixedCertificatePEM = `-----BEGIN CERTIFICATE-----
MIICtDCCAZwCCQDcY42Rc+GZWTANBgkqhkiG9w0BAQsFADAbMRkwFwYDVQQDDBBm
aW5nZXJwcmludC10ZXN0MCAXDTI2MDgwNDA1NTcyOVoYDzIxMjYwNzExMDU1NzI5
WjAbMRkwFwYDVQQDDBBmaW5nZXJwcmludC10ZXN0MIIBIjANBgkqhkiG9w0BAQEF
AAOCAQ8AMIIBCgKCAQEAv4il4Ws4ISYfrC5zezTIP8RGRG5E+Y5WTgbCOEpPRs8d
Ai2DhxCopCXYsWVEQ6708WEk6m/Gv43xZPqkhtVit5TuKx6z7d/BqBJSMY4MezpE
oHwSEKW4Vux+c22bFjDkv9mMtP15NUxQGLTNNlKRIQ95xAZpEj/s0NBskluHQqvb
V1MOa5DctWkgXplCLyo9H5camarrtPWZ+hDNEk2x83c2V1cxpsDnAV0mIUHrWGX2
n6o6OUL74kIBsJi5WXWt55Lorye8hQAbiyoArbrz4uALMF/5UVMdmVF2NW/PwWL1
ZEbLOVCpTVyV7t3fna9SWBY4FMzGsVrqyh72Cz9WSwIDAQABMA0GCSqGSIb3DQEB
CwUAA4IBAQAI+xMAN49DISWLHUW4FOr35YTEGpuFLLtI2iI1sCouAJNwIE7ogsLC
IFrc6h75/Pnmo7xlelCPkZKNLVo1axUCZw1P6fvTT5KbrZIdLwVihTazNvKjTJiG
riIVPXt0ONU2NKjnzXaG9t5NiLbNqjUfPVCLh8qEIi79BO3KTWLB0BjElgSDsX1U
vxJKzGtdy3Tu3W3hLsXvZRnyw/MqEcO0AYdNLQtfZT8M7TcZ3uSoGpu354nGx49R
TLgops9yjGgfhUOs3uLis3gs7Cbe6goCyrnx4hZt9jvRDlhqS0cZLrgdoFNYW3h9
o/+Ni9KIBZF2zxlS9nIsxi/msjVXAJcw
-----END CERTIFICATE-----`

const (
	fixedCertificateSHA256 = "547d1e4b18a2824cf11502804648c2d4c79f9fc471297462c94e40bf430942c9"
	fixedPublicKeySHA256   = "bce0d2a7a507e4071173a2cd3141a0eeca6aa38478e3c24bbbddfa1738c223fb"
)

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	require.NoError(t, err)
	return decoded
}

func fixedCertificateDER(t *testing.T) []byte {
	t.Helper()
	block, _ := pem.Decode([]byte(fixedCertificatePEM))
	require.NotNil(t, block)
	return block.Bytes
}

func generateTestCertificateDER(t *testing.T, commonName string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	require.NoError(t, err)
	return certificate
}

func TestVerifyCertificateSHA256MatchesWholeCertificate(t *testing.T) {
	t.Parallel()

	require.NoError(t, tls.VerifyCertificateSHA256(
		[][]byte{mustDecodeHex(t, fixedCertificateSHA256)},
		[][]byte{fixedCertificateDER(t)},
	))
}

// The fingerprint covers the DER certificate, not its public key: pinning the public-key
// hash (what certificate_public_key_sha256 takes) must not be accepted here.
func TestVerifyCertificateSHA256RejectsPublicKeyHash(t *testing.T) {
	t.Parallel()

	require.Error(t, tls.VerifyCertificateSHA256(
		[][]byte{mustDecodeHex(t, fixedPublicKeySHA256)},
		[][]byte{fixedCertificateDER(t)},
	))
}

func TestVerifyCertificateSHA256RejectsUnknownFingerprint(t *testing.T) {
	t.Parallel()

	require.Error(t, tls.VerifyCertificateSHA256(
		[][]byte{make([]byte, 32)},
		[][]byte{fixedCertificateDER(t)},
	))
}

// Only the leaf counts. A pin turns off chain verification, so nothing ties the remaining
// entries to the leaf — accepting a match further down would let any server pass the pin by
// appending the pinned certificate, which is a public document.
func TestVerifyCertificateSHA256IgnoresTheRestOfTheChain(t *testing.T) {
	t.Parallel()

	leaf := generateTestCertificateDER(t, "leaf")
	issuer := fixedCertificateDER(t)

	require.Error(t, tls.VerifyCertificateSHA256(
		[][]byte{mustDecodeHex(t, fixedCertificateSHA256)},
		[][]byte{leaf, issuer},
	))
}

func TestVerifyCertificateSHA256AcceptsAnyOfMultipleFingerprints(t *testing.T) {
	t.Parallel()

	require.NoError(t, tls.VerifyCertificateSHA256(
		[][]byte{make([]byte, 32), mustDecodeHex(t, fixedCertificateSHA256)},
		[][]byte{fixedCertificateDER(t)},
	))
}

func TestVerifyCertificateSHA256RejectsEmptyChain(t *testing.T) {
	t.Parallel()

	require.Error(t, tls.VerifyCertificateSHA256(
		[][]byte{mustDecodeHex(t, fixedCertificateSHA256)},
		nil,
	))
}

// An unparsable leaf is a failure, not something to look past: the only certificate the pin
// speaks for is the first one.
func TestVerifyCertificateSHA256RejectsAnUnparsableLeaf(t *testing.T) {
	t.Parallel()

	require.Error(t, tls.VerifyCertificateSHA256(
		[][]byte{mustDecodeHex(t, fixedCertificateSHA256)},
		[][]byte{[]byte("not a certificate"), fixedCertificateDER(t)},
	))
}
