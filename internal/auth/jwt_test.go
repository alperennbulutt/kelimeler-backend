package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-secret"

// signClaims, verilen claim'lerle (TokenService'i atlayarak) token imzalar.
func signClaims(t *testing.T, method jwt.SigningMethod, key any, claims jwt.Claims) string {
	t.Helper()

	s, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatalf("token imzalanamadı: %v", err)
	}
	return s
}

func TestGenerateAccessToken_IssVeAudTasir(t *testing.T) {
	ts := NewTokenService(testSecret, time.Minute, time.Hour)
	token, err := ts.GenerateAccessToken("kullanici-1", "a@example.com", 0)
	if err != nil {
		t.Fatalf("token üretilemedi: %v", err)
	}
	claims, err := ts.ParseAccessToken(token)
	if err != nil {
		t.Fatalf("yeni token reddedildi: %v", err)
	}
	if claims.Subject != "kullanici-1" || claims.Email != "a@example.com" {
		t.Errorf("claim'ler = %q %q", claims.Subject, claims.Email)
	}
	if claims.Issuer != AccessTokenIssuer {
		t.Errorf("iss = %q, beklenen %q", claims.Issuer, AccessTokenIssuer)
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != AccessTokenAudience {
		t.Errorf("aud = %v, beklenen [%s]", claims.Audience, AccessTokenAudience)
	}
}

func TestParseAccessToken(t *testing.T) {
	ts := NewTokenService(testSecret, time.Minute, time.Hour)
	secret := []byte(testSecret)
	now := time.Now()
	exp := jwt.NewNumericDate(now.Add(time.Minute))

	cases := []struct {
		name  string
		token string
		ok    bool
	}{
		// Dağıtımdan önce üretilen token'larda iss/aud yoktur: süresi dolana kadar geçerli.
		{"iss/aud'suz eski token", signClaims(t, jwt.SigningMethodHS256, secret, AccessClaims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "u", ExpiresAt: exp}}), true},
		{"exp'siz token", signClaims(t, jwt.SigningMethodHS256, secret, AccessClaims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "u", Issuer: AccessTokenIssuer}}), false},
		{"süresi dolmuş", signClaims(t, jwt.SigningMethodHS256, secret, AccessClaims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "u", ExpiresAt: jwt.NewNumericDate(now.Add(-time.Minute))}}), false},
		{"yanlış iss", signClaims(t, jwt.SigningMethodHS256, secret, AccessClaims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "u", ExpiresAt: exp, Issuer: "baska"}}), false},
		{"yanlış aud", signClaims(t, jwt.SigningMethodHS256, secret, AccessClaims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "u", ExpiresAt: exp, Audience: jwt.ClaimStrings{"baska"}}}), false},
		{"sub'suz", signClaims(t, jwt.SigningMethodHS256, secret, AccessClaims{
			RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: exp, Issuer: AccessTokenIssuer}}), false},
		{"HS512", signClaims(t, jwt.SigningMethodHS512, secret, AccessClaims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "u", ExpiresAt: exp}}), false},
		{"alg none", signClaims(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, AccessClaims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "u", ExpiresAt: exp}}), false},
		{"başka secret", signClaims(t, jwt.SigningMethodHS256, []byte("baska-secret"), AccessClaims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "u", ExpiresAt: exp}}), false},
		{"bozuk metin", "bu.bir.token-degil", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ts.ParseAccessToken(c.token)
			if c.ok && err != nil {
				t.Errorf("kabul edilmeliydi: %v", err)
			}
			if !c.ok && err == nil {
				t.Error("reddedilmeliydi")
			}
		})
	}
}

func TestRefreshTTLFor(t *testing.T) {
	ts := NewTokenService(testSecret, time.Minute, 720*time.Hour)
	if got := ts.RefreshTTLFor(true); got != 720*time.Hour {
		t.Errorf("misafir ömrü ayarlanmamışken = %s, beklenen olağan ömür 720h", got)
	}

	ts.WithGuestRefreshTTL(8760 * time.Hour)
	if got := ts.RefreshTTLFor(true); got != 8760*time.Hour {
		t.Errorf("misafir = %s, beklenen 8760h", got)
	}
	if got := ts.RefreshTTLFor(false); got != 720*time.Hour {
		t.Errorf("kayıtlı kullanıcı = %s, beklenen 720h", got)
	}
}

// Oturum sürümü (tv) token'da taşınır; claim'siz eski token 0 sayılır.
func TestAccessToken_OturumSurumu(t *testing.T) {
	ts := NewTokenService(testSecret, time.Minute, time.Hour)
	token, err := ts.GenerateAccessToken("kullanici-1", "a@example.com", 3)
	if err != nil {
		t.Fatalf("token üretilemedi: %v", err)
	}
	claims, err := ts.ParseAccessToken(token)
	if err != nil || claims.TokenVersion != 3 {
		t.Fatalf("tv = %v (err %v), beklenen 3", claims, err)
	}
	eski := signClaims(t, jwt.SigningMethodHS256, []byte(testSecret), jwt.RegisteredClaims{
		Subject: "u", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))})
	claims, err = ts.ParseAccessToken(eski)
	if err != nil || claims.TokenVersion != 0 {
		t.Fatalf("eski token tv = %v (err %v), beklenen 0", claims, err)
	}
}
