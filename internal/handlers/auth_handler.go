package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/observability"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
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

// usableSignInMethod, sağlayıcının giriş için kullanılabilir olup olmadığını
// söyler: bağlantı kaldırmada hesapta en az bir tane kalmalıdır. E-posta/parola
// girişi her ortamda açık olduğundan 'password' de bir giriş yoludur.
func (h *Handlers) usableSignInMethod(provider string) bool {
	switch provider {
	case "google", "apple", "password":
		return true
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
//
// Refresh token'ın ömrü kullanıcının o anki misafirliğine göredir
// (GUEST_REFRESH_TOKEN_TTL / REFRESH_TOKEN_TTL); rotasyon kullanıcıyı her
// seferinde yeniden okuduğu için kaydolan misafir olağan ömre geçer.
func (h *Handlers) newTokenPair(user *models.User, family string) (tokenPair, *models.RefreshToken, error) {
	access, err := h.Tokens.GenerateAccessToken(user.ID, user.Email, user.TokenVersion)
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
		ExpiresAt: time.Now().Add(h.Tokens.RefreshTTLFor(user.IsGuest)),
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

// respondUser, kullanıcıyı {"user": ...} olarak döner. Yalnızca bulunamayan
// satır 404 user_not_found'dur; diğer veritabanı hataları (bağlı yöntemlerin
// okunması dahil) 500 döner: bağlantı hatasını "hesap yok" ya da "bağlı yöntem
// yok" diye bildirmek istemciyi yanlış yönlendirir.
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

// requireCurrentSession, isteğin access token'ının hesabın şimdiki oturum
// sürümüyle (models.User.TokenVersion) verildiğini doğrular; değilse 401
// döner, istemci sessizce yenilemeyi dener.
//
// Hesap başka birine geçtiğinde (devir) ya da adresi geri alındığında refresh
// token'lar iptal edilir ve sürüm artar; ama önceden verilmiş access token'lar
// imzası geçerli olduğu için ACCESS_TOKEN_TTL boyunca kabul edilir. Hesabı
// denetleyen uçlar (giriş yolu ekleme/kaldırma, hesap silme) bu kontrolü
// yapar: yapmasalar adresi parolayla önceden kapatan kişi, hesap adresin
// sahibine devredildikten sonra elindeki access token'la kendi Apple/Google'ını
// o hesaba bağlayıp ona kalıcı olarak girebilir ya da hesabı silebilirdi.
func requireCurrentSession(c *fiber.Ctx, user *models.User) error {
	if middleware.TokenVersion(c) != user.TokenVersion {
		return apiErr(fiber.StatusUnauthorized, "unauthenticated", "oturum geçersiz")
	}
	return nil
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
	if err := h.DB.Model(&models.User{}).Where("email = ?", req.Email).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return apiErr(fiber.StatusConflict, "email_in_use", "bu email zaten kullanımda")
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return err
	}
	displayName := req.DisplayName
	if displayName == "" {
		// Yerel kısım 64 karaktere kadar çıkabilir: ad sınırına kesilir.
		displayName = dto.SanitizeDisplayName(emailLocalPart(req.Email))
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
	if isUniqueViolation(err) {
		// Aynı adresle eşzamanlı ikinci kayıt.
		return apiErr(fiber.StatusConflict, "email_in_use", "bu email zaten kullanımda")
	}
	if err != nil {
		return err
	}

	resp, err := h.issueTokens(&user)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(resp)
}

// errInvalidCredentials, girişte e-postanın bulunamadığını, hesabın parolası
// olmadığını ve parolanın yanlış olduğunu ayırt etmeden bildirir: yanıttan
// hangi adreslerin kayıtlı olduğu öğrenilmesin.
func errInvalidCredentials() error {
	return apiErr(fiber.StatusUnauthorized, "invalid_credentials", "email veya parola hatalı")
}

// dummyPasswordHash, hesabı ya da parolası olmayan girişlerde de bir bcrypt
// karşılaştırması yapmak için kullanılır: yanıt süresi kayıtlı adresi ele
// vermesin (bcrypt maliyeti 12, karşılaştırma ~250 ms sürer).
var dummyPasswordHash = sync.OnceValue(func() string {
	hash, err := auth.HashPassword("kelimeler-zamanlama-esitleme")
	if err != nil {
		return ""
	}
	return hash
})

// Login: email/parola doğrular.
//
// Refresh token, parola kimliği kilitliyken ve hâlâ duruyorken yazılır: aynı
// anda doğrulanmış bir sağlayıcı e-postası bu hesabı devralırsa ya da adresini
// geri alırsa (bkz. authenticateSocialOnce) ya devir/geri alma bu girişi bekler
// ve verdiği token'ı da iptal eder, ya da giriş onu bekler, kimliği silinmiş
// bulur ve 401 alır.
func (h *Handlers) Login(c *fiber.Ctx) error {
	var req dto.LoginRequest
	if err := c.BodyParser(&req); err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "geçersiz istek")
	}
	req.Email = dto.NormalizeEmail(req.Email)

	// Yalnızca bulunamayan kayıt yanlış kimlik bilgisidir; veritabanı hatası
	// 500 döner (401 istemciye "parola yanlış" der).
	var user models.User
	if err := h.DB.Where("email = ?", req.Email).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			auth.VerifyPassword(dummyPasswordHash(), req.Password)
			return errInvalidCredentials()
		}
		return err
	}
	var identity models.AuthIdentity
	if err := h.DB.Where("user_id = ? AND provider = ?", user.ID, "password").First(&identity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			auth.VerifyPassword(dummyPasswordHash(), req.Password)
			return errInvalidCredentials()
		}
		return err
	}
	if identity.PasswordHash == nil || !auth.VerifyPassword(*identity.PasswordHash, req.Password) {
		return errInvalidCredentials()
	}

	pair, row, err := h.newTokenPair(&user, "")
	if err != nil {
		return err
	}
	err = h.DB.Transaction(func(tx *gorm.DB) error {
		var locked models.AuthIdentity
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", identity.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errInvalidCredentials() // bu arada devralındı
			}
			return err
		}
		return tx.Create(row).Error
	})
	if err != nil {
		return err
	}
	resp, err := h.authResponse(&user, pair)
	if err != nil {
		return err
	}
	return c.JSON(resp)
}

// decideEmailLookup, adresin hesabındaki giriş yöntemlerinden (bkz.
// loadLinkedProviders) e-posta önce akışının sonraki adımını seçer; found
// false ise adres kayıtlı değildir. Veritabanına dokunmaz.
//
// Parola kimliği varsa yanında Google/Apple olsa da "login" döner: kullanıcı
// adresini yazdı, en kısa yol parolasıdır; sosyal girişler ekranda zaten
// durur. Parolasız hesapta providers yalnızca google/apple içerir. Hiç kimliği
// olmayan gerçek adresli hesap bugünkü akışlarda oluşmaz (bağlantı kaldırmada
// son giriş yolu kalır, devir ve geri alma kimliği aynı transaction'da
// değiştirir); oluşursa providers boş döner ve kayıt yine 409 alacağı için
// "social" seçilir: istemci iki sağlayıcıyı da gösterir, doğrulanmış adresle
// gelen Google/Apple girişi bu hesaba bağlanır.
func decideEmailLookup(found bool, linked []string) (string, []string) {
	if !found {
		return dto.LookupNextRegister, nil
	}
	social := []string{}
	for _, p := range linked {
		switch p {
		case "password":
			return dto.LookupNextLogin, nil
		case "google", "apple":
			social = append(social, p)
		}
	}
	return dto.LookupNextSocial, social
}

// EmailLookup: POST /auth/email/lookup — e-posta önce giriş ekranında adresin
// parola sayfasına mı, kayıt sayfasına mı, yoksa Google/Apple'a mı gideceğini
// söyler.
//
//	200 {"next":"login"}                           parola kimliği olan hesap
//	200 {"next":"register"}                        adres kayıtlı değil
//	200 {"next":"social","providers":["apple",…]}  yalnızca Google/Apple
//	400 invalid_request / invalid_email, 429 rate_limited (authLimit)
//
// Bilinçli olarak adresin kayıtlı olup olmadığını açığa vurur: e-posta önce
// akışının doğası budur (Google ve Microsoft girişleri de böyle çalışır) ve
// kayıt ucu zaten 409 email_in_use ile aynı bilgiyi verir. Toplu taramayı
// IP başına sıkı hız sınırı (AUTH_RATE_LIMIT, giriş/kayıtla aynı kova)
// yavaşlatır. Giriş ucunun birleşik 401 invalid_credentials davranışı
// değişmez: parola denemesi adresi ayrıca ele vermez. Ayrıntı: DEPLOYMENT.md.
//
// Yer tutucu adresler (misafir, e-postasız sosyal hesap, geri alınmış adres;
// @users.kelimeler.local) ValidateEmail'de 400 invalid_email alır, hiçbir
// zaman bir hesapla eşleşmez. Arama kayıt ve girişle aynı normalize edilmiş
// adresle yapılır.
func (h *Handlers) EmailLookup(c *fiber.Ctx) error {
	var req dto.EmailLookupRequest
	if err := c.BodyParser(&req); err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "geçersiz istek")
	}
	email := dto.NormalizeEmail(req.Email)
	if err := dto.ValidateEmail(email); err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid_email", err.Error())
	}

	// Kayıt ucuyla aynı sorgu: "register" yanıtı kaydın 409 almayacağı
	// anlamına gelmeli. Yalnızca bulunamayan kayıt "kayıtlı değil"dir;
	// veritabanı hatası 500 döner (yanlış "register" kullanıcıyı 409'a
	// gönderirdi).
	var user models.User
	found := true
	if err := h.DB.Select("id").Where("email = ?", email).First(&user).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		found = false
	}
	var linked []string
	if found {
		var err error
		if linked, err = h.loadLinkedProviders(user.ID); err != nil {
			return err
		}
	}

	next, providers := decideEmailLookup(found, linked)
	if next == dto.LookupNextSocial {
		return c.JSON(fiber.Map{"next": next, "providers": providers})
	}
	return c.JSON(fiber.Map{"next": next})
}

// guestEmailPrefix, misafir hesaplarının yer tutucu adresinin önekidir:
// guest_<kullanıcı id>@users.kelimeler.local.
const guestEmailPrefix = "guest_"

// Guest: POST /auth/guest — e-postasız bir misafir hesabı açar ve oturum verir.
//
// Gövde isteğe bağlıdır ({"displayName"}); boş ad boş kalır, istemci
// "Misafir" yazar. Hesabın hiçbir giriş kimliği yoktur: ona yalnızca dönen
// refresh token'la ulaşılır (ömrü GUEST_REFRESH_TOKEN_TTL). Hesap ve ilk token
// tek transaction'da yazılır; token yazılamazsa erişilemeyen bir misafir
// hesabı kalmaz.
func (h *Handlers) Guest(c *fiber.Ctx) error {
	var req dto.GuestRequest
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&req); err != nil {
			return apiErr(fiber.StatusBadRequest, "invalid_request", "geçersiz istek")
		}
	}
	name, err := dto.NormalizeDisplayName(req.DisplayName)
	if err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid_display_name", err.Error())
	}

	id := uuid.NewString()
	user := models.User{
		ID:           id,
		Email:        dto.SyntheticEmail(guestEmailPrefix + id),
		DisplayName:  name,
		CurrentLevel: "A1",
		CurrentGroup: 1,
		IsGuest:      true,
	}
	pair, row, err := h.newTokenPair(&user, "")
	if err != nil {
		return err
	}
	if err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		return tx.Create(row).Error
	}); err != nil {
		return err
	}
	resp, err := h.authResponse(&user, pair)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(resp)
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

// isUniqueViolation, PostgreSQL unique kısıt ihlalini (SQLSTATE 23505) tanır.
func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}

// providerEmail, sağlayıcının doğruladığı e-postayı (bkz. auth.verifiedEmail)
// eşleme ve saklama için normalize eder. Makul olmayan uzunluktaki ya da yer
// tutucu alan adındaki adres boş döner: böyle bir adresle hesap eşlenmez.
func providerEmail(si *auth.SocialIdentity) string {
	email := dto.NormalizeEmail(si.Email)
	if len(email) > dto.MaxEmailLength || dto.IsSyntheticEmail(email) {
		return ""
	}
	return email
}

// authenticateSocial: provider+subject ile bul; yoksa email ile eşle/oluştur ve kimliği bağla.
//
// E-postayla bulunan hesapta ne yapılacağı, adresin o hesaptaki dayanağına
// göre verilir (bkz. decideEmailMatch):
//   - Hesabın Google/Apple kimliklerinden biri aynı adresi doğrulamışsa adresin
//     sahipliği zaten kanıtlıdır: yeni kimlik o hesaba bağlanır; hesapta aynı
//     sağlayıcının başka bir kimliği varsa 409 email_in_use.
//   - Değilse ve hesabın adrese dayanağı yalnızca parolaysa (parola kaydında
//     adres doğrulanmaz): tek giriş yolu parolaysa hesap devralınır (bkz.
//     takeOverPasswordAccount); parolanın yanında başka bir giriş yolu varsa
//     adres hesaptan geri alınır ve adresin doğrulanmış sahibine yeni bir hesap
//     açılır (bkz. reclaimEmail).
//   - Parolasız hesapta aynı sağlayıcının başka bir kimliği varsa 409, yoksa
//     kimlik bağlanır.
//
// Doğrulanmamış (yer tutucuya düşen) adres hiçbir hesapla eşlenmez; devir ve
// geri alma yalnızca doğrulanmış adresle olur.
//
// İlk giriş aynı anda iki kez gelirse (çift dokunuş, yeniden deneme) ikinci
// istek kullanıcıyı ya da kimliği yazarken unique kısıta çarpar. Kısıt ancak
// öteki istek commit edince ihlal verir; bu yüzden bir kez yeniden okumak
// öteki isteğin açtığı hesabı bulur ve aynı kullanıcı döner. Hesap, kimlikleri
// kilitlenene kadar değişmişse (aynı anda süren bir geri alma adresi taşıdı;
// errAccountChanged) ya da PostgreSQL kilitlenme (40P01) ile işlemi iptal
// ettiyse de bir kez yeniden denenir: transaction geri alındığı için deneme
// temiz başlar.
func (h *Handlers) authenticateSocial(provider string, si *auth.SocialIdentity, displayName string) (*models.User, error) {
	user, err := h.authenticateSocialOnce(provider, si, displayName)
	if isUniqueViolation(err) || errors.Is(err, errAccountChanged) || isDeadlock(err) {
		return h.authenticateSocialOnce(provider, si, displayName)
	}
	return user, err
}

// errAccountChanged, e-postayla bulunan hesabın, kimlikleri kilitlenene kadar
// adresini kaybettiğini (ya da silindiğini) bildirir; karar yeniden verilmelidir.
var errAccountChanged = errors.New("e-postayla bulunan hesap bu arada değişti")

func (h *Handlers) authenticateSocialOnce(provider string, si *auth.SocialIdentity, displayName string) (*models.User, error) {
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

	// verified: adres sağlayıcının doğruladığı gerçek bir adres mi (yer
	// tutucuya düşmedi mi)? Yalnızca böyle bir adres parola hesabını devralabilir
	// ya da adresi geri alabilir.
	email := providerEmail(si)
	verified := email != ""
	if !verified {
		// Sağlayıcı email vermeyebilir (Apple sonraki girişlerde) ya da verdiği
		// adres doğrulanmamış olabilir; ikisinde de e-postayla eşleme yapılmaz.
		email = dto.SyntheticEmail(provider + "_" + si.Subject)
	}

	var user models.User
	tookOver := false
	reclaimedFrom := ""
	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		findErr := tx.Where("email = ?", email).First(&user).Error
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			var err error
			user, err = createSocialUser(tx, email, displayName, si)
			if err != nil {
				return err
			}
		} else if findErr != nil {
			return findErr
		} else {
			// Kimlikler kilitlenir, hesap kilitten sonra yeniden okunur: karar
			// verilirken aynı anda süren bir parola girişi, bağlantı kaldırma ya
			// da başka bir geri alma hesabı değiştiremez. Hesap bu arada adresini
			// kaybettiyse (ör. çift dokunuşta öteki istek adresi geri aldı) karar
			// yeniden verilir.
			var identities []models.AuthIdentity
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("user_id = ?", user.ID).Order("id").
				Find(&identities).Error; err != nil {
				return err
			}
			if err := tx.First(&user, "id = ?", user.ID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errAccountChanged
				}
				return err
			}
			if !strings.EqualFold(user.Email, email) {
				return errAccountChanged
			}

			match, password := decideEmailMatch(identities, provider, email, verified)
			switch match {
			case matchSameProvider:
				// Hesapta bu sağlayıcının başka bir kimliği (farklı subject)
				// varsa ikincisi eklenemez (uq_user_provider). Kısıta çarpıp
				// 500 dönmek yerine hesaba dokunmadan reddedilir. Eski kimliği
				// yenisiyle değiştirmek, adresi devralan kişiye hesabı vermek olurdu.
				return apiErr(fiber.StatusConflict, "email_in_use",
					"bu email aynı sağlayıcının başka bir hesabına bağlı")
			case matchPasswordConflict:
				return apiErr(fiber.StatusConflict, "email_in_use",
					"bu email parola ile kayıtlı bir hesaba ait")
			case matchTakeOver:
				if err := takeOverPasswordAccount(tx, user.ID, password.ID); err != nil {
					return err
				}
				// Sürüm artışı kullanıcı satırını kilitler: aynı anda süren bir
				// bağlama (linkProvider, kullanıcı satırı kilitli) bitene kadar
				// bekler. O bağlama kimliğini kilitlediğimiz kimliklerden sonra
				// yazdıysa burada görünür; hesabın parolanın yanında başka bir giriş
				// yolu vardır ve karar yeniden verilir (geri alma). Görmezden
				// gelinseydi adresin sahibi, bağlayanın da girebildiği hesaba konurdu.
				var kalan int64
				if err := tx.Model(&models.AuthIdentity{}).Where("user_id = ?", user.ID).Count(&kalan).Error; err != nil {
					return err
				}
				if kalan > 0 {
					return errAccountChanged
				}
				if err := tx.First(&user, "id = ?", user.ID).Error; err != nil {
					return err
				}
				tookOver = true
			case matchReclaim:
				if err := reclaimEmail(tx, user.ID, password.ID); err != nil {
					return err
				}
				reclaimedFrom = user.ID
				var err error
				user, err = createSocialUser(tx, email, displayName, si)
				if err != nil {
					return err
				}
			}
		}
		sub := si.Subject
		return tx.Create(&models.AuthIdentity{
			UserID:          user.ID,
			Provider:        provider,
			ProviderSubject: &sub,
			Email:           &email,
			EmailVerified:   verified,
		}).Error
	})
	if txErr != nil {
		return nil, txErr
	}
	if tookOver {
		log.Printf("auth: doğrulanmış %s e-postası parola hesabını devraldı; parola kimliği silindi, oturumlar iptal edildi (user=%s)",
			provider, user.ID)
	}
	if reclaimedFrom != "" {
		log.Printf("auth: doğrulanmış %s e-postası adresi parolayla tutan hesaptan geri aldı; eski hesabın parola kimliği silindi, adresi yer tutucuya çevrildi, oturumları iptal edildi (eski=%s, yeni=%s)",
			provider, reclaimedFrom, user.ID)
	}
	return &user, nil
}

// createSocialUser, sosyal girişle gelen kişiye email adresli yeni bir hesap
// açar.
//
// İstemcinin ya da sağlayıcının verdiği ad sınırsız ve denetimsizdir:
// temizlenir ve kısaltılır (reddedilmez, giriş bozulmasın).
func createSocialUser(tx *gorm.DB, email, displayName string, si *auth.SocialIdentity) (models.User, error) {
	name := dto.SanitizeDisplayName(displayName)
	if name == "" {
		name = dto.SanitizeDisplayName(si.Name)
	}
	if name == "" {
		name = dto.SanitizeDisplayName(emailLocalPart(email))
	}
	user := models.User{Email: email, DisplayName: name, CurrentLevel: "A1", CurrentGroup: 1}
	err := tx.Create(&user).Error
	return user, err
}

// emailMatch, sosyal girişte e-postayla bulunan hesap için verilen karardır.
type emailMatch int

const (
	matchLink             emailMatch = iota // kimlik hesaba bağlanır
	matchSameProvider                       // 409: hesapta aynı sağlayıcının başka kimliği var
	matchPasswordConflict                   // 409: doğrulanmamış adres parola hesabına dokunamaz
	matchTakeOver                           // tek giriş yolu parola: hesap devralınır
	matchReclaim                            // parola + başka giriş yolu: adres geri alınır
)

// decideEmailMatch, email adresli hesabın kimliklerine (identities) bakarak
// provider'dan gelen girişin ne yapacağını söyler; devir ve geri almada
// silinecek parola kimliğini de döner. verified, gelen adresin sağlayıcının
// doğruladığı gerçek bir adres olduğunu söyler.
//
// Adresin hesapta "kanıtlı" olması için hesabın bir Google/Apple kimliğinin
// kaydettiği adres bu adres olmalı ve o kimliğin sağlayıcısı adresi doğrulamış
// olmalıdır (auth_identities.email_verified). Parola kimliğinin adresi hiçbir
// zaman kanıt sayılmaz: kayıtta sahiplik doğrulanmaz. Kanıtlı hesap, adresin
// sahibinin kendi hesabıdır; parolası olsa da bağlanır, adresi geri alınmaz.
//
// Kanıt yoksa ve hesapta parola varsa hesabın adrese tek dayanağı o
// doğrulanmamış paroladır. Hesabın parolanın yanındaki kimliği (başka bir
// sağlayıcı ya da aynı sağlayıcının başka bir subject'i) başka bir adresi
// taşır ya da adresi doğrulamamıştır; onu kimin tuttuğu bilinmez: adresi
// parolayla önceden kapatan biri kendi Google/Apple hesabını bağlamış olabilir.
// Adresin sahibini o hesaba koymak (bağlamak) o kişiye sahibin girişini vermek,
// reddetmek (eski kural, 409) ise adresi kapatanın sahibi kalıcı olarak dışarıda
// bırakmasına izin vermek olurdu. Ürün kararı (26 Ekim): adres doğrulanmış
// sahibine geri verilir.
func decideEmailMatch(identities []models.AuthIdentity, provider, email string, verified bool) (emailMatch, *models.AuthIdentity) {
	var password *models.AuthIdentity
	sameProvider, others, proven := false, 0, false
	for i := range identities {
		identity := &identities[i]
		if identity.Provider == "password" {
			password = identity
			continue
		}
		if identity.Provider == provider {
			sameProvider = true
		} else {
			others++
		}
		// Saklanan ve gelen adres ikisi de dto.NormalizeEmail'den geçer; tam
		// eşitlik aranır. strings.EqualFold Unicode katlaması yapar ("ſ" ~ "s",
		// Kelvin "K" ~ "k"): başka bir adresi doğrulamış kimliği kanıt sayabilirdi.
		if verified && identity.EmailVerified && identity.Email != nil && dto.NormalizeEmail(*identity.Email) == email {
			proven = true
		}
	}
	switch {
	case sameProvider && (proven || password == nil || !verified):
		return matchSameProvider, nil
	case proven || password == nil:
		return matchLink, nil
	case !verified:
		return matchPasswordConflict, nil
	case !sameProvider && others == 0:
		return matchTakeOver, password
	default:
		return matchReclaim, password
	}
}

// takeOverPasswordAccount, tek giriş yolu e-posta/parola olan hesabı, adresi
// doğrulanmış bir sağlayıcı kimliğine devretmeye hazırlar: parola kimliği
// silinir ve kullanıcının bütün refresh token'ları iptal edilir (diğer
// cihazlardaki oturumlar ilk yenilemede düşer; verilmiş access token'lar en
// fazla ACCESS_TOKEN_TTL kadar geçerli kalır). Sosyal kimliği çağıran aynı
// transaction'da ekler.
//
// Gerekçe: parola kaydında e-posta sahipliği doğrulanmaz (doğrulama e-postası
// altyapısı yok). Adres sahipliği kanıtlanmadan açılan bir parola, adresin
// gerçek sahibini kendi adresiyle giriş yapmaktan alıkoymamalıdır. Hesabı
// parolayla açan gerçekten adresin sahibiyse ilerlemesi korunur ve artık
// Google/Apple ile girer; başkasıysa (adresi önceden kapatan biri) erişimi
// biter. Sahipliği kanıtlanmamış parolayı bırakıp sosyal kimliği yanına
// eklemek ise o kişiye adresin sahibinin hesabına girme yolu bırakırdı.
//
// Çağıran hesabın kimlik satırlarını kilitlemiştir: aynı anda süren bir parola
// girişi (Login) kimliği kilitleyip token yazar; kilit onu bekler, sonraki
// iptal o token'ı da görür. Kullanıcı satırı baştan tam kilitle (FOR UPDATE)
// kilitlenmez: giriş, token yazarken kullanıcı satırına anahtar kilidi (FK)
// ister ve kilitlenme (deadlock) olurdu.
//
// Oturum sürümü (users.token_version) en son artırılır: önceden verilmiş access
// token'lar hesabı denetleyen uçlarda reddedilir (bkz. requireCurrentSession).
// Artış anahtar sütununa dokunmaz (FOR NO KEY UPDATE); token yazan giriş ve
// rotasyonların FK anahtar kilidiyle çakışmaz, yalnızca kullanıcı satırını
// kilitleyen bağlamayı (linkProvider) bekler.
func takeOverPasswordAccount(tx *gorm.DB, userID string, passwordIdentityID uint64) error {
	if err := tx.Delete(&models.AuthIdentity{}, "id = ?", passwordIdentityID).Error; err != nil {
		return err
	}
	if err := revokeAllRefreshTokens(tx, userID); err != nil {
		return err
	}
	return tx.Model(&models.User{}).Where("id = ?", userID).
		Update("token_version", gorm.Expr("token_version + 1")).Error
}

// reclaimEmail, doğrulanmış bir adresi, onu yalnızca doğrulanmamış bir
// parolayla tutan hesaptan (parolanın yanında başka giriş yolu da olan; bkz.
// decideEmailMatch) geri alır: hesabın parola kimliği silinir, bütün refresh
// token'ları iptal edilir ve adresi yer tutucuya
// (reclaimed_<id>@users.kelimeler.local) çevrilir. Adresin sahibine yeni hesabı
// çağıran aynı transaction'da açar; tek transaction olduğu için ya hepsi olur
// ya hiçbiri.
//
// Hesap verisini ve kalan Google/Apple kimliğini korur, o kimlikle girmeye
// devam eder; yer tutucu adres istemciye hiç gösterilmez (email: ""). Parola
// silinir: adresi artık taşımayan hesapta o adresle parola girişi anlamsızdır
// ve sahipliği kanıtlanmamış parola, adres sahibine geçtikten sonra kimsenin
// elinde kalmamalıdır. Oturumlar iptal edilir: hesabı parolayla açmış kişi
// başka bir cihazda oturumunu sürdürüyorsa o oturum da biter; hesaba yalnızca
// kalan giriş yolunu elinde tutan yeniden girer.
//
// Kilit sırası: çağıran hesabın kimliklerini kilitlemiştir (parola girişi bunu
// bekler, bitince kimliği silinmiş bulur). Sonra token'lar kilitlenip iptal
// edilir, kullanıcı satırı en son güncellenir. Adres değişikliği kullanıcı
// satırına tam kilit alır (users.email unique'tir); bu, token yazan giriş ve
// rotasyonların FK anahtar kilidiyle çakışır. Önce alınsaydı, kimliği ya da eski
// token'ı kilitleyip token yazmayı bekleyen bir giriş ya da rotasyonla
// karşılıklı bekleme olurdu; en sonda alındığında o istekler ya bitmiştir ya da
// bu transaction'ın kilitlediği satırları beklemektedir.
func reclaimEmail(tx *gorm.DB, userID string, passwordIdentityID uint64) error {
	if err := tx.Delete(&models.AuthIdentity{}, "id = ?", passwordIdentityID).Error; err != nil {
		return err
	}
	if err := revokeAllRefreshTokens(tx, userID); err != nil {
		return err
	}
	// Oturum sürümü de artar: önceden verilmiş access token'lar hesabı
	// denetleyen uçlarda reddedilir (bkz. requireCurrentSession).
	return tx.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]any{
		"email":         dto.SyntheticEmail(reclaimedEmailPrefix + userID),
		"token_version": gorm.Expr("token_version + 1"),
	}).Error
}

// reclaimedEmailPrefix, adresi geri alınan hesabın yer tutucu adresinin
// önekidir: reclaimed_<kullanıcı id>@users.kelimeler.local.
const reclaimedEmailPrefix = "reclaimed_"

// revokeAllRefreshTokens, kullanıcının bütün etkin refresh token'larını iptal
// eder.
//
// Etkin token'lar iptalden önce ayrı bir sorguyla kilitlenir: aynı anda süren
// bir rotasyon eski token'ı kilitleyip halefini yazmışsa (henüz commit yok)
// kilit onun bitmesini bekletir. Tek bir UPDATE, başladığı anın görüntüsüyle
// çalıştığı için commit edilen halefi görmez ve etkin bırakırdı; kilitten
// sonraki UPDATE yeni bir görüntüyle başlar ve halefi de iptal eder. Kilitten
// sonra başlayan rotasyon ise eski token'ı kilitleyemeden bekler ve iptal
// edilmiş bulur.
func revokeAllRefreshTokens(tx *gorm.DB, userID string) error {
	if err := tx.Exec(`SELECT id FROM refresh_tokens WHERE user_id = ? AND revoked_at IS NULL FOR UPDATE`,
		userID).Error; err != nil {
		return err
	}
	return tx.Model(&models.RefreshToken{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", time.Now()).Error
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
	// Yanıt da commit'ten önce hazırlanır: rotasyon commit edildikten sonra
	// yanıt hazırlanamasaydı istemci 500 alır, yeni token'ı hiç görmez ve
	// yeniden denemesi tolerans süresini aşarsa oturum ailesi iptal edilirdi.
	resp, err := h.authResponse(&user, pair)
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

// linkProvider, doğrulanmış sağlayıcı kimliğini oturum sahibine bağlar ve
// {"user": ...} döner.
//
// Oturum sahibi misafirse bağlama hesabı kaydeder: is_guest=false olur ve
// sağlayıcının doğruladığı adres başka bir hesapta değilse kullanıcının
// e-postası o olur (değilse yer tutucu adres kalır). Kimlik başka bir
// kullanıcıya bağlıysa 409 provider_already_linked döner; istemci misafiri o
// hesaba geçirme akışını yürütür.
//
// Adres burada geri alınmaz (bkz. reclaimEmail). Sosyal girişte geri almayı
// gerektiren tıkanma bağlamada oluşmaz: adres başka bir hesaptayken de bağlama
// başarılıdır, adresin sahibi kimliğini bağlar ve ondan sonra o kimlikle
// (subject ile) her zaman kendi hesabına girer; eksik kalan yalnızca adresin
// hesaba yazılmasıdır. Geri almak ise bir bağlama isteğinin üçüncü bir hesabın
// parolasını silmesi ve oturumlarını kapatması olurdu; o hesabın tek giriş yolu
// parolaysa hesap girişsiz kalırdı (devirdeki gibi sahibine verilemez: oturum
// sahibinin zaten kendi hesabı vardır). O hesap adresin sahibinin önceden
// parolayla açtığı kendi hesabı da olabilir.
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
	verifiedEmail := providerEmail(si)
	var email *string
	if verifiedEmail != "" {
		email = &verifiedEmail
	}
	converted := false
	// Kullanıcı satırı kilitlenir: misafirlik kararı, aynı anda gelen
	// POST /auth/link/password ile çelişmesin (ikisi birden hesabı kaydedemez).
	err = h.DB.Transaction(func(tx *gorm.DB) error {
		var user models.User
		if err := lockUser(tx, userID, &user); err != nil {
			return err
		}
		// Kilit, aynı anda süren bir devri de bekletir: devir önce biterse
		// sürüm artmıştır ve eski oturum burada reddedilir.
		if err := requireCurrentSession(c, &user); err != nil {
			return err
		}
		if err := tx.Create(&models.AuthIdentity{
			UserID:          userID,
			Provider:        provider,
			ProviderSubject: &sub,
			Email:           email,
			EmailVerified:   email != nil,
		}).Error; err != nil {
			return err
		}
		if !user.IsGuest {
			return nil
		}
		converted = true
		return convertGuest(tx, userID, verifiedEmail)
	})
	if err != nil {
		// Kimlik bu arada başka bir hesaba bağlandı ya da hesapta bu sağlayıcının
		// başka bir kimliği var. Diğer hatalar geçicidir: 500 döner.
		if !isUniqueViolation(err) {
			return err
		}
		// Aynı bağlama isteği eşzamanlı iki kez geldiyse (çift dokunuş) öteki
		// istek kimliği bu kullanıcıya bağlamıştır: tekrar istek gibi 200 döner.
		var winner models.AuthIdentity
		rerr := h.DB.Where("provider = ? AND provider_subject = ?", provider, si.Subject).First(&winner).Error
		if rerr == nil && winner.UserID == userID {
			return h.respondUser(c, userID)
		}
		if rerr != nil && !errors.Is(rerr, gorm.ErrRecordNotFound) {
			return rerr
		}
		return apiErr(fiber.StatusConflict, "provider_already_linked", "bağlama başarısız")
	}
	if converted {
		log.Printf("auth: misafir hesabı %s ile kaydedildi (user=%s)", provider, userID)
	}
	return h.respondUser(c, userID)
}

// convertGuest, kilitli misafir hesabını kayıtlı hesaba çevirir: is_guest=false
// olur ve email boş değilse, başka bir hesapta değilse kullanıcının adresi o
// olur. Adres yazılamazsa (başka hesapta ya da eşzamanlı olarak alındı) yer
// tutucu adres kalır; dönüşüm yine tamamlanır.
func convertGuest(tx *gorm.DB, userID, email string) error {
	if err := tx.Model(&models.User{}).Where("id = ?", userID).Update("is_guest", false).Error; err != nil {
		return err
	}
	if email == "" {
		return nil
	}
	var taken int64
	if err := tx.Model(&models.User{}).Where("email = ?", email).Count(&taken).Error; err != nil {
		return err
	}
	if taken > 0 {
		return nil
	}
	// Savepoint: aynı adres bu arada başka bir hesaba yazıldıysa unique ihlali
	// yalnızca bu adımı geri alır, bağlama ve dönüşüm sürer.
	err := tx.Transaction(func(sp *gorm.DB) error {
		return sp.Model(&models.User{}).Where("id = ?", userID).Update("email", email).Error
	})
	if isUniqueViolation(err) {
		return nil
	}
	return err
}

// LinkPassword: POST /auth/link/password — misafir hesabına e-posta/parola
// bağlar (hesabı kaydeder) ve {"user": ...} döner.
//
// Yalnızca misafir hesapta çalışır (aksi 409 not_guest): kayıtlı bir hesaba
// sonradan parola eklemek, sahipliği kanıtlanmamış bir adresi o hesaba
// yazmak olurdu. Doğrulama ve hata kodları kayıtla aynıdır; adres başka bir
// hesaptaysa 409 email_in_use. Yanıtı kaybolan isteğin aynı e-posta ve
// parolayla tekrarı 200 döner.
func (h *Handlers) LinkPassword(c *fiber.Ctx) error {
	var req dto.LinkPasswordRequest
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
	// bcrypt yavaştır: kullanıcı satırı kilitlenmeden önce hesaplanır.
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return err
	}

	userID := middleware.UserID(c)
	err = h.DB.Transaction(func(tx *gorm.DB) error {
		var user models.User
		if err := lockUser(tx, userID, &user); err != nil {
			return err
		}
		if err := requireCurrentSession(c, &user); err != nil {
			return err
		}
		if !user.IsGuest {
			if linkedPasswordMatches(tx, &user, req.Email, req.Password) {
				return nil // yanıtı kaybolan bağlamanın tekrarı
			}
			return apiErr(fiber.StatusConflict, "not_guest", "hesap zaten kayıtlı")
		}
		var taken int64
		if err := tx.Model(&models.User{}).Where("email = ? AND id <> ?", req.Email, userID).
			Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return apiErr(fiber.StatusConflict, "email_in_use", "bu email zaten kullanımda")
		}
		if err := tx.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]any{
			"email":    req.Email,
			"is_guest": false,
		}).Error; err != nil {
			return err
		}
		return tx.Create(&models.AuthIdentity{
			UserID:       userID,
			Provider:     "password",
			PasswordHash: &hash,
			Email:        &req.Email,
		}).Error
	})
	if isUniqueViolation(err) {
		// Aynı adres bu arada başka bir hesaba yazıldı.
		return apiErr(fiber.StatusConflict, "email_in_use", "bu email zaten kullanımda")
	}
	if err != nil {
		return err
	}
	return h.respondUser(c, userID)
}

// linkedPasswordMatches, kayıtlı hesabın adresi email ise ve parola kimliği
// password'le eşleşiyorsa true döner. Okuma hatası "eşleşmiyor" sayılır;
// çağıran 409 döner, istemci durumu /auth/me ile yeniler.
func linkedPasswordMatches(tx *gorm.DB, user *models.User, email, password string) bool {
	if user.Email != email {
		return false
	}
	var identity models.AuthIdentity
	if err := tx.Where("user_id = ? AND provider = ?", user.ID, "password").First(&identity).Error; err != nil {
		return false
	}
	return identity.PasswordHash != nil && auth.VerifyPassword(*identity.PasswordHash, password)
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
			// Kimlikler kilitliyken okunur: aynı anda süren bir devir ya da geri
			// alma bitmişse sürüm artmıştır.
			var user models.User
			if err := findUser(tx, userID, &user); err != nil {
				return err
			}
			if err := requireCurrentSession(c, &user); err != nil {
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

	// Eski oturum (devirden ya da geri almadan önce verilmiş access token)
	// hesabı silemez; Apple erişimi de iptal edilmeden reddedilir. Hesap yoksa
	// silme eskisi gibi sessizce tamamlanır.
	version := middleware.TokenVersion(c)
	var user models.User
	err := h.DB.First(&user, "id = ?", userID).Error
	if err == nil {
		if err := requireCurrentSession(c, &user); err != nil {
			return err
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	if err := h.revokeAppleAccess(userID, strings.TrimSpace(req.AppleAuthorizationCode)); err != nil {
		return err
	}

	if err := deleteUserData(h.DB, userID, version); err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// deleteUserData, kullanıcıyı ve bütün verisini tek transaction'da siler.
//
// Kilit sırası learn ve swipe ile aynıdır: önce kelime satırları, sonra
// kullanıcı satırı. Doğrudan users'ı silmek önce kullanıcı satırını kilitler,
// CASCADE sonra kelime satırlarını ister; aynı anda kelime satırını tutup
// kullanıcı satırını bekleyen bir swipe ile kilitlenme (deadlock) olurdu.
//
// Başka bir ters sıra (ör. refresh token satırını tutup kullanıcıya bağlı
// satır yazan bir istek) yine kilitlenme üretebilir; PostgreSQL bu durumda
// işlemlerden birini 40P01 ile iptal eder. Silme o zaman bir kez yeniden
// denenir: karşı işlem bu arada ilerler ve ikinci deneme beklediği kilidi alır.
//
// Kullanıcı satırı yalnızca oturum sürümü tokenVersion'sa silinir: silme,
// aynı anda süren bir devri beklediyse sürüm artmıştır ve eski oturumun silme
// isteği 401 ile geri alınır (bkz. requireCurrentSession). Satır zaten yoksa
// silme tamamlanmış sayılır.
func deleteUserData(db *gorm.DB, userID string, tokenVersion int) error {
	del := func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("user_id = ?", userID).Delete(&models.WordProgress{}).Error; err != nil {
				return err
			}
			res := tx.Delete(&models.User{}, "id = ? AND token_version = ?", userID, tokenVersion)
			if res.Error != nil || res.RowsAffected > 0 {
				return res.Error
			}
			var kalan int64
			if err := tx.Model(&models.User{}).Where("id = ?", userID).Count(&kalan).Error; err != nil {
				return err
			}
			if kalan > 0 {
				return apiErr(fiber.StatusUnauthorized, "unauthenticated", "oturum geçersiz")
			}
			return nil
		})
	}
	err := del()
	if isDeadlock(err) {
		log.Printf("hesap silme: kilitlenme (40P01), yeniden deneniyor (user=%s)", userID)
		err = del()
	}
	return err
}

// isDeadlock, err PostgreSQL'in deadlock_detected (40P01) hatasıysa true döner.
func isDeadlock(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "40P01"
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
	if h.Cfg == nil || !h.Cfg.IsProduction() || h.Cfg.AppleClientID == "" || h.AppleRevoker != nil {
		return false
	}
	err := errors.New("APPLE_TEAM_ID/APPLE_KEY_ID/APPLE_PRIVATE_KEY eksik ya da geçersiz: " +
		"Apple ile giriş açık ama hesap silmede token iptali YAPILMAYACAK (App Store 5.1.1(v))")
	log.Printf("!!! UYARI !!! %v", err)
	observability.CaptureError(err)
	return true
}
