package handlers

import (
	"testing"
	"time"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/models"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Doğrulanmış adresin, onu yalnızca doğrulanmamış parolayla tutan hesaptan
// geri alınması ve bunun dayandığı auth_identities.email_verified kolonu.
// Gerçek PostgreSQL gerektirir (bkz. usage_integration_test.go).

func loadIdentity(t *testing.T, gdb *gorm.DB, userID, provider string) models.AuthIdentity {
	t.Helper()

	var identity models.AuthIdentity
	if err := gdb.First(&identity, "user_id = ? AND provider = ?", userID, provider).Error; err != nil {
		t.Fatalf("%s kimliği okunamadı: %v", provider, err)
	}
	return identity
}

// Kolonu bilmeyen (eski sunucunun yazdığı) kimlik satırları, sosyal olanlar
// da, doğrulanmamış sayılır.
func TestMigrate_EmailVerifiedEskiSatirlardaFalse(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)

	for _, provider := range []string{"password", "google", "apple"} {
		var sub any
		if provider != "password" {
			sub = provider + "-eski-" + u.ID
		}
		if err := gdb.Exec(`INSERT INTO auth_identities (user_id, provider, provider_subject, email, created_at)
			VALUES (?, ?, ?, ?, now())`, u.ID, provider, sub, u.Email).Error; err != nil {
			t.Fatalf("eski biçim %s satırı yazılamadı: %v", provider, err)
		}
		if loadIdentity(t, gdb, u.ID, provider).EmailVerified {
			t.Errorf("%s: email_verified varsayılanı true; eski satırların adresi doğrulanmış sayılmamalı", provider)
		}
	}
}

// Yeni kimliklerde email_verified açılışta yazılır: yalnızca sağlayıcının
// doğruladığı gerçek adres true'dur.
func TestEmailVerified_KimlikAcilirkenYazilir(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)

	t.Run("parola kaydı", func(t *testing.T) {
		u, _ := passwordAccount(t, gdb, app, "ev-parola")
		if loadIdentity(t, gdb, u.ID, "password").EmailVerified {
			t.Error("parola kimliği doğrulanmış sayıldı")
		}
	})
	t.Run("misafire parola bağlama", func(t *testing.T) {
		_, guest := createGuest(t, gdb, app, nil)
		if status, body := send(t, app, linkPasswordRequest(t, h, &guest, benzersizEposta("ev-bagla"), "parola-12345")); status != fiber.StatusOK {
			t.Fatalf("link/password: %d %v", status, body)
		}
		if loadIdentity(t, gdb, guest.ID, "password").EmailVerified {
			t.Error("bağlanan parola kimliği doğrulanmış sayıldı")
		}
	})
	for _, provider := range []string{"google", "apple"} {
		t.Run(provider+" girişi, doğrulanmış adres", func(t *testing.T) {
			email := benzersizEposta("ev-" + provider)
			t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", email) })
			u, err := h.authenticateSocial(provider, &auth.SocialIdentity{Subject: "ev-" + email, Email: email}, "")
			if err != nil {
				t.Fatalf("authenticateSocial: %v", err)
			}
			if !loadIdentity(t, gdb, u.ID, provider).EmailVerified {
				t.Error("doğrulanmış sağlayıcı adresi doğrulanmamış yazıldı")
			}
		})
		t.Run(provider+" girişi, adres yok", func(t *testing.T) {
			subject := "ev-adressiz-" + provider + "-" + time.Now().Format("150405.000000000")
			t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", dto.SyntheticEmail(provider+"_"+subject)) })
			u, err := h.authenticateSocial(provider, &auth.SocialIdentity{Subject: subject}, "")
			if err != nil {
				t.Fatalf("authenticateSocial: %v", err)
			}
			if loadIdentity(t, gdb, u.ID, provider).EmailVerified {
				t.Error("yer tutucu adres doğrulanmış yazıldı")
			}
		})
		t.Run(provider+" bağlama", func(t *testing.T) {
			for _, email := range []string{benzersizEposta("ev-link-" + provider), ""} {
				_, guest := createGuest(t, gdb, app, nil)
				si := &auth.SocialIdentity{Subject: "ev-link-" + guest.ID, Email: email}
				if status, body := post(t, linkApp(h, guest.ID, provider, si), "/link"); status != fiber.StatusOK {
					t.Fatalf("link: %d %v", status, body)
				}
				if got := loadIdentity(t, gdb, guest.ID, provider).EmailVerified; got != (email != "") {
					t.Errorf("adres %q: email_verified = %v, beklenen %v", email, got, email != "")
				}
			}
		})
	}
}
