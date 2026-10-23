package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/observability"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// --- ortak yardımcılar ---

// loadLinkedProviders, kullanıcının bağlı giriş yöntemlerini ('password','google','apple') döndürür.
func (h *Handlers) loadLinkedProviders(userID string) ([]string, error) {
	providers := []string{}
	err := h.DB.Model(&models.AuthIdentity{}).
		Where("user_id = ?", userID).
		Order("provider").
		Pluck("provider", &providers).Error
	return providers, err
}

// linkedProviders, loadLinkedProviders'ın hatayı döndürmeyen eski biçimidir;
// hata log'lanır ve boş liste döner. Yeni kod loadLinkedProviders kullanmalı.
func (h *Handlers) linkedProviders(userID string) []string {
	providers, err := h.loadLinkedProviders(userID)
	if err != nil {
		log.Printf("auth: bağlı giriş yöntemleri okunamadı (user=%s): %v", userID, err)
		return []string{}
	}
	return providers
}

// usableSignInMethod, sağlayıcının bu ortamda giriş için kullanılabilir olup
// olmadığını söyler. Production'da parola uçları kapalıdır (bkz. routes.go);
// eski 'password' kimliği orada giriş yöntemi sayılmaz.
func (h *Handlers) usableSignInMethod(provider string) bool {
	switch provider {
	case "google", "apple":
		return true
	case "password":
		return h.Cfg == nil || h.Cfg.AppEnv != "production"
	}
	return false
}

// tokenPair, bir oturum için üretilen access + refresh token çiftidir.
type tokenPair struct {
	access  string
	refresh string
}

// newTokenPair, access + refresh token üretir ve refresh hash'ini taşıyan
// satırı hazırlar. Satırı yazmak çağırana kalır: rotasyon onu eski token'ın
// iptaliyle aynı transaction'da yazar.
//
// family boşsa yeni bir oturum (token ailesi) başlatılır; rotasyon eski
// token'ın ailesini taşır.
func (h *Handlers) newTokenPair(user *models.User, family string) (tokenPair, *models.RefreshToken, error) {
	access, err := h.Tokens.GenerateAccessToken(user.ID, user.Email)
	if err != nil {
		return tokenPair{}, nil, err
	}
	refresh, err := auth.GenerateRefreshToken()
	if err != nil {
		return tokenPair{}, nil, err
	}
	if family == "" {
		if family, err = auth.GenerateFamilyID(); err != nil {
			return tokenPair{}, nil, err
		}
	}
	row := &models.RefreshToken{
		UserID:    user.ID,
		TokenHash: auth.HashToken(refresh),
		FamilyID:  &family,
		ExpiresAt: time.Now().Add(h.Tokens.RefreshTTL()),
	}
	return tokenPair{access: access, refresh: refresh}, row, nil
}

func (h *Handlers) authResponse(user *models.User, pair tokenPair) (*dto.AuthResponse, error) {
	providers, err := h.loadLinkedProviders(user.ID)
	if err != nil {
		return nil, err
	}
	return &dto.AuthResponse{
		AccessToken:  pair.access,
		RefreshToken: pair.refresh,
		User:         dto.NewUserDTO(user, providers),
	}, nil
}

// issueTokens, access + refresh token üretir ve refresh hash'ini DB'ye yazar.
func (h *Handlers) issueTokens(user *models.User) (*dto.AuthResponse, error) {
	pair, row, err := h.newTokenPair(user, "")
	if err != nil {
		return nil, err
	}
	if err := h.DB.Create(row).Error; err != nil {
		return nil, err
	}
	return h.authResponse(user, pair)
}

func (h *Handlers) respondUser(c *fiber.Ctx, userID string) error {
	var user models.User
	if err := h.DB.First(&user, "id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiErr(fiber.StatusNotFound, "user_not_found", "kullanıcı bulunamadı")
		}
		return err
	}
	providers, err := h.loadLinkedProviders(user.ID)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"user": dto.NewUserDTO(&user, providers)})
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
//
// E-postayla bulunan hesabın 'password' kimliği ya da aynı sağlayıcının başka
// bir kimliği varsa bağlama yapılmaz, 409 döner (bkz. aşağıdaki gerekçeler).
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
		// Sağlayıcı email vermeyebilir (Apple sonraki girişlerde) ya da verdiği
		// adres doğrulanmamış olabilir; ikisinde de e-postayla eşleme yapılmaz.
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
		} else {
			// 'password' kimliği e-posta sahipliği kanıtlanmadan açılır. Böyle bir
			// hesaba sosyal kimliği sessizce bağlamak, adresi önceden kaydeden
			// kişiye adresin gerçek sahibinin hesabına parolayla girme yolu
			// bırakır. Reddedilir; hesaba ve verisine dokunulmaz.
			var passwordIdentities int64
			if err := tx.Model(&models.AuthIdentity{}).
				Where("user_id = ? AND provider = ?", user.ID, "password").
				Count(&passwordIdentities).Error; err != nil {
				return err
			}
			if passwordIdentities > 0 {
				return apiErr(fiber.StatusConflict, "email_in_use",
					"bu email parola ile kayıtlı bir hesaba ait")
			}

			// Hesapta bu sağlayıcının başka bir kimliği (farklı subject) varsa
			// ikincisi eklenemez (uq_user_provider). Kısıta çarpıp 500 dönmek
			// yerine hesaba dokunmadan reddedilir. Eski kimliği yenisiyle
			// değiştirmek, adresi devralan kişiye hesabı vermek olurdu.
			var sameProvider int64
			if err := tx.Model(&models.AuthIdentity{}).
				Where("user_id = ? AND provider = ?", user.ID, provider).
				Count(&sameProvider).Error; err != nil {
				return err
			}
			if sameProvider > 0 {
				return apiErr(fiber.StatusConflict, "email_in_use",
					"bu email aynı sağlayıcının başka bir hesabına bağlı")
			}
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

// refreshReuseGrace, rotasyonla iptal edilmiş bir refresh token'ın yeniden
// kabul edildiği süredir. Sunucu yenilemeyi commit edip yanıt mobil ağda
// kaybolursa istemci aynı (artık iptal edilmiş) token'la yeniden dener; bu
// süre içinde ve halef hiç kullanılmamışsa yeni bir çift verilir. Süre
// dışındaki yeniden kullanım çalınmış token belirtisidir.
const refreshReuseGrace = 60 * time.Second

// refreshOutcome, yenileme transaction'ının sonucudur.
type refreshOutcome int

const (
	refreshIssued   refreshOutcome = iota // yeni çift verildi
	refreshRejected                       // token geçersiz, hiçbir şey değişmedi
	refreshReused                         // yeniden kullanım: aile iptal edildi
)

// Refresh: refresh token'ı doğrular, rotasyonla yeni çift üretir.
//
// 401 yalnızca token'ın kendisi reddedildiğinde döner: istemci 401'i kesin ret
// sayıp saklı token'ları siler. Veritabanına ulaşılamaması gibi geçici hatalar
// 500 döner ki istemci oturumu koruyup yeniden denesin.
//
// Rotasyonla iptal edilmiş bir token refreshReuseGrace içinde yeniden gelirse
// ve halefi hiç kullanılmamışsa, halef iptal edilip yeni bir çift verilir
// (yanıtı kaybolan yenileme). Bunun dışındaki her yeniden kullanımda token'ın
// ailesindeki bütün token'lar iptal edilir ve 401 döner.
func (h *Handlers) Refresh(c *fiber.Ctx) error {
	var req dto.RefreshRequest
	if err := c.BodyParser(&req); err != nil || req.RefreshToken == "" {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "refreshToken gerekli")
	}
	var rt models.RefreshToken
	if err := h.DB.Where("token_hash = ?", auth.HashToken(req.RefreshToken)).First(&rt).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiErr(fiber.StatusUnauthorized, "invalid_refresh_token", "geçersiz refresh token")
		}
		return err
	}
	if time.Now().After(rt.ExpiresAt) {
		return apiErr(fiber.StatusUnauthorized, "invalid_refresh_token", "refresh token süresi dolmuş")
	}
	// Logout ile iptal edilmiş (halefsiz) token'ın yeniden deneneceği bir şey yok.
	if rt.RevokedAt != nil && rt.ReplacedByID == nil {
		return apiErr(fiber.StatusUnauthorized, "invalid_refresh_token", "refresh token süresi dolmuş")
	}
	var user models.User
	if err := h.DB.First(&user, "id = ?", rt.UserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiErr(fiber.StatusUnauthorized, "invalid_refresh_token", "kullanıcı yok")
		}
		return err
	}

	// Çift, hiçbir şey iptal edilmeden önce üretilir: üretim hatası eski token'ı
	// geçerli bırakmalıdır.
	family := ""
	if rt.FamilyID != nil {
		family = *rt.FamilyID
	}
	pair, row, err := h.newTokenPair(&user, family)
	if err != nil {
		return err
	}

	// Karar, token satırı kilitliyken taze haliyle verilir ve bütün yazımlar
	// tek transaction'dadır. Ayrı yazılsalardı, iptalden sonra başarısız olan
	// bir yazım istemciye geçici hata (500) döner ama oturumu sunucuda yok etmiş
	// olurdu. Kilit, aynı token'la eşzamanlı gelen istekleri sıraya sokar:
	// ilki rotasyon yapar, sonrakiler tolerans yolundan geçer.
	var outcome refreshOutcome
	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		outcome, err = rotateRefreshToken(tx, rt.ID, row, time.Now())
		return err
	})
	if txErr != nil {
		return txErr
	}
	switch outcome {
	case refreshIssued:
		// Yanıt hazırlanamazsa istemci 500 alıp aynı token'la yeniden dener;
		// rotasyon commit edildiği için deneme tolerans yolundan geçer.
		resp, err := h.authResponse(&user, pair)
		if err != nil {
			return err
		}
		return c.JSON(resp)
	case refreshReused:
		log.Printf("auth: iptal edilmiş refresh token yeniden kullanıldı, oturum ailesi iptal edildi (user=%s, token=%d)",
			user.ID, rt.ID)
	}
	return apiErr(fiber.StatusUnauthorized, "invalid_refresh_token", "refresh token süresi dolmuş")
}

// rotateRefreshToken, id'li token'ı kilitleyip yeniden okur ve kararı verir:
//   - token etkinse iptal edilir, yerine row yazılır;
//   - rotasyonla iptal edileli refreshReuseGrace olmamışsa ve halefi hiç
//     kullanılmamışsa halef iptal edilir, yerine row yazılır;
//   - aksi halde token'ın ailesi iptal edilir (yeniden kullanım).
//
// row'un ailesi, kilitli satırın ailesine eşitlenir.
func rotateRefreshToken(tx *gorm.DB, id uint64, row *models.RefreshToken, now time.Time) (refreshOutcome, error) {
	var rt models.RefreshToken
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&rt, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return refreshRejected, nil // bu arada silinmiş (hesap silme)
		}
		return refreshRejected, err
	}
	if now.After(rt.ExpiresAt) {
		return refreshRejected, nil
	}
	if rt.FamilyID != nil {
		row.FamilyID = rt.FamilyID
	}

	// Etkin token: olağan rotasyon.
	if rt.RevokedAt == nil {
		if err := tx.Create(row).Error; err != nil {
			return refreshRejected, err
		}
		err := tx.Model(&models.RefreshToken{}).Where("id = ?", rt.ID).Updates(map[string]any{
			"revoked_at":     now,
			"replaced_by_id": row.ID,
			"family_id":      *row.FamilyID, // eski satırda boş olabilir
		}).Error
		return refreshIssued, err
	}
	if rt.ReplacedByID == nil {
		return refreshRejected, nil // logout ya da aile iptali
	}

	// İptal edilmiş token: tolerans içinde ve halef hiç kullanılmamışsa yanıtı
	// kaybolan bir yenilemedir.
	if now.Sub(*rt.RevokedAt) <= refreshReuseGrace {
		var successor models.RefreshToken
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&successor, "id = ?", *rt.ReplacedByID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return refreshRejected, err
		}
		if err == nil && successor.RevokedAt == nil {
			if err := tx.Create(row).Error; err != nil {
				return refreshRejected, err
			}
			if err := tx.Model(&models.RefreshToken{}).Where("id = ?", successor.ID).Updates(map[string]any{
				"revoked_at":     now,
				"replaced_by_id": row.ID,
			}).Error; err != nil {
				return refreshRejected, err
			}
			// Aynı eski token'la bir deneme daha gelirse yeni halefi görsün.
			err := tx.Model(&models.RefreshToken{}).Where("id = ?", rt.ID).
				Update("replaced_by_id", row.ID).Error
			return refreshIssued, err
		}
	}

	// Yeniden kullanım: aynı oturumdan türeyen bütün token'lar iptal edilir.
	// Çalınan token'ı önce saldırgan kullandıysa gerçek kullanıcı da çıkış
	// yapar; ama saldırganın oturumu da biter ve rotasyonla uzatılamaz.
	if rt.FamilyID == nil {
		return refreshRejected, nil
	}
	err := tx.Model(&models.RefreshToken{}).
		Where("family_id = ? AND revoked_at IS NULL", *rt.FamilyID).
		Update("revoked_at", now).Error
	return refreshReused, err
}

// Me: oturum sahibinin profilini döndürür.
func (h *Handlers) Me(c *fiber.Ctx) error {
	return h.respondUser(c, middleware.UserID(c))
}

// Logout: verilen refresh token'ı ve aynı oturumdan türeyen token'ları iptal eder.
//
// Bilinmeyen ya da hiç verilmemiş token sessizce 204 alır (istemci çıkışı
// yerelde zaten tamamlar); veritabanı hatası ise 500 döner.
func (h *Handlers) Logout(c *fiber.Ctx) error {
	var req dto.LogoutRequest
	if err := c.BodyParser(&req); err != nil || req.RefreshToken == "" {
		return c.SendStatus(fiber.StatusNoContent)
	}
	var rt models.RefreshToken
	err := h.DB.Where("token_hash = ?", auth.HashToken(req.RefreshToken)).First(&rt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c.SendStatus(fiber.StatusNoContent)
	}
	if err != nil {
		return err
	}
	q := h.DB.Model(&models.RefreshToken{}).Where("id = ?", rt.ID)
	if rt.FamilyID != nil {
		q = h.DB.Model(&models.RefreshToken{}).Where("(id = ? OR family_id = ?)", rt.ID, *rt.FamilyID)
	}
	if err := q.Where("revoked_at IS NULL").Update("revoked_at", time.Now()).Error; err != nil {
		return err
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
//
// Sayım ve silme, kullanıcının kimlik satırları kilitliyken tek transaction'da
// yapılır: aynı anda gelen DELETE /link/google ve DELETE /link/apple'dan
// yalnızca biri geçer, hesap girişsiz kalmaz. Kalan kimlikler arasında bu
// ortamda kullanılabilir bir giriş yöntemi yoksa 400 last_identity döner.
// Bağlı olmayan sağlayıcıyı kaldırmak değişiklik yapmadan 200 döner (yeniden
// deneme güvenli).
//
// Apple kimliği kaldırılırken saklı refresh token'ı Apple'da iptal edilir:
// satır silindikten sonra hesap silme sırasında iptal edilecek token kalmaz.
func (h *Handlers) UnlinkProvider(provider string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID := middleware.UserID(c)
		var removed []models.AuthIdentity
		err := h.DB.Transaction(func(tx *gorm.DB) error {
			var identities []models.AuthIdentity
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("user_id = ?", userID).Order("id").
				Find(&identities).Error; err != nil {
				return err
			}
			usable := 0
			for _, identity := range identities {
				if identity.Provider == provider {
					removed = append(removed, identity)
				} else if h.usableSignInMethod(identity.Provider) {
					usable++
				}
			}
			if len(removed) == 0 {
				return nil
			}
			if usable == 0 {
				removed = nil
				return apiErr(fiber.StatusBadRequest, "last_identity", "son giriş yöntemi kaldırılamaz")
			}
			return tx.Where("user_id = ? AND provider = ?", userID, provider).
				Delete(&models.AuthIdentity{}).Error
		})
		if err != nil {
			return err
		}
		for _, identity := range removed {
			if provider == "apple" {
				h.revokeAppleOnUnlink(userID, identity)
			}
		}
		return h.respondUser(c, userID)
	}
}

// appleRevokeTimeout, Apple'a yapılan exchange + revoke çağrılarının toplam
// süre sınırıdır. Silme ya da bağlantı kaldırma bu süreden uzun beklemez.
const appleRevokeTimeout = 20 * time.Second

// revokeAppleOnUnlink, kaldırılan Apple kimliğinin saklı token'ını iptal eder.
// Best effort: başarısızlık bağlantı kaldırmayı geri almaz, raporlanır.
func (h *Handlers) revokeAppleOnUnlink(userID string, identity models.AuthIdentity) {
	stored := ""
	if identity.AppleRefreshToken != nil {
		stored = *identity.AppleRefreshToken
	}
	if stored == "" {
		reportAppleRevokeFailure("bağlantı kaldırma", userID,
			errors.New("saklı refresh token yok, Apple erişimi iptal edilemedi"))
		return
	}
	if h.AppleRevoker == nil {
		reportAppleRevokeFailure("bağlantı kaldırma", userID, errAppleRevokerMissing)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), appleRevokeTimeout)
	defer cancel()
	if err := h.AppleRevoker.Revoke(ctx, stored); err != nil {
		reportAppleRevokeFailure("bağlantı kaldırma", userID, err)
		return
	}
	log.Printf("apple: bağlantı kaldırılırken token iptal edildi (user=%s)", userID)
}

// DeleteAccount: kullanıcıyı siler; FK CASCADE tüm ilişkili veriyi temizler.
//
// Silmeden önce Apple token'ı iptal edilir: Apple, "Sign in with Apple" sunan
// uygulamalardan hesap silinirken revoke çağrısı yapılmasını şart koşar.
// İsteğe bağlı gövde {"appleAuthorizationCode"}: saklı token yoksa ya da
// iptali başarısız olursa bu kod token'a çevrilip iptal edilir.
// Revoke başarısız olsa bile silme sürdürülür — kullanıcının silme hakkını
// üçüncü taraf bir servisin erişilebilirliğine bağlamayız; başarısızlık
// log'lanır ve Sentry'ye gider.
func (h *Handlers) DeleteAccount(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	// Gövde isteğe bağlıdır: yayındaki istemci gövdesiz gönderir, okunamayan
	// gövde kod gönderilmemiş sayılır.
	var req dto.DeleteAccountRequest
	_ = c.BodyParser(&req)

	if err := h.revokeAppleAccess(userID, strings.TrimSpace(req.AppleAuthorizationCode)); err != nil {
		return err
	}

	if err := h.DB.Delete(&models.User{}, "id = ?", userID).Error; err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// errAppleRevokerMissing, Apple kimliği olan bir hesapta revoke yapılandırması
// (APPLE_TEAM_ID/APPLE_KEY_ID/APPLE_PRIVATE_KEY) eksik olduğunda raporlanır.
var errAppleRevokerMissing = errors.New("Apple revoker yapılandırılmamış, token iptali yapılamadı")

// revokeAppleAccess, kullanıcının Apple kimliği varsa (ya da istemci bir
// authorization code gönderdiyse) uygulamanın Apple erişimini iptal eder.
// Önce saklı refresh token denenir; yoksa ya da iptal başarısızsa code
// token'a çevrilip iptal edilir.
//
// Revoke başarısızlığı hata olarak dönmez, raporlanır; dönen hata yalnızca
// kimliğin okunamadığı veritabanı hatasıdır.
func (h *Handlers) revokeAppleAccess(userID, code string) error {
	var identity models.AuthIdentity
	err := h.DB.Where("user_id = ? AND provider = ?", userID, "apple").First(&identity).Error
	linked := err == nil
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if !linked && code == "" {
		return nil // Apple ile bağlı değil
	}
	if h.AppleRevoker == nil {
		reportAppleRevokeFailure("hesap silme", userID, errAppleRevokerMissing)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), appleRevokeTimeout)
	defer cancel()

	var failures []error
	if identity.AppleRefreshToken != nil && *identity.AppleRefreshToken != "" {
		err := h.AppleRevoker.Revoke(ctx, *identity.AppleRefreshToken)
		if err == nil {
			log.Printf("apple: token iptal edildi (user=%s)", userID)
			return nil
		}
		failures = append(failures, fmt.Errorf("saklı token: %w", err))
	}
	if code != "" {
		refreshToken, err := h.AppleRevoker.ExchangeCode(ctx, code)
		if err == nil {
			err = h.AppleRevoker.Revoke(ctx, refreshToken)
		}
		if err == nil {
			log.Printf("apple: authorization code ile token iptal edildi (user=%s)", userID)
			return nil
		}
		failures = append(failures, fmt.Errorf("authorization code: %w", err))
	}
	if len(failures) == 0 {
		failures = append(failures, errors.New("saklı refresh token yok ve authorization code gönderilmedi"))
	}
	reportAppleRevokeFailure("hesap silme", userID, errors.Join(failures...))
	return nil
}

// reportAppleRevokeFailure, Apple erişiminin iptal edilemediğini log'lar ve
// Sentry'ye gönderir: Apple bunu şart koştuğu için sessiz kalmamalı.
func reportAppleRevokeFailure(step, userID string, err error) {
	log.Printf("apple: %s sırasında token iptal edilemedi (user=%s): %v", step, userID, err)
	observability.CaptureError(fmt.Errorf("apple revoke (%s, user=%s): %w", step, userID, err))
}

// WarnIfAppleRevokeDisabled, production'da Apple girişi açıkken revoke
// yapılandırılmamışsa açılışta yüksek sesle uyarır (log + Sentry) ve true
// döner. Bu durumda hesap silme Apple'ın şart koştuğu iptali yapamaz.
func (h *Handlers) WarnIfAppleRevokeDisabled() bool {
	if h.Cfg == nil || h.Cfg.AppEnv != "production" || h.Cfg.AppleClientID == "" || h.AppleRevoker != nil {
		return false
	}
	err := errors.New("APPLE_TEAM_ID/APPLE_KEY_ID/APPLE_PRIVATE_KEY eksik ya da geçersiz: " +
		"Apple ile giriş açık ama hesap silmede token iptali YAPILMAYACAK (App Store 5.1.1(v))")
	log.Printf("!!! UYARI !!! %v", err)
	observability.CaptureError(err)
	return true
}
