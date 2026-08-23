package main

import (
	"context"
	"log"
	"strings"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/handlers"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/observability"
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

	// Sentry, panic'leri ve handler hatalarını yakalar; recover'dan önce
	// gelmeli ki panic olayı Sentry'ye ulaşsın.
	if cfg.SentryDSN != "" {
		app.Use(sentryfiber.New(sentryfiber.Options{
			Repanic:         true, // recover.New() yanıtı üretsin
			WaitForDelivery: false,
		}))
	}
	app.Use(recover.New())
	app.Use(logger.New())
	app.Use(helmet.New())
	app.Use(cors.New(corsConfig(cfg)))

	// Gizlilik politikası: mağaza listelemelerinden ve uygulama içinden açılan
	// herkese açık sayfa. Binary'ye gömülü olduğu için ayrı bir statik site
	// kaynağına gerek yok.
	privacy := func(c *fiber.Ctx) error {
		c.Type("html", "utf-8")
		c.Set(fiber.HeaderCacheControl, "public, max-age=3600")
		return c.Send(web.PrivacyPolicyHTML)
	}
	app.Get("/privacy", privacy)
	app.Get("/privacy-policy", privacy)
	app.Get("/gizlilik", privacy)
	app.Get("/", func(c *fiber.Ctx) error {
		return c.Redirect("/privacy", fiber.StatusFound)
	})

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

// corsConfig, izin verilen origin'leri yapılandırır.
//
// Mobil istemci CORS'tan etkilenmez (tarayıcı politikasıdır); bu ayar yalnızca
// web kaynaklı istekleri ilgilendirir. Production'da liste boşsa hiçbir origin'e
// izin verilmez — "*" ile herkese açmak, tarayıcıdaki bir sayfanın kullanıcı
// adına API'ye istek atabilmesi demektir.
func corsConfig(cfg *config.Config) cors.Config {
	if len(cfg.CORSAllowedOrigins) > 0 {
		return cors.Config{
			AllowOrigins: strings.Join(cfg.CORSAllowedOrigins, ","),
			AllowHeaders: "Origin, Content-Type, Accept, Authorization",
			AllowMethods: "GET, POST, PUT, PATCH, DELETE, OPTIONS",
		}
	}

	if cfg.AppEnv == "production" {
		log.Println("cors: CORS_ALLOWED_ORIGINS tanımlı değil — tarayıcı kaynaklı istekler reddedilecek")
		return cors.Config{AllowOrigins: ""}
	}

	// Geliştirme kolaylığı: lokalde her origin'e izin ver.
	log.Println("cors: geliştirme modu, tüm origin'lere izin veriliyor")
	return cors.Config{AllowOrigins: "*"}
}

// authLimiter, giriş/kayıt uçları için sıkı hız sınırı üretir.
// Parola deneme (credential stuffing) saldırılarını yavaşlatır.
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
