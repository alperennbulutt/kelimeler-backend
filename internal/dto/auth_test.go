package dto

import (
	"testing"

	"kelimeler-backend/internal/models"
)

func TestIsSyntheticEmail(t *testing.T) {
	cases := map[string]bool{
		SyntheticEmail("guest_abc"):          true,
		"google_123@users.kelimeler.local":   true,
		" Apple_X@USERS.KELIMELER.LOCAL ":    true,
		"user@example.com":                   false,
		"user@kelimeler.local":               false, // yalnızca users. alt alan adı
		"users.kelimeler.local@example.com":  false,
		"user@users.kelimeler.local.example": false,
		"":                                   false,
	}
	for email, want := range cases {
		if got := IsSyntheticEmail(email); got != want {
			t.Errorf("IsSyntheticEmail(%q) = %v, beklenen %v", email, got, want)
		}
	}
}

func TestNewUserDTO_YerTutucuAdresBosDoner(t *testing.T) {
	cases := []struct {
		name, email, want string
		guest             bool
	}{
		{"misafir", SyntheticEmail("guest_abc"), "", true},
		{"e-postasız sosyal hesap", "apple_001@users.kelimeler.local", "", false},
		{"gerçek adres", "kisi@example.com", "kisi@example.com", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NewUserDTO(&models.User{ID: "u", Email: c.email, IsGuest: c.guest}, nil)
			if got.Email != c.want {
				t.Errorf("email = %q, beklenen %q", got.Email, c.want)
			}
			if got.IsGuest != c.guest {
				t.Errorf("isGuest = %v, beklenen %v", got.IsGuest, c.guest)
			}
			if got.LinkedProviders == nil {
				t.Error("linkedProviders nil; istemci boş dizi bekler")
			}
		})
	}
}
