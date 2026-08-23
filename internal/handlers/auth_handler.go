package handlers

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// --- ortak yardımcılar ---

// linkedProviders, kullanıcının bağlı giriş yöntemlerini ('password','google','apple') döndürür.
func (h *Handlers) linkedProviders(userID string) []string {
	providers := []string{}
	h.DB.Model(&models.AuthIdentity{}).
		Where("user_id = ?", userID).
		Order("provider").
		Pluck("provider", &providers)
	return providers
}

// issueTokens, access + refresh token üretir ve refresh hash'ini DB'ye yazar.
func (h *Handlers) issueTokens(user *models.User) (*dto.AuthResponse, error) {
	access, err := h.Tokens.GenerateAccessToken(user.ID, user.Email)
	if err != nil {
		return nil, err
	}
	refresh, err := auth.GenerateRefreshToken()
	if err != nil {
		return nil, err
	}
	rt := models.RefreshToken{
		UserID:    user.ID,
		TokenHash: auth.HashToken(refresh),
		ExpiresAt: time.Now().Add(h.Tokens.RefreshTTL()),
	}
	if err := h.DB.Create(&rt).Error; err != nil {
		return nil, err
	}
	return &dto.AuthResponse{
		AccessToken:  access,
		RefreshToken: refresh,
		User:         dto.NewUserDTO(user, h.linkedProviders(user.ID)),
	}, nil
}

func (h *Handlers) respondUser(c *fiber.Ctx, userID string) error {
	var user models.User
	if err := h.DB.First(&user, "id = ?", userID).Error; err != nil {
		return apiErr(fiber.StatusNotFound, "user_not_found", "kullanıcı bulunamadı")
	}
	return c.JSON(fiber.Map{"user": dto.NewUserDTO(&user, h.linkedProviders(user.ID))})
}

// --- endpoint'ler ---

// Register: email/parola ile yeni kullanıcı + 'password' kimliği oluşturur.
func (h *Handlers) Register(c *fiber.Ctx) error {
	var req dto.RegisterRequest
	if err := c.BodyParser(&req); err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "geçersiz istek")
	}
	req.Email = dto.NormalizeEmail(req.Email)
	if err := dto.ValidateEmail(req.Email); err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid_email", err.Error())
	}
	if err := dto.ValidatePassword(req.Password); err != nil {
		return apiErr(fiber.StatusBadRequest, "weak_password", err.Error())
	}
	displayNameInput, err := dto.NormalizeDisplayName(req.DisplayName)
	if err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid_display_name", err.Error())
	}
	req.DisplayName = displayNameInput

	var count int64
	h.DB.Model(&models.User{}).Where("email = ?", req.Email).Count(&count)
	if count > 0 {
		return apiErr(fiber.StatusConflict, "email_in_use", "bu email zaten kullanımda")
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return err
	}
	displayName := req.DisplayName
	if displayName == "" {
		displayName = emailLocalPart(req.Email)
	}

	user := models.User{
		Email:        req.Email,
		DisplayName:  displayName,
		CurrentLevel: "A1",
		CurrentGroup: 1,
	}
	err = h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		identity := models.AuthIdentity{
			UserID:       user.ID,
			Provider:     "password",
			PasswordHash: &hash,
			Email:        &req.Email,
		}
		return tx.Create(&identity).Error
	})
	if err != nil {
		return err
	}

	resp, err := h.issueTokens(&user)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(resp)
}

// Login: email/parola doğrular.
func (h *Handlers) Login(c *fiber.Ctx) error {
	var req dto.LoginRequest
	if err := c.BodyParser(&req); err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "geçersiz istek")
	}
	req.Email = dto.NormalizeEmail(req.Email)

	var user models.User
	if err := h.DB.Where("email = ?", req.Email).First(&user).Error; err != nil {
		return apiErr(fiber.StatusUnauthorized, "invalid_credentials", "email veya parola hatalı")
	}
	var identity models.AuthIdentity
	if err := h.DB.Where("user_id = ? AND provider = ?", user.ID, "password").First(&identity).Error; err != nil {
		return apiErr(fiber.StatusUnauthorized, "invalid_credentials", "email veya parola hatalı")
	}
	if identity.PasswordHash == nil || !auth.VerifyPassword(*identity.PasswordHash, req.Password) {
		return apiErr(fiber.StatusUnauthorized, "invalid_credentials", "email veya parola hatalı")
	}

	resp, err := h.issueTokens(&user)
	if err != nil {
		return err
	}
	return c.JSON(resp)
}

// GoogleAuth: Google idToken'ını doğrular, kullanıcıyı bulur/oluşturur.
func (h *Handlers) GoogleAuth(c *fiber.Ctx) error {
	var req dto.GoogleAuthRequest
	if err := c.BodyParser(&req); err != nil || req.IDToken == "" {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "idToken gerekli")
	}
	si, err := auth.VerifyGoogleIDToken(c.UserContext(), req.IDToken, h.Cfg.GoogleClientIDs)
	if err != nil {
		return apiErr(fiber.StatusUnauthorized, "invalid_google_token", "Google token doğrulanamadı")
	}
	user, err := h.authenticateSocial("google", si, req.DisplayName)
	if err != nil {
		return err
	}
	resp, err := h.issueTokens(user)
	if err != nil {
		return err
	}
	return c.JSON(resp)
}

// AppleAuth: Apple identityToken'ını doğrular, kullanıcıyı bulur/oluşturur.
func (h *Handlers) AppleAuth(c *fiber.Ctx) error {
	if h.Apple == nil {
		return apiErr(fiber.StatusServiceUnavailable, "apple_not_configured", "Apple girişi yapılandırılmamış")
	}
	var req dto.AppleAuthRequest
	if err := c.BodyParser(&req); err != nil || req.IdentityToken == "" {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "identityToken gerekli")
	}
	si, err := h.Apple.Verify(req.IdentityToken)
	if err != nil {
		return apiErr(fiber.StatusUnauthorized, "invalid_apple_token", "Apple token doğrulanamadı")
	}
	user, err := h.authenticateSocial("apple", si, req.FullName)
	if err != nil {
		return err
	}
	h.storeAppleRefreshToken(context.Background(), user.ID, si.Subject, req.AuthorizationCode)

	resp, err := h.issueTokens(user)
	if err != nil {
		return err
	}
	return c.JSON(resp)
}

// storeAppleRefreshToken, authorization code'u Apple refresh token'a çevirip
// kimlik satırına yazar. Hesap silinirken revoke için gereklidir.
//
// Bilinçli olarak "best effort": başarısız olursa girişi bozmaz, yalnızca log'lar.
// Revoke yapılamaması kullanıcıyı uygulamadan dışlamaktan iyidir; eksiklik
// hesap silme sırasında ayrıca raporlanır.
func (h *Handlers) storeAppleRefreshToken(ctx context.Context, userID, subject, code string) {
	if h.AppleRevoker == nil || code == "" {
		return
	}

	refreshToken, err := h.AppleRevoker.ExchangeCode(ctx, code)
	if err != nil {
		log.Printf("apple: authorization code refresh token'a çevrilemedi (user=%s): %v", userID, err)
		return
	}

	if err := h.DB.Model(&models.AuthIdentity{}).
		Where("user_id = ? AND provider = ? AND provider_subject = ?", userID, "apple", subject).
		Update("apple_refresh_token", refreshToken).Error; err != nil {
		log.Printf("apple: refresh token saklanamadı (user=%s): %v", userID, err)
	}
}

// authenticateSocial: provider+subject ile bul; yoksa email ile eşle/oluştur ve kimliği bağla.
func (h *Handlers) authenticateSocial(provider string, si *auth.SocialIdentity, displayName string) (*models.User, error) {
	var identity models.AuthIdentity
	err := h.DB.Where("provider = ? AND provider_subject = ?", provider, si.Subject).First(&identity).Error
	if err == nil {
		var user models.User
		if err := h.DB.First(&user, "id = ?", identity.UserID).Error; err != nil {
			return nil, err
		}
		return &user, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	email := strings.ToLower(strings.TrimSpace(si.Email))
	if email == "" {
		// Apple "hide my email" / sonraki girişlerde email vermeyebilir.
		email = provider + "_" + si.Subject + "@users.kelimeler.local"
	}

	var user models.User
	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		findErr := tx.Where("email = ?", email).First(&user).Error
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			name := displayName
			if name == "" {
				name = si.Name
			}
			if name == "" {
				name = emailLocalPart(email)
			}
			user = models.User{Email: email, DisplayName: name, CurrentLevel: "A1", CurrentGroup: 1}
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
		} else if findErr != nil {
			return findErr
		}
		sub := si.Subject
		return tx.Create(&models.AuthIdentity{
			UserID:          user.ID,
			Provider:        provider,
			ProviderSubject: &sub,
			Email:           &email,
		}).Error
	})
	if txErr != nil {
		return nil, txErr
	}
	return &user, nil
}

// Refresh: refresh token'ı doğrular, rotasyonla yeni çift üretir.
func (h *Handlers) Refresh(c *fiber.Ctx) error {
	var req dto.RefreshRequest
	if err := c.BodyParser(&req); err != nil || req.RefreshToken == "" {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "refreshToken gerekli")
	}
	var rt models.RefreshToken
	if err := h.DB.Where("token_hash = ?", auth.HashToken(req.RefreshToken)).First(&rt).Error; err != nil {
		return apiErr(fiber.StatusUnauthorized, "invalid_refresh_token", "geçersiz refresh token")
	}
	if rt.RevokedAt != nil || time.Now().After(rt.ExpiresAt) {
		return apiErr(fiber.StatusUnauthorized, "invalid_refresh_token", "refresh token süresi dolmuş")
	}
	var user models.User
	if err := h.DB.First(&user, "id = ?", rt.UserID).Error; err != nil {
		return apiErr(fiber.StatusUnauthorized, "invalid_refresh_token", "kullanıcı yok")
	}

	now := time.Now()
	rt.RevokedAt = &now
	h.DB.Save(&rt)

	resp, err := h.issueTokens(&user)
	if err != nil {
		return err
	}
	return c.JSON(resp)
}

// Me: oturum sahibinin profilini döndürür.
func (h *Handlers) Me(c *fiber.Ctx) error {
	return h.respondUser(c, middleware.UserID(c))
}

// Logout: verilen refresh token'ı iptal eder.
func (h *Handlers) Logout(c *fiber.Ctx) error {
	var req dto.LogoutRequest
	if err := c.BodyParser(&req); err == nil && req.RefreshToken != "" {
		h.DB.Model(&models.RefreshToken{}).
			Where("token_hash = ?", auth.HashToken(req.RefreshToken)).
			Update("revoked_at", time.Now())
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// LinkGoogle: oturum sahibine Google kimliği bağlar.
func (h *Handlers) LinkGoogle(c *fiber.Ctx) error {
	var req dto.GoogleAuthRequest
	if err := c.BodyParser(&req); err != nil || req.IDToken == "" {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "idToken gerekli")
	}
	si, err := auth.VerifyGoogleIDToken(c.UserContext(), req.IDToken, h.Cfg.GoogleClientIDs)
	if err != nil {
		return apiErr(fiber.StatusUnauthorized, "invalid_google_token", "Google token doğrulanamadı")
	}
	return h.linkProvider(c, middleware.UserID(c), "google", si)
}

// LinkApple: oturum sahibine Apple kimliği bağlar.
func (h *Handlers) LinkApple(c *fiber.Ctx) error {
	if h.Apple == nil {
		return apiErr(fiber.StatusServiceUnavailable, "apple_not_configured", "Apple girişi yapılandırılmamış")
	}
	var req dto.AppleAuthRequest
	if err := c.BodyParser(&req); err != nil || req.IdentityToken == "" {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "identityToken gerekli")
	}
	si, err := h.Apple.Verify(req.IdentityToken)
	if err != nil {
		return apiErr(fiber.StatusUnauthorized, "invalid_apple_token", "Apple token doğrulanamadı")
	}
	userID := middleware.UserID(c)
	if err := h.linkProvider(c, userID, "apple", si); err != nil {
		return err
	}
	// Bağlama başarılıysa revoke için refresh token'ı da al.
	h.storeAppleRefreshToken(context.Background(), userID, si.Subject, req.AuthorizationCode)
	return nil
}

func (h *Handlers) linkProvider(c *fiber.Ctx, userID, provider string, si *auth.SocialIdentity) error {
	var existing models.AuthIdentity
	err := h.DB.Where("provider = ? AND provider_subject = ?", provider, si.Subject).First(&existing).Error
	if err == nil {
		if existing.UserID != userID {
			return apiErr(fiber.StatusConflict, "provider_already_linked", "bu hesap başka bir kullanıcıya bağlı")
		}
		return h.respondUser(c, userID) // zaten bağlı
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	sub := si.Subject
	email := strings.ToLower(si.Email)
	if err := h.DB.Create(&models.AuthIdentity{
		UserID:          userID,
		Provider:        provider,
		ProviderSubject: &sub,
		Email:           &email,
	}).Error; err != nil {
		return apiErr(fiber.StatusConflict, "provider_already_linked", "bağlama başarısız")
	}
	return h.respondUser(c, userID)
}

// UnlinkProvider: belirtilen provider kimliğini kaldırır (son kimlik korunur).
func (h *Handlers) UnlinkProvider(provider string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID := middleware.UserID(c)
		var count int64
		h.DB.Model(&models.AuthIdentity{}).Where("user_id = ?", userID).Count(&count)
		if count <= 1 {
			return apiErr(fiber.StatusBadRequest, "last_identity", "son giriş yöntemi kaldırılamaz")
		}
		h.DB.Where("user_id = ? AND provider = ?", userID, provider).Delete(&models.AuthIdentity{})
		return h.respondUser(c, userID)
	}
}

// DeleteAccount: kullanıcıyı siler; FK CASCADE tüm ilişkili veriyi temizler.
//
// Silmeden önce Apple token'ı iptal edilir: Apple, "Sign in with Apple" sunan
// uygulamalardan hesap silinirken revoke çağrısı yapılmasını şart koşar.
// Revoke başarısız olsa bile silme sürdürülür — kullanıcının silme hakkını
// üçüncü taraf bir servisin erişilebilirliğine bağlamayız.
func (h *Handlers) DeleteAccount(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	h.revokeAppleAccess(context.Background(), userID)

	if err := h.DB.Delete(&models.User{}, "id = ?", userID).Error; err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// revokeAppleAccess, kullanıcının Apple kimliği varsa uygulamanın erişimini iptal eder.
func (h *Handlers) revokeAppleAccess(ctx context.Context, userID string) {
	if h.AppleRevoker == nil {
		return
	}

	var identity models.AuthIdentity
	err := h.DB.Where("user_id = ? AND provider = ?", userID, "apple").First(&identity).Error
	if err != nil {
		return // Apple ile bağlı değil
	}
	if identity.AppleRefreshToken == nil || *identity.AppleRefreshToken == "" {
		log.Printf("apple: revoke atlandı, saklı refresh token yok (user=%s)", userID)
		return
	}

	if err := h.AppleRevoker.Revoke(ctx, *identity.AppleRefreshToken); err != nil {
		log.Printf("apple: token iptal edilemedi (user=%s): %v", userID, err)
		return
	}
	log.Printf("apple: token iptal edildi (user=%s)", userID)
}
