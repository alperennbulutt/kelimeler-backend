package main

import (
	"context"
	"log"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/handlers"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/web"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

func main() {
	cfg := config.Load()

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

	h := handlers.New(database, cfg, tokens, appleVerifier)

	app := fiber.New(fiber.Config{
		AppName:      "kelimeler-backend",
		ErrorHandler: middleware.ErrorHandler,
	})
	app.Use(recover.New())
	app.Use(logger.New())
	app.Use(cors.New())

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
	handlers.RegisterRoutes(api, h, tokens)

	addr := ":" + cfg.Port
	log.Printf("kelimeler-backend %s adresinde dinliyor", addr)
	if err := app.Listen(addr); err != nil {
		log.Fatalf("server hatası: %v", err)
	}
}
