package option

import (
	"testing"

	"github.com/sagernet/sing/common/json"

	"github.com/stretchr/testify/require"
)

// knownFingerprintHex and knownFingerprintBytes are the same 32-byte value written
// two ways, so parsing can be checked against a literal instead of a recomputation.
const knownFingerprintHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

var knownFingerprintBytes = []byte{
	0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
	0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
	0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
	0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f,
}

func TestSHA256FingerprintUnmarshalJSONAcceptsHex(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		value string
	}{
		{"lower case", knownFingerprintHex},
		{"upper case", "000102030405060708090A0B0C0D0E0F101112131415161718191A1B1C1D1E1F"},
		{"colon separated", "00:01:02:03:04:05:06:07:08:09:0a:0b:0c:0d:0e:0f:10:11:12:13:14:15:16:17:18:19:1a:1b:1c:1d:1e:1f"},
		{"space separated", "00 01 02 03 04 05 06 07 08 09 0a 0b 0c 0d 0e 0f 10 11 12 13 14 15 16 17 18 19 1a 1b 1c 1d 1e 1f"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var fingerprint SHA256Fingerprint
			require.NoError(t, json.Unmarshal([]byte(`"`+testCase.value+`"`), &fingerprint))
			require.Equal(t, knownFingerprintBytes, []byte(fingerprint))
		})
	}
}

func TestSHA256FingerprintUnmarshalJSONRejectsMalformed(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		value string
	}{
		{"too short", "0001020304"},
		{"too long", knownFingerprintHex + "00"},
		{"odd length", knownFingerprintHex[1:]},
		{"non hex", "zz0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"},
		{"empty", ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var fingerprint SHA256Fingerprint
			require.Error(t, json.Unmarshal([]byte(`"`+testCase.value+`"`), &fingerprint))
		})
	}
}

func TestSHA256FingerprintMarshalJSONRoundTrips(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(SHA256Fingerprint(knownFingerprintBytes))
	require.NoError(t, err)
	require.Equal(t, `"`+knownFingerprintHex+`"`, string(encoded))
}

func TestOutboundTLSOptionsParsesCertificateSHA256(t *testing.T) {
	t.Parallel()

	var options OutboundTLSOptions
	require.NoError(t, json.Unmarshal([]byte(`{
		"enabled": true,
		"server_name": "example.com",
		"certificate_sha256": "`+knownFingerprintHex+`"
	}`), &options))
	require.Len(t, options.CertificateSHA256, 1)
	require.Equal(t, knownFingerprintBytes, []byte(options.CertificateSHA256[0]))
}
