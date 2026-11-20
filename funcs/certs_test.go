package funcs

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

func parseLeaf(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM block in cert")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}

func TestGenerateCA(t *testing.T) {
	ca, err := GenerateCA("external-model-e2e-ca", 48*time.Hour)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	if !ca.Cert.IsCA {
		t.Error("CA cert IsCA = false, want true")
	}
	if ca.Cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("CA cert missing KeyUsageCertSign")
	}
	if ca.Cert.Subject.CommonName != "external-model-e2e-ca" {
		t.Errorf("CA CN = %q", ca.Cert.Subject.CommonName)
	}
	if parseLeaf(t, ca.CertPEM) == nil || len(ca.KeyPEM) == 0 {
		t.Error("CA PEM encodings missing")
	}
}

func TestSignServingCertVerifiesAgainstCA(t *testing.T) {
	ca, err := GenerateCA("external-model-e2e-ca", 48*time.Hour)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	sans := []string{"maas-api", "maas-api.maas-system", "maas-api.maas-system.svc", "maas-api.maas-system.svc.cluster.local"}
	certPEM, keyPEM, err := ca.SignServingCert("maas-api.maas-system.svc", sans, 48*time.Hour)
	if err != nil {
		t.Fatalf("SignServingCert: %v", err)
	}
	if len(keyPEM) == 0 {
		t.Fatal("empty key PEM")
	}
	leaf := parseLeaf(t, certPEM)
	if leaf.IsCA {
		t.Error("serving cert must not be a CA")
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca.CertPEM) {
		t.Fatal("failed to add CA to pool")
	}
	for _, dns := range []string{"maas-api.maas-system.svc", "maas-api.maas-system.svc.cluster.local"} {
		if _, err := leaf.Verify(x509.VerifyOptions{
			Roots:     roots,
			DNSName:   dns,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			t.Errorf("verify %s against CA: %v", dns, err)
		}
	}
	// A SAN not on the cert must fail.
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "other.example.com"}); err == nil {
		t.Error("verify of unlisted DNS name unexpectedly succeeded")
	}
}

func TestSelfSignedServingCertHasSAN(t *testing.T) {
	certPEM, _, err := GenerateSelfSignedServingCert(
		"maas-controller-webhook-service.maas-system.svc",
		[]string{"maas-controller-webhook-service", "maas-controller-webhook-service.maas-system.svc"},
		48*time.Hour,
	)
	if err != nil {
		t.Fatalf("GenerateSelfSignedServingCert: %v", err)
	}
	leaf := parseLeaf(t, certPEM)
	if got := leaf.DNSNames; len(got) != 2 {
		t.Errorf("DNSNames = %v, want 2", got)
	}
	if len(leaf.ExtKeyUsage) == 0 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Error("missing serverAuth ext key usage")
	}
}
