package appversion

import (
	"strings"
	"testing"
)

func mustParse(t *testing.T, s string) Version {
	t.Helper()
	v, ok := Parse(s)
	if !ok {
		t.Fatalf("Parse(%q) başarısız", s)
	}
	return v
}

func TestParse(t *testing.T) {
	gecerli := []string{"1", "1.2", "1.2.3", "1.2.3+45", "0.0.0", "1.2+7", " 1.2.0 ", "10.20.30+0", "01.2.3"}
	for _, s := range gecerli {
		v, ok := Parse(s)
		if !ok {
			t.Errorf("Parse(%q) ok=false, beklenen true", s)
			continue
		}
		if v.Raw != strings.TrimSpace(s) {
			t.Errorf("Parse(%q).Raw = %q", s, v.Raw)
		}
	}

	gecersiz := []string{
		"", " ", "v1.2.0", "1.2.3.4", "1.2.0-beta", "1.2.0-beta+3", "1.2.0+", "+5", "1..2", "1.2.",
		".1", "1.2.0+abc", "1.2.0+1+2", "1.-2.0", "1.+2.0", "a.b.c", "1.2.0 +5",
		"99999999999999999999.0.0", strings.Repeat("1", maxVersionLen+1),
	}
	for _, s := range gecersiz {
		if v, ok := Parse(s); ok {
			t.Errorf("Parse(%q) = %+v, ok=true; beklenen false", s, v)
		}
	}
}

func TestBelow(t *testing.T) {
	cases := []struct {
		surum, sinir string
		altinda      bool
	}{
		// Ad karşılaştırması sayısaldır, metinsel değil.
		{"1.2.0", "1.10.0", true},
		{"1.10.0", "1.2.0", false},
		{"0.9.9", "1.0.0", true},
		{"2.0.0", "1.9.9", false},
		{"1.0.0", "1.0.0", false},

		// Eksik parça 0'dır: 1.2 = 1.2.0.
		{"1.2", "1.2.0", false},
		{"1.2.0", "1.2", false},
		{"1", "1.0.1", true},
		{"1.2", "1.2.1", true},

		// Sınırda build yoksa yalnızca ad karşılaştırılır.
		{"1.2.0+1", "1.2.0", false},
		{"1.1.9+999", "1.2.0", true},

		// Sınırda build varsa adlar eşitken build de karşılaştırılır.
		{"1.2.0+14", "1.2.0+15", true},
		{"1.2.0+15", "1.2.0+15", false},
		{"1.2.0+16", "1.2.0+15", false},
		{"1.2+3", "1.2.0+15", true},
		// ...ama ad farklıysa build'e bakılmaz.
		{"1.3.0+1", "1.2.0+15", false},
		{"1.1.0+99", "1.2.0+15", true},
		// İstemcide build yoksa build bilinmiyordur: engellenmez.
		{"1.2.0", "1.2.0+15", false},
	}
	for _, c := range cases {
		got := mustParse(t, c.surum).Below(mustParse(t, c.sinir))
		if got != c.altinda {
			t.Errorf("%q.Below(%q) = %v, beklenen %v", c.surum, c.sinir, got, c.altinda)
		}
	}

	// Boş sınır = sınır yok; boş sürüm hiçbir sınırın altında değildir.
	if mustParse(t, "0.0.1").Below(Version{}) {
		t.Error("boş sınır engelledi")
	}
	if (Version{}).Below(mustParse(t, "9.9.9")) {
		t.Error("boş sürüm sınırın altında sayıldı")
	}
}

func TestParsePlatform(t *testing.T) {
	for s, want := range map[string]Platform{"ios": IOS, "android": Android, " iOS ": IOS, "ANDROID": Android} {
		if got, ok := ParsePlatform(s); !ok || got != want {
			t.Errorf("ParsePlatform(%q) = %q, %v", s, got, ok)
		}
	}
	for _, s := range []string{"", "web", "macos", "i os"} {
		if _, ok := ParsePlatform(s); ok {
			t.Errorf("ParsePlatform(%q) ok=true, beklenen false", s)
		}
	}
}

func testPolicy(t *testing.T) Policy {
	t.Helper()
	return Policy{
		IOS: Rule{
			Min:      mustParse(t, "1.0.0"),
			Latest:   mustParse(t, "1.1.0"),
			StoreURL: "https://apps.apple.com/app/id123",
		},
		Android: Rule{
			Min:      mustParse(t, "1.0.0+3"),
			StoreURL: "https://play.google.com/store/apps/details?id=x",
		},
	}
}

func TestPolicyCheck(t *testing.T) {
	p := testPolicy(t)
	iosAlanlari := Result{MinVersion: "1.0.0", LatestVersion: "1.1.0", StoreURL: "https://apps.apple.com/app/id123"}
	androidAlanlari := Result{MinVersion: "1.0.0+3", StoreURL: "https://play.google.com/store/apps/details?id=x"}

	with := func(r Result, required, available bool) Result {
		r.UpdateRequired, r.UpdateAvailable = required, available
		return r
	}

	cases := []struct {
		ad, platform, surum string
		want                Result
	}{
		{"zorunlu", "ios", "0.9.0+1", with(iosAlanlari, true, true)},
		{"yalnizca yeni surum var", "ios", "1.0.5+20", with(iosAlanlari, false, true)},
		{"guncel", "ios", "1.1.0+30", with(iosAlanlari, false, false)},
		{"daha yeni", "ios", "2.0.0", with(iosAlanlari, false, false)},
		{"platform buyuk harf", "IOS", "0.9.0", with(iosAlanlari, true, true)},
		// Latest ayarlı değilken zorunlu güncelleme yine "yeni sürüm var"dır.
		{"android build ile zorunlu", "android", "1.0.0+2", with(androidAlanlari, true, true)},
		{"android ayni build", "android", "1.0.0+3", with(androidAlanlari, false, false)},
		{"android latest yok", "android", "1.5.0+9", with(androidAlanlari, false, false)},
		// Fail-open: bayraklar false; platform biliniyorsa alanlar yine döner.
		{"surum yok", "ios", "", with(iosAlanlari, false, false)},
		{"surum bozuk", "ios", "abc", with(iosAlanlari, false, false)},
		{"bilinmeyen platform", "web", "0.1.0", Result{}},
		{"platform yok", "", "0.1.0", Result{}},
	}
	for _, c := range cases {
		t.Run(c.ad, func(t *testing.T) {
			if got := p.Check(c.platform, c.surum); got != c.want {
				t.Errorf("Check(%q, %q) = %+v, beklenen %+v", c.platform, c.surum, got, c.want)
			}
		})
	}
}

func TestPolicyBlocked(t *testing.T) {
	p := testPolicy(t)
	if b, url := p.Blocked("ios", "0.9.0"); !b || url != "https://apps.apple.com/app/id123" {
		t.Errorf("Blocked(ios, 0.9.0) = %v, %q", b, url)
	}
	if b, _ := p.Blocked("ios", "1.0.0"); b {
		t.Error("sınırdaki sürüm engellendi")
	}
	// Sıfır politika hiçbir şeyi engellemez.
	if b, _ := (Policy{}).Blocked("ios", "0.0.1"); b {
		t.Error("boş politika engelledi")
	}
}
