package handlers

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"gorm.io/gorm"
)

// Rota tablosunu sınayan testler (TestRegisterRoutes_*) veritabanına dokunmaz ve
// her yerde çalışır; geri kalanı gerçek PostgreSQL ister ve TEST_DATABASE_URL
// tanımlı değilse atlanır (bkz. usage_integration_test.go).

func testTokens() *auth.TokenService {
	return auth.NewTokenService("test-secret", time.Minute, time.Hour)
}

// routesApp, gerçek rota tablosunu verilen ortam ve sıkı hız sınırıyla kurar.
func routesApp(gdb *gorm.DB, appEnv string, authLimit fiber.Handler) *fiber.App {
	tokens := testTokens()
	h := New(gdb, &config.Config{AppEnv: appEnv}, tokens, nil)

	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	RegisterRoutes(app.Group("/api/v1"), h, tokens, authLimit)
	return app
}

func sinirsiz(c *fiber.Ctx) error { return c.Next() }

func benzersizEposta(onEk string) string {
	return onEk + "-" + time.Now().Format("20060102150405.000000000") + "@example.test"
}

// --- Rota tablosu ---

func TestRegisterRoutes_ParolaUclariHerOrtamdaAcik(t *testing.T) {
	for _, env := range []string{"production", "development", "test"} {
		t.Run(env, func(t *testing.T) {
			app := routesApp(nil, env, sinirsiz)

			// Gövdesiz istek, uç açıksa veritabanına gitmeden 400 alır; kapalıysa 404.
			for _, path := range []string{"/api/v1/auth/register", "/api/v1/auth/login",
				"/api/v1/auth/google", "/api/v1/auth/refresh"} {
				status, body := post(t, app, path)
				if status != fiber.StatusBadRequest {
					t.Errorf("POST %s: status = %d, beklenen 400 (body: %v)", path, status, body)
				}
			}
		})
	}
}

func TestRegisterRoutes_YenilemeSikiHizSinirinaTabiDegil(t *testing.T) {
	const sinir = 10
	app := routesApp(nil, "production", limiter.New(limiter.Config{
		Max:        sinir,
		Expiration: time.Minute,
		LimitReached: func(*fiber.Ctx) error {
			return apiErr(fiber.StatusTooManyRequests, "rate_limited", "çok fazla deneme")
		},
	}))

	// Önce sıkı sınırı bu IP için doldur: giriş uçları ona tabidir.
	for i := 1; i <= sinir; i++ {
		if status, body := post(t, app, "/api/v1/auth/google"); status != fiber.StatusBadRequest {
			t.Fatalf("%d. giriş isteği: status = %d, beklenen 400 (body: %v)", i, status, body)
		}
	}
	if status, _ := post(t, app, "/api/v1/auth/google"); status != fiber.StatusTooManyRequests {
		t.Fatalf("sınır üstü giriş isteği: status = %d, beklenen 429", status)
	}

	// Aynı IP'den yenileme, sıkı sınır doluyken de reddedilmemeli.
	for i := 1; i <= sinir+1; i++ {
		status, body := post(t, app, "/api/v1/auth/refresh")
		if status != fiber.StatusBadRequest {
			t.Fatalf("%d. yenileme isteği: status = %d, beklenen 400 (body: %v)", i, status, body)
		}
	}
}

func TestRegisterRoutes_ProductiondaKayitVeGirisCalisir(t *testing.T) {
	gdb := testDB(t)
	app := routesApp(gdb, "production", sinirsiz)

	email := benzersizEposta("kayit")
	t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", email) })
	kimlik := fiber.Map{"email": email, "password": "parola-12345"}

	status, body := postJSON(t, app, "/api/v1/auth/register", kimlik)
	if status != fiber.StatusCreated {
		t.Fatalf("register: status = %d, beklenen 201 (body: %v)", status, body)
	}
	if body["accessToken"] == "" || body["refreshToken"] == "" {
		t.Errorf("register: token çifti yok (body: %v)", body)
	}

	status, body = postJSON(t, app, "/api/v1/auth/login", kimlik)
	if status != fiber.StatusOK {
		t.Fatalf("login: status = %d, beklenen 200 (body: %v)", status, body)
	}
	if token, _ := body["accessToken"].(string); token == "" {
		t.Errorf("login: accessToken yok (body: %v)", body)
	}
}

// --- Sosyal giriş: e-postayla eşleme ---

// testIdentity, kullanıcıya verilen sağlayıcı için bir kimlik satırı ekler.
// Satır, kullanıcı silinince CASCADE ile temizlenir.
func testIdentity(t *testing.T, gdb *gorm.DB, u *models.User, provider string) {
	t.Helper()

	identity := &models.AuthIdentity{UserID: u.ID, Provider: provider, Email: &u.Email}
	if provider == "password" {
		hash := "test-hash"
		identity.PasswordHash = &hash
	} else {
		sub := provider + "-" + u.ID
		identity.ProviderSubject = &sub
	}
	if err := gdb.Create(identity).Error; err != nil {
		t.Fatalf("%s kimliği oluşturulamadı: %v", provider, err)
	}
}

func identityCount(t *testing.T, gdb *gorm.DB, userID, provider string) int64 {
	t.Helper()

	var n int64
	if err := gdb.Model(&models.AuthIdentity{}).
		Where("user_id = ? AND provider = ?", userID, provider).Count(&n).Error; err != nil {
		t.Fatalf("kimlikler sayılamadı: %v", err)
	}
	return n
}

// Doğrulanmamış (ya da hiç verilmemiş) sağlayıcı e-postası parola hesabına
// hiçbir şekilde dokunmaz: yer tutucu adresli ayrı bir hesap açılır.
func TestAuthenticateSocial_DogrulanmamisEpostaParolaHesabinaDokunmaz(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	testIdentity(t, gdb, u, "password")
	refresh := testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), nil)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)

	for _, provider := range []string{"google", "apple"} {
		t.Run(provider, func(t *testing.T) {
			subject := "dogrulanmamis-" + provider + "-" + u.ID
			t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", dto.SyntheticEmail(provider+"_"+subject)) })

			// Doğrulayıcı doğrulanmamış adresi boş döndürür (bkz. auth.verifiedEmail).
			got, err := h.authenticateSocial(provider, &auth.SocialIdentity{Subject: subject}, "")
			if err != nil {
				t.Fatalf("authenticateSocial: %v", err)
			}
			if got.ID == u.ID {
				t.Fatal("doğrulanmamış kimlik parola hesabına bağlandı")
			}
		})
	}

	if n := identityCount(t, gdb, u.ID, "password"); n != 1 {
		t.Errorf("parola kimliği sayısı = %d, beklenen 1", n)
	}
	if loadRefreshToken(t, gdb, refresh).RevokedAt != nil {
		t.Error("parola hesabının oturumu iptal edildi")
	}
}

// Aynı doğrulanmış adres aynı sağlayıcıdan farklı bir subject ile gelebilir
// (sağlayıcı hesabı silinip yeniden açılmış ya da adres el değiştirmiş). İkinci
// kimlik eklenemez; istek kısıta çarpıp 500 dönmemeli, 409 ile reddedilmeli.
func TestAuthenticateSocial_AyniSaglayicininBaskaKimligiVarsaReddeder(t *testing.T) {
	gdb := testDB(t)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)

	for _, provider := range []string{"google", "apple"} {
		t.Run(provider, func(t *testing.T) {
			u := testUser(t, gdb)
			testIdentity(t, gdb, u, provider)

			si := &auth.SocialIdentity{Subject: "yeni-" + u.ID, Email: u.Email}
			got, err := h.authenticateSocial(provider, si, "")

			var apiError *middleware.APIError
			if !errors.As(err, &apiError) {
				t.Fatalf("hata = %v (kullanıcı: %v), beklenen APIError", err, got)
			}
			if apiError.Status != fiber.StatusConflict || apiError.Code != "email_in_use" {
				t.Errorf("hata = %d %q, beklenen 409 \"email_in_use\"", apiError.Status, apiError.Code)
			}
			// Eski kimlik yerinde kalır, yenisi eklenmez.
			if n := identityCount(t, gdb, u.ID, provider); n != 1 {
				t.Errorf("%s kimliği sayısı = %d, beklenen 1", provider, n)
			}

			// Eski subject ile giriş çalışmaya devam eder.
			old, err := h.authenticateSocial(provider, &auth.SocialIdentity{Subject: provider + "-" + u.ID}, "")
			if err != nil || old.ID != u.ID {
				t.Errorf("eski kimlikle giriş = (%v, %v), beklenen aynı hesap", old, err)
			}
		})
	}
}

func TestAuthenticateSocial_ParolasizHesabaEpostaIleBaglar(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	testIdentity(t, gdb, u, "apple")
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)

	// Her iki sağlayıcı da adresi doğrulamıştır; aynı kişi tek hesapta buluşur.
	si := &auth.SocialIdentity{Subject: "g-" + u.ID, Email: u.Email}
	got, err := h.authenticateSocial("google", si, "")
	if err != nil {
		t.Fatalf("authenticateSocial: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("kullanıcı = %s, beklenen mevcut hesap %s", got.ID, u.ID)
	}
	if n := identityCount(t, gdb, u.ID, "google"); n != 1 {
		t.Errorf("google kimliği sayısı = %d, beklenen 1", n)
	}

	// Sonraki girişler kimliği sağlayıcı + subject ile bulur.
	again, err := h.authenticateSocial("google", &auth.SocialIdentity{Subject: si.Subject}, "")
	if err != nil || again.ID != u.ID {
		t.Errorf("ikinci giriş = (%v, %v), beklenen aynı hesap", again, err)
	}
}

func TestAuthenticateSocial_EpostasizKimlikMevcutHesabaBaglanmaz(t *testing.T) {
	gdb := testDB(t)
	kurban := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)

	// Doğrulanmamış adres doğrulayıcıdan boş gelir: kimlik, o adresin sahibinin
	// hesabına değil yer tutucu adresli yeni bir hesaba bağlanmalı.
	subject := "dogrulanmamis-" + kurban.ID
	yerTutucu := "google_" + subject + "@users.kelimeler.local"
	t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", yerTutucu) })

	got, err := h.authenticateSocial("google", &auth.SocialIdentity{Subject: subject, Name: "Ad"}, "")
	if err != nil {
		t.Fatalf("authenticateSocial: %v", err)
	}
	if got.ID == kurban.ID {
		t.Fatal("e-postasız kimlik mevcut hesaba bağlandı")
	}
	if got.Email != yerTutucu {
		t.Errorf("email = %q, beklenen %q", got.Email, yerTutucu)
	}
	if n := identityCount(t, gdb, kurban.ID, "google"); n != 0 {
		t.Errorf("mevcut hesapta %d google kimliği var, beklenen 0", n)
	}
}

// --- Token yenileme ---

func refreshApp(h *Handlers) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	app.Post("/refresh", h.Refresh)
	return app
}

func refreshRequest(token string) *http.Request {
	return jsonRequest("/refresh", fiber.Map{"refreshToken": token})
}

// testRefreshToken, kullanıcı için bir refresh token satırı açar ve token'ın
// kendisini döner. Satır, kullanıcı silinince CASCADE ile temizlenir.
func testRefreshToken(t *testing.T, gdb *gorm.DB, userID string, expiresAt time.Time, revokedAt *time.Time) string {
	t.Helper()

	token, err := auth.GenerateRefreshToken()
	if err != nil {
		t.Fatalf("refresh token üretilemedi: %v", err)
	}
	row := &models.RefreshToken{
		UserID:    userID,
		TokenHash: auth.HashToken(token),
		ExpiresAt: expiresAt,
		RevokedAt: revokedAt,
	}
	if err := gdb.Create(row).Error; err != nil {
		t.Fatalf("refresh token yazılamadı: %v", err)
	}
	return token
}

func loadRefreshToken(t *testing.T, gdb *gorm.DB, token string) models.RefreshToken {
	t.Helper()

	var row models.RefreshToken
	if err := gdb.First(&row, "token_hash = ?", auth.HashToken(token)).Error; err != nil {
		t.Fatalf("refresh token okunamadı: %v", err)
	}
	return row
}

func activeRefreshTokens(t *testing.T, gdb *gorm.DB, userID string) int64 {
	t.Helper()

	var n int64
	if err := gdb.Model(&models.RefreshToken{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).Count(&n).Error; err != nil {
		t.Fatalf("refresh token'lar sayılamadı: %v", err)
	}
	return n
}

func TestRefresh_ReddedilenToken401Doner(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := refreshApp(New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil))

	gecmis := time.Now().Add(-time.Minute)
	cases := []struct {
		name  string
		token string
	}{
		{"bilinmeyen token", "hic-verilmemis-bir-token"},
		{"suresi dolmus", testRefreshToken(t, gdb, u.ID, gecmis, nil)},
		{"iptal edilmis", testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), &gecmis)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := send(t, app, refreshRequest(c.token))
			if status != fiber.StatusUnauthorized {
				t.Fatalf("status = %d, beklenen 401 (body: %v)", status, body)
			}
			if body["code"] != "invalid_refresh_token" {
				t.Errorf("code = %v, beklenen invalid_refresh_token", body["code"])
			}
		})
	}

	if n := activeRefreshTokens(t, gdb, u.ID); n != 1 {
		t.Errorf("etkin token sayısı = %d, beklenen 1 (yalnızca süresi dolmuş satır; reddedilen istek token yazmamalı)", n)
	}
}

func TestRefresh_VeritabaniHatasi500Doner(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	token := testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), nil)

	// Kapalı havuz her sorguda hata verir: ulaşılamayan veritabanının karşılığı.
	app := refreshApp(New(kopukDB(t), &config.Config{AppEnv: "test"}, testTokens(), nil))

	// İstemci 401'i kesin ret sayıp saklı token'ları siler; geçici bir hata
	// kullanıcıyı oturumdan düşürmemeli.
	for _, c := range []struct{ name, token string }{
		{"gecerli token", token},
		{"bilinmeyen token", "hic-verilmemis-bir-token"},
	} {
		t.Run(c.name, func(t *testing.T) {
			status, body := send(t, app, refreshRequest(c.token))
			if status != fiber.StatusInternalServerError {
				t.Fatalf("status = %d, beklenen 500 (body: %v)", status, body)
			}
			if body["code"] != "internal" {
				t.Errorf("code = %v, beklenen internal", body["code"])
			}
		})
	}

	if row := loadRefreshToken(t, gdb, token); row.RevokedAt != nil {
		t.Error("başarısız yenileme token'ı iptal etti")
	}
}

func TestRefresh_RotasyonEskiTokeniIptalEderYenisiniVerir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := refreshApp(New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil))
	eski := testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), nil)

	status, body := send(t, app, refreshRequest(eski))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, beklenen 200 (body: %v)", status, body)
	}
	yeni, _ := body["refreshToken"].(string)
	if yeni == "" || yeni == eski {
		t.Fatalf("refreshToken = %q, beklenen yeni bir token", yeni)
	}
	if access, _ := body["accessToken"].(string); access == "" {
		t.Errorf("accessToken yok (body: %v)", body)
	}
	if user, _ := body["user"].(map[string]any); user["id"] != u.ID {
		t.Errorf("user.id = %v, beklenen %s", user["id"], u.ID)
	}

	eskiSatir, yeniSatir := loadRefreshToken(t, gdb, eski), loadRefreshToken(t, gdb, yeni)
	if eskiSatir.RevokedAt == nil {
		t.Error("eski token iptal edilmedi")
	}
	if eskiSatir.ReplacedByID == nil || *eskiSatir.ReplacedByID != yeniSatir.ID {
		t.Errorf("eski token'ın halefi = %v, beklenen %d", eskiSatir.ReplacedByID, yeniSatir.ID)
	}
	// Bu kolondan önce yazılmış satır ilk rotasyonda bir aileye alınır.
	if eskiSatir.FamilyID == nil || yeniSatir.FamilyID == nil || *eskiSatir.FamilyID != *yeniSatir.FamilyID {
		t.Errorf("aile taşınmadı: eski %v, yeni %v", eskiSatir.FamilyID, yeniSatir.FamilyID)
	}
	if n := activeRefreshTokens(t, gdb, u.ID); n != 1 {
		t.Errorf("etkin token sayısı = %d, beklenen 1", n)
	}

	status, body = send(t, app, refreshRequest(yeni))
	if status != fiber.StatusOK {
		t.Fatalf("yeni token: status = %d, beklenen 200 (body: %v)", status, body)
	}
	sonraki, _ := body["refreshToken"].(string)
	if f := loadRefreshToken(t, gdb, sonraki).FamilyID; f == nil || *f != *yeniSatir.FamilyID {
		t.Errorf("rotasyon aileyi korumadı: %v", f)
	}
}

// Sunucu yenilemeyi commit eder ama yanıt mobil ağda kaybolur; istemci aynı
// (artık iptal edilmiş) token'la yeniden dener. Kullanıcı oturumdan düşmemeli
// ve aynı yeni çifti almalı: iki yanıttan hangisi istemciye ulaşırsa ulaşsın
// elindeki refresh token geçerli kalır.
func TestRefresh_ToleransIcindeYenidenDenemeAyniCiftiVerir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := refreshApp(New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil))
	eski := testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), nil)

	// İlk yanıt kayboldu: istemci kaybolan token'ı hiç görmedi.
	status, body := send(t, app, refreshRequest(eski))
	if status != fiber.StatusOK {
		t.Fatalf("ilk yenileme: status = %d (body: %v)", status, body)
	}
	kaybolan, _ := body["refreshToken"].(string)

	for deneme := 2; deneme <= 3; deneme++ {
		status, body = send(t, app, refreshRequest(eski))
		if status != fiber.StatusOK {
			t.Fatalf("%d. deneme: status = %d, beklenen 200 (body: %v)", deneme, status, body)
		}
		if got, _ := body["refreshToken"].(string); got != kaybolan {
			t.Fatalf("%d. deneme başka bir refresh token verdi: %q, beklenen %q", deneme, got, kaybolan)
		}
		if access, _ := body["accessToken"].(string); access == "" {
			t.Errorf("%d. deneme: accessToken yok", deneme)
		}
		if loadRefreshToken(t, gdb, kaybolan).RevokedAt != nil {
			t.Fatalf("%d. deneme halefi iptal etti", deneme)
		}
		if n := activeRefreshTokens(t, gdb, u.ID); n != 1 {
			t.Errorf("etkin token sayısı = %d, beklenen 1", n)
		}
	}

	// İstemci aldığı token'la olağan biçimde yeniler: halef artık kullanıldı.
	status, body = send(t, app, refreshRequest(kaybolan))
	if status != fiber.StatusOK {
		t.Fatalf("alınan token: status = %d, beklenen 200 (body: %v)", status, body)
	}
	guncel, _ := body["refreshToken"].(string)

	// Halefi kullanılmış eski token'ın yeniden gelmesi meşru bir yeniden deneme
	// olamaz: aile iptal edilir, güncel token da ölür.
	if status, body := send(t, app, refreshRequest(eski)); status != fiber.StatusUnauthorized {
		t.Fatalf("halefi kullanılmış token: status = %d, beklenen 401 (body: %v)", status, body)
	}
	if loadRefreshToken(t, gdb, guncel).RevokedAt == nil {
		t.Error("yeniden kullanımda aile iptal edilmedi")
	}
	if status, _ := send(t, app, refreshRequest(guncel)); status != fiber.StatusUnauthorized {
		t.Errorf("aile iptalinden sonra güncel token: status = %d, beklenen 401", status)
	}
}

// Yayından önce rastgele üretilmiş (eskisinden türetilmemiş) halef: tolerans
// içindeki deneme eski davranışla yeni bir çift alır, sonraki denemeler o
// çifti aynen alır.
func TestRefresh_ToleransIcindeEskiBicimHalefYeniCiftVerir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := refreshApp(New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil))
	once, halef := testRotatedToken(t, gdb, u.ID, 5*time.Second)

	status, body := send(t, app, refreshRequest(once))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, beklenen 200 (body: %v)", status, body)
	}
	yeni, _ := body["refreshToken"].(string)
	if yeni == "" || yeni == halef {
		t.Fatalf("refreshToken = %q, beklenen yeni bir token", yeni)
	}
	if loadRefreshToken(t, gdb, halef).RevokedAt == nil {
		t.Error("kullanılmamış eski halef iptal edilmedi")
	}
	status, body = send(t, app, refreshRequest(once))
	if got, _ := body["refreshToken"].(string); status != fiber.StatusOK || got != yeni {
		t.Errorf("ikinci deneme: status = %d token = %q, beklenen 200 %q", status, got, yeni)
	}
	if n := activeRefreshTokens(t, gdb, u.ID); n != 1 {
		t.Errorf("etkin token sayısı = %d, beklenen 1", n)
	}
}

// testRotatedToken, toleransı geçmiş bir rotasyonu kurar: önce=iptal edilmiş
// (revokedAgo önce), halef=etkin; ikisi de aynı ailededir. İkisinin token'ını döner.
func testRotatedToken(t *testing.T, gdb *gorm.DB, userID string, revokedAgo time.Duration) (string, string) {
	t.Helper()

	family, err := auth.GenerateFamilyID()
	if err != nil {
		t.Fatalf("aile üretilemedi: %v", err)
	}
	halef := testRefreshToken(t, gdb, userID, time.Now().Add(time.Hour), nil)
	halefSatir := loadRefreshToken(t, gdb, halef)
	revokedAt := time.Now().Add(-revokedAgo)
	once := testRefreshToken(t, gdb, userID, time.Now().Add(time.Hour), &revokedAt)
	if err := gdb.Model(&models.RefreshToken{}).
		Where("token_hash IN ?", []string{auth.HashToken(halef), auth.HashToken(once)}).
		Update("family_id", family).Error; err != nil {
		t.Fatalf("aile yazılamadı: %v", err)
	}
	if err := gdb.Model(&models.RefreshToken{}).Where("token_hash = ?", auth.HashToken(once)).
		Update("replaced_by_id", halefSatir.ID).Error; err != nil {
		t.Fatalf("halef yazılamadı: %v", err)
	}
	return once, halef
}

func TestRefresh_ToleransDisindaYenidenKullanimAileyiIptalEder(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := refreshApp(New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil))

	once, halef := testRotatedToken(t, gdb, u.ID, 2*refreshReuseGrace)
	// Aynı kullanıcının başka bir cihazdaki oturumu ayrı bir ailedir.
	digerCihaz := testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), nil)

	status, body := send(t, app, refreshRequest(once))
	if status != fiber.StatusUnauthorized || body["code"] != "invalid_refresh_token" {
		t.Fatalf("status = %d %v, beklenen 401 invalid_refresh_token", status, body["code"])
	}
	if loadRefreshToken(t, gdb, halef).RevokedAt == nil {
		t.Error("tolerans dışı yeniden kullanımda ailenin etkin token'ı iptal edilmedi")
	}
	if loadRefreshToken(t, gdb, digerCihaz).RevokedAt != nil {
		t.Error("başka bir oturumun token'ı iptal edildi")
	}
	if status, _ := send(t, app, refreshRequest(halef)); status != fiber.StatusUnauthorized {
		t.Errorf("iptal edilen halef: status = %d, beklenen 401", status)
	}
}

func TestRefresh_ToleransIcindeHalefKullanilmissaAileIptalEdilir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := refreshApp(New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil))

	once, halef := testRotatedToken(t, gdb, u.ID, 5*time.Second)
	// Halef kullanıldı (rotasyonla iptal edildi), yerine geçen etkin.
	status, body := send(t, app, refreshRequest(halef))
	if status != fiber.StatusOK {
		t.Fatalf("halef: status = %d (body: %v)", status, body)
	}
	guncel, _ := body["refreshToken"].(string)

	if status, _ := send(t, app, refreshRequest(once)); status != fiber.StatusUnauthorized {
		t.Fatalf("status = %d, beklenen 401", status)
	}
	if n := activeRefreshTokens(t, gdb, u.ID); n != 0 {
		t.Errorf("etkin token sayısı = %d, beklenen 0", n)
	}
	if loadRefreshToken(t, gdb, guncel).RevokedAt == nil {
		t.Error("güncel token iptal edilmedi")
	}
}

func TestRefresh_YeniTokenYazilamazsaEskisiGecerliKalir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := refreshApp(New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil))
	token := testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), nil)

	// Yeni satırın yazımını düşür: iptal aynı transaction'da olduğundan geri alınmalı.
	const hook = "test:refresh_token_yazilamaz"
	err := gdb.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "refresh_tokens" {
			tx.AddError(errors.New("refresh token yazılamadı"))
		}
	})
	if err != nil {
		t.Fatalf("hook kaydedilemedi: %v", err)
	}

	status, body := send(t, app, refreshRequest(token))
	if status != fiber.StatusInternalServerError {
		t.Fatalf("status = %d, beklenen 500 (body: %v)", status, body)
	}
	if loadRefreshToken(t, gdb, token).RevokedAt != nil {
		t.Fatal("yeni token yazılamadığı halde eski token iptal edildi: oturum sunucuda yok oldu")
	}

	// İstemci 500'ü geçici sayıp aynı token'la yeniden dener; deneme başarılı olmalı.
	if err := gdb.Callback().Create().Remove(hook); err != nil {
		t.Fatalf("hook kaldırılamadı: %v", err)
	}
	if status, body := send(t, app, refreshRequest(token)); status != fiber.StatusOK {
		t.Errorf("yeniden deneme: status = %d, beklenen 200 (body: %v)", status, body)
	}
	if n := activeRefreshTokens(t, gdb, u.ID); n != 1 {
		t.Errorf("etkin token sayısı = %d, beklenen 1", n)
	}
}

func TestRefresh_EszamanliAyniTokenTekOturumBirakir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := refreshApp(New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil))
	token := testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), nil)

	// Token satırı kilitliyken bütün istekler onu "geçerli" okur ve kilit için
	// sıraya girer. İlki rotasyon yapar; sonrakiler tolerans içinde yeniden
	// deneme sayılır ve aynı halefi alır. Sonuçta hiçbiri çıkışa düşmez ve
	// oturumda tek etkin token kalır.
	const n = 10
	statuses := satirKilidiAltindaEszamanli(t, gdb, app,
		"SELECT 1 FROM refresh_tokens WHERE token_hash = ? FOR UPDATE", auth.HashToken(token),
		n, func(int) *http.Request { return refreshRequest(token) })

	sayim := map[int]int{}
	for _, status := range statuses {
		sayim[status]++
	}
	if sayim[fiber.StatusOK] != n {
		t.Errorf("status dağılımı = %v, beklenen %d adet 200", sayim, n)
	}
	if got := activeRefreshTokens(t, gdb, u.ID); got != 1 {
		t.Errorf("etkin token sayısı = %d, beklenen 1", got)
	}
	// Etkin token, hepsinin aldığı (eskisinden türetilmiş) haleftir.
	if halef := loadRefreshToken(t, gdb, testTokens().RotatedRefreshToken(token)); halef.RevokedAt != nil {
		t.Error("ortak halef iptal edilmiş")
	}
}

// --- Logout ---

func logoutApp(h *Handlers) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	app.Post("/refresh", h.Refresh)
	app.Post("/logout", h.Logout)
	return app
}

func TestLogout_TokenVeAilesiOlur(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := logoutApp(New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil))

	// Tolerans içindeki eski token da çıkıştan sonra yeni çift alamamalı.
	once, halef := testRotatedToken(t, gdb, u.ID, 5*time.Second)
	digerCihaz := testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), nil)

	if status, body := postJSON(t, app, "/logout", fiber.Map{"refreshToken": halef}); status != fiber.StatusNoContent {
		t.Fatalf("logout: status = %d, beklenen 204 (body: %v)", status, body)
	}
	for name, token := range map[string]string{"cikis yapilan": halef, "toleranstaki eski": once} {
		if status, _ := send(t, app, refreshRequest(token)); status != fiber.StatusUnauthorized {
			t.Errorf("%s token: status = %d, beklenen 401", name, status)
		}
	}
	if loadRefreshToken(t, gdb, digerCihaz).RevokedAt != nil {
		t.Error("başka bir oturumun token'ı iptal edildi")
	}
}

func TestLogout_BilinmeyenYaDaEksikToken204Doner(t *testing.T) {
	gdb := testDB(t)
	app := logoutApp(New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil))

	for name, payload := range map[string]any{
		"bilinmeyen": fiber.Map{"refreshToken": "hic-verilmemis"},
		"bos":        fiber.Map{},
	} {
		if status, body := postJSON(t, app, "/logout", payload); status != fiber.StatusNoContent {
			t.Errorf("%s: status = %d, beklenen 204 (body: %v)", name, status, body)
		}
	}
	if status, _ := post(t, app, "/logout"); status != fiber.StatusNoContent {
		t.Errorf("gövdesiz: status = %d, beklenen 204", status)
	}
}

func TestLogout_VeritabaniHatasi500Doner(t *testing.T) {
	kopuk := kopukDB(t)
	app := logoutApp(New(kopuk, &config.Config{AppEnv: "test"}, testTokens(), nil))

	status, body := postJSON(t, app, "/logout", fiber.Map{"refreshToken": "herhangi"})
	if status != fiber.StatusInternalServerError {
		t.Errorf("status = %d, beklenen 500 (body: %v)", status, body)
	}
}

// kopukDB, havuzu kapatılmış bir bağlantı döner: her sorgu hata verir.
func kopukDB(t *testing.T) *gorm.DB {
	t.Helper()

	testDB(t) // TEST_DATABASE_URL yoksa atla
	kopuk, err := db.Connect(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("ikinci bağlantı açılamadı: %v", err)
	}
	sqlDB, err := kopuk.DB()
	if err != nil {
		t.Fatalf("havuz alınamadı: %v", err)
	}
	sqlDB.Close()
	return kopuk
}

// --- Refresh token temizliği ---

func TestDeleteStaleRefreshTokens_YalnizcaEskiSatirlariSiler(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)

	const saklama = 7 * 24 * time.Hour
	eski := time.Now().Add(-saklama - time.Hour)
	yakin := time.Now().Add(-time.Minute)
	silinecek := []string{
		testRefreshToken(t, gdb, u.ID, eski, nil),                        // çoktan dolmuş
		testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), &eski), // çoktan iptal
	}
	kalacak := []string{
		testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), nil),    // etkin
		testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), &yakin), // yeni iptal (tolerans/tespit için)
		testRefreshToken(t, gdb, u.ID, yakin, nil),                        // yeni dolmuş
	}

	n, err := models.DeleteStaleRefreshTokens(t.Context(), gdb, saklama)
	if err != nil {
		t.Fatalf("temizlik: %v", err)
	}
	if n < int64(len(silinecek)) {
		t.Errorf("silinen = %d, en az %d beklenir", n, len(silinecek))
	}
	for _, token := range silinecek {
		var c int64
		gdb.Model(&models.RefreshToken{}).Where("token_hash = ?", auth.HashToken(token)).Count(&c)
		if c != 0 {
			t.Errorf("eski satır silinmedi")
		}
	}
	for _, token := range kalacak {
		loadRefreshToken(t, gdb, token)
	}
}

func TestDeleteStaleRefreshTokens_BaskaReplikaKilidiTutarkenAtlar(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	eski := time.Now().Add(-30 * 24 * time.Hour)
	token := testRefreshToken(t, gdb, u.ID, eski, nil)

	kilit := gdb.Begin()
	defer kilit.Rollback()
	var alindi bool
	if err := kilit.Raw("SELECT pg_try_advisory_xact_lock(?)", int64(0x6b656c696d6572)).Scan(&alindi).Error; err != nil || !alindi {
		t.Fatalf("advisory lock alınamadı: %v", err)
	}

	n, err := models.DeleteStaleRefreshTokens(t.Context(), gdb, time.Hour)
	if err != nil || n != 0 {
		t.Errorf("kilit tutulurken = (%d, %v), beklenen (0, nil)", n, err)
	}
	loadRefreshToken(t, gdb, token)
}

// --- Hesap silme ve Apple token iptali ---

// fakeApple, Apple'ın token (code → refresh token) ve revoke uçlarını taklit
// eder; gelen istekleri kaydeder.
type fakeApple struct {
	mu        sync.Mutex
	exchanged []string        // token'a çevrilen authorization code'lar
	revoked   []string        // iptal edilen refresh token'lar
	failCodes map[string]bool // token'a çevrilemeyecek code'lar
	failToken map[string]bool // iptali başarısız olacak token'lar
}

func (f *fakeApple) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/token", func(w http.ResponseWriter, r *http.Request) {
		code := r.FormValue("code")
		f.mu.Lock()
		f.exchanged = append(f.exchanged, code)
		fail := f.failCodes[code]
		f.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		w.Write([]byte(`{"refresh_token":"rt-` + code + `"}`))
	})
	mux.HandleFunc("/auth/revoke", func(w http.ResponseWriter, r *http.Request) {
		token := r.FormValue("token")
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failToken[token] {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.revoked = append(f.revoked, token)
	})
	return mux
}

func (f *fakeApple) snapshot() (exchanged, revoked []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.exchanged...), append([]string(nil), f.revoked...)
}

// testAppleRevoker, istekleri sahte Apple sunucusuna yönlendiren bir revoker kurar.
func testAppleRevoker(t *testing.T) (*auth.AppleRevoker, *fakeApple) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("anahtar üretilemedi: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("anahtar kodlanamadı: %v", err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	r, err := auth.NewAppleRevoker("com.example.app", "TEAM123456", "KEY1234567", keyPEM)
	if err != nil || r == nil {
		t.Fatalf("revoker kurulamadı: %v", err)
	}

	fake := &fakeApple{failCodes: map[string]bool{}, failToken: map[string]bool{}}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	return r.WithEndpoints(srv.URL+"/auth/token", srv.URL+"/auth/revoke"), fake
}

// authApp, gerçek rota tablosunu (auth middleware dahil) kurar.
func authApp(h *Handlers) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	RegisterRoutes(app.Group("/api/v1"), h, h.Tokens, sinirsiz)
	return app
}

// authRequest, kullanıcının access token'ıyla bir istek hazırlar; payload nil
// değilse JSON gövde eklenir.
func authRequest(t *testing.T, h *Handlers, u *models.User, method, path string, payload any) *http.Request {
	t.Helper()

	var body io.Reader
	if payload != nil {
		raw, _ := json.Marshal(payload)
		body = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, body)
	if payload != nil {
		req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	}
	access, err := h.Tokens.GenerateAccessToken(u.ID, u.Email, u.TokenVersion)
	if err != nil {
		t.Fatalf("access token üretilemedi: %v", err)
	}
	req.Header.Set(fiber.HeaderAuthorization, "Bearer "+access)
	return req
}

// testAppleIdentity, kullanıcıya Apple kimliği ekler; stored boş değilse
// saklı Apple refresh token'ı olarak yazılır.
func testAppleIdentity(t *testing.T, gdb *gorm.DB, u *models.User, stored string) {
	t.Helper()

	testIdentity(t, gdb, u, "apple")
	if stored == "" {
		return
	}
	if err := gdb.Model(&models.AuthIdentity{}).
		Where("user_id = ? AND provider = ?", u.ID, "apple").
		Update("apple_refresh_token", stored).Error; err != nil {
		t.Fatalf("saklı Apple token yazılamadı: %v", err)
	}
}

func userExists(t *testing.T, gdb *gorm.DB, userID string) bool {
	t.Helper()

	var n int64
	if err := gdb.Model(&models.User{}).Where("id = ?", userID).Count(&n).Error; err != nil {
		t.Fatalf("kullanıcı sayılamadı: %v", err)
	}
	return n > 0
}

func TestDeleteAccount_TumVeriSilinirOturumOlur(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	testIdentity(t, gdb, u, "google")
	testWord(t, gdb, u.ID, "w-silme-"+u.ID, 2)
	if err := gdb.Create(&models.DailyLogin{UserID: u.ID, LoginDate: time.Now()}).Error; err != nil {
		t.Fatalf("giriş kaydı yazılamadı: %v", err)
	}
	if err := gdb.Create(&models.EarnedBadge{UserID: u.ID, BadgeID: "first_day", EarnedAt: time.Now()}).Error; err != nil {
		t.Fatalf("rozet yazılamadı: %v", err)
	}
	refresh := testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), nil)

	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)
	app := authApp(h)

	if status, body := send(t, app, authRequest(t, h, u, http.MethodDelete, "/api/v1/auth/account", nil)); status != fiber.StatusNoContent {
		t.Fatalf("silme: status = %d, beklenen 204 (body: %v)", status, body)
	}

	if userExists(t, gdb, u.ID) {
		t.Error("kullanıcı silinmedi")
	}
	for _, m := range []any{&models.AuthIdentity{}, &models.RefreshToken{}, &models.WordProgress{},
		&models.DailyLogin{}, &models.EarnedBadge{}} {
		var n int64
		if err := gdb.Model(m).Where("user_id = ?", u.ID).Count(&n).Error; err != nil {
			t.Fatalf("%T sayılamadı: %v", m, err)
		}
		if n != 0 {
			t.Errorf("%T: %d satır kaldı, beklenen 0", m, n)
		}
	}

	refreshStatus, _ := send(t, app, jsonRequest("/api/v1/auth/refresh", fiber.Map{"refreshToken": refresh}))
	if refreshStatus != fiber.StatusUnauthorized {
		t.Errorf("silinen hesabın refresh token'ı: status = %d, beklenen 401", refreshStatus)
	}
	meReq := authRequest(t, h, u, http.MethodGet, "/api/v1/auth/me", nil)
	if status, _ := send(t, app, meReq); status != fiber.StatusNotFound {
		t.Errorf("silinen hesabın access token'ıyla /auth/me: status = %d, beklenen 404", status)
	}
}

func TestDeleteAccount_AppleIptali(t *testing.T) {
	cases := []struct {
		name          string
		stored        string // saklı Apple refresh token ("" = yok)
		linked        bool   // Apple kimliği var mı
		body          any    // nil = gövdesiz (yayındaki istemci)
		failCode      bool
		failStored    bool
		wantExchanged []string
		wantRevoked   []string
	}{
		{name: "saklı token gövdesiz iptal edilir", stored: "sakli", linked: true,
			wantRevoked: []string{"sakli"}},
		{name: "saklı token varken kod kullanılmaz", stored: "sakli", linked: true,
			body: fiber.Map{"appleAuthorizationCode": "kod"}, wantRevoked: []string{"sakli"}},
		{name: "saklı token yoksa kod token'a çevrilip iptal edilir", linked: true,
			body: fiber.Map{"appleAuthorizationCode": "kod"}, wantExchanged: []string{"kod"}, wantRevoked: []string{"rt-kod"}},
		{name: "saklı token iptali başarısızsa kod denenir", stored: "sakli", linked: true, failStored: true,
			body: fiber.Map{"appleAuthorizationCode": "kod"}, wantExchanged: []string{"kod"}, wantRevoked: []string{"rt-kod"}},
		{name: "kod da çevrilemezse silme yine sürer", stored: "sakli", linked: true, failStored: true, failCode: true,
			body: fiber.Map{"appleAuthorizationCode": "kod"}, wantExchanged: []string{"kod"}},
		{name: "saklı token ve kod yoksa silme yine sürer", linked: true},
		{name: "Apple bağlı değilse Apple'a gidilmez"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gdb := testDB(t)
			u := testUser(t, gdb)
			testIdentity(t, gdb, u, "google")
			if c.linked {
				testAppleIdentity(t, gdb, u, c.stored)
			}
			revoker, fake := testAppleRevoker(t)
			fake.failCodes["kod"] = c.failCode
			fake.failToken["sakli"] = c.failStored
			h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil).WithAppleRevoker(revoker)

			req := authRequest(t, h, u, http.MethodDelete, "/api/v1/auth/account", c.body)
			if status, body := send(t, authApp(h), req); status != fiber.StatusNoContent {
				t.Fatalf("status = %d, beklenen 204 (body: %v)", status, body)
			}
			if userExists(t, gdb, u.ID) {
				t.Error("Apple iptalinin sonucundan bağımsız olarak hesap silinmeliydi")
			}
			exchanged, revoked := fake.snapshot()
			if !slices.Equal(exchanged, c.wantExchanged) {
				t.Errorf("token'a çevrilen kodlar = %v, beklenen %v", exchanged, c.wantExchanged)
			}
			if !slices.Equal(revoked, c.wantRevoked) {
				t.Errorf("iptal edilen token'lar = %v, beklenen %v", revoked, c.wantRevoked)
			}
		})
	}
}

func TestDeleteAccount_RevokerYoksaHesapYineSilinir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	testAppleIdentity(t, gdb, u, "sakli")
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)

	req := authRequest(t, h, u, http.MethodDelete, "/api/v1/auth/account", fiber.Map{"appleAuthorizationCode": "kod"})
	if status, body := send(t, authApp(h), req); status != fiber.StatusNoContent {
		t.Fatalf("status = %d, beklenen 204 (body: %v)", status, body)
	}
	if userExists(t, gdb, u.ID) {
		t.Error("hesap silinmedi")
	}
}

func TestUnlinkApple_SakliTokenIptalEdilir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	testIdentity(t, gdb, u, "google")
	testAppleIdentity(t, gdb, u, "sakli")
	revoker, fake := testAppleRevoker(t)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil).WithAppleRevoker(revoker)

	status, body := send(t, authApp(h), authRequest(t, h, u, http.MethodDelete, "/api/v1/auth/link/apple", nil))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, beklenen 200 (body: %v)", status, body)
	}
	if n := identityCount(t, gdb, u.ID, "apple"); n != 0 {
		t.Errorf("apple kimliği kaldırılmadı (%d satır)", n)
	}
	if _, revoked := fake.snapshot(); !slices.Equal(revoked, []string{"sakli"}) {
		t.Errorf("iptal edilen token'lar = %v, beklenen [sakli]", revoked)
	}
}

func TestWarnIfAppleRevokeDisabled(t *testing.T) {
	revoker, _ := testAppleRevoker(t)
	cases := []struct {
		name     string
		env      string
		clientID string
		revoker  *auth.AppleRevoker
		want     bool
	}{
		{"production, Apple açık, revoker yok", "production", "com.example.app", nil, true},
		{"production, revoker var", "production", "com.example.app", revoker, false},
		{"production, Apple girişi kapalı", "production", "", nil, false},
		{"geliştirme", "development", "com.example.app", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := New(nil, &config.Config{AppEnv: c.env, AppleClientID: c.clientID}, testTokens(), nil).
				WithAppleRevoker(c.revoker)
			if got := h.WarnIfAppleRevokeDisabled(); got != c.want {
				t.Errorf("WarnIfAppleRevokeDisabled = %v, beklenen %v", got, c.want)
			}
		})
	}
}

// --- Bağlantı kaldırma: son giriş yöntemi koruması ---

func unlinkRequest(t *testing.T, h *Handlers, u *models.User, provider string) *http.Request {
	return authRequest(t, h, u, http.MethodDelete, "/api/v1/auth/link/"+provider, nil)
}

func TestUnlinkProvider_SonGirisYontemiKorunur(t *testing.T) {
	cases := []struct {
		name       string
		env        string
		identities []string
		unlink     string
		wantStatus int
		wantCode   string
		wantLeft   []string
	}{
		{"tek kimlik kaldırılamaz", "test", []string{"google"}, "google",
			fiber.StatusBadRequest, "last_identity", []string{"google"}},
		{"iki sosyal kimlikten biri kaldırılır", "test", []string{"apple", "google"}, "google",
			fiber.StatusOK, "", []string{"apple"}},
		// E-posta/parola girişi her ortamda açık: parola kimliği bir giriş yoludur.
		{"production'da parola kimliği sayılır", "production", []string{"google", "password"}, "google",
			fiber.StatusOK, "", []string{"password"}},
		{"geliştirmede parola kimliği sayılır", "development", []string{"google", "password"}, "google",
			fiber.StatusOK, "", []string{"password"}},
		{"production'da tek kimlik parola ise sosyal kimlik kaldırılır", "production", []string{"apple", "password"}, "apple",
			fiber.StatusOK, "", []string{"password"}},
		{"bağlı olmayan sağlayıcı değişiklik yapmadan 200", "test", []string{"google"}, "apple",
			fiber.StatusOK, "", []string{"google"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gdb := testDB(t)
			u := testUser(t, gdb)
			for _, p := range c.identities {
				testIdentity(t, gdb, u, p)
			}
			h := New(gdb, &config.Config{AppEnv: c.env}, testTokens(), nil)

			status, body := send(t, authApp(h), unlinkRequest(t, h, u, c.unlink))
			if status != c.wantStatus {
				t.Fatalf("status = %d, beklenen %d (body: %v)", status, c.wantStatus, body)
			}
			if c.wantCode != "" && body["code"] != c.wantCode {
				t.Errorf("code = %v, beklenen %s", body["code"], c.wantCode)
			}
			if status == fiber.StatusOK {
				user, _ := body["user"].(map[string]any)
				got := []string{}
				for _, p := range user["linkedProviders"].([]any) {
					got = append(got, p.(string))
				}
				if !slices.Equal(got, c.wantLeft) {
					t.Errorf("yanıttaki linkedProviders = %v, beklenen %v", got, c.wantLeft)
				}
			}
			left, err := h.loadLinkedProviders(u.ID)
			if err != nil {
				t.Fatalf("kimlikler okunamadı: %v", err)
			}
			if !slices.Equal(left, c.wantLeft) {
				t.Errorf("kalan kimlikler = %v, beklenen %v", left, c.wantLeft)
			}
		})
	}
}

// Aynı anda gelen iki bağlantı kaldırma isteğinin ikisi de "iki kimlik var"
// görürse hesap girişsiz kalır. Kimlik satırları kilitliyken iki istek de
// sıraya girer; yalnızca biri geçmeli.
func TestUnlinkProvider_EszamanliIstekHesabiGirissizBirakmaz(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	testIdentity(t, gdb, u, "google")
	testIdentity(t, gdb, u, "apple")
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)
	app := authApp(h)

	providers := []string{"google", "apple"}
	statuses := satirKilidiAltindaEszamanli(t, gdb, app,
		"SELECT 1 FROM auth_identities WHERE user_id = ? FOR UPDATE", u.ID,
		len(providers), func(i int) *http.Request { return unlinkRequest(t, h, u, providers[i]) })

	sayim := map[int]int{}
	for _, status := range statuses {
		sayim[status]++
	}
	if sayim[fiber.StatusOK] != 1 || sayim[fiber.StatusBadRequest] != 1 {
		t.Errorf("status dağılımı = %v, beklenen bir 200 ve bir 400", sayim)
	}
	left, err := h.loadLinkedProviders(u.ID)
	if err != nil {
		t.Fatalf("kimlikler okunamadı: %v", err)
	}
	if len(left) != 1 {
		t.Errorf("kalan kimlikler = %v, beklenen tam bir kimlik", left)
	}
}

func TestUnlinkProvider_VeritabaniHatasi500Doner(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(kopukDB(t), &config.Config{AppEnv: "test"}, testTokens(), nil)

	status, body := send(t, authApp(h), unlinkRequest(t, h, u, "google"))
	if status != fiber.StatusInternalServerError {
		t.Errorf("status = %d, beklenen 500 (body: %v)", status, body)
	}
}

// --- Eşzamanlı ilk sosyal giriş ve bağlama ---

// İlk sosyal giriş aynı anda iki kez gelirse ikinci istek unique kısıta çarpar.
// Test, öteki isteği commit edilmemiş bir transaction olarak taklit eder: istek
// kullanıcıyı yazarken onun commit'ini bekler, sonra kısıt ihlali alır.
func TestAuthenticateSocial_EszamanliIlkGirisAyniKullaniciyiDoner(t *testing.T) {
	gdb := testDB(t)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)

	email := benzersizEposta("cift")
	t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", email) })
	si := &auth.SocialIdentity{Subject: "cift-" + email, Email: email, Name: "Çift"}

	oteki := gdb.Begin()
	defer oteki.Rollback()
	ilk := models.User{Email: email, DisplayName: "Çift", CurrentLevel: "A1", CurrentGroup: 1}
	if err := oteki.Create(&ilk).Error; err != nil {
		t.Fatalf("öteki istek kullanıcıyı yazamadı: %v", err)
	}
	sub := si.Subject
	if err := oteki.Create(&models.AuthIdentity{UserID: ilk.ID, Provider: "google",
		ProviderSubject: &sub, Email: &email}).Error; err != nil {
		t.Fatalf("öteki istek kimliği yazamadı: %v", err)
	}

	type sonuc struct {
		user *models.User
		err  error
	}
	done := make(chan sonuc, 1)
	go func() {
		u, err := h.authenticateSocial("google", si, "")
		done <- sonuc{u, err}
	}()

	var bekleyen int64
	for son := time.Now().Add(10 * time.Second); bekleyen < 1 && time.Now().Before(son); {
		time.Sleep(10 * time.Millisecond)
		gdb.Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&bekleyen)
	}
	if bekleyen < 1 {
		t.Error("istek öteki transaction'ı beklemedi; yarış sıralaması kurulamadı")
	}
	if err := oteki.Commit().Error; err != nil {
		t.Fatalf("öteki istek commit edilemedi: %v", err)
	}

	got := <-done
	if got.err != nil {
		t.Fatalf("eşzamanlı ikinci giriş hata verdi: %v", got.err)
	}
	if got.user.ID != ilk.ID {
		t.Errorf("kullanıcı = %s, beklenen öteki isteğin açtığı %s", got.user.ID, ilk.ID)
	}
}

// Çift dokunuşun öteki sıralaması: öteki istek, bu isteğin subject araması
// (transaction dışında, öteki commit etmeden önce: kimlik yok) ile e-postayla
// arama (commit'ten sonra: öteki isteğin açtığı hesap) arasında commit eder.
// Bu istek unique kısıta çarpmaz; e-postayla bulduğu hesapta kendi kimliğini
// (aynı sağlayıcı, aynı subject) görür. Bu "aynı sağlayıcının başka bir
// kimliği" değildir: 409 değil, aynı hesap dönmelidir.
//
// Sıralama, subject aramasından hemen sonra çalışan bir GORM geri çağrısıyla
// kurulur (gdb bu teste özel bir havuzdur, geri çağrı başka testi etkilemez).
func TestAuthenticateSocial_SubjectAramasindanSonraCommitEdilenIlkGirisAyniKullaniciyiDoner(t *testing.T) {
	for _, c := range []struct {
		ad    string
		email func() string // sağlayıcının verdiği adres
	}{
		{"doğrulanmış adres", func() string { return benzersizEposta("cift-sira") }},
		// Adres yoksa iki istek de aynı yer tutucu adresle (subject'ten) hesap açar.
		{"adres yok", func() string { return "" }},
	} {
		t.Run(c.ad, func(t *testing.T) {
			gdb := testDB(t)
			h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)

			email := c.email()
			si := &auth.SocialIdentity{Subject: "cift-sira-" + benzersizEposta("s"), Email: email}
			kayitli := email
			if kayitli == "" {
				kayitli = dto.SyntheticEmail("apple_" + si.Subject)
			}
			t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", kayitli) })

			oteki := gdb.Begin()
			defer oteki.Rollback()
			ilk := models.User{Email: kayitli, DisplayName: "Çift", CurrentLevel: "A1", CurrentGroup: 1}
			if err := oteki.Create(&ilk).Error; err != nil {
				t.Fatalf("öteki istek kullanıcıyı yazamadı: %v", err)
			}
			sub := si.Subject
			if err := oteki.Create(&models.AuthIdentity{UserID: ilk.ID, Provider: "apple",
				ProviderSubject: &sub, Email: &kayitli, EmailVerified: email != ""}).Error; err != nil {
				t.Fatalf("öteki istek kimliği yazamadı: %v", err)
			}

			var once sync.Once
			commitErr := errors.New("öteki istek commit edilmedi")
			if err := gdb.Callback().Query().After("gorm:query").Register("test:oteki_commit", func(tx *gorm.DB) {
				if tx.Statement.Table == "auth_identities" {
					once.Do(func() { commitErr = oteki.Commit().Error })
				}
			}); err != nil {
				t.Fatalf("geri çağrı kaydedilemedi: %v", err)
			}

			got, err := h.authenticateSocial("apple", si, "")
			if commitErr != nil {
				t.Fatalf("sıralama kurulamadı: %v", commitErr)
			}
			if err != nil {
				t.Fatalf("subject aramasından sonra commit edilen ilk giriş hata verdi: %v", err)
			}
			if got.ID != ilk.ID {
				t.Errorf("kullanıcı = %s, beklenen öteki isteğin açtığı %s", got.ID, ilk.ID)
			}
			var n int64
			gdb.Model(&models.AuthIdentity{}).Where("provider = 'apple' AND provider_subject = ?", sub).Count(&n)
			if n != 1 {
				t.Errorf("apple kimliği = %d satır, beklenen 1", n)
			}
		})
	}
}

func linkApp(h *Handlers, userID, provider string, si *auth.SocialIdentity) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	app.Post("/link", func(c *fiber.Ctx) error { return h.linkProvider(c, userID, provider, si) })
	return app
}

func TestLinkProvider(t *testing.T) {
	gdb := testDB(t)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)

	t.Run("başka kullanıcıya bağlı kimlik 409", func(t *testing.T) {
		sahip, u := testUser(t, gdb), testUser(t, gdb)
		testIdentity(t, gdb, sahip, "google")
		si := &auth.SocialIdentity{Subject: "google-" + sahip.ID, Email: sahip.Email}
		status, body := post(t, linkApp(h, u.ID, "google", si), "/link")
		if status != fiber.StatusConflict || body["code"] != "provider_already_linked" {
			t.Errorf("status = %d %v, beklenen 409 provider_already_linked", status, body["code"])
		}
		if n := identityCount(t, gdb, u.ID, "google"); n != 0 {
			t.Errorf("kimlik bağlandı (%d satır)", n)
		}
	})
	t.Run("hesapta aynı sağlayıcının başka kimliği varsa 409", func(t *testing.T) {
		u := testUser(t, gdb)
		testIdentity(t, gdb, u, "google")
		si := &auth.SocialIdentity{Subject: "baska-" + u.ID, Email: u.Email}
		status, body := post(t, linkApp(h, u.ID, "google", si), "/link")
		if status != fiber.StatusConflict || body["code"] != "provider_already_linked" {
			t.Errorf("status = %d %v, beklenen 409 provider_already_linked", status, body["code"])
		}
	})
	t.Run("yeni kimlik bağlanır, tekrar istek 200", func(t *testing.T) {
		u := testUser(t, gdb)
		testIdentity(t, gdb, u, "google")
		si := &auth.SocialIdentity{Subject: "apple-yeni-" + u.ID, Email: u.Email}
		for i := 0; i < 2; i++ {
			if status, body := post(t, linkApp(h, u.ID, "apple", si), "/link"); status != fiber.StatusOK {
				t.Fatalf("%d. istek: status = %d, beklenen 200 (body: %v)", i+1, status, body)
			}
		}
		if n := identityCount(t, gdb, u.ID, "apple"); n != 1 {
			t.Errorf("apple kimliği sayısı = %d, beklenen 1", n)
		}
	})
}

// --- Sosyal kayıtta görünen ad ---

func TestAuthenticateSocial_GorunenAdTemizlenirVeKisaltilir(t *testing.T) {
	gdb := testDB(t)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)

	cases := []struct {
		name, clientName, providerName, want string
	}{
		{"istemci adı uzunsa kesilir", strings.Repeat("ç", 500), "", strings.Repeat("ç", 60)},
		{"kontrol ve yön karakterleri atılır", "Ada\n\u202ELovelace", "", "Ada Lovelace"},
		{"istemci adı görünmezse sağlayıcı adı kullanılır", "\u200B ", "Sağlayıcı Adı", "Sağlayıcı Adı"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			email := benzersizEposta("ad")
			t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", email) })

			si := &auth.SocialIdentity{Subject: "ad-" + email, Email: email, Name: c.providerName}
			got, err := h.authenticateSocial("google", si, c.clientName)
			if err != nil {
				t.Fatalf("authenticateSocial: %v", err)
			}
			if got.DisplayName != c.want {
				t.Errorf("displayName = %q, beklenen %q", got.DisplayName, c.want)
			}
			if stored := loadUser(t, gdb, got.ID).DisplayName; stored != c.want {
				t.Errorf("saklanan displayName = %q, beklenen %q", stored, c.want)
			}
		})
	}
}

// Apple iptali başarısız olsa da (ya da saklı token yoksa) bağlantı kaldırılır:
// kullanıcının isteği üçüncü taraf bir servise bağlanmaz.
func TestUnlinkApple_IptalBasarisizOlsaDaKaldirilir(t *testing.T) {
	for _, c := range []struct{ name, stored string }{
		{"iptal başarısız", "sakli"},
		{"saklı token yok", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			gdb := testDB(t)
			u := testUser(t, gdb)
			testIdentity(t, gdb, u, "google")
			testAppleIdentity(t, gdb, u, c.stored)
			revoker, fake := testAppleRevoker(t)
			fake.failToken["sakli"] = true
			h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil).WithAppleRevoker(revoker)

			status, body := send(t, authApp(h), unlinkRequest(t, h, u, "apple"))
			if status != fiber.StatusOK {
				t.Fatalf("status = %d, beklenen 200 (body: %v)", status, body)
			}
			if n := identityCount(t, gdb, u.ID, "apple"); n != 0 {
				t.Errorf("apple kimliği kaldırılmadı (%d satır)", n)
			}
		})
	}
}

// Son kimlik reddedildiğinde Apple'a gidilmez: kimlik yerinde kaldığı için
// token'ı iptal etmek, hâlâ bağlı hesabın Apple erişimini koparırdı.
func TestUnlinkApple_SonKimlikseTokenIptalEdilmez(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	testAppleIdentity(t, gdb, u, "sakli")
	revoker, fake := testAppleRevoker(t)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil).WithAppleRevoker(revoker)

	if status, body := send(t, authApp(h), unlinkRequest(t, h, u, "apple")); status != fiber.StatusBadRequest {
		t.Fatalf("status = %d, beklenen 400 (body: %v)", status, body)
	}
	if _, revoked := fake.snapshot(); len(revoked) != 0 {
		t.Errorf("reddedilen kaldırmada token iptal edildi: %v", revoked)
	}
}

// --- QA: eşzamanlılık ve yayındaki istemciyle uyumluluk ---

// QA: aynı kimliği bağlama isteği aynı anda iki kez gelirse (çift dokunuş)
// ikinci istek unique kısıta çarpar. Kimlik aynı kullanıcıya bağlandığı için bu
// bir çakışma değil, tekrar istektir: 200 ve güncel kullanıcı dönmeli.
func TestLinkProvider_EszamanliAyniKimlikIkisi200(t *testing.T) {
	gdb := testDB(t)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)
	u := testUser(t, gdb)
	testIdentity(t, gdb, u, "google")
	si := &auth.SocialIdentity{Subject: "apple-cift-" + u.ID, Email: u.Email}

	// Öteki istek kimliği yazmış ama henüz commit etmemiş.
	oteki := gdb.Begin()
	defer oteki.Rollback()
	sub := si.Subject
	if err := oteki.Create(&models.AuthIdentity{UserID: u.ID, Provider: "apple", ProviderSubject: &sub}).Error; err != nil {
		t.Fatalf("öteki istek kimliği yazamadı: %v", err)
	}

	done := make(chan int, 1)
	go func() {
		res, err := linkApp(h, u.ID, "apple", si).Test(httptest.NewRequest(http.MethodPost, "/link", nil), -1)
		if err != nil {
			done <- 0
			return
		}
		res.Body.Close()
		done <- res.StatusCode
	}()

	var bekleyen int64
	for son := time.Now().Add(10 * time.Second); bekleyen < 1 && time.Now().Before(son); {
		time.Sleep(10 * time.Millisecond)
		gdb.Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&bekleyen)
	}
	if bekleyen < 1 {
		t.Error("istek öteki transaction'ı beklemedi; yarış sıralaması kurulamadı")
	}
	if err := oteki.Commit().Error; err != nil {
		t.Fatalf("öteki istek commit edilemedi: %v", err)
	}

	if status := <-done; status != fiber.StatusOK {
		t.Errorf("eşzamanlı ikinci bağlama: status = %d, beklenen 200", status)
	}
	if n := identityCount(t, gdb, u.ID, "apple"); n != 1 {
		t.Errorf("apple kimliği sayısı = %d, beklenen 1", n)
	}
}

// QA: yayındaki istemci DELETE /auth/account'u gövdesiz gönderir; Dio yine de
// Content-Type: application/json başlığı ekleyebilir. Boş ya da bozuk gövde
// silmeyi engellememeli, saklı Apple token yine iptal edilmeli.
func TestDeleteAccount_BosVeyaBozukGovdeSilmeyiEngellemez(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"json başlıklı boş gövde", ""},
		{"bozuk json", "{bozuk"},
		{"yanlış tip", `{"appleAuthorizationCode": 123}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			gdb := testDB(t)
			u := testUser(t, gdb)
			testIdentity(t, gdb, u, "google")
			testAppleIdentity(t, gdb, u, "sakli")
			revoker, fake := testAppleRevoker(t)
			h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil).WithAppleRevoker(revoker)

			req := authRequest(t, h, u, http.MethodDelete, "/api/v1/auth/account", nil)
			req.Body = http.NoBody
			if c.body != "" {
				req = httptest.NewRequest(http.MethodDelete, "/api/v1/auth/account", strings.NewReader(c.body))
				access, _ := h.Tokens.GenerateAccessToken(u.ID, u.Email, u.TokenVersion)
				req.Header.Set(fiber.HeaderAuthorization, "Bearer "+access)
			}
			req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)

			if status, body := send(t, authApp(h), req); status != fiber.StatusNoContent {
				t.Fatalf("status = %d, beklenen 204 (body: %v)", status, body)
			}
			if userExists(t, gdb, u.ID) {
				t.Error("hesap silinmedi")
			}
			if _, revoked := fake.snapshot(); len(revoked) != 1 || revoked[0] != "sakli" {
				t.Errorf("iptal edilen token'lar = %v, beklenen [sakli]", revoked)
			}
		})
	}
}

// QA: hesap silme aynı anda iki kez gönderilirse (çift dokunuş, yanıtı
// kaybolan yeniden deneme) ikisi de 204 almalı; ikinci istek 500 vermemeli.
func TestDeleteAccount_EszamanliCiftIstekIkisi204(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	testIdentity(t, gdb, u, "google")
	testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), nil)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)
	app := authApp(h)

	// Kullanıcı satırı kilitliyken iki istek de silmeye ulaşıp sırayla bekler.
	statuses := satirKilidiAltindaEszamanli(t, gdb, app,
		"SELECT 1 FROM users WHERE id = ? FOR UPDATE", u.ID, 2,
		func(int) *http.Request {
			return authRequest(t, h, u, http.MethodDelete, "/api/v1/auth/account", nil)
		})
	for _, s := range statuses {
		if s != fiber.StatusNoContent {
			t.Errorf("status dağılımı = %v, beklenen iki adet 204", statuses)
			break
		}
	}
	if userExists(t, gdb, u.ID) {
		t.Error("hesap silinmedi")
	}
}

// QA: aynı refresh token'la tolerans dışında yeniden kullanım eşzamanlı
// gelirse hiçbiri yeni çift almamalı, aile tamamen ölmeli.
func TestRefresh_ToleransDisiEszamanliYenidenKullanimHicbirineCiftVermez(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := refreshApp(New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil))
	once, halef := testRotatedToken(t, gdb, u.ID, 2*refreshReuseGrace)

	const n = 5
	var wg sync.WaitGroup
	statuses := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := app.Test(refreshRequest(once), -1)
			if err != nil {
				return
			}
			res.Body.Close()
			statuses[i] = res.StatusCode
		}(i)
	}
	wg.Wait()
	for _, s := range statuses {
		if s != fiber.StatusUnauthorized {
			t.Fatalf("status dağılımı = %v, beklenen hepsi 401", statuses)
		}
	}
	if loadRefreshToken(t, gdb, halef).RevokedAt == nil {
		t.Error("aile iptal edilmedi")
	}
	if got := activeRefreshTokens(t, gdb, u.ID); got != 0 {
		t.Errorf("etkin token sayısı = %d, beklenen 0", got)
	}
}

// QA: logout ile tolerans içi yeniden deneme aynı anda gelirse logout'tan sonra
// ailede etkin token kalmamalı ya da kalan tek token logout'u izleyen bir
// yeniden denemenin sonucu olmalı; her durumda logout yapılan token ölü kalır.
func TestLogout_SonrasiEskiVeYeniTokenOlu(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := logoutApp(New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil))
	eski := testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), nil)

	status, body := send(t, app, refreshRequest(eski))
	if status != fiber.StatusOK {
		t.Fatalf("yenileme: status = %d (body: %v)", status, body)
	}
	yeni, _ := body["refreshToken"].(string)

	// İstemci eski token'la çıkış yapar (yenileme yanıtı ona ulaşmadan).
	if status, _ := postJSON(t, app, "/logout", fiber.Map{"refreshToken": eski}); status != fiber.StatusNoContent {
		t.Fatalf("logout: status = %d, beklenen 204", status)
	}
	for name, tok := range map[string]string{"eski": eski, "yeni": yeni} {
		if status, _ := send(t, app, refreshRequest(tok)); status != fiber.StatusUnauthorized {
			t.Errorf("%s token logout'tan sonra: status = %d, beklenen 401", name, status)
		}
	}
	if got := activeRefreshTokens(t, gdb, u.ID); got != 0 {
		t.Errorf("etkin token sayısı = %d, beklenen 0", got)
	}
}

// QA: yanıt (bağlı yöntem listesi) hazırlanamazsa rotasyon commit edilmemeli.
// Aksi halde istemci 500 alır, yeni token'ı hiç görmez; yeniden denemesi
// tolerans süresini aşarsa yeniden kullanım sayılıp oturumdan düşerdi.
func TestRefresh_YanitHazirlanamazsaTokenIptalEdilmez(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := refreshApp(New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil))
	token := testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), nil)

	const hook = "test:kimlikler_okunamaz"
	err := gdb.Callback().Query().Before("gorm:query").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "auth_identities" {
			tx.AddError(errors.New("kimlikler okunamadı"))
		}
	})
	if err != nil {
		t.Fatalf("hook kaydedilemedi: %v", err)
	}
	removed := false
	removeHook := func() {
		if !removed {
			removed = true
			_ = gdb.Callback().Query().Remove(hook)
		}
	}
	t.Cleanup(removeHook)

	status, body := send(t, app, refreshRequest(token))
	removeHook()
	if status != fiber.StatusInternalServerError {
		t.Fatalf("status = %d, beklenen 500 (body: %v)", status, body)
	}
	if loadRefreshToken(t, gdb, token).RevokedAt != nil {
		t.Fatal("yanıt hazırlanamadığı halde token rotasyonla iptal edildi")
	}
	if n := activeRefreshTokens(t, gdb, u.ID); n != 1 {
		t.Errorf("etkin token sayısı = %d, beklenen 1", n)
	}
	if status, body := send(t, app, refreshRequest(token)); status != fiber.StatusOK {
		t.Errorf("yeniden deneme: status = %d, beklenen 200 (body: %v)", status, body)
	}
}
