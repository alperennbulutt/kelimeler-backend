package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// testPrivateKeyPEM, Apple'ın verdiği .p8 ile aynı formatta (PKCS#8, EC P-256)
// bir test anahtarı üretir.
func testPrivateKeyPEM(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("anahtar üretilemedi: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("anahtar kodlanamadı: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestNewAppleRevoker_EksikYapilandirmadaNilDoner(t *testing.T) {
	key := testPrivateKeyPEM(t)

	cases := []struct {
		name                          string
		clientID, teamID, keyID, priv string
	}{
		{"hepsi bos", "", "", "", ""},
		{"clientID yok", "", "TEAM123456", "KEY123", key},
		{"teamID yok", "com.example.app", "", "KEY123", key},
		{"keyID yok", "com.example.app", "TEAM123456", "", key},
		{"private key yok", "com.example.app", "TEAM123456", "KEY123", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := NewAppleRevoker(c.clientID, c.teamID, c.keyID, c.priv)
			if err != nil {
				t.Fatalf("eksik yapılandırma hata döndürmemeli: %v", err)
			}
			if r != nil {
				t.Error("eksik yapılandırmada nil beklenir (revoke devre dışı kalmalı)")
			}
		})
	}
}

func TestNewAppleRevoker_BozukAnahtarHataDoner(t *testing.T) {
	r, err := NewAppleRevoker("com.example.app", "TEAM123456", "KEY123", "bu bir PEM degil")
	if err == nil {
		t.Error("bozuk anahtar için hata beklenir")
	}
	if r != nil {
		t.Error("bozuk anahtarda revoker dönmemeli")
	}
}

func TestNewAppleRevoker_GecerliYapilandirma(t *testing.T) {
	r, err := NewAppleRevoker("com.example.app", "TEAM123456", "KEY123", testPrivateKeyPEM(t))
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if r == nil {
		t.Fatal("geçerli yapılandırmada revoker beklenir")
	}
}

// Ortam değişkenlerinde çok satırlı PEM sık sık "\n" olarak kaçırılır.
func TestParseApplePrivateKey_KacirilmisSatirSonlari(t *testing.T) {
	escaped := strings.ReplaceAll(testPrivateKeyPEM(t), "\n", `\n`)

	if _, err := parseApplePrivateKey(escaped); err != nil {
		t.Errorf("kaçırılmış satır sonlu PEM okunamadı: %v", err)
	}
}

func TestClientSecret_AppleninIstedigiClaimleri_Tasir(t *testing.T) {
	const clientID = "com.example.app"
	const teamID = "TEAM123456"
	const keyID = "KEY1234567"

	keyPEM := testPrivateKeyPEM(t)
	r, err := NewAppleRevoker(clientID, teamID, keyID, keyPEM)
	if err != nil {
		t.Fatalf("revoker kurulamadı: %v", err)
	}

	secret, err := r.clientSecret()
	if err != nil {
		t.Fatalf("client secret üretilemedi: %v", err)
	}

	// İmzayı, üretilen anahtarın public tarafıyla doğrula.
	block, _ := pem.Decode([]byte(keyPEM))
	parsed, _ := x509.ParsePKCS8PrivateKey(block.Bytes)
	pub := &parsed.(*ecdsa.PrivateKey).PublicKey

	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(secret, claims, func(*jwt.Token) (any, error) {
		return pub, nil
	})
	if err != nil || !token.Valid {
		t.Fatalf("client secret doğrulanamadı: %v", err)
	}

	if got := token.Method.Alg(); got != "ES256" {
		t.Errorf("alg = %s, Apple ES256 ister", got)
	}
	if got, _ := token.Header["kid"].(string); got != keyID {
		t.Errorf("kid = %q, beklenen %q", got, keyID)
	}
	if got, _ := claims["iss"].(string); got != teamID {
		t.Errorf("iss = %q, beklenen team id %q", got, teamID)
	}
	if got, _ := claims["sub"].(string); got != clientID {
		t.Errorf("sub = %q, beklenen client id %q", got, clientID)
	}
	if got, _ := claims["aud"].(string); got != appleAudience {
		t.Errorf("aud = %q, beklenen %q", got, appleAudience)
	}

	// Apple azami 6 ay kabul eder; kısa ömürlü üretiyoruz.
	exp, err := claims.GetExpirationTime()
	if err != nil || exp == nil {
		t.Fatal("exp claim'i yok")
	}
	if exp.After(time.Now().Add(6 * 30 * 24 * time.Hour)) {
		t.Error("exp Apple'ın 6 aylık üst sınırını aşıyor")
	}
	if !exp.After(time.Now()) {
		t.Error("exp geçmişte, token daha üretilirken süresi dolmuş")
	}
}

func TestRevoke_BosTokenIcinIstekYapmaz(t *testing.T) {
	r, err := NewAppleRevoker("com.example.app", "TEAM123456", "KEY123", testPrivateKeyPEM(t))
	if err != nil {
		t.Fatalf("revoker kurulamadı: %v", err)
	}

	// Ağa çıkmadan hata dönmeli; aksi halde her silmede boşuna istek atardık.
	if err := r.Revoke(t.Context(), ""); err == nil {
		t.Error("boş token için hata beklenir")
	}
}
