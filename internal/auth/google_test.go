package auth

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestVerifiedEmail_YalnizcaDogrulanmisEpostayiDoner(t *testing.T) {
	const email = "kisi@example.com"

	cases := []struct {
		name   string
		claims map[string]any
		want   string
	}{
		{"boolean true", map[string]any{"email": email, "email_verified": true}, email},
		{"metin true (Apple)", map[string]any{"email": email, "email_verified": "true"}, email},
		{"boolean false", map[string]any{"email": email, "email_verified": false}, ""},
		{"metin false (Apple)", map[string]any{"email": email, "email_verified": "false"}, ""},
		{"claim yok", map[string]any{"email": email}, ""},
		{"beklenmeyen tip", map[string]any{"email": email, "email_verified": 1}, ""},
		{"eposta yok", map[string]any{"email_verified": true}, ""},
		{"eposta metin degil", map[string]any{"email": 42, "email_verified": true}, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := verifiedEmail(c.claims); got != c.want {
				t.Errorf("verifiedEmail = %q, beklenen %q", got, c.want)
			}
		})
	}
}

func TestVerifiedEmail_AppleClaimTipiyleCalisir(t *testing.T) {
	// Apple doğrulayıcısı claim'leri jwt.MapClaims olarak taşır.
	claims := jwt.MapClaims{"email": "kisi@privaterelay.appleid.com", "email_verified": "true"}

	if got := verifiedEmail(claims); got != "kisi@privaterelay.appleid.com" {
		t.Errorf("verifiedEmail = %q, beklenen relay adresi", got)
	}
}

func TestGoogleIdentity_DogrulanmamisEpostayiTasimaz(t *testing.T) {
	si := googleIdentity("sub-123", map[string]any{
		"email":          "kurban@example.com",
		"email_verified": false,
		"name":           "Ad Soyad",
	})

	if si.Subject != "sub-123" {
		t.Errorf("Subject = %q, beklenen sub-123", si.Subject)
	}
	if si.Email != "" {
		t.Errorf("Email = %q, beklenen boş (doğrulanmamış adresle hesap eşlenmemeli)", si.Email)
	}
	if si.Name != "Ad Soyad" {
		t.Errorf("Name = %q, beklenen \"Ad Soyad\" (ad e-posta doğrulamasına bağlı değil)", si.Name)
	}
}

func TestGoogleIdentity_DogrulanmisEpostayiTasir(t *testing.T) {
	si := googleIdentity("sub-123", map[string]any{
		"email":          "kisi@example.com",
		"email_verified": true,
	})

	if si.Email != "kisi@example.com" {
		t.Errorf("Email = %q, beklenen kisi@example.com", si.Email)
	}
	if si.Name != "" {
		t.Errorf("Name = %q, beklenen boş", si.Name)
	}
}
