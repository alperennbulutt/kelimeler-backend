package auth

import (
	"context"
	"errors"

	"google.golang.org/api/idtoken"
)

// SocialIdentity, Google/Apple gibi sağlayıcılardan doğrulanmış kimlik bilgisi.
type SocialIdentity struct {
	Subject string // sağlayıcının stabil kullanıcı id'si (sub)
	Email   string // yalnızca sağlayıcı doğrulamışsa dolu (bkz. verifiedEmail)
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
		return googleIdentity(payload.Subject, payload.Claims), nil
	}
	return nil, lastErr
}

// googleIdentity, doğrulanmış bir Google token'ının claim'lerinden kimliği çıkarır.
func googleIdentity(subject string, claims map[string]any) *SocialIdentity {
	si := &SocialIdentity{Subject: subject, Email: verifiedEmail(claims)}
	if name, ok := claims["name"].(string); ok {
		si.Name = name
	}
	return si
}

// verifiedEmail, token'daki e-postayı yalnızca sağlayıcı onu doğrulamışsa
// (email_verified) döndürür; aksi halde boş döner.
//
// Giriş akışı kullanıcıyı e-postayla eşler: doğrulanmamış bir adres taşınırsa,
// o adresin gerçek sahibinin hesabına başkası bağlanabilir. E-posta boş kalınca
// akış sağlayıcıya özgü yer tutucu adrese düşer ve eşleme yapılmaz.
//
// Google claim'i boolean, Apple ise boolean ya da "true"/"false" metni olarak
// gönderir.
func verifiedEmail(claims map[string]any) string {
	email, _ := claims["email"].(string)
	switch verified := claims["email_verified"].(type) {
	case bool:
		if verified {
			return email
		}
	case string:
		if verified == "true" {
			return email
		}
	}
	return ""
}
