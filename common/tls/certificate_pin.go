package tls

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

// VerifyCertificateSHA256 pins the SHA-256 hash of a DER-encoded certificate. The whole chain
// is scanned, so a fingerprint taken from an issuer also authorises the peer. Unparsable
// entries are skipped so a malformed leaf cannot hide a pinned certificate below it.
func VerifyCertificateSHA256(knownHashValues [][]byte, rawCerts [][]byte) error {
	for _, rawCert := range rawCerts {
		certificate, err := x509.ParseCertificate(rawCert)
		if err != nil {
			continue
		}
		hashValue := sha256.Sum256(certificate.Raw)
		for _, value := range knownHashValues {
			if bytes.Equal(value, hashValue[:]) {
				return nil
			}
		}
	}
	if len(rawCerts) > 0 {
		leafCertificate, err := x509.ParseCertificate(rawCerts[0])
		if err == nil {
			hashValue := sha256.Sum256(leafCertificate.Raw)
			return E.New("unrecognized remote certificate: ", hex.EncodeToString(hashValue[:]))
		}
	}
	return E.New("unrecognized remote certificate")
}

// CertificatePins holds the pinning options of a client, which replace chain verification.
type CertificatePins struct {
	PublicKeySHA256   [][]byte
	CertificateSHA256 [][]byte
}

func (p CertificatePins) Enabled() bool {
	return len(p.PublicKeySHA256) > 0 || len(p.CertificateSHA256) > 0
}

func (p CertificatePins) clone() CertificatePins {
	return CertificatePins{
		PublicKeySHA256:   append([][]byte(nil), p.PublicKeySHA256...),
		CertificateSHA256: append([][]byte(nil), p.CertificateSHA256...),
	}
}

// Verify applies every configured pin; all of them must match.
func (p CertificatePins) Verify(rawCerts [][]byte) error {
	if len(p.PublicKeySHA256) > 0 {
		if len(rawCerts) == 0 {
			return E.New("no peer certificates")
		}
		err := VerifyPublicKeySHA256(p.PublicKeySHA256, rawCerts)
		if err != nil {
			return err
		}
	}
	if len(p.CertificateSHA256) > 0 {
		err := VerifyCertificateSHA256(p.CertificateSHA256, rawCerts)
		if err != nil {
			return err
		}
	}
	return nil
}

func (p CertificatePins) optionName() string {
	switch {
	case len(p.PublicKeySHA256) > 0 && len(p.CertificateSHA256) > 0:
		return "certificate_public_key_sha256/certificate_sha256"
	case len(p.CertificateSHA256) > 0:
		return "certificate_sha256"
	default:
		return "certificate_public_key_sha256"
	}
}

// ParseCertificatePins collects the pinning options and rejects combining them with a custom
// certificate authority, since a pin bypasses the chain verification that a CA would drive.
func ParseCertificatePins(options option.OutboundTLSOptions) (CertificatePins, error) {
	pins := CertificatePins{
		PublicKeySHA256:   options.CertificatePublicKeySHA256,
		CertificateSHA256: certificateSHA256Values(options.CertificateSHA256),
	}
	if pins.Enabled() && (len(options.Certificate) > 0 || options.CertificatePath != "") {
		return CertificatePins{}, E.New(pins.optionName(), " is conflict with certificate or certificate_path")
	}
	return pins, nil
}

func certificateSHA256Values(fingerprints []option.SHA256Fingerprint) [][]byte {
	if len(fingerprints) == 0 {
		return nil
	}
	values := make([][]byte, 0, len(fingerprints))
	for _, fingerprint := range fingerprints {
		values = append(values, fingerprint)
	}
	return values
}
