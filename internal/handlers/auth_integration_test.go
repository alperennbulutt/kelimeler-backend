package handlers

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/db"
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

func TestRegisterRoutes_ParolaUclariProductiondaKapali(t *testing.T) {
	app := routesApp(nil, "production", sinirsiz)

	// Gövdesiz istek, uç açıksa veritabanına gitmeden 400 alır; kapalıysa 404.
	for _, path := range []string{"/api/v1/auth/register", "/api/v1/auth/login"} {
		status, body := post(t, app, path)
		if status != fiber.StatusNotFound {
			t.Errorf("POST %s: status = %d, beklenen 404 (body: %v)", path, status, body)
		}
	}

	// İstemcinin kullandığı kimlik uçları açık kalmalı.
	for _, path := range []string{"/api/v1/auth/google", "/api/v1/auth/refresh"} {
		status, body := post(t, app, path)
		if status != fiber.StatusBadRequest {
			t.Errorf("POST %s: status = %d, beklenen 400 (body: %v)", path, status, body)
		}
	}
}

func TestRegisterRoutes_ParolaUclariProductionDisindaAcik(t *testing.T) {
	for _, env := range []string{"development", "test"} {
		t.Run(env, func(t *testing.T) {
			app := routesApp(nil, env, sinirsiz)

			for _, path := range []string{"/api/v1/auth/register", "/api/v1/auth/login"} {
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

func TestRegisterRoutes_GelistirmedeKayitVeGirisCalisir(t *testing.T) {
	gdb := testDB(t)
	app := routesApp(gdb, "development", sinirsiz)

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

func TestAuthenticateSocial_ParolaKimlikliHesabaBaglamaz(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	testIdentity(t, gdb, u, "password")
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)

	// Adres büyük harfle gelse de aynı hesaptır.
	si := &auth.SocialIdentity{Subject: "g-" + u.ID, Email: strings.ToUpper(u.Email)}
	for _, provider := range []string{"google", "apple"} {
		t.Run(provider, func(t *testing.T) {
			got, err := h.authenticateSocial(provider, si, "")

			var apiError *middleware.APIError
			if !errors.As(err, &apiError) {
				t.Fatalf("hata = %v (kullanıcı: %v), beklenen APIError", err, got)
			}
			if apiError.Status != fiber.StatusConflict || apiError.Code != "email_in_use" {
				t.Errorf("hata = %d %q, beklenen 409 \"email_in_use\"", apiError.Status, apiError.Code)
			}
			if n := identityCount(t, gdb, u.ID, provider); n != 0 {
				t.Errorf("%s kimliği parola hesabına bağlandı (%d satır)", provider, n)
			}
		})
	}

	// Reddetmek veri silmez: hesap ve parola kimliği yerinde kalır.
	if n := identityCount(t, gdb, u.ID, "password"); n != 1 {
		t.Errorf("parola kimliği sayısı = %d, beklenen 1", n)
	}
	loadUser(t, gdb, u.ID)
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
	kopuk, err := db.Connect(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("ikinci bağlantı açılamadı: %v", err)
	}
	sqlDB, err := kopuk.DB()
	if err != nil {
		t.Fatalf("havuz alınamadı: %v", err)
	}
	sqlDB.Close()
	app := refreshApp(New(kopuk, &config.Config{AppEnv: "test"}, testTokens(), nil))

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

	if loadRefreshToken(t, gdb, eski).RevokedAt == nil {
		t.Error("eski token iptal edilmedi")
	}
	if n := activeRefreshTokens(t, gdb, u.ID); n != 1 {
		t.Errorf("etkin token sayısı = %d, beklenen 1", n)
	}

	if status, _ := send(t, app, refreshRequest(eski)); status != fiber.StatusUnauthorized {
		t.Errorf("eski token yeniden kullanıldı: status = %d, beklenen 401", status)
	}
	if status, body := send(t, app, refreshRequest(yeni)); status != fiber.StatusOK {
		t.Errorf("yeni token: status = %d, beklenen 200 (body: %v)", status, body)
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

func TestRefresh_EszamanliAyniTokenBirKezYenilenir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := refreshApp(New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil))
	token := testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), nil)

	// Token satırı kilitliyken bütün istekler onu "geçerli" okur ve iptal için
	// sıraya girer: yalnızca ilki satırı değiştirebilmeli.
	const n = 10
	statuses := satirKilidiAltindaEszamanli(t, gdb, app,
		"SELECT 1 FROM refresh_tokens WHERE token_hash = ? FOR UPDATE", auth.HashToken(token),
		n, func(int) *http.Request { return refreshRequest(token) })

	sayim := map[int]int{}
	for _, status := range statuses {
		sayim[status]++
	}
	if sayim[fiber.StatusOK] != 1 || sayim[fiber.StatusUnauthorized] != n-1 {
		t.Errorf("status dağılımı = %v, beklenen 1 adet 200 ve %d adet 401", sayim, n-1)
	}
	if got := activeRefreshTokens(t, gdb, u.ID); got != 1 {
		t.Errorf("etkin token sayısı = %d, beklenen 1", got)
	}
}
