package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"

	"github.com/gofiber/fiber/v2"
)

// QA: adresin geri alınması ve devir için saldırı senaryoları.
// Gerçek PostgreSQL gerektirir (bkz. usage_integration_test.go).

// bearerRequest, verilen access token'la gövdesiz bir istek hazırlar.
func bearerRequest(method, path, access string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set(fiber.HeaderAuthorization, "Bearer "+access)
	return req
}

// sessionLinkApp, gerçek auth middleware'inin arkasında linkProvider'ı çalıştırır:
// kimlik doğrulanmış sağlayıcı token'ı yerine si ile gelir, oturum sahibi ise
// access token'dan okunur.
func sessionLinkApp(h *Handlers, provider string, si *auth.SocialIdentity) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	app.Post("/link", middleware.RequireAuth(h.Tokens), func(c *fiber.Ctx) error {
		return h.linkProvider(c, middleware.UserID(c), provider, si)
	})
	return app
}

// Devirden önce verilmiş access token (en fazla ACCESS_TOKEN_TTL geçerli),
// devirden sonra hesaba giriş yolu ekleyemez, giriş yolu kaldıramaz, hesabı
// silemez. Ekleyebilseydi adresi parolayla önceden kapatan kişi, refresh
// token'ları iptal edilmiş olsa da elindeki access token'la kendi Apple'ını
// adresin sahibine devredilen hesaba bağlar ve ona kalıcı olarak girerdi;
// silebilseydi sahibin hesabını yok ederdi.
func TestQA_DevirdenOnceVerilenAccessTokenHesabiDenetleyemez(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	u, _ := passwordAccount(t, gdb, app, "qa-eski-access")
	status, body := postJSON(t, app, "/api/v1/auth/login", fiber.Map{"email": u.Email, "password": "parola-12345"})
	if status != fiber.StatusOK {
		t.Fatalf("login: status = %d (body: %v)", status, body)
	}
	eskiAccess := body["accessToken"].(string)

	sahip, err := h.authenticateSocial("google", &auth.SocialIdentity{Subject: "google-sahip-" + u.ID, Email: u.Email}, "")
	if err != nil {
		t.Fatalf("authenticateSocial: %v", err)
	}
	if sahip.ID != u.ID {
		t.Fatalf("devir olmadı: kullanıcı = %s, beklenen %s", sahip.ID, u.ID)
	}

	saldirganApple := &auth.SocialIdentity{Subject: "apple-saldirgan-" + u.ID, Email: benzersizEposta("qa-saldirgan")}
	if status, body := send(t, sessionLinkApp(h, "apple", saldirganApple), bearerRequest(http.MethodPost, "/link", eskiAccess)); status != fiber.StatusUnauthorized {
		t.Errorf("eski access token'la apple bağlama: %d %v, beklenen 401", status, body)
	}
	if n := identityCount(t, gdb, u.ID, "apple"); n != 0 {
		t.Errorf("eski access token devredilen hesaba apple bağladı (%d satır)", n)
	}
	if status, body := send(t, app, bearerRequest(http.MethodDelete, "/api/v1/auth/link/google", eskiAccess)); status != fiber.StatusUnauthorized {
		t.Errorf("eski access token'la google kaldırma: %d %v, beklenen 401", status, body)
	}
	if status, body := send(t, app, bearerRequest(http.MethodDelete, "/api/v1/auth/account", eskiAccess)); status != fiber.StatusUnauthorized {
		t.Errorf("eski access token'la hesap silme: %d %v, beklenen 401", status, body)
	}
	var kalan int64
	gdb.Model(&models.User{}).Where("id = ?", u.ID).Count(&kalan)
	if kalan != 1 {
		t.Fatal("eski access token devredilen hesabı sildi")
	}

	// Sahibin devirde aldığı oturum aynı uçları kullanabilir.
	resp, err := h.issueTokens(sahip)
	if err != nil {
		t.Fatalf("issueTokens: %v", err)
	}
	sahipApple := &auth.SocialIdentity{Subject: "apple-sahip-" + u.ID, Email: u.Email}
	if status, body := send(t, sessionLinkApp(h, "apple", sahipApple), bearerRequest(http.MethodPost, "/link", resp.AccessToken)); status != fiber.StatusOK {
		t.Errorf("sahibin access token'ıyla apple bağlama: %d %v, beklenen 200", status, body)
	}
}

// Devirle aynı anda süren bir bağlama: adresi parolayla kapatan kişi, hesap
// sahibine devredilirken kendi Apple'ını bağlıyorsa (kullanıcı satırı kilitli,
// kimlik yazılmış, commit yok), devir o kimliği görmeden bitmemeli. Bitseydi
// sahip, saldırganın Apple'ıyla da girilebilen bir hesaba konurdu. Bağlama
// önce biterse hesabın parolanın yanında başka giriş yolu vardır: adres geri
// alınır, sahibe yeni hesap açılır.
func TestQA_DevirleEszamanliBaglamaSaldirganaHesapBirakmaz(t *testing.T) {
	gdb := testDB(t)
	h := guestHandlers(gdb, "production")
	app := authApp(h)
	u, _ := passwordAccount(t, gdb, app, "qa-devir-baglama")
	t.Cleanup(func() { gdb.Delete(&models.User{}, "email = ?", u.Email) })

	link := gdb.Begin()
	defer link.Rollback()
	var locked models.User
	if err := lockUser(link, u.ID, &locked); err != nil {
		t.Fatalf("kullanıcı kilitlenemedi: %v", err)
	}
	sub := "apple-saldirgan-yaris-" + u.ID
	saldirganEposta := benzersizEposta("qa-saldirgan-yaris")
	if err := link.Create(&models.AuthIdentity{
		UserID: u.ID, Provider: "apple", ProviderSubject: &sub, Email: &saldirganEposta, EmailVerified: true,
	}).Error; err != nil {
		t.Fatalf("bağlama kimliği yazılamadı: %v", err)
	}

	type sonuc struct {
		user *models.User
		err  error
	}
	done := make(chan sonuc, 1)
	go func() {
		got, err := h.authenticateSocial("google", &auth.SocialIdentity{Subject: "google-sahip-yaris-" + u.ID, Email: u.Email}, "")
		done <- sonuc{got, err}
	}()
	if !waitForLockWaiter(t, gdb, "") {
		t.Error("devir bağlamanın kilidini beklemedi; yarış sıralaması kurulamadı")
	}
	if err := link.Commit().Error; err != nil {
		t.Fatalf("bağlama commit edilemedi: %v", err)
	}
	got := <-done
	if got.err != nil {
		t.Fatalf("authenticateSocial: %v", got.err)
	}

	if n := identityCount(t, gdb, got.user.ID, "apple"); n != 0 {
		t.Errorf("adresin sahibi saldırganın Apple'ı bağlı hesaba kondu (user=%s)", got.user.ID)
	}
	if got.user.ID == u.ID {
		t.Errorf("sahip, parolanın yanında saldırganın Apple'ı olan hesaba kondu; adres geri alınmalıydı")
	}
	if n := identityCount(t, gdb, u.ID, "password"); n != 0 {
		t.Errorf("eski hesabın parola kimliği kaldı (%d satır)", n)
	}
}
