package funcs

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// A standalone in-process CA for kind clusters that lack OpenShift service-ca: mint
// a run-owned CA, sign serving certs with hostname SANs, and publish them as TLS
// Secrets / CA ConfigMaps. Pure crypto/x509 — no openssl binary.

// CA is a run-owned certificate authority (cert + key, with PEM encodings).
type CA struct {
	Cert    *x509.Certificate
	Key     *rsa.PrivateKey
	CertPEM []byte
	KeyPEM  []byte
}

// GenerateCA mints a self-signed CA certificate (keyCertSign|cRLSign, pathlen 1).
func GenerateCA(commonName string, validity time.Duration) (*CA, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now().Add(-time.Minute)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now,
		NotAfter:              now.Add(validity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            1,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	keyPEM, err := marshalKey(key)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: cert, Key: key, CertPEM: pemBlock("CERTIFICATE", der), KeyPEM: keyPEM}, nil
}

// SignServingCert signs a leaf serving certificate (serverAuth, the given DNS SANs)
// with the CA. Returns PEM cert and key.
func (ca *CA) SignServingCert(commonName string, dnsNames []string, validity time.Duration) (certPEM, keyPEM []byte, err error) {
	return newLeaf(commonName, dnsNames, validity, ca.Cert, ca.Key)
}

// GenerateSelfSignedServingCert mints a self-signed leaf serving certificate (for a
// webhook/metrics endpoint that only needs a cert, not a shared trust root).
func GenerateSelfSignedServingCert(commonName string, dnsNames []string, validity time.Duration) (certPEM, keyPEM []byte, err error) {
	return newLeaf(commonName, dnsNames, validity, nil, nil)
}

// newLeaf creates a serving leaf. If parent/parentKey are nil the cert is
// self-signed; otherwise it is signed by the parent CA.
func newLeaf(commonName string, dnsNames []string, validity time.Duration, parent *x509.Certificate, parentKey *rsa.PrivateKey) (certPEM, keyPEM []byte, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().Add(-time.Minute)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now,
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err = marshalKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pemBlock("CERTIFICATE", der), keyPEM, nil
}

// ServiceCAServingCertAnnotation is the OpenShift service-ca annotation naming the
// serving-cert Secret to generate for a Service.
const ServiceCAServingCertAnnotation = "service.beta.openshift.io/serving-cert-secret-name"

// SignAnnotatedServiceCerts stands in for the OpenShift service-ca operator on a
// cluster that lacks it (e.g. kind). It scans a namespace for Services carrying the
// serving-cert annotation and, for each, mints a serving certificate (SANs = the
// Service's cluster DNS) signed by ca and writes the annotated TLS Secret. It polls
// up to timeout so Services a controller creates asynchronously are still covered,
// returning once every annotated Service has its Secret (or the timeout elapses).
//
// This is the generic form of what the ext-proc data plane needs on kind: the
// controller annotates each ExtProc Service, and this creates exactly those secrets
// so the pods can mount them.
func SignAnnotatedServiceCerts(ca *CA, namespace string, validity, timeout time.Duration) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		r, err := newResources(cfg)
		if err != nil {
			return ctx, err
		}
		deadline := time.Now().Add(timeout)
		for {
			var services corev1.ServiceList
			if err := r.WithNamespace(namespace).List(ctx, &services); err != nil {
				return ctx, fmt.Errorf("list services in %s: %w", namespace, err)
			}
			annotated, minted := 0, 0
			for i := range services.Items {
				svc := &services.Items[i]
				secretName := svc.Annotations[ServiceCAServingCertAnnotation]
				if secretName == "" {
					continue
				}
				annotated++
				var existing corev1.Secret
				if err := r.Get(ctx, secretName, namespace, &existing); err == nil {
					continue // already issued
				}
				dns := svc.Name + "." + namespace + ".svc"
				cert, key, signErr := ca.SignServingCert(dns, []string{dns, dns + ".cluster.local"}, validity)
				if signErr != nil {
					return ctx, fmt.Errorf("sign serving cert for %s/%s: %w", namespace, svc.Name, signErr)
				}
				if _, err := CreateTLSSecret(namespace, secretName, cert, key)(ctx, cfg); err != nil {
					return ctx, err
				}
				minted++
			}
			if annotated > 0 && minted == 0 {
				return ctx, nil // every annotated Service has its secret
			}
			if time.Now().After(deadline) {
				return ctx, nil // best effort; late Services covered on a later call
			}
			time.Sleep(defaultWaitInterval)
		}
	}
}

// CreateTLSSecret server-side-applies a kubernetes.io/tls Secret (tls.crt/tls.key).
func CreateTLSSecret(namespace, name string, certPEM, keyPEM []byte) env.Func {
	return applySecret(namespace, name, corev1.SecretTypeTLS, map[string][]byte{
		corev1.TLSCertKey:       certPEM,
		corev1.TLSPrivateKeyKey: keyPEM,
	})
}

// CreateConfigMap server-side-applies a ConfigMap with string data.
func CreateConfigMap(namespace, name string, data map[string]string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Data:       data,
		}
		cm.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("ConfigMap"))
		return ctx, applyObject(ctx, cfg, cm, fmt.Sprintf("configmap %s/%s", namespace, name))
	}
}

func applySecret(namespace, name string, secretType corev1.SecretType, data map[string][]byte) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Type:       secretType,
			Data:       data,
		}
		secret.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
		return ctx, applyObject(ctx, cfg, secret, fmt.Sprintf("secret %s/%s", namespace, name))
	}
}

// applyObject server-side-applies a single typed object (with GVK set).
func applyObject(ctx context.Context, cfg *envconf.Config, obj k8s.Object, what string) error {
	r, err := newResources(cfg)
	if err != nil {
		return err
	}
	data, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", what, err)
	}
	force := true
	if err := r.Patch(ctx, obj, k8s.Patch{PatchType: types.ApplyPatchType, Data: data},
		func(o *metav1.PatchOptions) { o.FieldManager = fieldManager; o.Force = &force }); err != nil {
		return fmt.Errorf("apply %s: %w", what, err)
	}
	return nil
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

func marshalKey(key *rsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pemBlock("PRIVATE KEY", der), nil
}

func pemBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}
