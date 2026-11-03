package incident

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

const (
	generatedCertificateCN       = "cenci-incident-worker"
	generatedCertificateValidity = 365 * 24 * time.Hour
	generatedKeyBits             = 2048
	// Entra reports a certificate credential by its SHA-1 thumbprint: an
	// identifier to compare, not a security hash.
	thumbprintBytes = sha1.Size
)

// errCertificateGeneration is deliberately detail-free: the failing calls handled
// private key material.
var errCertificateGeneration = errors.New("could not generate the worker certificate")

// generateCertificate returns the worker's PEM file (an unencrypted PKCS#8
// PRIVATE KEY block, then the CERTIFICATE block) and its self-signed leaf: RSA
// 2048, SHA-256, CN=cenci-incident-worker, valid for 365 days from now, with a
// random 128-bit serial.
func generateCertificate(now time.Time) ([]byte, *x509.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, generatedKeyBits)
	if err != nil {
		return nil, nil, errCertificateGeneration
	}
	serialBytes := make([]byte, 16)
	if _, err := rand.Read(serialBytes); err != nil {
		return nil, nil, errCertificateGeneration
	}
	serialBytes[0] |= 0x80 // a full 128 bits, never a small number
	template := &x509.Certificate{
		SerialNumber:       new(big.Int).SetBytes(serialBytes),
		Subject:            pkix.Name{CommonName: generatedCertificateCN},
		NotBefore:          now,
		NotAfter:           now.Add(generatedCertificateValidity),
		KeyUsage:           x509.KeyUsageDigitalSignature,
		SignatureAlgorithm: x509.SHA256WithRSA,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, errCertificateGeneration
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, errCertificateGeneration
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, errCertificateGeneration
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	data = append(data, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	return data, leaf, nil
}

// createCertificateFile generates the worker certificate and writes it to path,
// which must not exist: the file is created exclusively with mode 0600 without
// following a symbolic link and is never overwritten. The parent directory is
// not created. A write that fails after the create removes only the file this
// call created. It returns the certificate's leaf.
func createCertificateFile(path string, now time.Time) (*x509.Certificate, error) {
	data, leaf, err := generateCertificate(now)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("create certificate file %s: %w", path, err)
	}
	created, statErr := f.Stat()
	err = statErr
	if err == nil {
		err = f.Chmod(0600)
	}
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		err = fmt.Errorf("write certificate file %s: %w", path, err)
		if rmErr := removeCreatedFile(path, created); rmErr != nil {
			err = fmt.Errorf("%w; the partial file %s could not be removed: %w", err, path, rmErr)
		}
		return nil, err
	}
	return leaf, nil
}

// removeCreatedFile removes the file this call created exclusively. When both
// identities can be read and the path now holds a different file, that file is
// not ours and is left alone. When the identity cannot be checked (created is
// nil, or the path cannot be inspected) the exclusive create still proves this
// call made the file, so removal is attempted rather than leaving a 0600
// private-key fragment behind.
func removeCreatedFile(path string, created os.FileInfo) error {
	if current, err := os.Lstat(path); err == nil && created != nil && !os.SameFile(created, current) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// loadExistingCertificate runs the checks the worker applies to its certificate
// file (no symbolic link, owner, mode, an unencrypted PEM certificate with its
// private key, a certificate matching that key, not expired) without changing
// the file. The warning is empty unless the certificate expires within 30 days.
func loadExistingCertificate(path string, now time.Time) (*x509.Certificate, string, error) {
	data, err := readCertificateFile(path)
	if err != nil {
		return nil, "", err
	}
	certs, key, err := azidentity.ParseCertificates(data, nil)
	if err != nil || len(certs) == 0 || key == nil {
		// The parser's detail is withheld: it handled private key material.
		return nil, "", fmt.Errorf("certificate %s could not be parsed as an unencrypted PEM certificate with its private key", path)
	}
	leaf := certificateForKey(certs, key)
	if leaf == nil {
		return nil, "", fmt.Errorf("certificate %s has no certificate matching its private key", path)
	}
	notAfter := leaf.NotAfter.UTC().Format(time.RFC3339)
	switch remaining := leaf.NotAfter.Sub(now); {
	case remaining <= 0:
		return nil, "", fmt.Errorf("certificate expired: %s NotAfter %s", path, notAfter)
	case remaining < certificateWarningWindow:
		return leaf, fmt.Sprintf("certificate %s expires within 30 days (NotAfter %s)", path, notAfter), nil
	}
	return leaf, "", nil
}

// certificateRegistered reports whether one of the credential identifiers az
// lists is the leaf's SHA-1 thumbprint. An identifier is hex (any case) or
// base64 of 20 bytes; when none matches and any other shape is present the
// answer is an error, never "not registered", so a second credential is not
// appended on a guess.
func certificateRegistered(identifiers []string, leaf *x509.Certificate) (bool, error) {
	want := sha1.Sum(leaf.Raw)
	unrecognized := false
	for _, id := range identifiers {
		if got, ok := decodeThumbprint(id); !ok {
			unrecognized = true
		} else if got == want {
			return true, nil
		}
	}
	if unrecognized {
		return false, errors.New("a certificate credential has an identifier that is neither a hex nor a base64 SHA-1 thumbprint, so the certificate cannot be matched")
	}
	return false, nil
}

func decodeThumbprint(id string) ([thumbprintBytes]byte, bool) {
	var out [thumbprintBytes]byte
	if b, err := hex.DecodeString(id); err == nil && len(b) == thumbprintBytes {
		copy(out[:], b)
		return out, true
	}
	if b, err := base64.StdEncoding.DecodeString(id); err == nil && len(b) == thumbprintBytes {
		copy(out[:], b)
		return out, true
	}
	return out, false
}

// withPublicCertificate writes the leaf's certificate, never a key, into a fresh
// 0700 temporary directory, calls use with the file's absolute path and removes
// the directory afterwards.
func withPublicCertificate(leaf *x509.Certificate, use func(path string) error) error {
	dir, err := os.MkdirTemp("", "cenci-public-cert-")
	if err != nil {
		return fmt.Errorf("create a temporary directory for the public certificate: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if !filepath.IsAbs(dir) {
		return errors.New("the temporary directory is not an absolute path")
	}
	path := filepath.Join(dir, "worker-public.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}), 0600); err != nil {
		return fmt.Errorf("write the public certificate: %w", err)
	}
	return use(path)
}
