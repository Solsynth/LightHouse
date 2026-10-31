package passkit

import (
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"

	"github.com/digitorus/pkcs7"
	"golang.org/x/crypto/pkcs12"
)

// signingMaterial is the loaded pass-signing identity.
type signingMaterial struct {
	certificate *x509.Certificate
	key         crypto.Signer
	chain       []*x509.Certificate
	wwdr        []*x509.Certificate
}

// loadSigningMaterial reads the signer certificate + private key and the
// Apple WWDR certificate. Files may be PEM or DER; a DER PKCS#12 bundle is
// accepted as the signer file (Passport's password is ignored, but a
// configured password is passed to the PKCS#12 decoder).
func loadSigningMaterial(certificatePath, password, wwdrPath string) (*signingMaterial, error) {
	certificateData, err := os.ReadFile(certificatePath)
	if err != nil {
		return nil, fmt.Errorf("read signing certificate: %w", err)
	}
	certs, key, pemFound := parsePEMIdentity(certificateData)
	if key == nil {
		// A .p12/.pfx is DER. Decode it with the configured password (empty
		// for the passwordless bundles Passport ships).
		p12Key, p12Cert, p12Err := pkcs12.Decode(certificateData, password)
		if p12Err == nil && p12Cert != nil {
			signer, ok := p12Key.(crypto.Signer)
			if !ok {
				return nil, errors.New("Apple Wallet signing key does not implement crypto.Signer")
			}
			certs = []*x509.Certificate{p12Cert}
			key = signer
			pemFound = true
		}
	}
	if len(certs) == 0 || key == nil {
		if pemFound {
			return nil, errors.New("Apple Wallet signing certificate has no private key; provide a PEM bundle with the key or a PKCS#12 file")
		}
		return nil, errors.New("Apple Wallet signing certificate is not a PEM identity or a readable PKCS#12 bundle")
	}

	wwdrData, err := os.ReadFile(wwdrPath)
	if err != nil {
		return nil, fmt.Errorf("read Apple WWDR certificate: %w", err)
	}
	wwdr, err := parseCertificates(wwdrData)
	if err != nil {
		return nil, fmt.Errorf("parse Apple WWDR certificate: %w", err)
	}
	if len(wwdr) == 0 {
		return nil, errors.New("Apple WWDR certificate file contains no certificate")
	}

	return &signingMaterial{
		certificate: certs[0],
		key:         key,
		chain:       certs[1:],
		wwdr:        wwdr,
	}, nil
}

// signManifest produces the detached PKCS#7 signature over manifest.json.
func signManifest(manifest []byte, material *signingMaterial) ([]byte, error) {
	if material == nil || material.certificate == nil || material.key == nil {
		return nil, errors.New("Apple Wallet signing material is unavailable")
	}
	signed, err := pkcs7.NewSignedData(manifest)
	if err != nil {
		return nil, fmt.Errorf("initialize PKCS#7 signature: %w", err)
	}
	signed.SetDigestAlgorithm(pkcs7.OIDDigestAlgorithmSHA256)
	if err := signed.AddSigner(material.certificate, material.key, pkcs7.SignerInfoConfig{}); err != nil {
		return nil, fmt.Errorf("add PKCS#7 signer: %w", err)
	}
	for _, cert := range material.chain {
		if cert != nil && !cert.Equal(material.certificate) {
			signed.AddCertificate(cert)
		}
	}
	for _, cert := range material.wwdr {
		if cert != nil && !cert.Equal(material.certificate) {
			signed.AddCertificate(cert)
		}
	}
	signed.Detach()
	signature, err := signed.Finish()
	if err != nil {
		return nil, fmt.Errorf("finish PKCS#7 signature: %w", err)
	}
	return signature, nil
}

func parsePEMIdentity(data []byte) ([]*x509.Certificate, crypto.Signer, bool) {
	var certs []*x509.Certificate
	var signer crypto.Signer
	rest := data
	found := false
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			break
		}
		found = true
		rest = remaining
		switch block.Type {
		case "CERTIFICATE":
			cert, err := x509.ParseCertificate(block.Bytes)
			if err == nil {
				certs = append(certs, cert)
			}
		case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY":
			parsed, err := parsePrivateKey(block)
			if err == nil {
				signer = parsed
			}
		}
	}
	return certs, signer, found
}

func parsePrivateKey(block *pem.Block) (crypto.Signer, error) {
	var parsed any
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		parsed, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	}
	if err != nil {
		return nil, err
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, errors.New("private key does not implement crypto.Signer")
	}
	return signer, nil
}

func parseCertificates(data []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := data
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = remaining
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		certs = append(certs, cert)
	}
	if len(certs) > 0 {
		return certs, nil
	}
	cert, err := x509.ParseCertificate(data)
	if err != nil {
		return nil, err
	}
	return []*x509.Certificate{cert}, nil
}
