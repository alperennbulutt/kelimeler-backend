package handlers

import (
	"strings"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/middleware"

	"gorm.io/gorm"
)

// Handlers, tüm HTTP handler'larının paylaştığı bağımlılıkları tutar.
type Handlers struct {
	DB     *gorm.DB
	Cfg    *config.Config
	Tokens *auth.TokenService
	Apple  *auth.AppleVerifier
}

func New(db *gorm.DB, cfg *config.Config, tokens *auth.TokenService, apple *auth.AppleVerifier) *Handlers {
	return &Handlers{DB: db, Cfg: cfg, Tokens: tokens, Apple: apple}
}

// apiErr, kısa yol.
func apiErr(status int, code, msg string) *middleware.APIError {
	return middleware.NewAPIError(status, code, msg)
}

func emailLocalPart(email string) string {
	if i := strings.Index(email, "@"); i > 0 {
		return email[:i]
	}
	return email
}
