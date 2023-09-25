//go:build windows

package x509

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"os"
	"time"

	"github.com/foundriesio/fioctl/subcommands"
)

func writeFile(filename, contents string, mode os.FileMode) {
	err := os.WriteFile(filename, []byte(contents), mode)
	subcommands.DieNotNil(err)
}

func genRandomSerialNumber() *big.Int {
	// Generate a 160 bits serial number (20 octets)
	max := big.NewInt(0).Exp(big.NewInt(2), big.NewInt(160), nil)
	serial, err := rand.Int(rand.Reader, max)
	subcommands.DieNotNil(err)
	return serial
}

func genAndSaveKeyToFile(fn string) (*ecdsa.PrivateKey, *ecdsa.PublicKey) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	subcommands.DieNotNil(err)

	keyRaw, err := x509.MarshalECPrivateKey(priv)
	subcommands.DieNotNil(err)

	keyBlock := &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyRaw}

	factoryKeyBytes := pem.EncodeToMemory(keyBlock)
	err = os.WriteFile(fn, factoryKeyBytes, 0600)
	subcommands.DieNotNil(err)
	return priv, &priv.PublicKey
}

func genCertificate(crtTemplate *x509.Certificate, caCrt *x509.Certificate, pub any, signerKey any) string {
	certRaw, err := x509.CreateCertificate(rand.Reader, crtTemplate, caCrt, pub, signerKey)
	subcommands.DieNotNil(err)

	certPemBlock := pem.Block{Type: "CERTIFICATE", Bytes: certRaw}
	var certRow bytes.Buffer
	err = pem.Encode(&certRow, &certPemBlock)
	subcommands.DieNotNil(err)

	return certRow.String()
}

func parsePemCertificateRequest(csrPem string) *x509.CertificateRequest {
	pemBlock, _ := pem.Decode([]byte(csrPem))
	clientCSR, err := x509.ParseCertificateRequest(pemBlock.Bytes)
	subcommands.DieNotNil(err)
	err = clientCSR.CheckSignature()
	subcommands.DieNotNil(err)
	return clientCSR
}

func parsePemPrivateKey(keyPem string) *ecdsa.PrivateKey {
	caPrivateKeyPemBlock, _ := pem.Decode([]byte(keyPem))
	caPrivateKey, err := x509.ParseECPrivateKey(caPrivateKeyPemBlock.Bytes)
	subcommands.DieNotNil(err)
	return caPrivateKey
}

func parsePemCertificate(crtPem string) *x509.Certificate {
	caCrtPemBlock, _ := pem.Decode([]byte(crtPem))
	crt, err := x509.ParseCertificate(caCrtPemBlock.Bytes)
	subcommands.DieNotNil(err)
	return crt
}

func marshalSubject(cn string, ou string) pkix.Name {
	// In it's simpler form, this function would be replaced by
	// pkix.Name{CommonName: cn, OrganizationalUnit: []string{ou}}
	// However, x509 library uses PrintableString instead of UTF8String
	// as ASN.1 field type. This function forces UTF8String instead, to
	// avoid compatibility issues when using a device certificate created
	// with libraries such as MbedTLS.
	// x509 library also encodes OU and CN in a different order if compared
	// to OpenSSL, which is less of an issue, but still worth to adjust
	// while we are at it.
	cnBytes, err := asn1.MarshalWithParams(cn, "utf8")
	subcommands.DieNotNil(err)
	ouBytes, err := asn1.MarshalWithParams(ou, "utf8")
	subcommands.DieNotNil(err)
	var (
		oidCommonName         = []int{2, 5, 4, 3}
		oidOrganizationalUnit = []int{2, 5, 4, 11}
	)
	pkixAttrTypeValue := []pkix.AttributeTypeAndValue{
		{
			Type:  oidCommonName,
			Value: asn1.RawValue{FullBytes: cnBytes},
		},
		{
			Type:  oidOrganizationalUnit,
			Value: asn1.RawValue{FullBytes: ouBytes},
		},
	}
	return pkix.Name{ExtraNames: pkixAttrTypeValue}
}

func CreateFactoryCa(storage KeyStorage, ou string) string {
	priv, pub := storage.genAndSaveFactoryCaKey()
	crtTemplate := x509.Certificate{
		SerialNumber: genRandomSerialNumber(),
		Subject:      marshalSubject("Factory-CA", ou),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(20, 0, 0),

		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	factoryCaString := genCertificate(&crtTemplate, &crtTemplate, pub, priv)
	writeFile(FactoryCaCertFile, factoryCaString, 0400)
	return factoryCaString
}
func CreateDeviceCa(storage KeyStorage, cn string, ou string) string {
	factoryKey := storage.getFactoryCaKey()
	factoryCa := parsePemCertificate(readFile(FactoryCaCertFile))
	_, pub := genAndSaveKeyToFile(DeviceCaKeyFile)
	crtTemplate := x509.Certificate{
		SerialNumber: genRandomSerialNumber(),
		Subject:      marshalSubject(cn, ou),
		Issuer:       factoryCa.Subject,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(10, 0, 0),

		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	crtPem := genCertificate(&crtTemplate, factoryCa, pub, factoryKey)
	writeFile(DeviceCaCertFile, crtPem, 0400)
	return crtPem
}

func SignTlsCsr(storage KeyStorage, csrPem string) string {
	csr := parsePemCertificateRequest(csrPem)
	factoryKey := storage.getFactoryCaKey()
	factoryCa := parsePemCertificate(readFile(FactoryCaCertFile))
	crtTemplate := x509.Certificate{
		SerialNumber: genRandomSerialNumber(),
		Subject:      csr.Subject,
		Issuer:       factoryCa.Subject,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(10, 0, 0),

		IsCA:        true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageKeyAgreement,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    csr.DNSNames,
	}
	crtPem := genCertificate(&crtTemplate, factoryCa, csr.PublicKey, factoryKey)
	return crtPem
}

func SignCaCsr(storage KeyStorage, csrPem string) string {
	csr := parsePemCertificateRequest(csrPem)
	factoryKey := storage.getFactoryCaKey()
	factoryCa := parsePemCertificate(readFile(FactoryCaCertFile))
	crtTemplate := x509.Certificate{
		SerialNumber: genRandomSerialNumber(),
		Subject:      csr.Subject,
		Issuer:       factoryCa.Subject,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(10, 0, 0),

		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	crtPem := genCertificate(&crtTemplate, factoryCa, csr.PublicKey, factoryKey)
	return crtPem
}

func SignEl2GoCsr(storage KeyStorage, csrPem string) string {
	return SignCaCsr(storage, csrPem)
}

type KeyStorage interface {
	genAndSaveFactoryCaKey() (any, any)
	getFactoryCaKey() any
}

type KeyStorageFiles struct{}

func (s *KeyStorageFiles) genAndSaveFactoryCaKey() (any, any) {
	return genAndSaveKeyToFile(FactoryCaKeyFile)
}

func (s *KeyStorageFiles) getFactoryCaKey() any {
	return parsePemPrivateKey(readFile(FactoryCaKeyFile))
}
