package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

const (
	testAppleClientID = "com.example.app"
	testAppleIssuer   = "https://appleid.apple.com"
)

// jwks, gömülü arayüz alanına Keyfunc metoduyla çakışmayan bir ad verir.
type jwks = keyfunc.Keyfunc

// sabitAnahtar, Apple'ın JWKS'i yerine tek bir test anahtarı döndürür.
// Doğrulayıcı yalnızca Keyfunc metodunu çağırır.
type sabitAnahtar struct {
	jwks
	pub *ecdsa.PublicKey
}

func (s sabitAnahtar) Keyfunc(*jwt.Token) (any, error) { return s.pub, nil }

// testAppleVerifier, ağa çıkmayan bir doğrulayıcı ve onun kabul edeceği
// identityToken'ları imzalayan fonksiyonu döner.
func testAppleVerifier(t *testing.T) (*AppleVerifier, func(extra jwt.MapClaims) string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("anahtar üretilemedi: %v", err)
	}
	v := &AppleVerifier{
		clientID: testAppleClientID,
		issuer:   testAppleIssuer,
		jwks:     sabitAnahtar{pub: &key.PublicKey},
	}

	sign := func(extra jwt.MapClaims) string {
		t.Helper()

		claims := jwt.MapClaims{
			"iss": testAppleIssuer,
			"aud": testAppleClientID,
			"sub": "apple-sub-1",
			"exp": time.Now().Add(time.Minute).Unix(),
		}
		for k, val := range extra {
			claims[k] = val
		}
		token, err := jwt.NewWithClaims(jwt.SigningMethodES256, claims).SignedString(key)
		if err != nil {
			t.Fatalf("token imzalanamadı: %v", err)
		}
		return token
	}
	return v, sign
}

func TestAppleVerify_EpostayiYalnizcaDogrulanmissaTasir(t *testing.T) {
	const email = "kisi@privaterelay.appleid.com"
	v, sign := testAppleVerifier(t)

	cases := []struct {
		name   string
		claims jwt.MapClaims
		want   string
	}{
		// Apple claim'i metin ya da boolean olarak gönderebilir.
		{"metin true", jwt.MapClaims{"email": email, "email_verified": "true"}, email},
		{"boolean true", jwt.MapClaims{"email": email, "email_verified": true}, email},
		{"metin false", jwt.MapClaims{"email": email, "email_verified": "false"}, ""},
		{"boolean false", jwt.MapClaims{"email": email, "email_verified": false}, ""},
		{"claim yok", jwt.MapClaims{"email": email}, ""},
		{"eposta yok", jwt.MapClaims{}, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			si, err := v.Verify(sign(c.claims))
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if si.Subject != "apple-sub-1" {
				t.Errorf("Subject = %q, beklenen apple-sub-1", si.Subject)
			}
			if si.Email != c.want {
				t.Errorf("Email = %q, beklenen %q", si.Email, c.want)
			}
		})
	}
}

func TestAppleVerify_YanlisAudienceVeIssuerReddedilir(t *testing.T) {
	v, sign := testAppleVerifier(t)

	cases := []struct {
		name   string
		claims jwt.MapClaims
	}{
		{"baska uygulamanin token'i", jwt.MapClaims{"aud": "com.baska.app"}},
		{"baska issuer", jwt.MapClaims{"iss": "https://sahte.example"}},
		{"sub yok", jwt.MapClaims{"sub": ""}},
		{"suresi dolmus", jwt.MapClaims{"exp": time.Now().Add(-time.Minute).Unix()}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if si, err := v.Verify(sign(c.claims)); err == nil {
				t.Errorf("Verify = %+v, beklenen hata", si)
			}
		})
	}
}
