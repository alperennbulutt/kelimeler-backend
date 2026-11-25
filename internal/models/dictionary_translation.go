package models

import "time"

// DictionaryTranslation, bir sözlük kartının (kavramın) tek bir dildeki
// metnidir. Kaynak seed/dictionary_translations/<dil>.json dosyalarıdır
// (bkz. db.ApplyDictionaryTranslations).
//
// İngilizcenin kelimesi ve örneği dictionary_words.english/example'dadır
// (yamalar orayı düzeltir); "en" satırı yalnızca anlam notu (Meaning) gereken
// kartlar için vardır. Türkçenin anlamı dictionary_words.turkish'tir; "tr"
// satırı yalın kelimeyi ve Türkçe örneği taşır.
type DictionaryTranslation struct {
	WordID string `gorm:"primaryKey" json:"-"`
	Lang   string `gorm:"primaryKey;size:8" json:"-"`
	// Word, o dilde öğrenilecek yalın kelimedir (ör. "die Schule").
	Word string `gorm:"not null;default:''"`
	// Meaning, kart o dili BİLEN birine anlam olarak gösterilirken kullanılır;
	// boşsa Word gösterilir. Yalnızca Word tek başına belirsizse doludur
	// (ör. "break (at school)").
	Meaning string `gorm:"not null;default:''"`
	// Example, o dilde Word'ü bu anlamda kullanan örnek cümledir.
	Example string `gorm:"not null;default:''"`
	// Reading, okunuştur (Japonca için hiragana); diğer dillerde boştur.
	Reading string `gorm:"not null;default:''"`
}

// DictionaryTranslationFile, veritabanına yüklenmiş çeviri dosyasının
// kaydıdır: içerik değişmedikçe dosya açılışta yeniden yüklenmez.
type DictionaryTranslationFile struct {
	Lang     string    `gorm:"primaryKey;size:8"`
	Checksum string    `gorm:"not null"` // dosya içeriğinin SHA-256'sı (hex)
	Entries  int       `gorm:"not null"`
	LoadedAt time.Time `gorm:"not null"`
}
