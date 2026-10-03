package auth

import (
	"context"
	"errors"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

const appleKeysURL = "https://appleid.apple.com/auth/keys"

// AppleVerifier, Apple identityToken'larını Apple'ın JWKS'i ile doğrular.
type AppleVerifier struct {
	clientID string
	issuer   string
	jwks     keyfunc.Keyfunc
}

// NewAppleVerifier, Apple JWKS'ini (otomatik yenilenen cache ile) yükler.
// clientID boşsa nil döner (Apple girişi devre dışı kalır).
func NewAppleVerifier(ctx context.Context, clientID, issuer string) (*AppleVerifier, error) {
	if clientID == "" {
		return nil, nil
	}
	jwks, err := keyfunc.NewDefaultCtx(ctx, []string{appleKeysURL})
	if err != nil {
		return nil, err
	}
	return &AppleVerifier{clientID: clientID, issuer: issuer, jwks: jwks}, nil
}

// Verify, Apple identityToken'ının imzasını, issuer'ını ve audience'ını doğrular.
func (v *AppleVerifier) Verify(identityToken string) (*SocialIdentity, error) {
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(identityToken, claims, v.jwks.Keyfunc)
	if err != nil || !token.Valid {
		return nil, errors.New("geçersiz apple token")
	}

	if iss, _ := claims["iss"].(string); iss != v.issuer {
		return nil, errors.New("apple iss uyuşmuyor")
	}
	if !audienceContains(claims["aud"], v.clientID) {
		return nil, errors.New("apple aud uyuşmuyor")
	}

	sub, _ := claims["sub"].(string)
	if sub == "" {
		return nil, errors.New("apple sub yok")
	}

	// Apple, kurum/okul tarafından yönetilen Apple hesaplarında e-postayı
	// doğrulamayabilir ve bunu email_verified ile bildirir; Google ile aynı kural.
	return &SocialIdentity{Subject: sub, Email: verifiedEmail(claims)}, nil
}

// audienceContains, aud claim'i (string veya []any olabilir) hedefi içeriyor mu?
func audienceContains(aud any, target string) bool {
	switch v := aud.(type) {
	case string:
		return v == target
	case []any:
		for _, a := range v {
			if s, ok := a.(string); ok && s == target {
				return true
			}
		}
	case []string:
		for _, s := range v {
			if s == target {
				return true
			}
		}
	}
	return false
}
