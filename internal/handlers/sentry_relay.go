package handlers

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"kelimeler-backend/internal/config"

	"github.com/gofiber/fiber/v2"
)

// Sentry rölesi sınırları.
const (
	// SentryRelayMaxBody, kabul edilen en büyük envelope gövdesidir (sıkıştırılmış
	// hâliyle). Bir çökme raporu birkaç on KB'dır.
	SentryRelayMaxBody = 1 << 20 // 1 MiB

	// sentryRelayTimeout, Sentry'ye yapılan isteğin üst süresidir.
	// SHUTDOWN_TIMEOUT varsayılanından (8 sn) kısa tutulur: Sentry yavaşken
	// yapılan bir redeploy kapanışı aşıp süreci hatayla bitirmesin.
	sentryRelayTimeout = 5 * time.Second

	// sentryRelayMaxResponse, Sentry yanıt gövdesinden istemciye aktarılan en
	// fazla bayt ({"id":"..."} gibi küçük bir JSON beklenir).
	sentryRelayMaxResponse = 64 << 10
)

// sentryRelayRequestHeaders, Sentry'ye aktarılan istek başlıklarıdır. Başka
// hiçbir başlık (X-Forwarded-For, Cookie, Authorization...) gitmez: istemcinin
// IP'si Sentry'ye sunucunun IP'si olarak görünür.
var sentryRelayRequestHeaders = []string{
	"X-Sentry-Auth",
	fiber.HeaderContentType,
	fiber.HeaderContentEncoding,
	fiber.HeaderUserAgent,
}

// sentryRelayResponseHeaders, Sentry yanıtından istemciye aktarılan
// başlıklardır. SDK hız sınırına uymak için Retry-After ve
// X-Sentry-Rate-Limits'e bakar.
var sentryRelayResponseHeaders = []string{
	fiber.HeaderContentType,
	fiber.HeaderRetryAfter,
	"X-Sentry-Rate-Limits",
}

// sentryRelayQueryParams, kimlik bilgisini sorguda gönderen SDK'lar için
// Sentry'ye aktarılan sorgu parametreleridir.
var sentryRelayQueryParams = []string{"sentry_key", "sentry_version", "sentry_client"}

// SentryRelay, mobil uygulamanın Sentry envelope'larını gerçek Sentry'ye
// aktarır. Türk Telekom Sentry'nin alım adreslerini (*.ingest.*.sentry.io)
// engellediği için uygulama raporlarını bu sunucuya gönderir.
type SentryRelay struct {
	// Target, SENTRY_RELAY_DSN'in ayrıştırılmış hâlidir.
	Target config.SentryDSN
	// Client, Sentry'ye istek atan istemcidir.
	Client *http.Client
	// UpstreamURL, envelope'un gönderildiği adrestir; varsayılanı
	// Target.EnvelopeURL(). Testlerde sahte sunucuya çevrilir.
	UpstreamURL string
}

// NewSentryRelay, hedef DSN için röleyi kurar.
func NewSentryRelay(target config.SentryDSN) *SentryRelay {
	return &SentryRelay{
		Target:      target,
		Client:      &http.Client{Timeout: sentryRelayTimeout},
		UpstreamURL: target.EnvelopeURL(),
	}
}

// SentryRelayEnvelope, POST /sentry/api/:projectId/envelope: uygulamanın
// Sentry SDK'sının gönderdiği envelope'u değiştirmeden Sentry'ye aktarır.
//
// Yalnızca yapılandırılmış projeye ve açık anahtara (X-Sentry-Auth'taki ya da
// sorgudaki sentry_key) ait istekler aktarılır; aksi halde 403. Röle kapalıysa
// 404, gövde 1 MiB'tan büyükse 413. Sentry'nin durum kodu ve küçük yanıt
// gövdesi olduğu gibi döner; Sentry'ye ulaşılamazsa 502.
//
// Gövde açılmaz, ayrıştırılmaz ve loglanmaz (gzip'li gelirse gzip'li gider).
func (h *Handlers) SentryRelayEnvelope(c *fiber.Ctx) error {
	r := h.SentryRelay
	if r == nil {
		return apiErr(fiber.StatusNotFound, "not_found", "Sentry rölesi kapalı")
	}

	if c.Params("projectId") != r.Target.ProjectID {
		return apiErr(fiber.StatusForbidden, "forbidden", "bilinmeyen Sentry projesi")
	}
	key := sentryKeyFromAuth(c.Get("X-Sentry-Auth"))
	if key == "" {
		key = c.Query("sentry_key")
	}
	if key != r.Target.PublicKey {
		return apiErr(fiber.StatusForbidden, "forbidden", "geçersiz Sentry anahtarı")
	}

	// c.Body() Content-Encoding'e göre gövdeyi açar; ham gövde aktarılır.
	body := c.Request().Body()
	if len(body) > SentryRelayMaxBody {
		return apiErr(fiber.StatusRequestEntityTooLarge, "payload_too_large", "envelope çok büyük")
	}

	upstream := r.UpstreamURL
	q := url.Values{}
	for _, p := range sentryRelayQueryParams {
		if v := c.Query(p); v != "" {
			q.Set(p, v)
		}
	}
	if len(q) > 0 {
		upstream += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(c.UserContext(), http.MethodPost, upstream, bytes.NewReader(body))
	if err != nil {
		log.Printf("sentry rölesi: istek kurulamadı: %v", err)
		return c.SendStatus(fiber.StatusBadGateway)
	}
	for _, k := range sentryRelayRequestHeaders {
		if v := c.Get(k); v != "" {
			req.Header.Set(k, v)
		}
	}
	if req.Header.Get(fiber.HeaderUserAgent) == "" {
		// Go'nun varsayılan User-Agent'ı yerine sabit bir değer.
		req.Header.Set(fiber.HeaderUserAgent, "kelimeler-sentry-relay")
	}

	resp, err := r.Client.Do(req)
	if err != nil {
		// Hata ErrorHandler'a bırakılmaz: Sentry'ye ulaşılamadığında her rapor
		// bir de sunucunun Sentry olayına dönüşürdü.
		log.Printf("sentry rölesi: Sentry'ye ulaşılamadı: %v", err)
		return c.SendStatus(fiber.StatusBadGateway)
	}
	defer resp.Body.Close()

	for _, k := range sentryRelayResponseHeaders {
		if v := resp.Header.Get(k); v != "" {
			c.Set(k, v)
		}
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, sentryRelayMaxResponse))
	return c.Status(resp.StatusCode).Send(respBody)
}

// sentryKeyFromAuth, "Sentry sentry_version=7, sentry_client=x, sentry_key=KEY"
// biçimindeki X-Sentry-Auth başlığından sentry_key'i okur; yoksa "" döner.
func sentryKeyFromAuth(h string) string {
	h = strings.TrimSpace(h)
	if len(h) >= len("Sentry ") && strings.EqualFold(h[:len("Sentry ")], "Sentry ") {
		h = h[len("Sentry "):]
	}
	for _, part := range strings.Split(h, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.TrimSpace(k) == "sentry_key" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
