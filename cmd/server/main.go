package main

import (
	"context"
	"io"
	"log"
	"os"
	"strings"

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

func main() {
	cfg := config.Load()

	// Hata raporlamayı en başta kur ki başlangıç hataları da yakalansın.
	flushSentry := observability.InitSentry(cfg.SentryDSN, cfg.AppEnv, cfg.SentryRelease)
	defer flushSentry()

	// Günlük kullanım sayaçlarının gün sınırı. Ad çözülemezse varsayılan konum
	// korunur; hangi konumun geçerli olduğu her durumda loglanır.
	if err := services.SetUsageLocation(cfg.UsageTimezone); err != nil {
		log.Printf("uyarı: USAGE_TIMEZONE=%q yüklenemedi, varsayılan kullanılacak: %v", cfg.UsageTimezone, err)
	}
	log.Printf("kullanım günü saat dilimi: %s", services.UsageLocation())

	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db bağlantısı başarısız: %v", err)
	}
	if err := db.Migrate(database); err != nil {
		log.Fatalf("db migrate başarısız: %v", err)
	}
	// İlk açılışta (boş DB) imaja gömülü kelime sözlüğünü yükle.
	db.SeedDictionaryIfEmpty(database, "/seed/dictionary_words.sql")

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
		log.Printf("uyarı: Apple revoker başlatılamadı, hesap silmede token iptali yapılmayacak: %v", err)
		appleRevoker = nil
	} else if appleRevoker == nil && cfg.AppleClientID != "" {
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

	// Genel hız sınırı: tüm API uçları için IP başına.
	api.Use(limiter.New(limiter.Config{
		Max:        cfg.APIRateLimit,
		Expiration: cfg.APIRateLimitWindow,
		LimitReached: func(c *fiber.Ctx) error {
			return middleware.NewAPIError(fiber.StatusTooManyRequests,
				"rate_limited", "çok fazla istek gönderdiniz, lütfen biraz bekleyin")
		},
	}))

	handlers.RegisterRoutes(api, h, tokens, authLimiter(cfg))

	addr := ":" + cfg.Port
	log.Printf("kelimeler-backend %s adresinde dinliyor", addr)
	if err := app.Listen(addr); err != nil {
		log.Fatalf("server hatası: %v", err)
	}
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

		// Coolify/nginx arkasında gerçek istemci IP'si için; rate limit
		// doğru IP'yi görmezse tüm trafiği tek proxy IP'sine sayar.
		ProxyHeader: fiber.HeaderXForwardedFor,
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
	app.Use(logger.New(logger.Config{Output: accessLog}))
	app.Use(recover.New(recover.Config{EnableStackTrace: true}))
	if cfg.SentryDSN != "" {
		app.Use(sentryfiber.New(sentryfiber.Options{
			Repanic:         true, // yanıtı recover üretsin
			WaitForDelivery: false,
		}))
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
