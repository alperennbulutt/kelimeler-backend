package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	appleTokenURL  = "https://appleid.apple.com/auth/token"
	appleRevokeURL = "https://appleid.apple.com/auth/revoke"
	appleAudience  = "https://appleid.apple.com"
)

// AppleRevoker, Apple'ın authorization code'unu refresh token'a çevirir ve
// hesap silinirken bu token'ı iptal eder.
//
// Apple, "Sign in with Apple" sunan uygulamalardan hesap silme akışında
// kullanıcı token'ının revoke edilmesini şart koşar; yalnızca yerel kaydı
// silmek yeterli değildir.
type AppleRevoker struct {
	clientID   string
	teamID     string
	keyID      string
	privateKey *ecdsa.PrivateKey
	http       *http.Client
}

// NewAppleRevoker, gerekli kimlik bilgilerinin tamamı verildiyse bir revoker
// döner. Eksik yapılandırmada (nil, nil) döner: Apple revoke devre dışı kalır
// ama hesap silme çalışmaya devam eder. Anahtar bozuksa hata döner.
func NewAppleRevoker(clientID, teamID, keyID, privateKeyPEM string) (*AppleRevoker, error) {
	if clientID == "" || teamID == "" || keyID == "" || privateKeyPEM == "" {
		return nil, nil
	}
	key, err := parseApplePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("apple private key okunamadı: %w", err)
	}
	return &AppleRevoker{
		clientID:   clientID,
		teamID:     teamID,
		keyID:      keyID,
		privateKey: key,
		http:       &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// parseApplePrivateKey, Apple'ın verdiği .p8 (PKCS#8, EC P-256) anahtarını okur.
// Ortam değişkeninde satır sonları "\n" olarak kaçırılmış olabilir; ikisi de kabul edilir.
func parseApplePrivateKey(pemStr string) (*ecdsa.PrivateKey, error) {
	pemStr = strings.ReplaceAll(pemStr, `\n`, "\n")

	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("geçerli PEM bloğu bulunamadı")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("anahtar ECDSA değil")
	}
	return key, nil
}

// clientSecret, Apple'ın istediği ES256 imzalı client_secret JWT'sini üretir.
// Apple azami 6 aylık ömür kabul eder; her istek için kısa ömürlü üretiyoruz.
func (r *AppleRevoker) clientSecret() (string, error) {
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": r.teamID,
		"iat": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
		"aud": appleAudience,
		"sub": r.clientID,
	})
	token.Header["kid"] = r.keyID
	return token.SignedString(r.privateKey)
}

// ExchangeCode, girişte alınan authorization code'u refresh token'a çevirir.
// Revoke ancak bu token saklanmışsa yapılabilir.
func (r *AppleRevoker) ExchangeCode(ctx context.Context, code string) (string, error) {
	secret, err := r.clientSecret()
	if err != nil {
		return "", err
	}

	form := url.Values{
		"client_id":     {r.clientID},
		"client_secret": {secret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
	}

	body, err := r.postForm(ctx, appleTokenURL, form)
	if err != nil {
		return "", err
	}

	var parsed struct {
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("apple token yanıtı çözülemedi: %w", err)
	}
	if parsed.Error != "" {
		return "", fmt.Errorf("apple token hatası: %s", parsed.Error)
	}
	if parsed.RefreshToken == "" {
		return "", errors.New("apple refresh_token döndürmedi")
	}
	return parsed.RefreshToken, nil
}

// Revoke, kullanıcının refresh token'ını iptal eder; uygulamanın Apple
// hesabına erişimi sona erer.
func (r *AppleRevoker) Revoke(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return errors.New("refresh token yok")
	}

	secret, err := r.clientSecret()
	if err != nil {
		return err
	}

	form := url.Values{
		"client_id":       {r.clientID},
		"client_secret":   {secret},
		"token":           {refreshToken},
		"token_type_hint": {"refresh_token"},
	}

	// Apple başarıda boş gövdeyle 200 döner.
	_, err = r.postForm(ctx, appleRevokeURL, form)
	return err
}

func (r *AppleRevoker) postForm(ctx context.Context, endpoint string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body := make([]byte, 0, 512)
	buf := make([]byte, 512)
	for {
		n, readErr := res.Body.Read(buf)
		body = append(body, buf[:n]...)
		if readErr != nil {
			break
		}
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return body, fmt.Errorf("apple %s: HTTP %d: %s", endpoint, res.StatusCode, string(body))
	}
	return body, nil
}
