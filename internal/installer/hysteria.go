package installer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"time"

	"xray-installer/internal/config"
)

func newHysteriaParameters(domain string, now time.Time) (config.HysteriaParameters, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return config.HysteriaParameters{}, err
	}
	certificate := x509.Certificate{
		DNSNames:    []string{domain},
		NotBefore:   now.Add(-time.Hour),
		NotAfter:    now.AddDate(5, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &certificate, &certificate, &key.PublicKey, key)
	if err != nil {
		return config.HysteriaParameters{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return config.HysteriaParameters{}, err
	}
	fingerprint := sha256.Sum256(certDER)
	return config.HysteriaParameters{
		NodeName:    HysteriaNodeName,
		Password:    rand.Text(),
		Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})),
		Key:         string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
		Fingerprint: hex.EncodeToString(fingerprint[:]),
	}, nil
}
