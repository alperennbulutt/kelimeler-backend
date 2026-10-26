package dto

import "kelimeler-backend/internal/models"

type RegisterRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
}

// GuestRequest, POST /auth/guest'in isteğe bağlı gövdesidir.
type GuestRequest struct {
	DisplayName string `json:"displayName"`
}

// LinkPasswordRequest, POST /auth/link/password gövdesidir (misafir hesabına
// e-posta/parola bağlar).
type LinkPasswordRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type GoogleAuthRequest struct {
	IDToken     string `json:"idToken"`
	DisplayName string `json:"displayName"`
}

type AppleAuthRequest struct {
	IdentityToken string `json:"identityToken"`
	FullName      string `json:"fullName"`

	// Hesap silinirken Apple'a revoke gönderebilmek için refresh token'a
	// çevrilir. İstemci göndermezse revoke yapılamaz, giriş yine çalışır.
	AuthorizationCode string `json:"authorizationCode"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

type LogoutRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// DeleteAccountRequest, DELETE /auth/account'un isteğe bağlı gövdesidir.
type DeleteAccountRequest struct {
	// Saklı Apple refresh token yoksa (ya da iptali başarısız olursa) bu kod
	// token'a çevrilip iptal edilir. Gövdesiz istek de geçerlidir.
	AppleAuthorizationCode string `json:"appleAuthorizationCode"`
}

// AuthResponse, login/register/social uçlarının dönüşü.
type AuthResponse struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	User         *UserDTO `json:"user"`
}

// UserDTO, istemciye dönen kullanıcı temsili. linkedProviders, Firebase
// providerData'nın yerini alır (isGoogleLinked/isAppleLinked buradan türetilir).
//
// email, yer tutucu adreslerde (misafir ve e-postasız sosyal hesaplar, bkz.
// SyntheticEmailDomain) boş döner: istemci bunu "e-posta yok" diye gösterir.
type UserDTO struct {
	ID                      string   `json:"id"`
	Email                   string   `json:"email"`
	IsGuest                 bool     `json:"isGuest"`
	DisplayName             string   `json:"displayName"`
	CurrentLevel            string   `json:"currentLevel"`
	CurrentGroup            int      `json:"currentGroup"`
	CurrentGroupTitle       *string  `json:"currentGroupTitle,omitempty"`
	CurrentGroupDescription *string  `json:"currentGroupDescription,omitempty"`
	CurrentGroupTotalWords  int      `json:"currentGroupTotalWords"`
	TotalLearned            int      `json:"totalLearned"`
	LinkedProviders         []string `json:"linkedProviders"`
}

func NewUserDTO(u *models.User, providers []string) *UserDTO {
	if providers == nil {
		providers = []string{}
	}
	email := u.Email
	if IsSyntheticEmail(email) {
		email = ""
	}
	return &UserDTO{
		ID:                      u.ID,
		Email:                   email,
		IsGuest:                 u.IsGuest,
		DisplayName:             u.DisplayName,
		CurrentLevel:            u.CurrentLevel,
		CurrentGroup:            u.CurrentGroup,
		CurrentGroupTitle:       u.CurrentGroupTitle,
		CurrentGroupDescription: u.CurrentGroupDescription,
		CurrentGroupTotalWords:  u.CurrentGroupTotalWords,
		TotalLearned:            u.TotalLearned,
		LinkedProviders:         providers,
	}
}
