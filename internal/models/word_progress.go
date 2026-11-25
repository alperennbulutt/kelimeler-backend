package models

import "time"

// WordProgress, Firestore'daki users/{uid}/wordProgress/{wordId} alt dökümanıdır.
// Leitner sisteminin çekirdeğidir. JSON şekli Flutter'daki UserWord.fromMap ile uyumludur
// (id = wordId; tarihler ISO-8601 string olarak serialize edilir).
type WordProgress struct {
	ID uint64 `gorm:"primaryKey" json:"-"`

	// Index adları kurs eklenirken değişti (eskileri uq_user_word,
	// idx_wp_user_box, idx_wp_user_level_box, idx_wp_user_box_next):
	// AutoMigrate aynı adlı var olan bir index'in kolonlarını değiştirmez.
	// Eskileri db.migrate siler.
	UserID string `gorm:"type:uuid;not null;uniqueIndex:uq_wp_user_course_word,priority:1;index:idx_wp_user_course_box,priority:1;index:idx_wp_user_course_level_box,priority:1;index:idx_wp_user_course_box_next,priority:1" json:"-"`
	// Course, kelimenin öğrenildiği kurstur: öğrenilen dilin kodu (bkz.
	// dictlang). Her kursun ilerlemesi ayrıdır; aynı kart her kursta ayrı bir
	// satırdır. Kurs öncesi satırlar (ve kurs göndermeyen eski istemcinin
	// yazdıkları) İngilizce kursudur ("en"). JSON'a yazılmaz: yanıtın alanları
	// mağazadaki istemcinin UserWord'üyle birebir kalır; kurs zaten isteğin
	// kursudur.
	Course string `gorm:"not null;default:'en';uniqueIndex:uq_wp_user_course_word,priority:2;index:idx_wp_user_course_box,priority:2;index:idx_wp_user_course_level_box,priority:2;index:idx_wp_user_course_box_next,priority:2" json:"-"`
	WordID string `gorm:"not null;uniqueIndex:uq_wp_user_course_word,priority:3" json:"wordId"`

	Level string `gorm:"not null;index:idx_wp_user_course_level_box,priority:3" json:"level"`
	Grp   int    `gorm:"column:grp;not null" json:"group"`
	Box   int    `gorm:"not null;default:1;index:idx_wp_user_course_box,priority:3;index:idx_wp_user_course_level_box,priority:4;index:idx_wp_user_course_box_next,priority:3" json:"box"` // 1..6

	LastReviewDate *time.Time `json:"lastReviewDate"`
	NextReviewDate *time.Time `gorm:"index:idx_wp_user_course_box_next,priority:4" json:"nextReviewDate"`

	// Art arda verilen yanlış cevap sayısı; doğru cevap sıfırlar. Kutuyu
	// etkilemez, yalnızca istatistik içindir (kural: services.OnLeftSwipe).
	WrongCount int    `gorm:"not null;default:0" json:"wrongCount"`
	Learned    bool   `gorm:"not null;default:false" json:"learned"`
	English    string `gorm:"not null;default:''" json:"english"`
	Turkish    string `gorm:"not null;default:''" json:"turkish"`

	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"-"`

	User User `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
}
