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
