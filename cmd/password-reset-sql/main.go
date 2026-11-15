// Command password-reset-sql, "Parolamı unuttum" destek e-postasına yardım
// etmek için bir kullanıcıya geçici parola üretir ve canlı veritabanında
// çalıştırılacak SQL'i yazdırır. Veritabanına kendisi bağlanmaz; çıktı
// Coolify > kelimeler-db > Terminal'e yapıştırılır.
//
// Kullanım (backend klasöründe):
//
//	make password-reset-sql EMAIL=ayse@example.com
//
// Önce e-postanın gerçekten o adresten geldiğinden emin olun: hesabın sahibi
// yazanın kendisi olmalı. SQL parolayı değiştirir, kullanıcının bütün
// cihazlardaki oturumlarını kapatır ve "UPDATE 1" yazmazsa hiçbir şey
// değiştirmez (hesap yok ya da hesabın parola girişi yok).
package main

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"net/mail"
	"os"
	"strings"

	"kelimeler-backend/internal/auth"
)

// Karışabilen karakterler (0/O, 1/l/I) yok; telefonda yazması kolay olsun.
const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// tempPasswordLength: dto.MinPasswordLength (8) üzerinde, tahmin edilemez.
const tempPasswordLength = 12

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "Kullanım: password-reset-sql <e-posta>")
		os.Exit(2)
	}
	email, err := normalizeEmail(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "HATA:", err)
		os.Exit(2)
	}
	password, err := tempPassword()
	if err != nil {
		fmt.Fprintln(os.Stderr, "HATA: parola üretilemedi:", err)
		os.Exit(1)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		fmt.Fprintln(os.Stderr, "HATA: parola özetlenemedi:", err)
		os.Exit(1)
	}

	fmt.Println("1) Coolify > kelimeler-db > Terminal'e aşağıdaki bloğun TAMAMINI yapıştırın.")
	fmt.Println("   Çıktıda üç satırın da 'UPDATE 1' / 'DELETE n' / 'UPDATE 1' olduğunu ve en sonda COMMIT yazdığını görün.")
	fmt.Println("   'UPDATE 0' görürseniz hesap yok ya da hesabın parola girişi yok (Google/Apple ile açılmış): hiçbir şey değişmez.")
	fmt.Println()
	fmt.Print(resetSQL(email, hash))
	fmt.Println()
	fmt.Println("2) Kullanıcıya yalnızca kendi e-postasına yanıt olarak şunu iletin:")
	fmt.Printf("   Geçici parolan: %s\n", password)
	fmt.Println("   Bu parolayla giriş yapabilirsin. Uygulamada parolayı değiştirme henüz olmadığı için")
	fmt.Println("   bir sonraki güncellemeye kadar bu parolayı kullanabilir ya da hesabına Google/Apple bağlayabilirsin.")
}

func normalizeEmail(s string) (string, error) {
	s = strings.TrimSpace(s)
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s || strings.ContainsAny(s, `'\;`) {
		return "", fmt.Errorf("geçerli bir e-posta adresi değil: %q", s)
	}
	return s, nil
}

func tempPassword() (string, error) {
	var b strings.Builder
	max := big.NewInt(int64(len(alphabet)))
	for i := 0; i < tempPasswordLength; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(alphabet[n.Int64()])
	}
	return b.String(), nil
}

// sqlString, SQL tek tırnak dizgisi üretir. normalizeEmail tırnağı zaten
// reddeder; bu ikinci bir güvenceden ibarettir.
func sqlString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// resetSQL tek bir işlemde (1) parola kimliğinin özetini değiştirir, (2)
// hesabın bütün yenileme anahtarlarını siler ve (3) oturum sürümünü artırıp
// verilmiş access token'ları geçersiz kılar. ON_ERROR_STOP ile bir adım
// hata verirse işlem geri alınır. users.email citext olduğu için büyük/küçük
// harf farkı önemsizdir.
func resetSQL(email, hash string) string {
	e := sqlString(email)
	return fmt.Sprintf(`psql -U postgres -d kelimeler -v ON_ERROR_STOP=1 <<'SQL'
BEGIN;
UPDATE auth_identities SET password_hash = %s
  WHERE provider = 'password' AND user_id = (SELECT id FROM users WHERE email = %s);
DELETE FROM refresh_tokens WHERE user_id = (SELECT id FROM users WHERE email = %s);
UPDATE users SET token_version = token_version + 1, updated_at = now()
  WHERE email = %s AND EXISTS (
    SELECT 1 FROM auth_identities WHERE provider = 'password' AND user_id = users.id);
COMMIT;
SQL
`, sqlString(hash), e, e, e)
}
