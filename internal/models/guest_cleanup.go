package models

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// Terk edilmiş misafir hesaplarının temizliği (cmd/cleanup-guests).
//
// Misafirin son etkinliği, hesabın bıraktığı zaman damgalarının en yenisidir:
//   - refresh_tokens.created_at: asıl ölçüt. Uygulama her açılışta access
//     token'ı (ACCESS_TOKEN_TTL, 15 dk) yenilemek için rotasyon yapar ve her
//     rotasyon yeni bir satır yazar. Misafirin token ömrü (GUEST_REFRESH_TOKEN_TTL,
//     varsayılan 1 yıl) bu temizliğin eşiğinden uzun olduğu için son satır
//     saatlik token temizliğinden (süresi/iptali 7 günden eski satırlar) sağ
//     çıkar. Satır kalmamışsa misafirin token'ı da kalmamıştır: hesaba zaten
//     ulaşılamaz.
//   - word_progresses.updated_at, daily_logins.created_at: öğrenme/tekrar ve
//     günlük giriş kaydı (token satırı bir şekilde yoksa yedek ölçüt).
//   - users.updated_at / users.created_at: profil ve işaretçi yazımları; hiç
//     kullanılmamış hesapta açılış anı.
//
// Yalnızca hâlâ misafir olan (is_guest) ve hiçbir giriş kimliği olmayan
// hesaplar aday olur: kaydedilmiş bir hesap hiçbir koşulda silinmez.

// guestLastActivitySQL, u takma adlı users satırının son etkinlik zamanıdır.
// GREATEST, NULL'ları (hiç satırı olmayan tablolar) yok sayar.
const guestLastActivitySQL = `GREATEST(u.created_at, u.updated_at,
	(SELECT max(rt.created_at) FROM refresh_tokens rt WHERE rt.user_id = u.id),
	(SELECT max(wp.updated_at) FROM word_progresses wp WHERE wp.user_id = u.id),
	(SELECT max(dl.created_at) FROM daily_logins dl WHERE dl.user_id = u.id))`

// inactiveGuestSQL, u satırının cutoff'tan (tek parametre) önce son kez
// etkin olmuş bir misafir olduğu koşuludur.
const inactiveGuestSQL = `u.is_guest
	AND NOT EXISTS (SELECT 1 FROM auth_identities ai WHERE ai.user_id = u.id)
	AND ` + guestLastActivitySQL + ` < ?`

// InactiveGuest, temizlik adayı bir misafir hesabıdır.
type InactiveGuest struct {
	ID           string
	CreatedAt    time.Time
	LastActivity time.Time
}

// FindInactiveGuests, son etkinliği cutoff'tan önce olan misafirleri en eski
// etkinlikten başlayarak döner.
func FindInactiveGuests(ctx context.Context, db *gorm.DB, cutoff time.Time) ([]InactiveGuest, error) {
	var out []InactiveGuest
	err := db.WithContext(ctx).Raw(`SELECT u.id, u.created_at, `+guestLastActivitySQL+` AS last_activity
		FROM users u WHERE `+inactiveGuestSQL+` ORDER BY last_activity, u.id`, cutoff).
		Scan(&out).Error
	return out, err
}

// DeleteInactiveGuest, misafiri hâlâ aday ise siler ve silindiyse true döner.
//
// Kullanıcı satırı önce kilitlenir, koşul kilitten sonra yeniden sınanır:
// aday listesi okunduktan sonra uygulamayı açan misafirin yeni token'ı (ya da
// hesabı kaydetmesi) görülür ve hesap silinmez. Kilit alınmışken gelen
// yenileme, token satırını yazmak için bu silmeyi bekler. Bağlı veriler
// (token'lar, kelime ilerlemesi, girişler, rozetler) FK CASCADE ile silinir.
//
// Kilit sırası (önce kullanıcı, CASCADE ile sonra kelime satırları) learn ve
// swipe'ınkinin tersidir; ama aday aylardır etkin değildir. Yine de aynı anda
// tekrar yapan bir misafirle kilitlenme olursa PostgreSQL işlemlerden birini
// 40P01 ile iptal eder; iptal edilen silmeyse hata döner ve misafir silinmez.
func DeleteInactiveGuest(ctx context.Context, db *gorm.DB, userID string, cutoff time.Time) (bool, error) {
	var deleted bool
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ids []string
		if err := tx.Raw(`SELECT id FROM users WHERE id = ? AND is_guest FOR UPDATE`, userID).
			Scan(&ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil // bu arada silindi ya da kaydedildi
		}
		res := tx.Exec(`DELETE FROM users u WHERE u.id = ? AND `+inactiveGuestSQL, userID, cutoff)
		deleted = res.RowsAffected > 0
		return res.Error
	})
	return deleted, err
}
