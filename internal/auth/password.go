package auth

import "golang.org/x/crypto/bcrypt"

// HashPassword, parolayı bcrypt (cost 12) ile hash'ler.
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyPassword, düz metin parolayı hash ile karşılaştırır.
func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
