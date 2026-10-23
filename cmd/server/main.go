package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/handlers"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/observability"
	"kelimeler-backend/internal/services"
	"kelimeler-backend/internal/web"

	sentryfiber "github.com/getsentry/sentry-go/fiber"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/helmet"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

// seedPath, imaja gömülü sözlük dökümünün yoludur (bkz. Dockerfile).
const seedPath = "/seed/dictionary_words.sql"

func main() {
	// log.Fatalf defer'ları atlar; hatalar run'dan döner ki Sentry kuyruğu
	// boşaltılsın ve veritabanı bağlantıları kapatılsın.
	if err := run(); err != nil {
		log.Printf("kelimeler-backend durdu: %v", err)
		os.Exit(1)
	}
}

// run, sunucuyu kurar ve SIGTERM/SIGINT gelene kadar çalıştırır.
func run() (err error) {
	cfg := config.Load()

	// Hata raporlamayı en başta kur ki başlangıç hataları da yakalansın.
	// Defer'lar ters sırayla çalışır: önce hata bildirilir, sonra kuyruk boşaltılır.
	flushSentry := observability.InitSentry(cfg.SentryDSN, cfg.AppEnv, cfg.SentryRelease)
	defer flushSentry()
	defer func() {
		if err != nil {
			observability.CaptureError(err)
		}
	}()

	log.Printf("ortam: APP_ENV=%s GATE_ENFORCEMENT=%s", cfg.AppEnv, cfg.GateEnforcement)
	for _, w := range cfg.Warnings() {
		log.Printf("config: uyarı: %s", w)
		observability.CaptureWarning("config: " + w)
	}

	// Günlük kullanım sayaçlarının gün sınırı. Ad çözülemezse varsayılan konum
	// korunur; hangi konumun geçerli olduğu her durumda loglanır.
	if err := services.SetUsageLocation(cfg.UsageTimezone); err != nil {
		log.Printf("uyarı: USAGE_TIMEZONE=%q yüklenemedi, varsayılan kullanılacak: %v", cfg.UsageTimezone, err)
	}
	log.Printf("kullanım günü saat dilimi: %s", services.UsageLocation())

	database, err := db.ConnectWithOptions(cfg.DatabaseURL, db.Options{
		MaxOpenConns:     cfg.DBMaxOpenConns,
		MaxIdleConns:     cfg.DBMaxIdleConns,
		ConnMaxLifetime:  cfg.DBConnMaxLifetime,
		StatementTimeout: cfg.DBStatementTimeout,
	})
	if err != nil {
		return fmt.Errorf("db bağlantısı başarısız: %w", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		return fmt.Errorf("db havuzu alınamadı: %w", err)
	}
	// Sunucu kapandıktan sonra çalışır: süren istekler bağlantılarını bitirmiş olur.
	defer sqlDB.Close()

	if err := db.Migrate(database); err != nil {
		return fmt.Errorf("db migrate başarısız: %w", err)
	}
	// İlk açılışta (boş DB) imaja gömülü kelime sözlüğünü yükle. Production'da
	// sözlüksüz açılmak yerine durulur: deploy başarısız görünür, Coolify eski
	// container'ı çalıştırmaya devam eder.
	if _, err := db.SeedDictionaryIfEmpty(database, seedPath); err != nil {
		if cfg.IsProduction() {
			return fmt.Errorf("sözlük yüklenemedi: %w", err)
		}
		log.Printf("uyarı: sözlük yüklenemedi, kelimeler gösterilemeyecek: %v", err)
	}

	tokens := auth.NewTokenService(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL)

	// Apple JWKS'i yüklemeye çalış; başarısız olursa Apple girişi devre dışı kalır.
	appleVerifier, err := auth.NewAppleVerifier(context.Background(), cfg.AppleClientID, cfg.AppleIssuer)
	if err != nil {
		log.Printf("uyarı: Apple verifier başlatılamadı, Apple girişi devre dışı: %v", err)
		appleVerifier = nil
	}

	// Apple hesap silme (revoke) akışı: TEAM_ID + KEY_ID + PRIVATE_KEY verilmezse
	// devre dışı kalır ve hesap silme revoke olmadan sürer.
	appleRevoker, err := auth.NewAppleRevoker(
		cfg.AppleClientID, cfg.AppleTeamID, cfg.AppleKeyID, cfg.ApplePrivateKey)
	if err != nil {
		msg := fmt.Sprintf("Apple revoker başlatılamadı, hesap silmede token iptali yapılmayacak: %v", err)
		log.Printf("uyarı: %s", msg)
		if cfg.IsProduction() {
			observability.CaptureWarning(msg)
		}
		appleRevoker = nil
	} else if appleRevoker == nil && cfg.AppleClientID != "" && !cfg.IsProduction() {
		// Production'da aynı uyarı cfg.Warnings() ile Sentry'ye de gider.
		log.Printf("uyarı: APPLE_TEAM_ID/APPLE_KEY_ID/APPLE_PRIVATE_KEY eksik — " +
			"Apple hesap silmede token iptali YAPILMAYACAK (Apple bunu şart koşar)")
	}

	h := handlers.New(database, cfg, tokens, appleVerifier).WithAppleRevoker(appleRevoker)

	app := newApp(cfg, os.Stdout)

	// Herkese açık sayfalar (gizlilik politikası, hesap silme, app-ads.txt):
	// mağaza listelemelerinden açılır. Binary'ye gömülü oldukları için ayrı bir
	// statik site kaynağına gerek yok.
	web.Register(app)

	api := app.Group("/api/v1")

	// Genel hız sınırı: tüm API uçları için kullanıcı ya da IP başına.
	api.Use(apiLimiter(cfg, tokens))

	handlers.RegisterRoutes(api, h, tokens, authLimiter(cfg))

	addr := ":" + cfg.Port
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("%s dinlenemedi: %w", addr, err)
	}
	log.Printf("kelimeler-backend %s adresinde dinliyor", addr)

	// Coolify her redeploy'da eski container'a SIGTERM gönderir.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, app, ln, cfg.ShutdownTimeout)
}

// serve, uygulamayı ln üzerinde ctx iptal edilene kadar çalıştırır; sonra yeni
// bağlantı kabul etmeyi bırakır ve süren isteklerin bitmesini en fazla timeout
// kadar bekler.
//
// Kapanma beklenmeden süreç bitseydi o anda işlenen istekler (ör. reklam
// sonrası grup açma) yanıtsız kalır, istemci bunları ağ hatası olarak görüp
// yeniden denerdi.
func serve(ctx context.Context, app *fiber.App, ln net.Listener, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- app.Listener(ln) }()

	select {
	case err := <-done:
		// Sinyal gelmeden dinleme bitti: gerçek bir sunucu hatasıdır.
		return fmt.Errorf("server hatası: %w", err)
	case <-ctx.Done():
	}

	log.Printf("kapanma sinyali alındı; süren istekler en fazla %s bekleniyor", timeout)
	if err := app.ShutdownWithTimeout(timeout); err != nil {
		return fmt.Errorf("düzgün kapanma %s içinde tamamlanamadı: %w", timeout, err)
	}
	// Listener kapanınca döner; sinyal dinleme başlamadan geldiyse dönmeyebilir.
	select {
	case <-done:
	case <-time.After(timeout):
	}
	log.Println("sunucu düzgün kapandı")
	return nil
}

// newApp, Fiber uygulamasını ve her isteğe uygulanan middleware zincirini kurar.
// accessLog, istek satırlarının yazıldığı hedeftir.
func newApp(cfg *config.Config, accessLog io.Writer) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName:      "kelimeler-backend",
		ErrorHandler: middleware.ErrorHandler,

		// Yavaş/asılı bağlantıların bağlantı havuzunu tüketmesini engeller.
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,

		// Devasa gövdelerle bellek şişirmeyi engeller (en büyük istek birkaç KB'dır).
		BodyLimit: cfg.BodyLimit,

		// Coolify/Traefik arkasında gerçek istemci IP'si için; rate limit
		// doğru IP'yi görmezse tüm trafiği tek proxy IP'sine sayar.
		//
		// Başlık yalnızca bağlantı güvenilen bir proxy'den geliyorsa okunur:
		// doğrudan bağlanan biri her isteğe farklı X-Forwarded-For yazıp hız
		// sınırını atlatamaz. IP doğrulaması, "a, b" gibi zincirlerde ham
		// metin yerine ilk geçerli adresin anahtar olmasını sağlar.
		ProxyHeader:             cfg.ProxyHeader,
		EnableTrustedProxyCheck: true,
		TrustedProxies:          cfg.TrustedProxies,
		EnableIPValidation:      true,
	})

	// Fiber'da ilk kaydedilen middleware en dışta çalışır; bir panic ise en içten
	// dışa doğru açılır. Sıra buna göre kurulur:
	//
	//   logger  (en dış) recover'ın döndürdüğü hatayı yanıta çevirir ve istek
	//           satırını yazar; daha içte olsaydı panic'te satır hiç yazılmazdı.
	//   recover panic'i hataya çevirir (istemci 500 alır, süreç ayakta kalır) ve
	//           stack'i stderr'e yazar.
	//   sentry  (en iç) panic'i recover'dan önce görür, olayı gönderir ve yeniden
	//           fırlatır (Repanic). recover'ın dışında olsaydı panic ona hiç
	//           ulaşmazdı.
	//
	// İstek kimliği hepsinden önce atanır ki erişim logu, hata işleyici ve
	// Sentry olayı aynı kimliği taşısın.
	app.Use(observability.RequestID())
	app.Use(logger.New(logger.Config{Output: accessLog, Format: accessLogFormat}))
	app.Use(recover.New(recover.Config{EnableStackTrace: true, StackTraceHandler: stackTrace}))
	if cfg.SentryDSN != "" {
		app.Use(sentryfiber.New(sentryfiber.Options{
			Repanic:         true, // yanıtı recover üretsin
			WaitForDelivery: false,
		}))
		app.Use(observability.TagSentryScope)
		// Hata yanıta en dışta (logger) çevrilir; Sentry transaction'ı ondan önce
		// kapanır. Durum kodu burada yazılmazsa hatalı istekler Sentry'de 200 görünür.
		app.Use(middleware.WriteErrorStatus)
	}
	app.Use(helmet.New())
	if corsCfg, ok := corsConfig(cfg); ok {
		app.Use(cors.New(corsCfg))
	}

	return app
}

// accessLogFormat, Fiber'ın varsayılan erişim logu biçimine istek kimliğini ekler.
const accessLogFormat = "${time} | ${status} | ${latency} | ${ip} | ${method} | ${path} | ${locals:requestid} | ${error}\n"

// stackTrace, panic'in stack'ini stderr'e yazar (Fiber'ın varsayılanıyla aynı)
// ve isteği panic'li olarak işaretler: panic Sentry'ye middleware'de zaten
// gitmiştir, ErrorHandler tekrar göndermez.
func stackTrace(c *fiber.Ctx, e any) {
	middleware.MarkPanic(c)
	_, _ = fmt.Fprintf(os.Stderr, "panic: %v (request_id=%s)\n\n%s\n",
		e, observability.RequestIDFrom(c), debug.Stack())
}

// corsConfig, izin verilen origin'leri yapılandırır; ok=false ise CORS
// middleware'i hiç kaydedilmemelidir.
//
// Mobil istemci CORS'tan etkilenmez (tarayıcı politikasıdır); bu ayar yalnızca
// web kaynaklı istekleri ilgilendirir. Production'da liste boşsa hiçbir origin'e
// izin verilmez — "*" ile herkese açmak, tarayıcıdaki herhangi bir sayfanın
// API yanıtlarını okuyabilmesi demektir.
//
// "Hiçbir origin" boş bir AllowOrigins ile ifade edilemez: Fiber boş değeri
// varsayılanına ("*") çevirir. Middleware kaydedilmezse yanıtlara hiçbir CORS
// başlığı yazılmaz ve tarayıcı başka origin'den gelen isteği engeller.
func corsConfig(cfg *config.Config) (corsCfg cors.Config, ok bool) {
	if len(cfg.CORSAllowedOrigins) > 0 {
		return cors.Config{
			AllowOrigins: strings.Join(cfg.CORSAllowedOrigins, ","),
			AllowHeaders: "Origin, Content-Type, Accept, Authorization",
			AllowMethods: "GET, POST, PUT, PATCH, DELETE, OPTIONS",
		}, true
	}

	if cfg.AppEnv == "production" {
		log.Println("cors: CORS_ALLOWED_ORIGINS tanımlı değil — hiçbir origin'e CORS izni verilmeyecek")
		return cors.Config{}, false
	}

	// Geliştirme kolaylığı: lokalde her origin'e izin ver.
	log.Println("cors: geliştirme modu, tüm origin'lere izin veriliyor")
	return cors.Config{AllowOrigins: "*"}, true
}

// apiLimiter, tüm API uçlarına uygulanan genel hız sınırıdır (API_RATE_LIMIT).
func apiLimiter(cfg *config.Config, tokens *auth.TokenService) fiber.Handler {
	return limiter.New(limiter.Config{
		Max:          cfg.APIRateLimit,
		Expiration:   cfg.APIRateLimitWindow,
		KeyGenerator: rateLimitKey(tokens),
		LimitReached: func(c *fiber.Ctx) error {
			return middleware.NewAPIError(fiber.StatusTooManyRequests,
				"rate_limited", "çok fazla istek gönderdiniz, lütfen biraz bekleyin")
		},
	})
}

// rateLimitKey, geçerli bir access token taşıyan isteği kullanıcısına, diğerlerini
// istemci IP'sine sayar.
//
// Kimliği doğrulanmış uçlarda IP anahtarı, aynı operatör IP'sini paylaşan (CGNAT)
// kullanıcıların sınırı birlikte doldurması demekti. Token imzası doğrulandığı
// için uydurma bir token yeni bir kova açamaz; geçersiz ya da süresi dolmuş token
// IP kovasına düşer.
func rateLimitKey(tokens *auth.TokenService) func(*fiber.Ctx) string {
	return func(c *fiber.Ctx) string {
		if raw, ok := strings.CutPrefix(c.Get(fiber.HeaderAuthorization), "Bearer "); ok {
			if claims, err := tokens.ParseAccessToken(raw); err == nil && claims.Subject != "" {
				return "user:" + claims.Subject
			}
		}
		return "ip:" + c.IP()
	}
}

// authLimiter, kimlik üreten uçlar (Google/Apple girişi; production dışında
// parola ile kayıt/giriş) için sıkı hız sınırı üretir. Deneme saldırılarını
// yavaşlatır.
func authLimiter(cfg *config.Config) fiber.Handler {
	return limiter.New(limiter.Config{
		Max:        cfg.AuthRateLimit,
		Expiration: cfg.AuthRateLimitWindow,
		LimitReached: func(c *fiber.Ctx) error {
			return middleware.NewAPIError(fiber.StatusTooManyRequests,
				"rate_limited", "çok fazla deneme yaptınız, lütfen biraz bekleyin")
		},
	})
}
