package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/models"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// QA: e-posta girişi ve misafir hesabı için kenar durumlar ve yarışlar.
// Gerçek PostgreSQL gerektirir (bkz. usage_integration_test.go).

// rawSend, isteği atar ve ham gövdeyi döner (yer tutucu adres sızıntısını
// ayrıştırmadan aramak için).
func rawSend(t *testing.T, app *fiber.App, req *http.Request) (int, string) {
	t.Helper()

	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("istek başarısız: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

// waitForLockWaiter, sorgusu pattern içeren bir bağlantı kilit beklemeye
// girene kadar bekler.
func waitForLockWaiter(t *testing.T, gdb *gorm.DB, pattern string) bool {
	t.Helper()

	for son := time.Now().Add(10 * time.Second); time.Now().Before(son); {
		var n int64
		gdb.Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND query ILIKE ?`,
			"%"+pattern+"%").Scan(&n)
		if n > 0 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// Devir ile aynı anda süren bir refresh rotasyonu: rotasyon eski token'ı
// kilitleyip halefini yazmışken (commit yok) devir başlarsa, halef devirden
// sonra etkin kalmamalı. Kalsaydı adresi önceden kapatan kişi (parolayı açan)
// devirden sonra da hesaba erişmeye devam ederdi.
func TestQA_DevirSurenRotasyonunHalefiniDeIptalEder(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	u, refresh := passwordAccount(t, gdb, app, "qa-devir-rotasyon")
	old := loadRefreshToken(t, gdb, refresh)

	pair, row, err := h.newTokenPair(&u, "")
	if err != nil {
		t.Fatalf("token çifti üretilemedi: %v", err)
	}
	rot := gdb.Begin()
	defer rot.Rollback()
	outcome, err := rotateRefreshToken(rot, old.ID, row, time.Now())
	if err != nil || outcome != refreshIssued {
		t.Fatalf("rotasyon = (%v, %v), beklenen refreshIssued", outcome, err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := h.authenticateSocial("google", &auth.SocialIdentity{Subject: "qa-rot-" + u.ID, Email: u.Email}, "")
		done <- err
	}()
	if !waitForLockWaiter(t, gdb, "refresh_tokens") {
		t.Error("devir rotasyonun kilidini beklemedi; yarış sıralaması kurulamadı")
	}
	if err := rot.Commit().Error; err != nil {
		t.Fatalf("rotasyon commit edilemedi: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("devir: %v", err)
	}

	if loadRefreshToken(t, gdb, pair.refresh).RevokedAt == nil {
		t.Error("devirle eşzamanlı rotasyonun halefi etkin kaldı: eski parola sahibi oturumunu sürdürür")
	}
	if status, _ := send(t, app, jsonRequest("/api/v1/auth/refresh", fiber.Map{"refreshToken": pair.refresh})); status != fiber.StatusUnauthorized {
		t.Errorf("halefle yenileme: status = %d, beklenen 401", status)
	}
	if n := activeRefreshTokens(t, gdb, u.ID); n != 0 {
		t.Errorf("devirden sonra %d etkin refresh token kaldı", n)
	}
}

// Devirden sonra adres yeniden parolayla kapatılamaz (kayıt da misafir parola
// bağlama da 409 email_in_use), eski parolayla giriş 401'dir.
func TestQA_DevirdenSonraAdresYenidenKapatilamaz(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	u, _ := passwordAccount(t, gdb, app, "qa-devir-sonrasi")
	if _, err := h.authenticateSocial("apple", &auth.SocialIdentity{Subject: "qa-sahip-" + u.ID, Email: u.Email}, ""); err != nil {
		t.Fatalf("devir: %v", err)
	}

	if status, body := postJSON(t, app, "/api/v1/auth/register", fiber.Map{"email": strings.ToUpper(u.Email), "password": "baska-parola-1"}); status != fiber.StatusConflict || body["code"] != "email_in_use" {
		t.Errorf("yeniden kayıt: %d %v, beklenen 409 email_in_use", status, body["code"])
	}
	_, guest := createGuest(t, gdb, app, nil)
	if status, body := send(t, app, linkPasswordRequest(t, h, &guest, u.Email, "baska-parola-1")); status != fiber.StatusConflict || body["code"] != "email_in_use" {
		t.Errorf("misafir parola bağlama: %d %v, beklenen 409 email_in_use", status, body["code"])
	}
	if g := loadUser(t, gdb, guest.ID); !g.IsGuest || !dto.IsSyntheticEmail(g.Email) {
		t.Errorf("reddedilen bağlama misafiri değiştirdi: is_guest=%v email=%q", g.IsGuest, g.Email)
	}
}

// Misafirin dönüşümü (Google, Apple, e-posta/parola) ilerlemeyi korur: kelime
// kutuları, öğrenilen sayısı, seviye/grup, günlük sayaçlar ve giriş günleri.
func TestQA_DonusumIlerlemeyiKorur(t *testing.T) {
	for _, yol := range []string{"google", "apple", "password"} {
		t.Run(yol, func(t *testing.T) {
			gdb := testDB(t)
			h := guestHandlers(gdb, "production")
			app := authApp(h)
			_, guest := createGuest(t, gdb, app, fiber.Map{"displayName": "Deneme"})

			for box := 1; box <= 5; box++ {
				testWord(t, gdb, guest.ID, "w-qa-"+yol+"-"+guest.ID+"-"+string(rune('0'+box)), box)
			}
			if err := gdb.Model(&models.User{}).Where("id = ?", guest.ID).Updates(map[string]any{
				"total_learned":               7,
				"current_level":               "A2",
				"current_group":               3,
				"groups_started_today_count":  2,
				"groups_unlocked_by_ad_today": 1,
				"review_ad_pass_count_today":  1,
				"review_answers_today":        40,
				"no_fill_passes_today":        1,
				"unlocked_groups_today":       "A2:3",
				"last_reset_date":             time.Now().UnixMilli(),
			}).Error; err != nil {
				t.Fatalf("ilerleme yazılamadı: %v", err)
			}
			if err := gdb.Create(&models.DailyLogin{UserID: guest.ID, LoginDate: time.Now()}).Error; err != nil {
				t.Fatalf("giriş günü yazılamadı: %v", err)
			}
			before := loadUser(t, gdb, guest.ID)
			wordsBefore := wordBoxes(t, gdb, guest.ID)

			email := benzersizEposta("qa-donusum-" + yol)
			var status int
			var body map[string]any
			if yol == "password" {
				status, body = send(t, app, linkPasswordRequest(t, h, &guest, email, "parola-12345"))
			} else {
				si := &auth.SocialIdentity{Subject: "qa-" + yol + "-" + guest.ID, Email: email}
				status, body = post(t, linkApp(h, guest.ID, yol, si), "/link")
			}
			if status != fiber.StatusOK {
				t.Fatalf("dönüşüm: status = %d (body: %v)", status, body)
			}
			user := userField(t, body)
			if user["isGuest"] != false || user["email"] != email || user["id"] != guest.ID {
				t.Errorf("user = %v", user)
			}
			if user["totalLearned"] != float64(7) || user["currentLevel"] != "A2" || user["currentGroup"] != float64(3) {
				t.Errorf("yanıttaki ilerleme değişti: %v", user)
			}

			after := loadUser(t, gdb, guest.ID)
			if after.IsGuest || after.Email != email {
				t.Errorf("is_guest/email = %v/%q", after.IsGuest, after.Email)
			}
			// Dönüşümün değiştirdiği alanlar dışında hesap aynı kalır.
			after.IsGuest, after.Email, after.UpdatedAt = before.IsGuest, before.Email, before.UpdatedAt
			if !reflect.DeepEqual(after, before) {
				t.Errorf("hesap alanları değişti:\nönce  %+v\nsonra %+v", before, after)
			}
			if got := wordBoxes(t, gdb, guest.ID); !slices.Equal(got, wordsBefore) {
				t.Errorf("kelime kutuları = %v, beklenen %v", got, wordsBefore)
			}
			var logins int64
			gdb.Model(&models.DailyLogin{}).Where("user_id = ?", guest.ID).Count(&logins)
			if logins != 1 {
				t.Errorf("giriş günleri = %d, beklenen 1", logins)
			}
			// Kullanım uçları aynı sayaçları döner.
			status, usage := send(t, app, authRequest(t, h, &after, http.MethodGet, "/api/v1/users/me/usage", nil))
			if status != fiber.StatusOK || usage["reviewAnswersToday"] != float64(40) {
				t.Errorf("usage = %d %v, beklenen reviewAnswersToday 40", status, usage)
			}
		})
	}
}

// wordBoxes, kullanıcının "kelime:kutu" listesini sıralı döner.
func wordBoxes(t *testing.T, gdb *gorm.DB, userID string) []string {
	t.Helper()

	var rows []models.WordProgress
	if err := gdb.Where("user_id = ?", userID).Order("word_id").Find(&rows).Error; err != nil {
		t.Fatalf("kelimeler okunamadı: %v", err)
	}
	out := []string{}
	for _, r := range rows {
		out = append(out, r.WordID+":"+string(rune('0'+r.Box)))
	}
	return out
}

// Yer tutucu adresler hiçbir yanıtta görünmez ve giriş/kayıt/bağlama için
// kullanılamaz; misafirin adresiyle giriş bilinmeyen adresle aynı 401'i alır.
func TestQA_YerTutucuAdresSizmazVeKullanilamaz(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)

	status, raw := rawSend(t, app, httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", nil))
	if status != fiber.StatusCreated {
		t.Fatalf("guest: %d %s", status, raw)
	}
	var first struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(raw), &first); err != nil || first.User.ID == "" {
		t.Fatalf("guest yanıtı okunamadı: %v (%s)", err, raw)
	}
	t.Cleanup(func() { gdb.Delete(&models.User{}, "id = ?", first.User.ID) })
	body, guest := createGuest(t, gdb, app, nil)
	if strings.Contains(strings.ToLower(raw), dto.SyntheticEmailDomain) {
		t.Errorf("POST /auth/guest yanıtında yer tutucu adres: %s", raw)
	}

	checks := map[string]*http.Request{
		"refresh":        jsonRequest("/api/v1/auth/refresh", fiber.Map{"refreshToken": body["refreshToken"]}),
		"auth/me":        authRequest(t, h, &guest, http.MethodGet, "/api/v1/auth/me", nil),
		"users/me":       authRequest(t, h, &guest, http.MethodGet, "/api/v1/users/me", nil),
		"users/me PATCH": authRequest(t, h, &guest, http.MethodPatch, "/api/v1/users/me", fiber.Map{"displayName": "Yeni"}),
		"unlink google":  authRequest(t, h, &guest, http.MethodDelete, "/api/v1/auth/link/google", nil),
	}
	for ad, req := range checks {
		status, raw := rawSend(t, app, req)
		if status != fiber.StatusOK {
			t.Errorf("%s: status = %d (%s)", ad, status, raw)
		}
		if strings.Contains(strings.ToLower(raw), dto.SyntheticEmailDomain) {
			t.Errorf("%s yanıtında yer tutucu adres: %s", ad, raw)
		}
	}

	// Giriş: misafirin adresi (her yazımda) bilinmeyen adresle aynı yanıtı alır.
	_, unknown := rawSend(t, app, jsonRequest("/api/v1/auth/login", fiber.Map{"email": benzersizEposta("qa-yok"), "password": "parola-12345"}))
	for _, e := range []string{guest.Email, strings.ToUpper(guest.Email), " " + guest.Email + " "} {
		status, raw := rawSend(t, app, jsonRequest("/api/v1/auth/login", fiber.Map{"email": e, "password": "parola-12345"}))
		if status != fiber.StatusUnauthorized || raw != unknown {
			t.Errorf("misafir adresiyle giriş %q: %d %s, beklenen bilinmeyen adresle aynı 401 (%s)", e, status, raw, unknown)
		}
	}
	// Kayıt ve parola bağlama yer tutucu adresi reddeder.
	if status, body := postJSON(t, app, "/api/v1/auth/register", fiber.Map{"email": guest.Email, "password": "parola-12345"}); status != fiber.StatusBadRequest || body["code"] != "invalid_email" {
		t.Errorf("yer tutucu adresle kayıt: %d %v, beklenen 400 invalid_email", status, body["code"])
	}
	_, other := createGuest(t, gdb, app, nil)
	if status, body := send(t, app, linkPasswordRequest(t, h, &other, strings.ToUpper(guest.Email), "parola-12345")); status != fiber.StatusBadRequest || body["code"] != "invalid_email" {
		t.Errorf("yer tutucu adresle parola bağlama: %d %v, beklenen 400 invalid_email", status, body["code"])
	}
	// Sağlayıcı yer tutucu adres bildirse bile misafirle eşlenmez.
	u, err := h.authenticateSocial("google", &auth.SocialIdentity{Subject: "qa-sahte-" + guest.ID, Email: guest.Email}, "")
	if err != nil {
		t.Fatalf("authenticateSocial: %v", err)
	}
	t.Cleanup(func() { gdb.Delete(&models.User{}, "id = ?", u.ID) })
	if u.ID == guest.ID {
		t.Error("yer tutucu adres bildiren sağlayıcı kimliği misafire bağlandı")
	}
	if n := identityCount(t, gdb, guest.ID, "google"); n != 0 {
		t.Errorf("misafire %d google kimliği eklendi", n)
	}
}

// Misafir Google ile kaydedilince sonraki rotasyon olağan ömre döner;
// misafirin ilk token'ı ve rotasyonu misafir ömründedir.
func TestQA_GoogleIleKayittanSonraOlaganOmur(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	body, guest := createGuest(t, gdb, app, nil)
	assertExpiresAround(t, gdb, body["refreshToken"].(string), testGuestRefreshTTL)

	si := &auth.SocialIdentity{Subject: "qa-ttl-" + guest.ID}
	if status, body := post(t, linkApp(h, guest.ID, "google", si), "/link"); status != fiber.StatusOK {
		t.Fatalf("link: %d %v", status, body)
	}
	status, body := send(t, app, jsonRequest("/api/v1/auth/refresh", fiber.Map{"refreshToken": body["refreshToken"]}))
	if status != fiber.StatusOK {
		t.Fatalf("refresh: %d %v", status, body)
	}
	assertExpiresAround(t, gdb, body["refreshToken"].(string), time.Hour)
	// Google ile giriş de olağan ömür verir.
	u, err := h.authenticateSocial("google", si, "")
	if err != nil || u.ID != guest.ID {
		t.Fatalf("giriş = (%v, %v)", u, err)
	}
	resp, err := h.issueTokens(u)
	if err != nil {
		t.Fatalf("issueTokens: %v", err)
	}
	assertExpiresAround(t, gdb, resp.RefreshToken, time.Hour)
}

// Parola kimliği kaldırılamaz (uç yok) ve hesabın tek giriş yolu parola
// kalırken sosyal kimlik kaldırılabilir; ardından parolayla giriş çalışır.
func TestQA_ParolaSonGirisYoluOlarakKalir(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	u, _ := passwordAccount(t, gdb, app, "qa-unlink")
	testIdentity(t, gdb, &u, "google")

	if status, _ := send(t, app, authRequest(t, h, &u, http.MethodDelete, "/api/v1/auth/link/password", nil)); status >= 200 && status < 300 {
		t.Errorf("DELETE /auth/link/password status = %d, beklenen başarısız", status)
	}
	if status, body := send(t, app, authRequest(t, h, &u, http.MethodDelete, "/api/v1/auth/link/google", nil)); status != fiber.StatusOK {
		t.Fatalf("google kaldırma: %d %v", status, body)
	} else if got := linkedProviders(t, userField(t, body)); !slices.Equal(got, []string{"password"}) {
		t.Errorf("linkedProviders = %v, beklenen [password]", got)
	}
	if status, body := postJSON(t, app, "/api/v1/auth/login", fiber.Map{"email": u.Email, "password": "parola-12345"}); status != fiber.StatusOK {
		t.Errorf("parolayla giriş: %d %v", status, body)
	}

	// Yalnızca Google'lı hesapta Google kaldırılamaz.
	g := testUser(t, gdb)
	testIdentity(t, gdb, g, "google")
	if status, body := send(t, app, authRequest(t, h, g, http.MethodDelete, "/api/v1/auth/link/google", nil)); status != fiber.StatusBadRequest || body["code"] != "last_identity" {
		t.Errorf("son kimlik: %d %v, beklenen 400 last_identity", status, body["code"])
	}
}

// Misafir hesabı silinince bütün verisi gider ve token'ı artık çalışmaz;
// silinen misafirin access token'ıyla parola bağlama 404 user_not_found döner.
func TestQA_SilinenMisafirTokenlariCalismaz(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	body, guest := createGuest(t, gdb, app, nil)
	testWord(t, gdb, guest.ID, "w-qa-sil-"+guest.ID, 3)

	if status, _ := send(t, app, authRequest(t, h, &guest, http.MethodDelete, "/api/v1/auth/account", nil)); status != fiber.StatusNoContent {
		t.Fatalf("silme status = %d", status)
	}
	if userExists(t, gdb, guest.ID) {
		t.Fatal("misafir silinmedi")
	}
	var words int64
	gdb.Model(&models.WordProgress{}).Where("user_id = ?", guest.ID).Count(&words)
	if words != 0 {
		t.Errorf("%d kelime satırı kaldı", words)
	}
	if status, _ := send(t, app, jsonRequest("/api/v1/auth/refresh", fiber.Map{"refreshToken": body["refreshToken"]})); status != fiber.StatusUnauthorized {
		t.Errorf("silinen misafirin yenilemesi: %d, beklenen 401", status)
	}
	if status, body := send(t, app, linkPasswordRequest(t, h, &guest, benzersizEposta("qa-silinen"), "parola-12345")); status != fiber.StatusNotFound || body["code"] != "user_not_found" {
		t.Errorf("silinen misafire parola bağlama: %d %v, beklenen 404 user_not_found", status, body["code"])
	}
}
