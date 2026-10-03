package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// Bu testler veritabanı gerektirmez: sayfalar binary'ye gömülüdür ve rotalar
// yalnızca Register ile kurulan bir Fiber uygulaması üzerinden sınanır.

var (
	gizlilikYollari   = []string{"/privacy", "/privacy-policy", "/privacy-policy.html", "/gizlilik"}
	hesapSilmeYollari = []string{"/account-deletion", "/account-deletion.html", "/hesap-silme"}
)

func testApp() *fiber.App {
	app := fiber.New()
	Register(app)
	return app
}

// istek, verilen yola GET atar; yanıtı ve gövdesini döner. Yönlendirmeler
// izlenmez, böylece 302 yanıtları olduğu gibi görülür.
func istek(t *testing.T, app *fiber.App, yol string) (*http.Response, string) {
	t.Helper()

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, yol, nil))
	if err != nil {
		t.Fatalf("GET %s başarısız: %v", yol, err)
	}
	defer resp.Body.Close()

	govde, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s gövdesi okunamadı: %v", yol, err)
	}
	return resp, string(govde)
}

// htmlSayfaBekle, yolun 200 ile ve tarayıcının sayfa olarak göstereceği
// başlıklarla yanıtlandığını doğrular; gövdeyi döner.
func htmlSayfaBekle(t *testing.T, app *fiber.App, yol string) string {
	t.Helper()

	resp, govde := istek(t, app, yol)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s durum = %d, beklenen 200", yol, resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("GET %s Content-Type = %q, beklenen %q", yol, got, "text/html; charset=utf-8")
	}
	if got := resp.Header.Get("Cache-Control"); got != "public, max-age=3600" {
		t.Errorf("GET %s Cache-Control = %q, beklenen %q", yol, got, "public, max-age=3600")
	}
	return govde
}

// goreliBaglantilar, sayfadaki aynı siteye giden (şemasız) bağlantıları döner.
// Dış adresler, mailto ve sayfa içi çapalar sunucunun rotalarını ilgilendirmez.
func goreliBaglantilar(html string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`href="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		href := m[1]
		u, err := url.Parse(href)
		if err != nil || u.Scheme != "" || u.Host != "" || strings.HasPrefix(href, "#") {
			continue
		}
		out = append(out, href)
	}
	return out
}

func TestRegister_GizlilikYollariGuncelSayfayiDoner(t *testing.T) {
	app := testApp()

	for _, yol := range gizlilikYollari {
		govde := htmlSayfaBekle(t, app, yol)

		if govde != string(PrivacyPolicyHTML) {
			t.Errorf("GET %s gömülü gizlilik sayfasını dönmedi", yol)
		}
		if !strings.Contains(govde, "devruler.support@gmail.com") {
			t.Errorf("GET %s gövdesinde güncel destek adresi yok", yol)
		}
		if strings.Contains(govde, "destek@kutularlaingilizce.com") {
			t.Errorf("GET %s gövdesinde kullanılmayan destek adresi duruyor", yol)
		}
	}
}

func TestRegister_HesapSilmeYollariSayfayiDoner(t *testing.T) {
	app := testApp()

	for _, yol := range hesapSilmeYollari {
		govde := htmlSayfaBekle(t, app, yol)

		if govde == "" {
			t.Fatalf("GET %s boş gövde döndü", yol)
		}
		if govde != string(AccountDeletionHTML) {
			t.Errorf("GET %s gömülü hesap silme sayfasını dönmedi", yol)
		}
		if !strings.Contains(govde, `href="privacy-policy.html"`) {
			t.Errorf("GET %s gövdesinde gizlilik politikası bağlantısı yok", yol)
		}
	}
}

// Sayfalar birbirine göreli bağlantı verir; tarayıcı bağlantıyı sayfanın
// açıldığı yola göre çözer. Hangi yoldan açılırsa açılsın, sayfadaki her
// göreli bağlantının bir rotası olmalı ve karşı sayfayı açmalı.
func TestRegister_SayfalardakiGoreliBaglantilarCalisir(t *testing.T) {
	app := testApp()

	sayfalar := []struct {
		yollar   []string
		baglanti string // sayfada bulunması gereken göreli bağlantı
		hedef    []byte // bağlantının açması gereken sayfa
	}{
		{gizlilikYollari, "account-deletion.html", AccountDeletionHTML},
		{hesapSilmeYollari, "privacy-policy.html", PrivacyPolicyHTML},
	}

	for _, s := range sayfalar {
		for _, yol := range s.yollar {
			baglantilar := goreliBaglantilar(htmlSayfaBekle(t, app, yol))
			if !slices.Contains(baglantilar, s.baglanti) {
				t.Errorf("%s sayfasında %q bağlantısı bulunamadı", yol, s.baglanti)
			}

			for _, href := range baglantilar {
				taban, _ := url.Parse(yol)
				ref, _ := url.Parse(href)
				cozulen := taban.ResolveReference(ref).Path

				resp, govde := istek(t, app, cozulen)
				if resp.StatusCode != http.StatusOK {
					t.Errorf("%s sayfasındaki %q bağlantısı %s yoluna çözüldü, durum = %d, beklenen 200",
						yol, href, cozulen, resp.StatusCode)
					continue
				}
				if href == s.baglanti && govde != string(s.hedef) {
					t.Errorf("%s sayfasındaki %q bağlantısı (%s) beklenen sayfayı açmadı", yol, href, cozulen)
				}
			}
		}
	}
}

// Sonu eğik çizgili adres sayfayı doğrudan dönerse göreli bağlantılar
// "/privacy/account-deletion.html" gibi var olmayan yollara çözülür.
func TestRegister_SonuEgikCizgiliYolAsilYolaYonlenir(t *testing.T) {
	app := testApp()

	yollar := append(append([]string{}, gizlilikYollari...), hesapSilmeYollari...)
	for _, yol := range yollar {
		resp, _ := istek(t, app, yol+"/")

		if resp.StatusCode != http.StatusFound {
			t.Errorf("GET %s/ durum = %d, beklenen 302", yol, resp.StatusCode)
		}
		if got := resp.Header.Get("Location"); got != yol {
			t.Errorf("GET %s/ Location = %q, beklenen %q", yol, got, yol)
		}
	}
}

func TestRegister_AppAdsTxtDuzMetinDoner(t *testing.T) {
	// AdMob'un tarama robotu satırı alan alan ayrıştırır; biçim ya da yayıncı
	// kimliği bozulursa doğrulama sessizce başarısız olur.
	const beklenen = "google.com, pub-2597183942350435, DIRECT, f08c47fec0942fa0\n"
	if AppAdsTxt != beklenen {
		t.Fatalf("AppAdsTxt = %q, beklenen %q", AppAdsTxt, beklenen)
	}

	resp, govde := istek(t, testApp(), "/app-ads.txt")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /app-ads.txt durum = %d, beklenen 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, beklenen %q", got, "text/plain; charset=utf-8")
	}
	if govde != AppAdsTxt {
		t.Errorf("gövde = %q, beklenen %q", govde, AppAdsTxt)
	}
}

func TestRegister_KokAdresGizlilikSayfasinaYonlenir(t *testing.T) {
	resp, _ := istek(t, testApp(), "/")

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("GET / durum = %d, beklenen 302", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/privacy" {
		t.Errorf("Location = %q, beklenen %q", got, "/privacy")
	}
}

func TestRegister_BilinmeyenYol404Doner(t *testing.T) {
	resp, _ := istek(t, testApp(), "/olmayan-sayfa.html")

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /olmayan-sayfa.html durum = %d, beklenen 404", resp.StatusCode)
	}
}
