// Package web, binary'ye gömülü herkese açık sayfaları (gizlilik politikası,
// hesap silme, app-ads.txt) tutar ve servis eder. Coolify'de ayrı bir statik
// site kaynağı açmamak için sayfalar doğrudan API uygulaması tarafından
// servis edilir.
package web

import (
	_ "embed"

	"github.com/gofiber/fiber/v2"
)

// Sayfaların kaynağı Flutter reposundaki docs/ klasörüdür; buradaki dosyalar
// onun bayt bayt kopyasıdır. Elle düzenleme, `make sync-web` ile yeniden kopyala.

// PrivacyPolicyHTML, mağaza listelemelerinde verilen gizlilik politikası sayfası.
//
//go:embed privacy-policy.html
var PrivacyPolicyHTML []byte

// AccountDeletionHTML, uygulamaya erişimi olmayan kullanıcının hesabını ve
// verilerini nasıl sildireceğini anlatan sayfa. Google Play, hesap açılabilen
// uygulamalar için böyle bir web adresini şart koşar.
//
//go:embed account-deletion.html
var AccountDeletionHTML []byte

// AppAdsTxt, reklam envanterini satmaya yetkili satıcıları bildirir. Yayıncı
// kimliği uygulamanın AdMob kimliklerindeki (ca-app-pub-2597183942350435)
// kimliktir; sondaki değer Google'ın tüm yayıncılar için aynı olan sertifika
// kimliğidir.
const AppAdsTxt = "google.com, pub-2597183942350435, DIRECT, f08c47fec0942fa0\n"

// Metin seyrek değişir; bir saatlik önbellek, güncellemenin yayılmasını
// geciktirmeden tekrar eden istekleri keser.
const pageCacheControl = "public, max-age=3600"

// Register, herkese açık sayfaların rotalarını uygulamaya ekler.
//
// ".html" uzantılı yollar bilinçlidir: sayfalar birbirine göreli bağlantı verir
// (privacy-policy.html <-> account-deletion.html), yani /privacy'deki bağlantıya
// tıklayan tarayıcı /account-deletion.html ister.
func Register(app *fiber.App) {
	privacy := page(PrivacyPolicyHTML)
	app.Get("/privacy", privacy)
	app.Get("/privacy-policy", privacy)
	app.Get("/privacy-policy.html", privacy)
	app.Get("/gizlilik", privacy)

	deletion := page(AccountDeletionHTML)
	app.Get("/account-deletion", deletion)
	app.Get("/account-deletion.html", deletion)
	app.Get("/hesap-silme", deletion)

	app.Get("/app-ads.txt", func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, fiber.MIMETextPlainCharsetUTF8)
		return c.SendString(AppAdsTxt)
	})

	app.Get("/", func(c *fiber.Ctx) error {
		return c.Redirect("/privacy", fiber.StatusFound)
	})
}

// page, gömülü bir HTML sayfasını servis eden handler üretir.
func page(body []byte) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Fiber "/privacy/" isteğini de bu rotayla eşleştirir; o adresten
		// sayfadaki göreli bağlantılar "/privacy/account-deletion.html" gibi
		// var olmayan yollara çözülür. Rotanın kendi yoluna yönlendir.
		if p := c.Path(); len(p) > 1 && p[len(p)-1] == '/' {
			return c.Redirect(c.Route().Path, fiber.StatusFound)
		}
		c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
		c.Set(fiber.HeaderCacheControl, pageCacheControl)
		return c.Send(body)
	}
}
