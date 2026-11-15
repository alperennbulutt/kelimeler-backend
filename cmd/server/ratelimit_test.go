package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/config"

	"github.com/gofiber/fiber/v2"
)

// app.Test bağlantının karşı ucunu 0.0.0.0 olarak verir. Varsayılan güvenilen
// aralıklar bunu kapsamaz: doğrudan internetten bağlanan istemci gibi davranır.
// "0.0.0.0" güvenilenlere eklenince istek bir proxy'den gelmiş sayılır.
const testProxyIP = "0.0.0.0"

func limiterApp(t *testing.T, max int, trusted []string) (*fiber.App, *auth.TokenService) {
	t.Helper()
	tokens := auth.NewTokenService("test-secret-test-secret-test-secret", time.Hour, time.Hour)
	cfg := &config.Config{
		AppEnv:              "development",
		APIRateLimit:        max,
		APIRateLimitWindow:  time.Minute,
		AuthRateLimit:       max,
		AuthRateLimitWindow: time.Minute,
		ProxyHeader:         fiber.HeaderXForwardedFor,
		TrustedProxies:      trusted,
	}
	app := newApp(cfg, io.Discard)
	app.Use(apiLimiter(cfg, tokens))
	app.Get("/ok", func(c *fiber.Ctx) error { return c.SendString(c.IP()) })
	app.Post("/auth", authLimiter(cfg), func(c *fiber.Ctx) error { return c.SendString(c.IP()) })
	return app, tokens
}

func token(t *testing.T, ts *auth.TokenService, userID string) string {
	t.Helper()
	tok, err := ts.GenerateAccessToken(userID, userID+"@example.test", 0)
	if err != nil {
		t.Fatalf("token üretilemedi: %v", err)
	}
	return tok
}

type istekAyari struct {
	method string
	path   string
	xff    string
	bearer string
}

func durum(t *testing.T, app *fiber.App, a istekAyari) (int, string) {
	t.Helper()
	method, path := a.method, a.path
	if method == "" {
		method = http.MethodGet
	}
	if path == "" {
		path = "/ok"
	}
	req := httptest.NewRequest(method, path, nil)
	if a.xff != "" {
		req.Header.Set(fiber.HeaderXForwardedFor, a.xff)
	}
	if a.bearer != "" {
		req.Header.Set(fiber.HeaderAuthorization, "Bearer "+a.bearer)
	}
	resp, govde := istek(t, app, req)
	return resp.StatusCode, govde
}

// Proxy'den gelmeyen bağlantıda X-Forwarded-For dikkate alınmaz: her isteğe
// farklı başlık yazmak yeni bir kova açmaz.
func TestHizSiniri_GuvenilmeyenKaynaginXFFBasligiYokSayilir(t *testing.T) {
	for _, ad := range []string{"genel", "auth"} {
		t.Run(ad, func(t *testing.T) {
			app, _ := limiterApp(t, 2, config.DefaultTrustedProxies)
			a := istekAyari{}
			if ad == "auth" {
				a = istekAyari{method: http.MethodPost, path: "/auth"}
			}
			for i, xff := range []string{"1.1.1.1", "2.2.2.2"} {
				a.xff = xff
				if kod, ip := durum(t, app, a); kod != fiber.StatusOK || ip != testProxyIP {
					t.Fatalf("%d. istek: status = %d, IP = %q; beklenen 200 ve bağlantı IP'si", i+1, kod, ip)
				}
			}
			a.xff = "3.3.3.3"
			if kod, _ := durum(t, app, a); kod != fiber.StatusTooManyRequests {
				t.Errorf("uydurma XFF ile 3. istek: status = %d, beklenen 429", kod)
			}
		})
	}
}

func TestHizSiniri_GuvenilenProxydenGelenIPKullanilir(t *testing.T) {
	app, _ := limiterApp(t, 1, []string{testProxyIP})

	cases := []struct{ xff, want string }{
		{"203.0.113.7", "203.0.113.7"},
		// Zincirde ham metin değil ilk geçerli adres anahtar olur.
		{"uydurma, 198.51.100.9", "198.51.100.9"},
	}
	for _, c := range cases {
		if kod, ip := durum(t, app, istekAyari{xff: c.xff}); kod != fiber.StatusOK || ip != c.want {
			t.Errorf("XFF %q: status = %d, IP = %q; beklenen 200 %q", c.xff, kod, ip, c.want)
		}
	}
	// Aynı istemcinin ikinci isteği kendi kovasını doldurur.
	if kod, _ := durum(t, app, istekAyari{xff: "203.0.113.7"}); kod != fiber.StatusTooManyRequests {
		t.Errorf("aynı istemci 2. istek: status = %d, beklenen 429", kod)
	}
}

// Aynı IP'yi paylaşan (CGNAT) kullanıcılar genel sınırı birlikte doldurmaz.
func TestHizSiniri_KimligiDogrulanmisIstekKullaniciBasinaSayilir(t *testing.T) {
	app, ts := limiterApp(t, 2, nil)
	ali, ayse := token(t, ts, "ali"), token(t, ts, "ayse")

	for i := 0; i < 2; i++ {
		if kod, _ := durum(t, app, istekAyari{bearer: ali}); kod != fiber.StatusOK {
			t.Fatalf("ali %d. istek: status = %d, beklenen 200", i+1, kod)
		}
	}
	if kod, _ := durum(t, app, istekAyari{bearer: ali}); kod != fiber.StatusTooManyRequests {
		t.Errorf("ali 3. istek: status = %d, beklenen 429", kod)
	}
	if kod, _ := durum(t, app, istekAyari{bearer: ayse}); kod != fiber.StatusOK {
		t.Errorf("aynı IP'deki ayse: status = %d, beklenen 200 (kendi kovası)", kod)
	}
	// Kimliksiz istek IP kovasındadır; kullanıcı kovaları onu doldurmamıştır.
	if kod, _ := durum(t, app, istekAyari{}); kod != fiber.StatusOK {
		t.Errorf("kimliksiz istek: status = %d, beklenen 200 (IP kovası boş)", kod)
	}
}

// Geçersiz ya da başka bir anahtarla imzalanmış token yeni bir kova açmaz.
func TestHizSiniri_GecersizTokenIPKovasinaDuser(t *testing.T) {
	app, _ := limiterApp(t, 2, nil)
	sahte := auth.NewTokenService("baska-secret-baska-secret-baska-secret", time.Hour, time.Hour)

	tokenlar := []string{"bozuk", token(t, sahte, "u1"), token(t, sahte, "u2")}
	kodlar := make([]int, len(tokenlar))
	for i, tok := range tokenlar {
		kodlar[i], _ = durum(t, app, istekAyari{bearer: tok})
	}
	if kodlar[0] != fiber.StatusOK || kodlar[1] != fiber.StatusOK || kodlar[2] != fiber.StatusTooManyRequests {
		t.Errorf("geçersiz token'lı istekler: status = %v, beklenen [200 200 429] (tek IP kovası)", kodlar)
	}
}

// Eşzamanlı istekler kovayı yarışla aşamaz: aynı kullanıcının aynı anda gelen
// isteklerinden tam olarak Max kadarı geçer; aynı IP'deki başka bir kullanıcının
// ve kimliksiz isteklerin kovası bundan etkilenmez.
func TestHizSiniri_EszamanliIsteklerSiniriAsmaz(t *testing.T) {
	const max, n = 5, 40
	app, ts := limiterApp(t, max, nil)
	kovalar := map[string]string{
		"ali":       token(t, ts, "ali"),
		"ayse":      token(t, ts, "ayse"),
		"kimliksiz": "",
	}

	type sonuc struct {
		kova string
		kod  int
		err  error
	}
	sonuclar := make(chan sonuc, n*len(kovalar))
	var wg sync.WaitGroup
	basla := make(chan struct{})
	for kova, tok := range kovalar {
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(kova, tok string) {
				defer wg.Done()
				<-basla
				req := httptest.NewRequest(http.MethodGet, "/ok", nil)
				if tok != "" {
					req.Header.Set(fiber.HeaderAuthorization, "Bearer "+tok)
				}
				resp, err := app.Test(req, -1)
				if err != nil {
					sonuclar <- sonuc{kova: kova, err: err}
					return
				}
				resp.Body.Close()
				sonuclar <- sonuc{kova: kova, kod: resp.StatusCode}
			}(kova, tok)
		}
	}
	close(basla)
	wg.Wait()
	close(sonuclar)

	gecen := map[string]int{}
	for s := range sonuclar {
		switch {
		case s.err != nil:
			t.Fatalf("%s: istek başarısız: %v", s.kova, s.err)
		case s.kod == fiber.StatusOK:
			gecen[s.kova]++
		case s.kod != fiber.StatusTooManyRequests:
			t.Errorf("%s: beklenmeyen status %d", s.kova, s.kod)
		}
	}
	for kova := range kovalar {
		if gecen[kova] != max {
			t.Errorf("%s: %d eşzamanlı istekten %d tanesi geçti, beklenen tam %d", kova, n, gecen[kova], max)
		}
	}
}
