// Package appversion, mobil istemcinin sürümünü sunucudaki sınırlarla
// karşılaştırır (zorunlu güncelleme / "yeni sürüm var").
//
// Karar sunucuda verilir, istemci yalnızca sonucu uygular: karşılaştırma kuralı
// ileride değişse de yayındaki sürümler etkilenmez. Her belirsiz durumda
// (bilinmeyen platform, ayrıştırılamayan sürüm, ayarlanmamış sınır) istemci
// engellenmez (fail-open): yanlış bir 426, kullanıcıyı uygulamadan büsbütün
// kilitlerdi.
package appversion

import (
	"strconv"
	"strings"
)

// Platform, istemcinin işletim sistemidir.
type Platform string

const (
	IOS     Platform = "ios"
	Android Platform = "android"
)

// ParsePlatform, "ios" ya da "android" değerini (büyük/küçük harf ve baştaki/
// sondaki boşluk fark etmez) çözer; başka her değerde ok=false döner.
func ParsePlatform(s string) (Platform, bool) {
	switch p := Platform(strings.ToLower(strings.TrimSpace(s))); p {
	case IOS, Android:
		return p, true
	}
	return "", false
}

// maxVersionLen, kabul edilen en uzun sürüm metnidir; daha uzunu ayrıştırılmaz.
const maxVersionLen = 32

// Version, pubspec biçiminde bir sürümdür: "X.Y.Z" ya da "X.Y.Z+build".
// Ad kısmında eksik parçalar 0 sayılır ("1.2" = "1.2.0").
type Version struct {
	// Raw, ayrıştırılan metindir (baştaki/sondaki boşluk atılmış).
	Raw string

	parts    [3]int
	build    int
	hasBuild bool
}

// IsZero, sürümün ayarlanmamış (boş) olduğunu söyler.
func (v Version) IsZero() bool { return v.Raw == "" }

// Parse, "1", "1.2", "1.2.3" ya da bunların "+build" ekli hâlini çözer. Her
// parça negatif olmayan bir ondalık tam sayıdır. Başka her biçimde ("v1.2",
// "1.2.3.4", "1.2.0-beta", "1.2.0+", boş) ok=false döner.
func Parse(s string) (v Version, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxVersionLen {
		return Version{}, false
	}
	name, build, hasBuild := strings.Cut(s, "+")
	parts := strings.Split(name, ".")
	if len(parts) > 3 {
		return Version{}, false
	}
	for i, p := range parts {
		n, ok := parseNumber(p)
		if !ok {
			return Version{}, false
		}
		v.parts[i] = n
	}
	if hasBuild {
		n, ok := parseNumber(build)
		if !ok {
			return Version{}, false
		}
		v.build, v.hasBuild = n, true
	}
	v.Raw = s
	return v, true
}

// parseNumber, yalnızca rakamlardan oluşan, int'e sığan bir parçayı çözer.
// strconv.Atoi tek başına "+1" ve "-1"i de kabul ederdi.
func parseNumber(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// Below, istemci sürümü v'nin limit'in altında olup olmadığını söyler.
//
// Adlar sayısal olarak karşılaştırılır. Sınır "+build" içeriyorsa adlar
// eşitken build numarası da karşılaştırılır; sınırda build yoksa yalnızca ad
// karşılaştırılır. Adlar eşit, sınırda build var ama istemcide yoksa build
// bilinmiyordur: engellenmez. Boş sınır ya da boş sürüm hiçbir zaman "altında"
// değildir.
func (v Version) Below(limit Version) bool {
	if v.IsZero() || limit.IsZero() {
		return false
	}
	for i := range v.parts {
		if v.parts[i] != limit.parts[i] {
			return v.parts[i] < limit.parts[i]
		}
	}
	if !limit.hasBuild || !v.hasBuild {
		return false
	}
	return v.build < limit.build
}

// Rule, tek bir platformun sınırları ve mağaza adresidir.
type Rule struct {
	// Min'in altındaki sürümler zorunlu güncellemeye düşer. Boşsa sınır yok.
	Min Version
	// Latest'in altındaki sürümlere kapatılabilir "yeni sürüm var" gösterilir.
	// Boşsa gösterilmez.
	Latest Version
	// StoreURL, "Güncelle" butonunun açacağı adrestir; boş olabilir.
	StoreURL string
}

// Policy, iki platformun kurallarıdır. Sıfır değeri hiçbir şeyi engellemez.
type Policy struct {
	IOS     Rule
	Android Rule
}

// Rule, platformun kuralını döner; bilinmeyen platformda ok=false.
func (p Policy) Rule(platform Platform) (Rule, bool) {
	switch platform {
	case IOS:
		return p.IOS, true
	case Android:
		return p.Android, true
	}
	return Rule{}, false
}

// Result, bir istemci sürümü için verilen karardır; JSON biçimi
// GET /api/v1/app/version-check yanıtıdır.
type Result struct {
	UpdateRequired  bool   `json:"updateRequired"`
	UpdateAvailable bool   `json:"updateAvailable"`
	MinVersion      string `json:"minVersion"`
	LatestVersion   string `json:"latestVersion"`
	StoreURL        string `json:"storeUrl"`
}

// Check, ham platform ve sürüm metinleri için kararı verir.
//
// Bilinmeyen platformda her alan boştur. Platform biliniyorsa sınırlar ve
// mağaza adresi her zaman döner; sürüm eksik ya da ayrıştırılamıyorsa iki bayrak
// da false'tur. Zorunlu güncelleme "yeni sürüm var"ı da içerir: UpdateRequired
// true ise UpdateAvailable da true'dur.
func (p Policy) Check(platform, version string) Result {
	pl, ok := ParsePlatform(platform)
	if !ok {
		return Result{}
	}
	rule, _ := p.Rule(pl)
	res := Result{
		MinVersion:    rule.Min.Raw,
		LatestVersion: rule.Latest.Raw,
		StoreURL:      rule.StoreURL,
	}
	v, ok := Parse(version)
	if !ok {
		return res
	}
	res.UpdateRequired = v.Below(rule.Min)
	res.UpdateAvailable = res.UpdateRequired || v.Below(rule.Latest)
	return res
}

// Blocked, isteğin zorunlu güncelleme yüzünden reddedilmesi gerekip
// gerekmediğini ve o platformun mağaza adresini döner. Check'in
// UpdateRequired'ıyla aynı kuraldır.
func (p Policy) Blocked(platform, version string) (blocked bool, storeURL string) {
	r := p.Check(platform, version)
	return r.UpdateRequired, r.StoreURL
}
