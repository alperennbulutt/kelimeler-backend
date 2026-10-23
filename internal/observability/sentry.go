package observability

import (
	"log"
	"time"

	"github.com/getsentry/sentry-go"
)

// InitSentry, DSN verilmişse Sentry'yi başlatır ve kapatma fonksiyonu döner.
//
// DSN boşsa hiçbir şey yapmaz ve no-op bir kapatıcı döner; geliştirme
// ortamı ve DSN'siz derlemeler etkilenmez.
func InitSentry(dsn, environment, release string) func() {
	if dsn == "" {
		log.Println("sentry: SENTRY_DSN tanımlı değil, hata raporlama devre dışı")
		return func() {}
	}

	err := sentry.Init(sentry.ClientOptions{
		Dsn:         dsn,
		Environment: environment,
		Release:     release,

		// Performans izleme örneklemesi: ücretsiz kotayı korumak için düşük.
		EnableTracing:    true,
		TracesSampleRate: 0.1,

		// Kişisel veri gönderme. Gizlilik politikamız verilerin yalnızca
		// kendi sunucumuzda tutulduğunu söylüyor; IP/başlık göndermek
		// bu beyanı ihlal ederdi.
		SendDefaultPII: false,

		BeforeSend: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			// Authorization başlığı olaya sızmasın.
			if event.Request != nil {
				delete(event.Request.Headers, "Authorization")
				delete(event.Request.Headers, "Cookie")
			}
			return event
		},
	})
	if err != nil {
		log.Printf("sentry: başlatılamadı, hata raporlama devre dışı: %v", err)
		return func() {}
	}

	log.Printf("sentry: etkin (environment=%s)", environment)
	return func() { sentry.Flush(2 * time.Second) }
}

// CaptureError, bir hatayı Sentry'ye bildirir. Sentry kapalıysa no-op'tur.
func CaptureError(err error) {
	if err == nil {
		return
	}
	sentry.CaptureException(err)
}

// CaptureWarning, bir yapılandırma uyarısını Sentry'ye uyarı düzeyinde bildirir.
// Sentry kapalıysa no-op'tur.
func CaptureWarning(msg string) {
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelWarning)
		sentry.CaptureMessage(msg)
	})
}
