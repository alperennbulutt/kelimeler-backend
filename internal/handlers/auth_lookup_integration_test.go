package handlers

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/models"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
)

// POST /auth/email/lookup: e-posta önce giriş ekranının adres sorgusu.
// Gerçek PostgreSQL gerektirir (bkz. usage_integration_test.go).

const lookupPath = "/api/v1/auth/email/lookup"

// lookup, adresi sorar ve 200 bekler; gövdeyi döner.
func lookup(t *testing.T, app *fiber.App, email string) map[string]any {
	t.Helper()

	status, body := postJSON(t, app, lookupPath, fiber.Map{"email": email})
	if status != fiber.StatusOK {
		t.Fatalf("lookup(%q): status = %d, beklenen 200 (body: %v)", email, status, body)
	}
	return body
}

// assertLookup, yanıtın tam olarak beklenen anahtarları taşıdığını sınar:
// providers yalnızca "social"da vardır ve orada her zaman bir dizidir.
func assertLookup(t *testing.T, body map[string]any, wantNext string, wantProviders []string) {
	t.Helper()

	if body["next"] != wantNext {
		t.Errorf("next = %v, beklenen %q (body: %v)", body["next"], wantNext, body)
	}
	raw, has := body["providers"]
	if wantProviders == nil {
		if has || len(body) != 1 {
			t.Errorf("yanıtta yalnızca next olmalı (body: %v)", body)
		}
		return
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("providers yok ya da dizi değil (body: %v)", body)
	}
	got := []string{}
	for _, p := range list {
		got = append(got, p.(string))
	}
	if !slices.Equal(got, wantProviders) {
		t.Errorf("providers = %v, beklenen %v", got, wantProviders)
	}
	if len(body) != 2 {
		t.Errorf("yanıtta yalnızca next ve providers olmalı (body: %v)", body)
	}
}

func TestEmailLookup_KayitliOlmayanAdresKayitSayfasinaGider(t *testing.T) {
	gdb := testDB(t)
	app := routesApp(gdb, "production", sinirsiz)

	assertLookup(t, lookup(t, app, benzersizEposta("yok")), dto.LookupNextRegister, nil)
}

func TestEmailLookup_ParolaHesabiGiriseGider(t *testing.T) {
	gdb := testDB(t)
	app := routesApp(gdb, "production", sinirsiz)

	// Kayıt gerçek uçtan: lookup'ın "login" dediği hesap login'le açılabilmeli.
	email := benzersizEposta("parola")
	t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", email) })
	kimlik := fiber.Map{"email": email, "password": "parola-12345"}
	if status, body := postJSON(t, app, "/api/v1/auth/register", kimlik); status != fiber.StatusCreated {
		t.Fatalf("register: status = %d (body: %v)", status, body)
	}

	assertLookup(t, lookup(t, app, email), dto.LookupNextLogin, nil)

	// Kayıt ve girişle aynı normalleştirme: büyük harf ve kenar boşlukları.
	assertLookup(t, lookup(t, app, "  "+strings.ToUpper(email)+"\t"), dto.LookupNextLogin, nil)

	if status, body := postJSON(t, app, "/api/v1/auth/login", kimlik); status != fiber.StatusOK {
		t.Errorf("login: status = %d, beklenen 200 (body: %v)", status, body)
	}
}

func TestEmailLookup_ParolaVeSosyalKimlikliHesapGiriseGider(t *testing.T) {
	gdb := testDB(t)
	app := routesApp(gdb, "production", sinirsiz)

	u := testUser(t, gdb)
	testIdentity(t, gdb, u, "password")
	testIdentity(t, gdb, u, "google")
	testIdentity(t, gdb, u, "apple")

	assertLookup(t, lookup(t, app, u.Email), dto.LookupNextLogin, nil)
}

func TestEmailLookup_YalnizcaSosyalHesapSaglayicilariDoner(t *testing.T) {
	gdb := testDB(t)
	app := routesApp(gdb, "production", sinirsiz)

	cases := []struct {
		name      string
		providers []string
		want      []string
	}{
		{"google", []string{"google"}, []string{"google"}},
		{"apple", []string{"apple"}, []string{"apple"}},
		{"google + apple sıralı", []string{"google", "apple"}, []string{"apple", "google"}},
		{"kimliksiz hesap", nil, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := testUser(t, gdb)
			for _, p := range c.providers {
				testIdentity(t, gdb, u, p)
			}
			assertLookup(t, lookup(t, app, u.Email), dto.LookupNextSocial, c.want)

			// "social" adresinde kayıt gerçekten 409 alır: istemci kayıt
			// sayfasını açmamakta haklı.
			status, body := postJSON(t, app, "/api/v1/auth/register",
				fiber.Map{"email": u.Email, "password": "parola-12345"})
			if status != fiber.StatusConflict || body["code"] != "email_in_use" {
				t.Errorf("register: status = %d code = %v, beklenen 409 email_in_use", status, body["code"])
			}
		})
	}
}

func TestEmailLookup_ParolaBaglananMisafirGiriseGider(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)

	_, guest := createGuest(t, gdb, app, nil)
	email := benzersizEposta("misafir-kayit")
	req := authRequest(t, h, &guest, http.MethodPost, "/api/v1/auth/link/password",
		fiber.Map{"email": email, "password": "parola-12345"})
	if status, body := send(t, app, req); status != fiber.StatusOK {
		t.Fatalf("link/password: status = %d (body: %v)", status, body)
	}

	assertLookup(t, lookup(t, app, email), dto.LookupNextLogin, nil)
}

func TestEmailLookup_YerTutucuAdreslerHicEslesmez(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)

	_, guest := createGuest(t, gdb, app, nil)

	// E-postasız sosyal hesap (yer tutucu adresli, gerçek bir kimlikle).
	social := &models.User{Email: dto.SyntheticEmail("google_lookup-" + time.Now().Format("150405.000000000"))}
	if err := gdb.Create(social).Error; err != nil {
		t.Fatalf("sosyal hesap oluşturulamadı: %v", err)
	}
	t.Cleanup(func() { gdb.Delete(&models.User{}, "id = ?", social.ID) })
	testIdentity(t, gdb, social, "google")

	for _, email := range []string{
		guest.Email,
		strings.ToUpper(guest.Email),
		social.Email,
		dto.SyntheticEmail(reclaimedEmailPrefix + guest.ID),
	} {
		status, body := postJSON(t, app, lookupPath, fiber.Map{"email": email})
		if status != fiber.StatusBadRequest || body["code"] != "invalid_email" {
			t.Errorf("lookup(%q): status = %d code = %v, beklenen 400 invalid_email", email, status, body["code"])
		}
	}
}

func TestEmailLookup_GecersizIstek400Doner(t *testing.T) {
	gdb := testDB(t)
	app := routesApp(gdb, "production", sinirsiz)

	cases := []struct {
		name     string
		req      *http.Request
		wantCode string
	}{
		{"boş adres", jsonRequest(lookupPath, fiber.Map{"email": ""}), "invalid_email"},
		{"e-posta alanı yok", jsonRequest(lookupPath, fiber.Map{}), "invalid_email"},
		{"biçimsiz adres", jsonRequest(lookupPath, fiber.Map{"email": "kisi@"}), "invalid_email"},
		{"noktasız alan adı", jsonRequest(lookupPath, fiber.Map{"email": "kisi@ornek"}), "invalid_email"},
		{"çok uzun adres", jsonRequest(lookupPath, fiber.Map{"email": strings.Repeat("a", 250) + "@ex.test"}), "invalid_email"},
		{"bozuk JSON", rawJSONRequest(lookupPath, `{"email":`), "invalid_request"},
		{"e-posta sayı", rawJSONRequest(lookupPath, `{"email":42}`), "invalid_request"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := send(t, app, c.req)
			if status != fiber.StatusBadRequest || body["code"] != c.wantCode {
				t.Errorf("status = %d code = %v, beklenen 400 %s (body: %v)", status, body["code"], c.wantCode, body)
			}
		})
	}

	// Gövdesiz istek de 400'dür (veritabanına gitmez).
	if status, body := post(t, app, lookupPath); status != fiber.StatusBadRequest {
		t.Errorf("gövdesiz: status = %d, beklenen 400 (body: %v)", status, body)
	}
}

func rawJSONRequest(path, raw string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(raw))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	return req
}

func TestEmailLookup_VeritabaniHatasi500Doner(t *testing.T) {
	kopuk := kopukDB(t)
	app := authApp(New(kopuk, &config.Config{AppEnv: "test"}, testTokens(), nil))

	// Hata "kayıtlı değil" diye yutulmamalı: istemci kullanıcıyı 409 alacağı
	// kayıt sayfasına gönderirdi.
	status, body := postJSON(t, app, lookupPath, fiber.Map{"email": benzersizEposta("kopuk")})
	if status != fiber.StatusInternalServerError {
		t.Errorf("status = %d, beklenen 500 (body: %v)", status, body)
	}
}

func TestEmailLookup_SikiHizSinirinaGirisleAyniKovadaTabi(t *testing.T) {
	gdb := testDB(t)
	const sinir = 3
	app := routesApp(gdb, "production", limiter.New(limiter.Config{
		Max:        sinir,
		Expiration: time.Minute,
		LimitReached: func(*fiber.Ctx) error {
			return apiErr(fiber.StatusTooManyRequests, "rate_limited", "çok fazla deneme")
		},
	}))

	for i := 1; i < sinir; i++ {
		lookup(t, app, benzersizEposta("sinir"))
	}
	// Son hak giriş ucunda harcanır: sorgu ve giriş aynı IP kovasını paylaşır,
	// sorgu taramaya ayrı bir bütçe açmaz.
	status, _ := postJSON(t, app, "/api/v1/auth/login",
		fiber.Map{"email": benzersizEposta("sinir"), "password": "parola-12345"})
	if status != fiber.StatusUnauthorized {
		t.Fatalf("login: status = %d, beklenen 401", status)
	}

	status, body := postJSON(t, app, lookupPath, fiber.Map{"email": benzersizEposta("sinir")})
	if status != fiber.StatusTooManyRequests || body["code"] != "rate_limited" {
		t.Errorf("sınır üstü sorgu: status = %d code = %v, beklenen 429 rate_limited", status, body["code"])
	}
}

func TestEmailLookup_GirisinBirlesik401DavranisiDegismez(t *testing.T) {
	gdb := testDB(t)
	app := routesApp(gdb, "production", sinirsiz)

	// Sorgu adresi ayırt etse de giriş ucu bilinmeyen adresi, parolasız
	// hesabı ve yanlış parolayı aynı gövdeyle reddeder.
	parolali := benzersizEposta("birlesik")
	t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", parolali) })
	if status, body := postJSON(t, app, "/api/v1/auth/register",
		fiber.Map{"email": parolali, "password": "parola-12345"}); status != fiber.StatusCreated {
		t.Fatalf("register: status = %d (body: %v)", status, body)
	}
	sosyal := testUser(t, gdb)
	testIdentity(t, gdb, sosyal, "google")

	var ilk map[string]any
	for _, email := range []string{benzersizEposta("bilinmeyen"), sosyal.Email, parolali} {
		status, body := postJSON(t, app, "/api/v1/auth/login",
			fiber.Map{"email": email, "password": "yanlis-parola"})
		if status != fiber.StatusUnauthorized || body["code"] != "invalid_credentials" {
			t.Errorf("login(%q): status = %d code = %v, beklenen 401 invalid_credentials", email, status, body["code"])
		}
		if ilk == nil {
			ilk = body
		} else if body["error"] != ilk["error"] {
			t.Errorf("login(%q): hata metni %v, ilk yanıtta %v; aynı olmalı", email, body["error"], ilk["error"])
		}
	}
}
