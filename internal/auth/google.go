package auth

import (
	"context"
	"errors"

	"google.golang.org/api/idtoken"
)

// SocialIdentity, Google/Apple gibi sağlayıcılardan doğrulanmış kimlik bilgisi.
type SocialIdentity struct {
	Subject string // sağlayıcının stabil kullanıcı id'si (sub)
	Email   string
	Name    string
}

// VerifyGoogleIDToken, Google idToken'ını izin verilen audience'lara karşı doğrular.
// Audience listesindeki herhangi biriyle eşleşirse kimliği döndürür.
func VerifyGoogleIDToken(ctx context.Context, token string, allowedAudiences []string) (*SocialIdentity, error) {
	if len(allowedAudiences) == 0 {
		return nil, errors.New("google client id yapılandırılmamış")
	}

	var lastErr error
	for _, aud := range allowedAudiences {
		payload, err := idtoken.Validate(ctx, token, aud)
		if err != nil {
			lastErr = err
			continue
		}
		si := &SocialIdentity{Subject: payload.Subject}
		if email, ok := payload.Claims["email"].(string); ok {
			si.Email = email
		}
		if name, ok := payload.Claims["name"].(string); ok {
			si.Name = name
		}
		return si, nil
	}
	return nil, lastErr
}
