package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
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

// --- Adresin geri alınması ---

// squatter, adresi (X) parolayla önceden kapatıp yanına kendi Google/Apple
// hesabını bağlamış kişinin hesabıdır.
type squatter struct {
	user      models.User
	refreshes []string // kayıt ve ikinci cihaz oturumları
	subject   string   // kendi sağlayıcı kimliğinin subject'i
}

// newSquatter, X ile parola hesabı açar, ikinci bir cihazda parolayla girer ve
// hesaba provider kimliğini bağlar. Bağlanan kimliğin adresi kişinin kendi
// (doğrulanmış) adresidir, X değildir.
func newSquatter(t *testing.T, gdb *gorm.DB, h *Handlers, app *fiber.App, onEk, provider string) squatter {
	t.Helper()

	u, refresh := passwordAccount(t, gdb, app, onEk)
	status, body := postJSON(t, app, "/api/v1/auth/login", fiber.Map{"email": u.Email, "password": "parola-12345"})
	if status != fiber.StatusOK {
		t.Fatalf("login: status = %d (body: %v)", status, body)
	}
	subject := provider + "-saldirgan-" + u.ID
	si := &auth.SocialIdentity{Subject: subject, Email: benzersizEposta("saldirgan-kendi")}
	if status, body := post(t, linkApp(h, u.ID, provider, si), "/link"); status != fiber.StatusOK {
		t.Fatalf("saldırganın %s bağlaması: %d %v", provider, status, body)
	}
	// Adresin sahibine açılacak hesap test bitince silinsin.
	t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", u.Email) })
	return squatter{user: u, refreshes: []string{refresh, body["refreshToken"].(string)}, subject: subject}
}

// assertReclaimed, a'nın adresinin geri alındığını ve owner'ın X adresli yeni
// hesabı olduğunu sınar.
func assertReclaimed(t *testing.T, gdb *gorm.DB, app *fiber.App, a squatter, owner *models.User, ownerProvider string) {
	t.Helper()

	x := a.user.Email
	if owner.ID == a.user.ID {
		t.Fatal("adresin sahibi adresi tutan hesaba kondu")
	}
	if owner.Email != x {
		t.Errorf("yeni hesabın adresi = %q, beklenen %q", owner.Email, x)
	}
	if got := loadUser(t, gdb, owner.ID); got.Email != x || got.IsGuest {
		t.Errorf("yeni hesap email/is_guest = %q/%v, beklenen %q/false", got.Email, got.IsGuest, x)
	}
	if id := loadIdentity(t, gdb, owner.ID, ownerProvider); !id.EmailVerified || id.Email == nil || *id.Email != x {
		t.Errorf("yeni hesabın %s kimliği: email=%v verified=%v", ownerProvider, id.Email, id.EmailVerified)
	}

	eski := loadUser(t, gdb, a.user.ID)
	if want := dto.SyntheticEmail("reclaimed_" + a.user.ID); eski.Email != want {
		t.Errorf("eski hesabın adresi = %q, beklenen %q", eski.Email, want)
	}
	if n := identityCount(t, gdb, a.user.ID, "password"); n != 0 {
		t.Errorf("eski hesabın parola kimliği silinmedi (%d satır)", n)
	}
	if n := activeRefreshTokens(t, gdb, a.user.ID); n != 0 {
		t.Errorf("eski hesapta %d etkin refresh token kaldı, beklenen 0", n)
	}
	for i, token := range a.refreshes {
		if status, _ := send(t, app, jsonRequest("/api/v1/auth/refresh", fiber.Map{"refreshToken": token})); status != fiber.StatusUnauthorized {
			t.Errorf("eski oturum %d: yenileme status = %d, beklenen 401", i, status)
		}
	}
	if status, body := postJSON(t, app, "/api/v1/auth/login", fiber.Map{"email": x, "password": "parola-12345"}); status != fiber.StatusUnauthorized || body["code"] != "invalid_credentials" {
		t.Errorf("geri almadan sonra parola girişi: %d %v, beklenen 401 invalid_credentials", status, body["code"])
	}
	if status, body := postJSON(t, app, "/api/v1/auth/register", fiber.Map{"email": x, "password": "baska-parola-1"}); status != fiber.StatusConflict || body["code"] != "email_in_use" {
		t.Errorf("adresle yeniden kayıt: %d %v, beklenen 409 email_in_use", status, body["code"])
	}
}

// Saldırı: X'i parolayla kapatıp kendi Google'ını (ya da Apple'ını) bağlayan
// kişi, adresin doğrulanmış sahibini artık dışarıda bırakamaz. Sahibin girişi
// X adresli yeni bir hesap açar; saldırgan hesabını ve verisini korur, kendi
// sağlayıcısıyla girer ama adresi, parolası ve oturumları gider.
func TestReclaim_SaldirganinParolasiVeKendiSaglayicisi(t *testing.T) {
	cases := []struct{ attacker, owner string }{
		{"google", "apple"},
		{"apple", "google"},
		// Aynı sağlayıcı, farklı subject: eskiden 409.
		{"google", "google"},
		{"apple", "apple"},
	}
	for _, c := range cases {
		t.Run(c.attacker+"+parola, sahip "+c.owner, func(t *testing.T) {
			gdb := testDB(t)
			h := guestHandlers(gdb, "production")
			app := authApp(h)
			a := newSquatter(t, gdb, h, app, "geri-al-"+c.attacker+"-"+c.owner, c.attacker)
			testWord(t, gdb, a.user.ID, "w-geri-al-"+a.user.ID, 3)
			x := a.user.Email

			si := &auth.SocialIdentity{Subject: c.owner + "-sahip-" + a.user.ID, Email: strings.ToUpper(x), Name: "Sahip"}
			owner, err := h.authenticateSocial(c.owner, si, "")
			if err != nil {
				t.Fatalf("adresin sahibinin girişi: %v", err)
			}
			assertReclaimed(t, gdb, app, a, owner, c.owner)

			// Sahip, ilk sosyal girişteki gibi oturum alır.
			resp, err := h.issueTokens(owner)
			if err != nil {
				t.Fatalf("issueTokens: %v", err)
			}
			if resp.User.Email != x || resp.User.IsGuest || !slices.Equal(resp.User.LinkedProviders, []string{c.owner}) {
				t.Errorf("yanıt: email=%q isGuest=%v linkedProviders=%v", resp.User.Email, resp.User.IsGuest, resp.User.LinkedProviders)
			}
			var words int64
			gdb.Model(&models.WordProgress{}).Where("user_id = ?", owner.ID).Count(&words)
			if words != 0 {
				t.Errorf("yeni hesapta %d kelime var, beklenen 0", words)
			}
			// Sahibin sonraki girişi aynı yeni hesabı açar.
			again, err := h.authenticateSocial(c.owner, &auth.SocialIdentity{Subject: si.Subject}, "")
			if err != nil || again.ID != owner.ID {
				t.Errorf("sahibin ikinci girişi = (%v, %v), beklenen %s", again, err, owner.ID)
			}

			// Saldırgan kendi sağlayıcısıyla eski hesabına girer; veri yerinde,
			// adres yer tutucu ve istemciye boş görünür.
			back, err := h.authenticateSocial(c.attacker, &auth.SocialIdentity{Subject: a.subject}, "")
			if err != nil || back.ID != a.user.ID {
				t.Fatalf("saldırganın girişi = (%v, %v), beklenen eski hesap", back, err)
			}
			gdb.Model(&models.WordProgress{}).Where("user_id = ?", a.user.ID).Count(&words)
			if words != 1 {
				t.Errorf("eski hesabın kelime ilerlemesi = %d satır, beklenen 1", words)
			}
			status, raw := rawSend(t, app, authRequest(t, h, back, http.MethodGet, "/api/v1/auth/me", nil))
			if status != fiber.StatusOK || strings.Contains(raw, dto.SyntheticEmailDomain) || !strings.Contains(raw, `"email":""`) {
				t.Errorf("eski hesabın /auth/me yanıtı: %d %s", status, raw)
			}
			if !strings.Contains(raw, `"linkedProviders":["`+c.attacker+`"]`) {
				t.Errorf("eski hesabın bağlı yöntemleri: %s", raw)
			}
		})
	}
}

// Eski bir satır: parolanın yanındaki kimlik X'i taşır ama sağlayıcının
// doğruladığı kayıtlı değildir (email_verified=false; kolon eklenmeden önce
// yazılmış). Kanıt sayılmaz, adres geri alınır.
func TestReclaim_DogrulanmamisSaklananAdresKanitSayilmaz(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	u, refresh := passwordAccount(t, gdb, app, "geri-al-eski-satir")
	t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", u.Email) })
	testIdentity(t, gdb, &u, "google") // adres X, email_verified=false

	owner, err := h.authenticateSocial("apple", &auth.SocialIdentity{Subject: "apple-sahip-" + u.ID, Email: u.Email}, "")
	if err != nil {
		t.Fatalf("authenticateSocial: %v", err)
	}
	assertReclaimed(t, gdb, app, squatter{user: u, refreshes: []string{refresh}}, owner, "apple")
	if n := identityCount(t, gdb, u.ID, "google"); n != 1 {
		t.Errorf("eski hesabın google kimliği = %d, beklenen 1", n)
	}
}

// Meşru hesap: kişi X ile parolayla kaydolmuş, sonra X'i doğrulayan kendi
// Google'ını bağlamış. Adresin sahipliği hesapta kanıtlıdır: Apple ile X
// gelince bu hesaba bağlanır, geri alma olmaz; parola ve oturumlar kalır.
// Aynı sağlayıcının farklı subject'i ise bugünkü gibi 409 alır.
func TestReclaim_KanitliHesaptaGeriAlmaYok(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	u, refresh := passwordAccount(t, gdb, app, "kanitli")
	x := u.Email
	googleSub := "google-kendi-" + u.ID
	if status, body := post(t, linkApp(h, u.ID, "google", &auth.SocialIdentity{Subject: googleSub, Email: x}), "/link"); status != fiber.StatusOK {
		t.Fatalf("google bağlama: %d %v", status, body)
	}

	t.Run("başka sağlayıcı bağlanır", func(t *testing.T) {
		got, err := h.authenticateSocial("apple", &auth.SocialIdentity{Subject: "apple-kendi-" + u.ID, Email: strings.ToUpper(x)}, "")
		if err != nil {
			t.Fatalf("authenticateSocial: %v", err)
		}
		if got.ID != u.ID {
			t.Fatalf("kullanıcı = %s, beklenen kanıtlı hesap %s", got.ID, u.ID)
		}
		if got := loadUser(t, gdb, u.ID); got.Email != x {
			t.Errorf("adres = %q, beklenen %q", got.Email, x)
		}
		if n := identityCount(t, gdb, u.ID, "password"); n != 1 {
			t.Errorf("parola kimliği = %d, beklenen 1", n)
		}
		if !loadIdentity(t, gdb, u.ID, "apple").EmailVerified {
			t.Error("bağlanan apple kimliği doğrulanmamış yazıldı")
		}
		if loadRefreshToken(t, gdb, refresh).RevokedAt != nil {
			t.Error("bağlama oturumu iptal etti")
		}
		if status, _ := postJSON(t, app, "/api/v1/auth/login", fiber.Map{"email": x, "password": "parola-12345"}); status != fiber.StatusOK {
			t.Errorf("parola girişi: status = %d, beklenen 200", status)
		}
	})
	t.Run("aynı sağlayıcının başka kimliği 409", func(t *testing.T) {
		_, err := h.authenticateSocial("google", &auth.SocialIdentity{Subject: "google-baska-" + u.ID, Email: x}, "")
		apiError := apiErrorOf(t, err)
		if apiError.Status != fiber.StatusConflict || apiError.Code != "email_in_use" || !strings.Contains(apiError.Message, "aynı sağlayıcının") {
			t.Errorf("hata = %d %q %q, beklenen 409 email_in_use (aynı sağlayıcının)", apiError.Status, apiError.Code, apiError.Message)
		}
		if n := identityCount(t, gdb, u.ID, "google"); n != 1 {
			t.Errorf("google kimliği = %d, beklenen 1", n)
		}
		if got := loadUser(t, gdb, u.ID); got.Email != x {
			t.Errorf("adres = %q, beklenen %q", got.Email, x)
		}
	})
}

// Doğrulanmamış (ya da hiç verilmemiş) adres hiçbir zaman geri almaz: kimlik
// yer tutucu adresli ayrı bir hesaba bağlanır, adresi tutan hesaba dokunulmaz.
func TestReclaim_DogrulanmamisAdresGeriAlmaz(t *testing.T) {
	for _, provider := range []string{"google", "apple"} {
		t.Run(provider, func(t *testing.T) {
			gdb := testDB(t)
			h := guestHandlers(gdb, "production")
			app := authApp(h)
			a := newSquatter(t, gdb, h, app, "dogrulanmamis-"+provider, "google")
			subject := provider + "-dogrulanmamis-" + a.user.ID
			t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", dto.SyntheticEmail(provider+"_"+subject)) })

			// Doğrulayıcı doğrulanmamış adresi boş döndürür (bkz. auth.verifiedEmail).
			got, err := h.authenticateSocial(provider, &auth.SocialIdentity{Subject: subject}, "")
			if err != nil {
				t.Fatalf("authenticateSocial: %v", err)
			}
			if got.ID == a.user.ID || got.Email == a.user.Email {
				t.Fatalf("doğrulanmamış kimlik adresi tutan hesaba dokundu: %s %q", got.ID, got.Email)
			}
			if eski := loadUser(t, gdb, a.user.ID); eski.Email != a.user.Email {
				t.Errorf("adres = %q, beklenen %q", eski.Email, a.user.Email)
			}
			if n := identityCount(t, gdb, a.user.ID, "password"); n != 1 {
				t.Errorf("parola kimliği = %d, beklenen 1", n)
			}
			if n := activeRefreshTokens(t, gdb, a.user.ID); n != int64(len(a.refreshes)) {
				t.Errorf("etkin token = %d, beklenen %d", n, len(a.refreshes))
			}
		})
	}
}

// Misafirin Google/Apple bağlaması geri alma yapmaz: X başka bir hesaptaysa
// bağlama yine başarılıdır, misafir kaydedilir ve yer tutucu adresi kalır; X'i
// tutan hesaba dokunulmaz (gerekçe: linkProvider).
func TestReclaim_BaglamaGeriAlmaYapmaz(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	a := newSquatter(t, gdb, h, app, "bagla-geri-alma", "google")
	_, guest := createGuest(t, gdb, app, nil)

	si := &auth.SocialIdentity{Subject: "apple-misafir-" + guest.ID, Email: a.user.Email}
	status, body := post(t, linkApp(h, guest.ID, "apple", si), "/link")
	if status != fiber.StatusOK {
		t.Fatalf("bağlama: %d %v", status, body)
	}
	if g := loadUser(t, gdb, guest.ID); g.IsGuest || g.Email != guest.Email {
		t.Errorf("misafir is_guest/email = %v/%q, beklenen false/%q", g.IsGuest, g.Email, guest.Email)
	}
	if eski := loadUser(t, gdb, a.user.ID); eski.Email != a.user.Email {
		t.Errorf("X'i tutan hesabın adresi değişti: %q", eski.Email)
	}
	if n := identityCount(t, gdb, a.user.ID, "password"); n != 1 {
		t.Errorf("parola kimliği = %d, beklenen 1", n)
	}
	if n := activeRefreshTokens(t, gdb, a.user.ID); n != int64(len(a.refreshes)) {
		t.Errorf("etkin token = %d, beklenen %d", n, len(a.refreshes))
	}
}

// Geri alma ile aynı anda süren bir refresh rotasyonu: rotasyon eski token'ı
// kilitleyip halefini yazmışken (commit yok) geri alma başlarsa, halef geri
// almadan sonra etkin kalmamalı (4bab53f'deki devir yarışının aynısı).
func TestReclaim_SurenRotasyonunHalefiniDeIptalEder(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	a := newSquatter(t, gdb, h, app, "geri-al-rotasyon", "google")
	old := loadRefreshToken(t, gdb, a.refreshes[0])

	pair, row, err := h.newTokenPair(&a.user, "")
	if err != nil {
		t.Fatalf("token çifti üretilemedi: %v", err)
	}
	rot := gdb.Begin()
	defer rot.Rollback()
	outcome, err := rotateRefreshToken(rot, old.ID, row, time.Now())
	if err != nil || outcome != refreshIssued {
		t.Fatalf("rotasyon = (%v, %v), beklenen refreshIssued", outcome, err)
	}

	type sonuc struct {
		user *models.User
		err  error
	}
	done := make(chan sonuc, 1)
	go func() {
		u, err := h.authenticateSocial("apple", &auth.SocialIdentity{Subject: "apple-rot-" + a.user.ID, Email: a.user.Email}, "")
		done <- sonuc{u, err}
	}()
	if !waitForLockWaiter(t, gdb, "refresh_tokens") {
		t.Error("geri alma rotasyonun kilidini beklemedi; yarış sıralaması kurulamadı")
	}
	if err := rot.Commit().Error; err != nil {
		t.Fatalf("rotasyon commit edilemedi: %v", err)
	}
	got := <-done
	if got.err != nil {
		t.Fatalf("geri alma: %v", got.err)
	}

	if loadRefreshToken(t, gdb, pair.refresh).RevokedAt == nil {
		t.Error("geri almayla eşzamanlı rotasyonun halefi etkin kaldı")
	}
	assertReclaimed(t, gdb, app, squatter{user: a.user, refreshes: append(a.refreshes, pair.refresh)}, got.user, "apple")
}

// Geri alma ile aynı anda süren bir parola girişi: giriş parola kimliğini
// kilitleyip token yazarken geri alma bekler, sonra o token'ı da iptal eder.
func TestReclaim_EszamanliGirisinTokeniniDeIptalEder(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	a := newSquatter(t, gdb, h, app, "geri-al-giris", "apple")

	// Girişin transaction'ı (Login ile aynı sıra): kimlik kilitli, token
	// yazıldı, henüz commit yok.
	giris := gdb.Begin()
	defer giris.Rollback()
	if err := giris.Exec(`SELECT 1 FROM auth_identities WHERE user_id = ? AND provider = 'password' FOR UPDATE`, a.user.ID).Error; err != nil {
		t.Fatalf("kimlik kilitlenemedi: %v", err)
	}
	token, _ := auth.GenerateRefreshToken()
	if err := giris.Create(&models.RefreshToken{UserID: a.user.ID, TokenHash: auth.HashToken(token),
		ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatalf("girişin token'ı yazılamadı: %v", err)
	}

	type sonuc struct {
		user *models.User
		err  error
	}
	done := make(chan sonuc, 1)
	go func() {
		u, err := h.authenticateSocial("google", &auth.SocialIdentity{Subject: "google-giris-" + a.user.ID, Email: a.user.Email}, "")
		done <- sonuc{u, err}
	}()
	if !waitForLockWaiter(t, gdb, "auth_identities") {
		t.Error("geri alma girişin kilidini beklemedi; yarış sıralaması kurulamadı")
	}
	if err := giris.Commit().Error; err != nil {
		t.Fatalf("giriş commit edilemedi: %v", err)
	}
	got := <-done
	if got.err != nil {
		t.Fatalf("geri alma: %v", got.err)
	}
	if loadRefreshToken(t, gdb, token).RevokedAt == nil {
		t.Error("geri almayla eşzamanlı girişin token'ı etkin kaldı")
	}
	assertReclaimed(t, gdb, app, a, got.user, "google")
}

// Geri alma, gerçek uçlardan aynı anda gelen yenileme ve parola girişiyle
// yarışır (sıra her turda değişebilir). Hangi sıra olursa olsun: geri alma
// başarılıdır, eski hesapta etkin token kalmaz, yenileme ve giriş yalnızca
// 200 ya da 401 alır (kilitlenme ya da 500 yok) ve 200 aldılarsa verdikleri
// token da iptal edilmiştir.
func TestReclaim_YenilemeVeGirisleYaris(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)

	for tur := range 4 {
		t.Run(fmt.Sprintf("tur %d", tur), func(t *testing.T) {
			a := newSquatter(t, gdb, h, app, fmt.Sprintf("geri-al-yaris-%d", tur), "google")

			var wg sync.WaitGroup
			start := make(chan struct{})
			var reclaimErr error
			var owner *models.User
			var refreshStatus, loginStatus int
			var refreshBody, loginBody map[string]any
			var refreshErr, loginErr error
			do := func(req *http.Request, status *int, body *map[string]any, errp *error) {
				defer wg.Done()
				<-start
				res, err := app.Test(req, -1)
				if err != nil {
					*errp = err
					return
				}
				defer res.Body.Close()
				*status = res.StatusCode
				_ = json.NewDecoder(res.Body).Decode(body)
			}
			wg.Add(3)
			go func() {
				defer wg.Done()
				<-start
				owner, reclaimErr = h.authenticateSocial("apple",
					&auth.SocialIdentity{Subject: "apple-yaris-" + a.user.ID, Email: a.user.Email}, "")
			}()
			go do(jsonRequest("/api/v1/auth/refresh", fiber.Map{"refreshToken": a.refreshes[0]}), &refreshStatus, &refreshBody, &refreshErr)
			go do(jsonRequest("/api/v1/auth/login", fiber.Map{"email": a.user.Email, "password": "parola-12345"}), &loginStatus, &loginBody, &loginErr)
			close(start)
			wg.Wait()

			if reclaimErr != nil {
				t.Fatalf("geri alma: %v", reclaimErr)
			}
			if refreshErr != nil || loginErr != nil {
				t.Fatalf("istek başarısız: yenileme %v, giriş %v", refreshErr, loginErr)
			}
			for ad, r := range map[string]struct {
				status int
				body   map[string]any
			}{"yenileme": {refreshStatus, refreshBody}, "giriş": {loginStatus, loginBody}} {
				switch r.status {
				case fiber.StatusOK:
					token, _ := r.body["refreshToken"].(string)
					if token == "" {
						t.Errorf("%s: 200 ama refreshToken yok", ad)
						continue
					}
					if loadRefreshToken(t, gdb, token).RevokedAt == nil {
						t.Errorf("%s: geri almayla yarışan isteğin token'ı etkin kaldı", ad)
					}
				case fiber.StatusUnauthorized:
				default:
					t.Errorf("%s: status = %d, beklenen 200 ya da 401 (body: %v)", ad, r.status, r.body)
				}
			}
			assertReclaimed(t, gdb, app, a, owner, "apple")
		})
	}
}

// Adresin sahibi aynı anda iki kez girerse (çift dokunuş) tek bir geri alma
// olur ve iki istek de aynı yeni hesabı döner.
func TestReclaim_CiftDokunusTekHesap(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	a := newSquatter(t, gdb, h, app, "geri-al-cift", "google")
	si := &auth.SocialIdentity{Subject: "apple-cift-" + a.user.ID, Email: a.user.Email}

	var wg sync.WaitGroup
	start := make(chan struct{})
	users := make([]*models.User, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			users[i], errs[i] = h.authenticateSocial("apple", si, "")
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("istek %d: %v", i, err)
		}
	}
	if users[0].ID != users[1].ID {
		t.Errorf("iki istek farklı hesap döndü: %s, %s", users[0].ID, users[1].ID)
	}
	var n int64
	gdb.Model(&models.AuthIdentity{}).Where("provider = 'apple' AND provider_subject = ?", si.Subject).Count(&n)
	if n != 1 {
		t.Errorf("apple kimliği = %d satır, beklenen 1", n)
	}
	assertReclaimed(t, gdb, app, a, users[0], "apple")
}
