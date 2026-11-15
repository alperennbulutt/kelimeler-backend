package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"gorm.io/gorm"
)

// Misafir hesabı, e-posta/parola girişinin her ortamda açılması, parola
// hesabının doğrulanmış sağlayıcı e-postasına devri ve misafirin kayıtlı
// hesaba dönüşümü. Gerçek PostgreSQL gerektirir (bkz. usage_integration_test.go).

const testGuestRefreshTTL = 48 * time.Hour

// guestHandlers, misafir refresh ömrü olağandan (1 saat) ayrı ayarlanmış
// handler'lar kurar.
func guestHandlers(gdb *gorm.DB, env string) *Handlers {
	return New(gdb, &config.Config{AppEnv: env}, testTokens().WithGuestRefreshTTL(testGuestRefreshTTL), nil)
}

// createGuest, POST /auth/guest ile misafir açar; gövdeyi ve kullanıcıyı döner.
// Kullanıcı test bitince silinir.
func createGuest(t *testing.T, gdb *gorm.DB, app *fiber.App, payload any) (map[string]any, models.User) {
	t.Helper()

	var status int
	var body map[string]any
	if payload == nil {
		status, body = post(t, app, "/api/v1/auth/guest")
	} else {
		status, body = postJSON(t, app, "/api/v1/auth/guest", payload)
	}
	if status != fiber.StatusCreated {
		t.Fatalf("POST /auth/guest: status = %d, beklenen 201 (body: %v)", status, body)
	}
	user, _ := body["user"].(map[string]any)
	id, _ := user["id"].(string)
	if id == "" {
		t.Fatalf("yanıtta user.id yok (body: %v)", body)
	}
	t.Cleanup(func() { gdb.Delete(&models.User{}, "id = ?", id) })
	return body, loadUser(t, gdb, id)
}

func linkedProviders(t *testing.T, user map[string]any) []string {
	t.Helper()

	raw, ok := user["linkedProviders"].([]any)
	if !ok {
		t.Fatalf("linkedProviders yok ya da dizi değil (user: %v)", user)
	}
	out := []string{}
	for _, p := range raw {
		out = append(out, p.(string))
	}
	return out
}

// assertExpiresAround, refresh token'ın ömrünün yaklaşık ttl olduğunu sınar.
func assertExpiresAround(t *testing.T, gdb *gorm.DB, token string, ttl time.Duration) {
	t.Helper()

	row := loadRefreshToken(t, gdb, token)
	got := time.Until(row.ExpiresAt)
	if got < ttl-time.Minute || got > ttl+time.Minute {
		t.Errorf("refresh token ömrü ≈ %s, beklenen ≈ %s", got.Round(time.Minute), ttl)
	}
}

func apiErrorOf(t *testing.T, err error) *middleware.APIError {
	t.Helper()

	var apiError *middleware.APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("hata = %v, beklenen APIError", err)
	}
	return apiError
}

// --- B1: is_guest kolonu ---

func TestMigrate_IsGuestEskiSatirlardaFalse(t *testing.T) {
	gdb := testDB(t)

	// Kolonu bilmeyen (eski sunucunun yazdığı) satır.
	email := benzersizEposta("eski-satir")
	t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", email) })
	if err := gdb.Exec(`INSERT INTO users (email, created_at, updated_at) VALUES (?, now(), now())`, email).Error; err != nil {
		t.Fatalf("eski biçim satır yazılamadı: %v", err)
	}
	var u models.User
	if err := gdb.First(&u, "email = ?", email).Error; err != nil {
		t.Fatalf("satır okunamadı: %v", err)
	}
	if u.IsGuest {
		t.Error("is_guest varsayılanı true; eski hesaplar misafir sayılmamalı")
	}
}

// --- B2: POST /auth/guest ---

func TestGuest_GovdesizMisafirHesabiAcar(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)

	body, u := createGuest(t, gdb, app, nil)

	if body["accessToken"] == "" || body["refreshToken"] == "" {
		t.Errorf("token çifti yok (body: %v)", body)
	}
	user := userField(t, body)
	if user["isGuest"] != true {
		t.Errorf("isGuest = %v, beklenen true", user["isGuest"])
	}
	if user["email"] != "" {
		t.Errorf("email = %v, beklenen \"\" (yer tutucu adres istemciye gitmez)", user["email"])
	}
	if user["displayName"] != "" {
		t.Errorf("displayName = %v, beklenen \"\" (istemci \"Misafir\" yazar)", user["displayName"])
	}
	if user["currentLevel"] != "A1" || user["currentGroup"] != float64(1) {
		t.Errorf("seviye/grup = %v/%v, beklenen A1/1", user["currentLevel"], user["currentGroup"])
	}
	if got := linkedProviders(t, user); len(got) != 0 {
		t.Errorf("linkedProviders = %v, beklenen []", got)
	}

	if !u.IsGuest {
		t.Error("veritabanında is_guest false")
	}
	if want := "guest_" + u.ID + "@users.kelimeler.local"; u.Email != want {
		t.Errorf("email = %q, beklenen %q", u.Email, want)
	}
	var identities int64
	gdb.Model(&models.AuthIdentity{}).Where("user_id = ?", u.ID).Count(&identities)
	if identities != 0 {
		t.Errorf("misafirin %d kimlik satırı var, beklenen 0", identities)
	}
	assertExpiresAround(t, gdb, body["refreshToken"].(string), testGuestRefreshTTL)

	// /auth/me aynı biçimi döner.
	status, me := send(t, app, authRequest(t, h, &u, http.MethodGet, "/api/v1/auth/me", nil))
	if status != fiber.StatusOK {
		t.Fatalf("/auth/me: status = %d (body: %v)", status, me)
	}
	if mu := userField(t, me); mu["isGuest"] != true || mu["email"] != "" {
		t.Errorf("/auth/me isGuest/email = %v/%v, beklenen true/\"\"", mu["isGuest"], mu["email"])
	}
}

func TestGuest_GorunenAdVeGecersizGovde(t *testing.T) {
	gdb := testDB(t)
	app := authApp(guestHandlers(gdb, "production"))

	body, _ := createGuest(t, gdb, app, fiber.Map{"displayName": "  Ayşe ‮ Kaya  "})
	if got := userField(t, body)["displayName"]; got != "Ayşe Kaya" {
		t.Errorf("displayName = %q, beklenen temizlenmiş \"Ayşe Kaya\"", got)
	}
	body, _ = createGuest(t, gdb, app, fiber.Map{})
	if got := userField(t, body)["displayName"]; got != "" {
		t.Errorf("boş gövde: displayName = %q, beklenen \"\"", got)
	}

	count := func() int64 {
		var n int64
		gdb.Model(&models.User{}).Where("is_guest").Count(&n)
		return n
	}
	before := count()
	status, body := postJSON(t, app, "/api/v1/auth/guest", fiber.Map{"displayName": strings.Repeat("a", 61)})
	if status != fiber.StatusBadRequest || body["code"] != "invalid_display_name" {
		t.Errorf("uzun ad: status = %d %v, beklenen 400 invalid_display_name", status, body["code"])
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", strings.NewReader("{bozuk"))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	if status, body := send(t, app, req); status != fiber.StatusBadRequest || body["code"] != "invalid_request" {
		t.Errorf("bozuk gövde: status = %d %v, beklenen 400 invalid_request", status, body["code"])
	}
	if after := count(); after != before {
		t.Errorf("reddedilen istekler %d misafir hesabı açtı", after-before)
	}
}

func TestGuest_TokenYazilamazsaHesapKalmaz(t *testing.T) {
	gdb := testDB(t)
	app := authApp(guestHandlers(gdb, "production"))

	const hook = "test:misafir_token_yazilamaz"
	if err := gdb.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "refresh_tokens" {
			tx.AddError(errors.New("refresh token yazılamadı"))
		}
	}); err != nil {
		t.Fatalf("hook kaydedilemedi: %v", err)
	}
	t.Cleanup(func() { gdb.Callback().Create().Remove(hook) })

	var before, after int64
	gdb.Model(&models.User{}).Where("is_guest").Count(&before)
	if status, body := post(t, app, "/api/v1/auth/guest"); status != fiber.StatusInternalServerError {
		t.Fatalf("status = %d, beklenen 500 (body: %v)", status, body)
	}
	gdb.Model(&models.User{}).Where("is_guest").Count(&after)
	if after != before {
		t.Error("token yazılamadığı halde misafir hesabı kaldı (hesaba hiç ulaşılamaz)")
	}
}

func TestRegisterRoutes_MisafirVeParolaBaglamaSikiHizSinirinda(t *testing.T) {
	gdb := testDB(t)
	const sinir = 2
	h := guestHandlers(gdb, "production")
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	RegisterRoutes(app.Group("/api/v1"), h, h.Tokens, limiter.New(limiter.Config{
		Max:        sinir,
		Expiration: time.Minute,
		LimitReached: func(*fiber.Ctx) error {
			return apiErr(fiber.StatusTooManyRequests, "rate_limited", "çok fazla deneme")
		},
	}))

	for i := 0; i < sinir; i++ {
		createGuest(t, gdb, app, nil)
	}
	if status, body := post(t, app, "/api/v1/auth/guest"); status != fiber.StatusTooManyRequests {
		t.Errorf("sınır üstü misafir: status = %d, beklenen 429 (body: %v)", status, body)
	}
	// Aynı IP'nin sıkı sınırı dolmuşken parola bağlama ve giriş de reddedilir.
	for _, path := range []string{"/api/v1/auth/link/password", "/api/v1/auth/login", "/api/v1/auth/register"} {
		if status, _ := postJSON(t, app, path, fiber.Map{}); status != fiber.StatusTooManyRequests {
			t.Errorf("%s: status = %d, beklenen 429", path, status)
		}
	}
}

// --- B4: misafir refresh ömrü ---

func TestRefresh_MisafireUzunOmurKayittanSonraOlaganOmur(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	body, u := createGuest(t, gdb, app, nil)

	// Misafirin rotasyonu da uzun ömürlüdür.
	status, body := send(t, app, jsonRequest("/api/v1/auth/refresh", fiber.Map{"refreshToken": body["refreshToken"]}))
	if status != fiber.StatusOK {
		t.Fatalf("misafir yenilemesi: status = %d (body: %v)", status, body)
	}
	guestToken := body["refreshToken"].(string)
	assertExpiresAround(t, gdb, guestToken, testGuestRefreshTTL)

	// Hesabını kaydeder.
	email := benzersizEposta("kaydolan")
	req := authRequest(t, h, &u, http.MethodPost, "/api/v1/auth/link/password",
		fiber.Map{"email": email, "password": "parola-12345"})
	if status, body := send(t, app, req); status != fiber.StatusOK {
		t.Fatalf("link/password: status = %d (body: %v)", status, body)
	}

	// Sonraki rotasyon olağan ömre döner.
	status, body = send(t, app, jsonRequest("/api/v1/auth/refresh", fiber.Map{"refreshToken": guestToken}))
	if status != fiber.StatusOK {
		t.Fatalf("kayıt sonrası yenileme: status = %d (body: %v)", status, body)
	}
	assertExpiresAround(t, gdb, body["refreshToken"].(string), time.Hour)
	if userField(t, body)["isGuest"] != false {
		t.Errorf("yenileme yanıtında isGuest = %v, beklenen false", userField(t, body)["isGuest"])
	}
}

// --- B6: misafirde hesap silme ve çıkış ---

func TestGuest_HesapSilmeVeCikisCalisir(t *testing.T) {
	gdb := testDB(t)
	revoker, fake := testAppleRevoker(t)
	h := guestHandlers(gdb, "production").WithAppleRevoker(revoker)
	app := authApp(h)

	body, u := createGuest(t, gdb, app, nil)
	if status, _ := postJSON(t, app, "/api/v1/auth/logout", fiber.Map{"refreshToken": body["refreshToken"]}); status != fiber.StatusNoContent {
		t.Errorf("logout: status = %d, beklenen 204", status)
	}
	if status, _ := send(t, app, jsonRequest("/api/v1/auth/refresh", fiber.Map{"refreshToken": body["refreshToken"]})); status != fiber.StatusUnauthorized {
		t.Errorf("çıkıştan sonra yenileme: status = %d, beklenen 401", status)
	}

	status, resp := send(t, app, authRequest(t, h, &u, http.MethodDelete, "/api/v1/auth/account", nil))
	if status != fiber.StatusNoContent {
		t.Fatalf("hesap silme: status = %d, beklenen 204 (body: %v)", status, resp)
	}
	if userExists(t, gdb, u.ID) {
		t.Error("misafir hesabı silinmedi")
	}
	if exchanged, revoked := fake.snapshot(); len(exchanged) != 0 || len(revoked) != 0 {
		t.Errorf("misafir silinirken Apple'a gidildi: %v %v", exchanged, revoked)
	}
}

// Misafirin bağlı kimliği yoktur: bağlantı kaldırma bir şey yapmadan 200 döner.
func TestGuest_BaglantiKaldirmaDegisiklikYapmaz(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	_, u := createGuest(t, gdb, app, nil)

	for _, p := range []string{"google", "apple"} {
		status, body := send(t, app, unlinkRequest(t, h, &u, p))
		if status != fiber.StatusOK {
			t.Errorf("DELETE /link/%s: status = %d, beklenen 200 (body: %v)", p, status, body)
		}
	}
	if !loadUser(t, gdb, u.ID).IsGuest {
		t.Error("bağlantı kaldırma misafirliği değiştirdi")
	}
}

// --- A1: kayıt ve giriş (production) ---

func TestRegister_DogrulamaVeHataKodlari(t *testing.T) {
	gdb := testDB(t)
	app := routesApp(gdb, "production", sinirsiz)

	mevcut := testUser(t, gdb)
	cases := []struct {
		name       string
		payload    any
		wantStatus int
		wantCode   string
	}{
		{"geçersiz e-posta", fiber.Map{"email": "duz-metin", "password": "parola-12345"}, 400, "invalid_email"},
		{"yer tutucu adres", fiber.Map{"email": dto.SyntheticEmail("guest_" + mevcut.ID), "password": "parola-12345"}, 400, "invalid_email"},
		{"kısa parola", fiber.Map{"email": benzersizEposta("kisa"), "password": "1234567"}, 400, "weak_password"},
		{"uzun parola", fiber.Map{"email": benzersizEposta("uzun"), "password": strings.Repeat("a", 73)}, 400, "weak_password"},
		{"uzun ad", fiber.Map{"email": benzersizEposta("ad"), "password": "parola-12345", "displayName": strings.Repeat("a", 61)}, 400, "invalid_display_name"},
		{"kullanılan adres (büyük harfle)", fiber.Map{"email": strings.ToUpper(mevcut.Email), "password": "parola-12345"}, 409, "email_in_use"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := postJSON(t, app, "/api/v1/auth/register", c.payload)
			if status != c.wantStatus || body["code"] != c.wantCode {
				t.Errorf("status = %d %v, beklenen %d %s", status, body["code"], c.wantStatus, c.wantCode)
			}
		})
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader("{bozuk"))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	if status, body := send(t, app, req); status != fiber.StatusBadRequest || body["code"] != "invalid_request" {
		t.Errorf("bozuk gövde: status = %d %v, beklenen 400 invalid_request", status, body["code"])
	}
}

func TestRegister_YanitBicimiVeGiris(t *testing.T) {
	gdb := testDB(t)
	app := routesApp(gdb, "production", sinirsiz)

	email := benzersizEposta("Kayit")
	t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", strings.ToLower(email)) })
	status, body := postJSON(t, app, "/api/v1/auth/register",
		fiber.Map{"email": "  " + email + " ", "password": "parola-12345", "displayName": "Kübra"})
	if status != fiber.StatusCreated {
		t.Fatalf("register: status = %d, beklenen 201 (body: %v)", status, body)
	}
	user := userField(t, body)
	if user["email"] != strings.ToLower(email) || user["displayName"] != "Kübra" || user["isGuest"] != false {
		t.Errorf("user = %v", user)
	}
	if got := linkedProviders(t, user); !slices.Equal(got, []string{"password"}) {
		t.Errorf("linkedProviders = %v, beklenen [password]", got)
	}

	status, body = postJSON(t, app, "/api/v1/auth/login", fiber.Map{"email": strings.ToUpper(email), "password": "parola-12345"})
	if status != fiber.StatusOK {
		t.Fatalf("login: status = %d, beklenen 200 (body: %v)", status, body)
	}
	if userField(t, body)["id"] != user["id"] {
		t.Errorf("login başka hesap döndü: %v", userField(t, body)["id"])
	}
}

// Yanıt, e-postanın kayıtlı olup olmadığını ele vermemeli: bilinmeyen adres,
// parolasız hesap ve yanlış parola aynı gövdeyi alır.
func TestLogin_YanlisKimlikBilgileriAyirtEdilmez(t *testing.T) {
	gdb := testDB(t)
	app := routesApp(gdb, "production", sinirsiz)

	email := benzersizEposta("giris")
	t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", email) })
	if status, body := postJSON(t, app, "/api/v1/auth/register", fiber.Map{"email": email, "password": "dogru-parola"}); status != fiber.StatusCreated {
		t.Fatalf("register: status = %d (body: %v)", status, body)
	}
	parolasiz := testUser(t, gdb)
	testIdentity(t, gdb, parolasiz, "google")

	var ilk map[string]any
	for ad, kimlik := range map[string]fiber.Map{
		"yanlış parola":     {"email": email, "password": "yanlis-parola"},
		"bilinmeyen adres":  {"email": benzersizEposta("yok"), "password": "dogru-parola"},
		"parolasız hesap":   {"email": parolasiz.Email, "password": "dogru-parola"},
		"yer tutucu adres":  {"email": dto.SyntheticEmail("guest_x"), "password": "dogru-parola"},
		"boş alanlar":       {},
		"parola uzunluğu 0": {"email": email, "password": ""},
	} {
		status, body := postJSON(t, app, "/api/v1/auth/login", kimlik)
		if status != fiber.StatusUnauthorized {
			t.Errorf("%s: status = %d, beklenen 401 (body: %v)", ad, status, body)
			continue
		}
		if ilk == nil {
			ilk = body
		} else if body["code"] != ilk["code"] || body["error"] != ilk["error"] {
			t.Errorf("%s: gövde = %v, diğerleri %v; ayırt edilebiliyor", ad, body, ilk)
		}
	}
	if ilk["code"] != "invalid_credentials" {
		t.Errorf("code = %v, beklenen invalid_credentials", ilk["code"])
	}
}

// --- A2: doğrulanmış sağlayıcı e-postası parola hesabını devralır ---

// passwordAccount, gerçek kayıt ucuyla e-posta/parola hesabı açar ve bir
// oturumun refresh token'ını döner.
func passwordAccount(t *testing.T, gdb *gorm.DB, app *fiber.App, onEk string) (models.User, string) {
	t.Helper()

	email := benzersizEposta(onEk)
	status, body := postJSON(t, app, "/api/v1/auth/register", fiber.Map{"email": email, "password": "parola-12345"})
	if status != fiber.StatusCreated {
		t.Fatalf("register: status = %d (body: %v)", status, body)
	}
	id := userField(t, body)["id"].(string)
	t.Cleanup(func() { gdb.Delete(&models.User{}, "id = ?", id) })
	return loadUser(t, gdb, id), body["refreshToken"].(string)
}

func TestAuthenticateSocial_DogrulanmisEpostaParolaHesabiniDevralir(t *testing.T) {
	for _, provider := range []string{"google", "apple"} {
		t.Run(provider, func(t *testing.T) {
			gdb := testDB(t)
			h := guestHandlers(gdb, "production")
			app := authApp(h)
			u, refresh := passwordAccount(t, gdb, app, "devir-"+provider)
			testWord(t, gdb, u.ID, "w-devir-"+u.ID, 3)
			// İkinci cihazdaki oturum.
			status, body := postJSON(t, app, "/api/v1/auth/login", fiber.Map{"email": u.Email, "password": "parola-12345"})
			if status != fiber.StatusOK {
				t.Fatalf("login: status = %d (body: %v)", status, body)
			}
			ikinciCihaz := body["refreshToken"].(string)

			si := &auth.SocialIdentity{Subject: provider + "-sahip-" + u.ID, Email: strings.ToUpper(u.Email)}
			got, err := h.authenticateSocial(provider, si, "")
			if err != nil {
				t.Fatalf("authenticateSocial: %v", err)
			}
			if got.ID != u.ID {
				t.Fatalf("kullanıcı = %s, beklenen devralınan hesap %s", got.ID, u.ID)
			}

			if n := identityCount(t, gdb, u.ID, "password"); n != 0 {
				t.Errorf("parola kimliği silinmedi (%d satır)", n)
			}
			if n := identityCount(t, gdb, u.ID, provider); n != 1 {
				t.Errorf("%s kimliği sayısı = %d, beklenen 1", provider, n)
			}
			if n := activeRefreshTokens(t, gdb, u.ID); n != 0 {
				t.Errorf("devirden sonra %d etkin refresh token kaldı, beklenen 0", n)
			}
			for ad, token := range map[string]string{"kayıt oturumu": refresh, "ikinci cihaz": ikinciCihaz} {
				if status, _ := send(t, app, jsonRequest("/api/v1/auth/refresh", fiber.Map{"refreshToken": token})); status != fiber.StatusUnauthorized {
					t.Errorf("%s: yenileme status = %d, beklenen 401", ad, status)
				}
			}
			if status, body := postJSON(t, app, "/api/v1/auth/login", fiber.Map{"email": u.Email, "password": "parola-12345"}); status != fiber.StatusUnauthorized || body["code"] != "invalid_credentials" {
				t.Errorf("devirden sonra parola girişi: status = %d %v, beklenen 401 invalid_credentials", status, body["code"])
			}
			// İlerleme korunur.
			var words int64
			gdb.Model(&models.WordProgress{}).Where("user_id = ?", u.ID).Count(&words)
			if words != 1 {
				t.Errorf("kelime ilerlemesi = %d satır, beklenen 1", words)
			}
			// Sonraki girişler kimliği subject ile bulur.
			again, err := h.authenticateSocial(provider, &auth.SocialIdentity{Subject: si.Subject}, "")
			if err != nil || again.ID != u.ID {
				t.Errorf("ikinci giriş = (%v, %v), beklenen aynı hesap", again, err)
			}
		})
	}
}

// Parolanın yanında başka bir giriş yolu olan hesap devralınmaz; adresi geri
// alınır (bkz. auth_reclaim_integration_test.go).

// Devir ile aynı anda süren bir parola girişi: giriş, parola kimliğini
// kilitleyip token yazarken devir bekler; sonra o token'ı da iptal eder.
// Kilit sırası tersine dönerse girişin token'ı devirden sonra etkin kalırdı.
func TestAuthenticateSocial_DevirEszamanliGirisinTokeniniDeIptalEder(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	u, _ := passwordAccount(t, gdb, app, "devir-yaris")

	// Girişin transaction'ı: kimlik kilitli, token yazıldı, henüz commit yok.
	giris := gdb.Begin()
	defer giris.Rollback()
	if err := giris.Exec(`SELECT 1 FROM auth_identities WHERE user_id = ? AND provider = 'password' FOR UPDATE`, u.ID).Error; err != nil {
		t.Fatalf("kimlik kilitlenemedi: %v", err)
	}
	token, _ := auth.GenerateRefreshToken()
	if err := giris.Create(&models.RefreshToken{UserID: u.ID, TokenHash: auth.HashToken(token),
		ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatalf("girişin token'ı yazılamadı: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := h.authenticateSocial("google", &auth.SocialIdentity{Subject: "yaris-" + u.ID, Email: u.Email}, "")
		done <- err
	}()

	var bekleyen int64
	for son := time.Now().Add(10 * time.Second); bekleyen < 1 && time.Now().Before(son); {
		time.Sleep(10 * time.Millisecond)
		gdb.Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&bekleyen)
	}
	if bekleyen < 1 {
		t.Error("devir girişin kilidini beklemedi; yarış sıralaması kurulamadı")
	}
	if err := giris.Commit().Error; err != nil {
		t.Fatalf("giriş commit edilemedi: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("devir: %v", err)
	}
	if loadRefreshToken(t, gdb, token).RevokedAt == nil {
		t.Error("devirle eşzamanlı girişin token'ı etkin kaldı")
	}
}

// --- A3: POST /auth/link/password ---

func linkPasswordRequest(t *testing.T, h *Handlers, u *models.User, email, password string) *http.Request {
	return authRequest(t, h, u, http.MethodPost, "/api/v1/auth/link/password",
		fiber.Map{"email": email, "password": password})
}

func TestLinkPassword_MisafirHesabiKaydeder(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	_, u := createGuest(t, gdb, app, fiber.Map{"displayName": "Misafir Ali"})
	testWord(t, gdb, u.ID, "w-misafir-"+u.ID, 2)

	email := benzersizEposta("Kaydet")
	status, body := send(t, app, linkPasswordRequest(t, h, &u, " "+email, "parola-12345"))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, beklenen 200 (body: %v)", status, body)
	}
	user := userField(t, body)
	if user["isGuest"] != false || user["email"] != strings.ToLower(email) || user["displayName"] != "Misafir Ali" {
		t.Errorf("user = %v", user)
	}
	if got := linkedProviders(t, user); !slices.Equal(got, []string{"password"}) {
		t.Errorf("linkedProviders = %v, beklenen [password]", got)
	}
	if _, ok := body["accessToken"]; ok {
		t.Errorf("bağlama yanıtı token taşımamalı (diğer bağlama uçları gibi {user}): %v", body)
	}

	// Aynı e-posta/parolayla giriş aynı hesabı (ilerlemesiyle) açar.
	status, body = postJSON(t, app, "/api/v1/auth/login", fiber.Map{"email": email, "password": "parola-12345"})
	if status != fiber.StatusOK || userField(t, body)["id"] != u.ID {
		t.Fatalf("login: status = %d (body: %v), beklenen aynı hesap", status, body)
	}

	// Yanıtı kaybolan isteğin tekrarı 200; farklı parola ya da adres 409 not_guest.
	if status, body := send(t, app, linkPasswordRequest(t, h, &u, email, "parola-12345")); status != fiber.StatusOK {
		t.Errorf("tekrar: status = %d, beklenen 200 (body: %v)", status, body)
	}
	for ad, req := range map[string]*http.Request{
		"farklı parola": linkPasswordRequest(t, h, &u, email, "baska-parola"),
		"farklı adres":  linkPasswordRequest(t, h, &u, benzersizEposta("baska"), "parola-12345"),
	} {
		if status, body := send(t, app, req); status != fiber.StatusConflict || body["code"] != "not_guest" {
			t.Errorf("%s: status = %d %v, beklenen 409 not_guest", ad, status, body["code"])
		}
	}
	if n := identityCount(t, gdb, u.ID, "password"); n != 1 {
		t.Errorf("parola kimliği sayısı = %d, beklenen 1", n)
	}
}

func TestLinkPassword_HataKodlari(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	_, guest := createGuest(t, gdb, app, nil)
	kayitli := testUser(t, gdb)
	testIdentity(t, gdb, kayitli, "google")

	cases := []struct {
		name       string
		user       *models.User
		email      string
		password   string
		wantStatus int
		wantCode   string
	}{
		{"kayıtlı hesap", kayitli, benzersizEposta("kayitli"), "parola-12345", 409, "not_guest"},
		{"kullanılan adres", &guest, strings.ToUpper(kayitli.Email), "parola-12345", 409, "email_in_use"},
		{"geçersiz e-posta", &guest, "duz-metin", "parola-12345", 400, "invalid_email"},
		{"yer tutucu adres", &guest, guest.Email, "parola-12345", 400, "invalid_email"},
		{"kısa parola", &guest, benzersizEposta("kisa"), "1234567", 400, "weak_password"},
		{"uzun parola", &guest, benzersizEposta("uzun"), strings.Repeat("a", 73), 400, "weak_password"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := send(t, app, linkPasswordRequest(t, h, c.user, c.email, c.password))
			if status != c.wantStatus || body["code"] != c.wantCode {
				t.Errorf("status = %d %v, beklenen %d %s", status, body["code"], c.wantStatus, c.wantCode)
			}
		})
	}
	if u := loadUser(t, gdb, guest.ID); !u.IsGuest || !dto.IsSyntheticEmail(u.Email) {
		t.Errorf("reddedilen istekler misafiri değiştirdi: is_guest=%v email=%q", u.IsGuest, u.Email)
	}
	if n := identityCount(t, gdb, kayitli.ID, "password"); n != 0 {
		t.Errorf("kayıtlı hesaba parola eklendi (%d satır)", n)
	}

	// Token'sız istek 401.
	if status, _ := postJSON(t, app, "/api/v1/auth/link/password", fiber.Map{"email": benzersizEposta("x"), "password": "parola-12345"}); status != fiber.StatusUnauthorized {
		t.Errorf("token'sız: status = %d, beklenen 401", status)
	}
	req := authRequest(t, h, &guest, http.MethodPost, "/api/v1/auth/link/password", nil)
	if status, body := send(t, app, req); status != fiber.StatusBadRequest || body["code"] != "invalid_request" {
		t.Errorf("gövdesiz: status = %d %v, beklenen 400 invalid_request", status, body["code"])
	}
}

// Aynı misafirden eşzamanlı iki farklı adresle bağlama: kullanıcı satırı
// kilitli olduğu için yalnızca biri geçer, hesapta tek parola kalır.
func TestLinkPassword_EszamanliIstekTekKimlikBirakir(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	_, guest := createGuest(t, gdb, app, nil)

	emails := []string{benzersizEposta("es-1"), benzersizEposta("es-2")}
	statuses := kilitAltindaEszamanli(t, gdb, app, guest.ID, len(emails), func(i int) *http.Request {
		return linkPasswordRequest(t, h, &guest, emails[i], "parola-12345")
	})
	sayim := map[int]int{}
	for _, s := range statuses {
		sayim[s]++
	}
	if sayim[fiber.StatusOK] != 1 || sayim[fiber.StatusConflict] != 1 {
		t.Errorf("status dağılımı = %v, beklenen bir 200 ve bir 409", sayim)
	}
	if n := identityCount(t, gdb, guest.ID, "password"); n != 1 {
		t.Errorf("parola kimliği sayısı = %d, beklenen 1", n)
	}
	if u := loadUser(t, gdb, guest.ID); u.IsGuest || !slices.Contains(emails, u.Email) {
		t.Errorf("is_guest=%v email=%q", u.IsGuest, u.Email)
	}
}

// --- B5: misafirin Google/Apple bağlayarak dönüşümü ---

func TestLinkProvider_MisafirDonusumu(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	baskasi := testUser(t, gdb)

	cases := []struct {
		name      string
		email     func(guest models.User) string // sağlayıcının doğruladığı adres ("" = yok)
		wantEmail func(guest models.User, verified string) string
	}{
		{"doğrulanmış ve boş adres kullanıcının olur",
			func(models.User) string { return benzersizEposta("donusum") },
			func(_ models.User, v string) string { return v }},
		{"adres başka hesaptaysa yer tutucu kalır",
			func(models.User) string { return baskasi.Email },
			func(g models.User, _ string) string { return g.Email }},
		{"sağlayıcı adres vermezse yer tutucu kalır",
			func(models.User) string { return "" },
			func(g models.User, _ string) string { return g.Email }},
	}
	for _, provider := range []string{"google", "apple"} {
		for _, c := range cases {
			t.Run(provider+"/"+c.name, func(t *testing.T) {
				_, guest := createGuest(t, gdb, app, nil)
				verified := c.email(guest)
				si := &auth.SocialIdentity{Subject: provider + "-misafir-" + guest.ID, Email: verified}

				status, body := post(t, linkApp(h, guest.ID, provider, si), "/link")
				if status != fiber.StatusOK {
					t.Fatalf("status = %d, beklenen 200 (body: %v)", status, body)
				}
				user := userField(t, body)
				if user["isGuest"] != false {
					t.Errorf("isGuest = %v, beklenen false", user["isGuest"])
				}
				if got := linkedProviders(t, user); !slices.Equal(got, []string{provider}) {
					t.Errorf("linkedProviders = %v, beklenen [%s]", got, provider)
				}
				u := loadUser(t, gdb, guest.ID)
				if want := c.wantEmail(guest, verified); u.Email != want {
					t.Errorf("email = %q, beklenen %q", u.Email, want)
				}
				wantJSON := u.Email
				if dto.IsSyntheticEmail(u.Email) {
					wantJSON = ""
				}
				if user["email"] != wantJSON {
					t.Errorf("yanıttaki email = %v, beklenen %q", user["email"], wantJSON)
				}
				// Bağlanan kimlikle normal giriş aynı hesabı açar.
				got, err := h.authenticateSocial(provider, &auth.SocialIdentity{Subject: si.Subject}, "")
				if err != nil || got.ID != guest.ID {
					t.Errorf("bağlanan kimlikle giriş = (%v, %v), beklenen misafirin hesabı", got, err)
				}
			})
		}
	}
	if u := loadUser(t, gdb, baskasi.ID); u.Email != baskasi.Email {
		t.Errorf("başka hesabın adresi değişti: %q", u.Email)
	}
}

func TestLinkProvider_MisafirBaskaHesabaBagliKimlik409(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	_, guest := createGuest(t, gdb, app, nil)
	sahip := testUser(t, gdb)
	testIdentity(t, gdb, sahip, "google")

	si := &auth.SocialIdentity{Subject: "google-" + sahip.ID, Email: sahip.Email}
	status, body := post(t, linkApp(h, guest.ID, "google", si), "/link")
	if status != fiber.StatusConflict || body["code"] != "provider_already_linked" {
		t.Errorf("status = %d %v, beklenen 409 provider_already_linked", status, body["code"])
	}
	if u := loadUser(t, gdb, guest.ID); !u.IsGuest {
		t.Error("reddedilen bağlama misafirliği kaldırdı")
	}
	// İstemci o hesaba geçer ve misafiri eski token'ıyla siler (M5).
	if status, _ := send(t, app, authRequest(t, h, &guest, http.MethodDelete, "/api/v1/auth/account", nil)); status != fiber.StatusNoContent {
		t.Errorf("misafir silme: status = %d, beklenen 204", status)
	}
	if !userExists(t, gdb, sahip.ID) {
		t.Error("misafiri silmek o hesabı da sildi")
	}
}

// Kayıtlı (misafir olmayan) hesaba bağlama e-postayı değiştirmez.
func TestLinkProvider_KayitliHesaptaEpostaDegismez(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	u := testUser(t, gdb)
	testIdentity(t, gdb, u, "google")

	si := &auth.SocialIdentity{Subject: "apple-" + u.ID + "-yeni", Email: benzersizEposta("apple")}
	if status, body := post(t, linkApp(h, u.ID, "apple", si), "/link"); status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}
	if got := loadUser(t, gdb, u.ID); got.Email != u.Email || got.IsGuest {
		t.Errorf("email/is_guest = %q/%v, beklenen %q/false", got.Email, got.IsGuest, u.Email)
	}
}

// --- B3: e-postasız sosyal hesap ---

func TestMe_EpostasizSosyalHesaptaEpostaBos(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	subject := "epostasiz-" + time.Now().Format("150405.000000000")
	t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", dto.SyntheticEmail("apple_"+subject)) })

	u, err := h.authenticateSocial("apple", &auth.SocialIdentity{Subject: subject}, "")
	if err != nil {
		t.Fatalf("authenticateSocial: %v", err)
	}
	status, body := send(t, authApp(h), authRequest(t, h, u, http.MethodGet, "/api/v1/auth/me", nil))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}
	if user := userField(t, body); user["email"] != "" || user["isGuest"] != false {
		t.Errorf("email/isGuest = %v/%v, beklenen \"\"/false", user["email"], user["isGuest"])
	}
}

// --- B1/B6: kullanıcı uçları ve oyun kuralları misafirde aynıdır ---

// Kullanıcı uçlarının yanıtı da isGuest taşır ve yer tutucu adresi göstermez.
func TestUsersMe_MisafirdeIsGuestVeBosEposta(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	_, u := createGuest(t, gdb, app, nil)

	for _, req := range []*http.Request{
		authRequest(t, h, &u, http.MethodGet, "/api/v1/users/me", nil),
		authRequest(t, h, &u, http.MethodPatch, "/api/v1/users/me", fiber.Map{"displayName": "Deniz"}),
	} {
		status, body := send(t, app, req)
		if status != fiber.StatusOK {
			t.Fatalf("%s %s: status = %d (body: %v)", req.Method, req.URL.Path, status, body)
		}
		user := userField(t, body)
		if user["isGuest"] != true || user["email"] != "" {
			t.Errorf("%s %s: isGuest/email = %v/%v, beklenen true/\"\"", req.Method, req.URL.Path, user["isGuest"], user["email"])
		}
	}
	if got := loadUser(t, gdb, u.ID); got.DisplayName != "Deniz" || !got.IsGuest {
		t.Errorf("ad/is_guest = %q/%v, beklenen \"Deniz\"/true", got.DisplayName, got.IsGuest)
	}
}

// Misafir için oyun kuralları değişmez: grup açma ve kapı denetimi kayıtlı bir
// kullanıcıyla aynı sırada aynı yanıtları verir.
func TestGuest_OyunKurallariKayitliKullaniciylaAyni(t *testing.T) {
	gdb := testDB(t)
	h := New(gdb, &config.Config{AppEnv: "production", GateEnforcement: config.GateEnforce},
		testTokens().WithGuestRefreshTTL(testGuestRefreshTTL), nil)
	app := authApp(h)
	w1 := testDictWord(t, gdb, "itest_misafir_kural_1", "A1", 1, 1)
	w2 := testDictWord(t, gdb, "itest_misafir_kural_2", "A1", 2, 1)

	type adim struct {
		method, path string
		payload      any
	}
	adimlar := []adim{
		{http.MethodPost, "/api/v1/progress/learn", fiber.Map{"wordId": w1.ID}}, // grup açılmadan
		{http.MethodPost, "/api/v1/users/me/usage/group-started", fiber.Map{"level": "A1", "group": 1}},
		{http.MethodPost, "/api/v1/progress/learn", fiber.Map{"wordId": w1.ID}},
		{http.MethodPost, "/api/v1/users/me/usage/group-started", fiber.Map{"level": "A1", "group": 2}},
		{http.MethodPost, "/api/v1/progress/learn", fiber.Map{"wordId": w2.ID}},
		{http.MethodGet, "/api/v1/users/me/usage", nil},
	}
	calistir := func(u *models.User) []string {
		out := []string{}
		for _, a := range adimlar {
			status, body := send(t, app, authRequest(t, h, u, a.method, a.path, a.payload))
			out = append(out, fmt.Sprintf("%s %s → %d %v", a.method, a.path, status, body["code"]))
		}
		return out
	}

	_, guest := createGuest(t, gdb, app, nil)
	misafir := calistir(&guest)
	kayitli := calistir(testUser(t, gdb))

	if !slices.Equal(misafir, kayitli) {
		t.Errorf("misafir ve kayıtlı kullanıcı farklı yanıt aldı:\nmisafir: %v\nkayıtlı: %v", misafir, kayitli)
	}
	// Karşılaştırma boş geçmesin: kapı gerçekten devrede, ilk grup açılabiliyor.
	for i, want := range map[int]string{0: "403 gate_required", 1: "200 <nil>", 2: "200 <nil>"} {
		if !strings.HasSuffix(misafir[i], want) {
			t.Errorf("adım %d: %s, beklenen %q", i, misafir[i], want)
		}
	}
}
