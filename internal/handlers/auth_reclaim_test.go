package handlers

import (
	"testing"

	"kelimeler-backend/internal/models"
)

// decideEmailMatch'in karar tablosu (veritabanısız).
func TestDecideEmailMatch(t *testing.T) {
	const x = "sahip@example.test"
	id := func(provider, email string, verified bool) models.AuthIdentity {
		e := email
		return models.AuthIdentity{Provider: provider, Email: &e, EmailVerified: verified}
	}
	password := id("password", x, false)
	cases := []struct {
		name       string
		identities []models.AuthIdentity
		provider   string
		verified   bool
		want       emailMatch
	}{
		{"kimliksiz hesap bağlanır", nil, "google", true, matchLink},
		{"parolasız, başka sağlayıcı bağlanır", []models.AuthIdentity{id("apple", x, false)}, "google", true, matchLink},
		{"parolasız, aynı sağlayıcı 409", []models.AuthIdentity{id("google", x, true)}, "google", true, matchSameProvider},
		{"yalnızca parola devralınır", []models.AuthIdentity{password}, "google", true, matchTakeOver},
		{"yalnızca parola, doğrulanmamış adres 409", []models.AuthIdentity{password}, "google", false, matchPasswordConflict},
		{"parola + başka adresli google, apple geri alır", []models.AuthIdentity{password, id("google", "baska@example.test", true)}, "apple", true, matchReclaim},
		{"parola + başka adresli google, google geri alır", []models.AuthIdentity{password, id("google", "baska@example.test", true)}, "google", true, matchReclaim},
		{"parola + X'i doğrulanmamış taşıyan google, apple geri alır", []models.AuthIdentity{password, id("google", x, false)}, "apple", true, matchReclaim},
		{"parola + X'i doğrulamış google, apple bağlanır", []models.AuthIdentity{password, id("google", "SAHIP@example.test", true)}, "apple", true, matchLink},
		{"parola + X'i doğrulamış google, başka google 409", []models.AuthIdentity{password, id("google", x, true)}, "google", true, matchSameProvider},
		{"parola + google, doğrulanmamış adres 409", []models.AuthIdentity{password, id("google", x, true)}, "apple", false, matchPasswordConflict},
		{"parola + google, doğrulanmamış aynı sağlayıcı 409", []models.AuthIdentity{password, id("google", x, true)}, "google", false, matchSameProvider},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, pw := decideEmailMatch(c.identities, c.provider, x, c.verified)
			if got != c.want {
				t.Errorf("karar = %d, beklenen %d", got, c.want)
			}
			wantPassword := c.want == matchTakeOver || c.want == matchReclaim
			if (pw != nil) != wantPassword || (pw != nil && pw.Provider != "password") {
				t.Errorf("parola kimliği = %v, beklenen dönmesi: %v", pw, wantPassword)
			}
		})
	}
}
