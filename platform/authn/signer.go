package authn

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Signer mints RS256 tokens. It is constructed only inside the Auth service,
// which is the sole holder of the private key.
type Signer struct {
	kid    string
	key    *rsa.PrivateKey
	issuer string
	ttl    time.Duration
}

// NewSigner builds a signer around an RSA private key.
func NewSigner(key *rsa.PrivateKey, kid, issuer string, ttl time.Duration) *Signer {
	return &Signer{kid: kid, key: key, issuer: issuer, ttl: ttl}
}

// KeyID returns the kid stamped into token headers and the JWKS.
func (s *Signer) KeyID() string { return s.kid }

// TTLSeconds returns the configured token lifetime in seconds.
func (s *Signer) TTLSeconds() int { return int(s.ttl.Seconds()) }

// PublicKey returns the verifying public key (for JWKS publication).
func (s *Signer) PublicKey() *rsa.PublicKey { return &s.key.PublicKey }

// Sign mints a token for subject/role. The kid is placed in the JWT header so
// verifiers select the matching public key; alg is RS256.
func (s *Signer) Sign(subject string, role Role) (token string, expiresIn int, err error) {
	now := time.Now()
	claims := Claims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			Issuer:    s.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	t.Header["kid"] = s.kid
	signed, err := t.SignedString(s.key)
	if err != nil {
		return "", 0, err
	}
	return signed, int(s.ttl.Seconds()), nil
}

// --- RSA key (de)serialisation helpers used by Auth for persistence ---

// GenerateRSAKey creates a fresh RSA private key of the given size.
func GenerateRSAKey(bits int) (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, bits)
}

// ParseRSAPrivateKeyPEM parses a PEM-encoded PKCS#1 or PKCS#8 RSA private key.
func ParseRSAPrivateKeyPEM(pemStr string) (*rsa.PrivateKey, error) {
	return jwt.ParseRSAPrivateKeyFromPEM([]byte(pemStr))
}

// EncodePrivateKeyPEM serialises a private key to PKCS#1 PEM.
func EncodePrivateKeyPEM(key *rsa.PrivateKey) string {
	der := x509.MarshalPKCS1PrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
}

// EncodePublicKeyPEM serialises a public key to PKIX PEM.
func EncodePublicKeyPEM(pub *rsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// ParseRSAPublicKeyPEM parses a PKIX PEM public key.
func ParseRSAPublicKeyPEM(pemStr string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("invalid public key PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rk, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("not an RSA public key")
	}
	return rk, nil
}
