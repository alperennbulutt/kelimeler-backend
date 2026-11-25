// Package dictlang, sözlük kartlarının sunulduğu dilleri tanımlar.
//
// Her kart tek bir kavramdır (bir anlam); kullanıcı bu dillerden birini
// "bildiğim dil", bir başkasını "öğrendiğim dil" seçer. İngilizce ve Türkçe
// metnin bir kısmı dictionary_words'tedir (english, turkish, example), diğer
// her şey dictionary_translations'tadır (bkz. db.ApplyDictionaryTranslations).
package dictlang

// Language, desteklenen bir dildir.
type Language struct {
	// Code, ISO 639-1 kodudur (ör. "de").
	Code string `json:"code"`
	// Name, dilin kendi dilindeki adıdır (ör. "Deutsch").
	Name string `json:"name"`
	// RTL, dil sağdan sola yazılıyorsa true'dur.
	RTL bool `json:"rtl"`
}

// All, desteklenen dillerdir; sıra uygulamadaki seçim listesinin sırasıdır.
var All = []Language{
	{Code: "tr", Name: "Türkçe"},
	{Code: "en", Name: "English"},
	{Code: "de", Name: "Deutsch"},
	{Code: "fr", Name: "Français"},
	{Code: "es", Name: "Español"},
	{Code: "it", Name: "Italiano"},
	{Code: "pt", Name: "Português"},
	{Code: "ru", Name: "Русский"},
	{Code: "ar", Name: "العربية", RTL: true},
	{Code: "az", Name: "Azərbaycan"},
	{Code: "hi", Name: "हिन्दी"},
	{Code: "id", Name: "Indonesia"},
	{Code: "ja", Name: "日本語"},
	{Code: "ko", Name: "한국어"},
}

// Supported, kodun desteklenen bir dil olup olmadığını söyler.
func Supported(code string) bool {
	for _, l := range All {
		if l.Code == code {
			return true
		}
	}
	return false
}

// NeedsReading, kartlarında okunuş (r) zorunlu olan dillerdir (Japonca: kana).
func NeedsReading(code string) bool { return code == "ja" }
