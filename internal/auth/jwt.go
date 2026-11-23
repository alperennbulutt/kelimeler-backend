package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenService, JWT access token üretir/doğrular ve opak refresh token üretir.
type TokenService struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration

	// guestRefreshTTL, misafir hesaplarının refresh token ömrüdür; 0 ise
	// refreshTTL kullanılır (bkz. WithGuestRefreshTTL).
	guestRefreshTTL time.Duration
}

func NewTokenService(secret string, accessTTL, refreshTTL time.Duration) *TokenService {
	return &TokenService{
		secret:     []byte(secret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}
}

// Access token'ın iss ve aud claim'leri. Bu claim'lerden önce üretilmiş
// token'larda ikisi de yoktur; süreleri dolana kadar (access TTL) kabul edilmeye
// devam eder, ama varsa değerleri doğrulanır.
const (
	AccessTokenIssuer   = "kelimeler-backend"
	AccessTokenAudience = "kelimeler-app"
)

// AccessClaims, access token'ın taşıdığı bilgiler.
type AccessClaims struct {
	jwt.RegisteredClaims
	Email string `json:"email"`

	// TokenVersion, token verilirken hesabın oturum sürümüdür
	// (models.User.TokenVersion). Claim'i taşımayan eski token'lar 0 sayılır.
	TokenVersion int `json:"tv,omitempty"`
}

// GenerateAccessToken, kullanıcı için kısa ömürlü HS256 access token üretir.
// tokenVersion, hesabın o anki oturum sürümüdür.
func (s *TokenService) GenerateAccessToken(userID, email string, tokenVersion int) (string, error) {
	now := time.Now()
	claims := AccessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    AccessTokenIssuer,
			Audience:  jwt.ClaimStrings{AccessTokenAudience},
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessTTL)),
		},
		Email:        email,
		TokenVersion: tokenVersion,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
}

// errInvalidAccessToken, istemciye ayrıntı vermeyen tek tip ret hatasıdır.
var errInvalidAccessToken = errors.New("geçersiz veya süresi dolmuş token")

// ParseAccessToken, access token'ı doğrular ve claim'leri döndürür.
//
// Yalnızca HS256 kabul edilir ve exp zorunludur: exp'siz bir token süresiz
// geçerli sayılırdı. iss/aud varsa beklenen değer olmalıdır; yoksa token bu
// claim'lerden önce üretilmiştir ve süresi dolana kadar geçerlidir.
func (s *TokenService) ParseAccessToken(tokenStr string) (*AccessClaims, error) {
	claims := &AccessClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		return s.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		return nil, errInvalidAccessToken
	}
	if claims.Issuer != "" && claims.Issuer != AccessTokenIssuer {
		return nil, errInvalidAccessToken
	}
	if len(claims.Audience) > 0 && !slices.Contains(claims.Audience, AccessTokenAudience) {
		return nil, errInvalidAccessToken
	}
	if claims.Subject == "" {
		return nil, errInvalidAccessToken
	}
	return claims, nil
}

func (s *TokenService) AccessTTL() time.Duration  { return s.accessTTL }
func (s *TokenService) RefreshTTL() time.Duration { return s.refreshTTL }

// WithGuestRefreshTTL, misafir hesaplarına verilecek refresh token ömrünü
// ayarlar. Sıfır ya da negatif değer misafire olağan ömrü verir.
func (s *TokenService) WithGuestRefreshTTL(d time.Duration) *TokenService {
	s.guestRefreshTTL = d
	return s
}

// RefreshTTLFor, kullanıcının yeni refresh token'ının ömrünü döner: misafire
// guestRefreshTTL (ayarlıysa), diğerlerine refreshTTL. Karar her token
// üretiminde (giriş ve rotasyon) kullanıcının o anki durumuna göre verilir;
// misafir hesabını kaydedince sonraki rotasyonlar olağan ömre döner.
func (s *TokenService) RefreshTTLFor(guest bool) time.Duration {
	if guest && s.guestRefreshTTL > 0 {
		return s.guestRefreshTTL
	}
	return s.refreshTTL
}

// GenerateRefreshToken, kriptografik olarak rastgele opak bir token üretir.
// Token'ın kendisi kullanıcıya verilir; DB'de yalnızca SHA-256 hash'i saklanır.
func GenerateRefreshToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// rotatedRefreshTokenLabel, rotasyonla türetilen refresh token'ın HMAC
// girdisinin önekidir: aynı gizli anahtarla başka bir amaçla üretilen bir
// değerle çakışmasın.
const rotatedRefreshTokenLabel = "kelimeler-refresh-rotation-v1:"

// RotatedRefreshToken, prev refresh token'ının rotasyonda yerine geçecek
// token'ı üretir: HMAC-SHA256(gizli anahtar, etiket + prev), hex. Biçimi
// GenerateRefreshToken'ınkiyle aynıdır (64 hex karakter).
//
// Halef rastgele değil prev'den türetilir: yanıtı kaybolan bir yenileme
// aynı (artık iptal edilmiş) token'la yeniden denendiğinde sunucu yalnızca
// hash'ini sakladığı halefi yeniden üretip AYNI token'ı döner. Rastgele
// üretilseydi her deneme yeni bir token verir, istemci eline ilk ulaşan
// yanıtı saklayıp ötekini kaybettiğinde oturumu yeniden kullanım tespitine
// takılabilirdi. Halefi üretmek için prev ve gizli anahtar gerekir; prev'e
// sahip olan zaten yenileme yapabildiği için yeni bir yetki doğmaz.
func (s *TokenService) RotatedRefreshToken(prev string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(rotatedRefreshTokenLabel))
	mac.Write([]byte(prev))
	return hex.EncodeToString(mac.Sum(nil))
}

// GenerateFamilyID, bir girişte açılan oturumun (refresh token ailesinin)
// kimliğini üretir. Rotasyonla üretilen her token aynı kimliği taşır.
func GenerateFamilyID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// HashToken, refresh token'ın DB'de saklanacak hash'ini döndürür.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
