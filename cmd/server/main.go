package main

import (
	"context"
	"log"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/handlers"
	"kelimeler-backend/internal/middleware"

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

	api := app.Group("/api/v1")
	handlers.RegisterRoutes(api, h, tokens)

	addr := ":" + cfg.Port
	log.Printf("kelimeler-backend %s adresinde dinliyor", addr)
	if err := app.Listen(addr); err != nil {
		log.Fatalf("server hatası: %v", err)
	}
}
