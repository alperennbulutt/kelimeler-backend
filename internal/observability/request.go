package observability

import (
	"github.com/getsentry/sentry-go"
	sentryfiber "github.com/getsentry/sentry-go/fiber"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/utils"
)

// RequestIDHeader, isteği uçtan uca izlemek için kullanılan başlıktır. Gelen
// istekte geçerli bir değer varsa korunur (ör. proxy ya da istemci üretmiştir),
// yoksa üretilir; her yanıtta geri yazılır.
const RequestIDHeader = "X-Request-ID"

// RequestIDLocal, istek kimliğinin c.Locals'taki anahtarıdır; erişim logu
// ${locals:requestid} ile okur.
const RequestIDLocal = "requestid"

const maxRequestIDLen = 128

// RequestID, her isteğe bir kimlik atar ve yanıt başlığına yazar. Zincirin en
// dışında çalışmalıdır ki erişim logu ve hata işleyici de görsün.
func RequestID() fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Get(RequestIDHeader)
		if !validRequestID(id) {
			id = utils.UUIDv4()
		}
		c.Locals(RequestIDLocal, id)
		c.Set(RequestIDHeader, id)
		return c.Next()
	}
}

// validRequestID, dışarıdan gelen kimliği yalnızca kısa ve güvenli
// karakterlerden oluşuyorsa kabul eder: değer loglara ve Sentry etiketine
// yazıldığı için satır sonu ya da ayraç taşıyamaz.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		ch := id[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		case ch == '-', ch == '_', ch == '.', ch == ':':
		default:
			return false
		}
	}
	return true
}

// AppVersionHeader, mobil istemcinin sürümünü taşıyan isteğe bağlı başlıktır
// (ör. "1.0.3+57"). Yayındaki eski istemci göndermez.
const AppVersionHeader = "X-App-Version"

// AppPlatformHeader, mobil istemcinin platformunu ("ios" | "android") taşıyan
// isteğe bağlı başlıktır. Zorunlu güncelleme denetimi yalnızca bu başlık ve
// AppVersionHeader birlikte geldiğinde yapılır (bkz. middleware.RequireAppVersion).
const AppPlatformHeader = "X-App-Platform"

// Başlığı olmayan ya da geçersiz değer gönderen isteklerin sürüm etiketi.
const (
	AppVersionLegacy  = "eski"
	AppVersionInvalid = "gecersiz"
)

const maxAppVersionLen = 32

// AppVersionFrom, isteğin istemci sürümünü döner: başlık yoksa
// AppVersionLegacy, kısa ve güvenli karakterlerden oluşmuyorsa
// AppVersionInvalid. Değer log satırına ve Sentry etiketine yazıldığı için
// boşluk, satır sonu ya da "=" taşıyamaz.
func AppVersionFrom(c *fiber.Ctx) string {
	v := c.Get(AppVersionHeader)
	if v == "" {
		return AppVersionLegacy
	}
	if len(v) > maxAppVersionLen {
		return AppVersionInvalid
	}
	for i := 0; i < len(v); i++ {
		ch := v[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		case ch == '.', ch == '+', ch == '-', ch == '_':
		default:
			return AppVersionInvalid
		}
	}
	return v
}

// RequestIDFrom, RequestID middleware'inin atadığı kimliği döner; yoksa "".
func RequestIDFrom(c *fiber.Ctx) string {
	id, _ := c.Locals(RequestIDLocal).(string)
	return id
}

// TagSentryScope, isteğin Sentry hub'ına istek kimliğini etiket olarak ekler;
// panic olayları da bu etiketle gider. Sentry middleware'inin hemen içine konur.
func TagSentryScope(c *fiber.Ctx) error {
	if hub := sentryfiber.GetHubFromContext(c); hub != nil {
		if id := RequestIDFrom(c); id != "" {
			hub.Scope().SetTag("request_id", id)
		}
	}
	return c.Next()
}

// CaptureRequestError, bir istek sırasında oluşan hatayı istek kimliği ve
// rotasıyla Sentry'ye bildirir. Sentry kapalıysa no-op'tur.
func CaptureRequestError(c *fiber.Ctx, err error) {
	if err == nil {
		return
	}
	hub := sentryfiber.GetHubFromContext(c)
	if hub == nil {
		hub = sentry.CurrentHub().Clone()
	}
	hub.WithScope(func(scope *sentry.Scope) {
		if id := RequestIDFrom(c); id != "" {
			scope.SetTag("request_id", id)
		}
		scope.SetTag("http.method", c.Method())
		if r := c.Route(); r != nil && r.Path != "" {
			scope.SetTag("http.route", r.Path)
		}
		hub.CaptureException(err)
	})
}
