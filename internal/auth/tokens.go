package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type AccessClaims struct {
	EmailVerified bool `json:"email_verified"`
	jwt.RegisteredClaims
}

type TokenManager struct {
	secret   []byte
	issuer   string
	lifetime time.Duration
	now      func() time.Time
}

func NewTokenManager(secret []byte, issuer string, lifetime time.Duration) *TokenManager {
	return &TokenManager{secret: secret, issuer: issuer, lifetime: lifetime, now: time.Now}
}

func (m *TokenManager) IssueAccessToken(userID uuid.UUID, emailVerified bool) (string, time.Time, error) {
	now := m.now().UTC()
	expiresAt := now.Add(m.lifetime)
	claims := AccessClaims{
		EmailVerified: emailVerified,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: m.issuer, Subject: userID.String(), Audience: jwt.ClaimStrings{"cubetimer-clients"},
			ExpiresAt: jwt.NewNumericDate(expiresAt), IssuedAt: jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)), ID: uuid.NewString(),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(m.secret)
	return signed, expiresAt, err
}

func (m *TokenManager) ParseAccessToken(raw string) (uuid.UUID, AccessClaims, error) {
	var claims AccessClaims
	token, err := jwt.ParseWithClaims(raw, &claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return m.secret, nil
	}, jwt.WithIssuer(m.issuer), jwt.WithAudience("cubetimer-clients"), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		return uuid.Nil, AccessClaims{}, errors.New("invalid access token")
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, AccessClaims{}, errors.New("invalid access token subject")
	}
	return userID, claims, nil
}

func randomToken() (raw string, hash []byte, err error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", nil, err
	}
	raw = base64.RawURLEncoding.EncodeToString(value)
	digest := sha256.Sum256([]byte(raw))
	return raw, digest[:], nil
}

func tokenHash(raw string) []byte {
	digest := sha256.Sum256([]byte(raw))
	return digest[:]
}
