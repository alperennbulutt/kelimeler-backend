package auth

import (
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
}

// GenerateAccessToken, kullanıcı için kısa ömürlü HS256 access token üretir.
func (s *TokenService) GenerateAccessToken(userID, email string) (string, error) {
	now := time.Now()
	claims := AccessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    AccessTokenIssuer,
			Audience:  jwt.ClaimStrings{AccessTokenAudience},
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessTTL)),
		},
		Email: email,
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

// GenerateRefreshToken, kriptografik olarak rastgele opak bir token üretir.
// Token'ın kendisi kullanıcıya verilir; DB'de yalnızca SHA-256 hash'i saklanır.
func GenerateRefreshToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
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
