// SPDX-License-Identifier: AGPL-3.0-or-later

package fakebt

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// WithCA gives the fake a CA certificate to hand out, so a full preflight
// against it can pass. Without one GET /api/v1/ca answers 404 — the state of a
// BlankTrail that has not generated its CA yet — and the preflight fails on it,
// which is right for a test about that and in the way of every other.
func (s *Server) WithCA(t *testing.T) *Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("fakebt: CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fakebt test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("fakebt: CA certificate: %v", err)
	}
	s.SetCA(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return s
}
