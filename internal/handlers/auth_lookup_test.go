package handlers

import (
	"slices"
	"testing"

	"kelimeler-backend/internal/dto"
)

// decideEmailLookup'un karar tablosu (veritabanısız). linked,
// loadLinkedProviders gibi sağlayıcı adına göre sıralıdır.
func TestDecideEmailLookup(t *testing.T) {
	cases := []struct {
		name          string
		found         bool
		linked        []string
		wantNext      string
		wantProviders []string
	}{
		{"kayıtlı değil", false, nil, dto.LookupNextRegister, nil},
		{"yalnızca parola", true, []string{"password"}, dto.LookupNextLogin, nil},
		{"parola + google girişe gider", true, []string{"google", "password"}, dto.LookupNextLogin, nil},
		{"parola + apple + google girişe gider", true, []string{"apple", "google", "password"}, dto.LookupNextLogin, nil},
		{"yalnızca google", true, []string{"google"}, dto.LookupNextSocial, []string{"google"}},
		{"yalnızca apple", true, []string{"apple"}, dto.LookupNextSocial, []string{"apple"}},
		{"apple + google", true, []string{"apple", "google"}, dto.LookupNextSocial, []string{"apple", "google"}},
		{"bilinmeyen sağlayıcı listelenmez", true, []string{"github", "google"}, dto.LookupNextSocial, []string{"google"}},
		{"kimliksiz hesap: sosyal, boş liste", true, nil, dto.LookupNextSocial, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			next, providers := decideEmailLookup(c.found, c.linked)
			if next != c.wantNext {
				t.Errorf("next = %q, beklenen %q", next, c.wantNext)
			}
			if !slices.Equal(providers, c.wantProviders) {
				t.Errorf("providers = %v, beklenen %v", providers, c.wantProviders)
			}
			// "social" yanıtında providers her zaman dizi olarak yazılır (null değil).
			if next == dto.LookupNextSocial && providers == nil {
				t.Error("social yanıtında providers nil; JSON'da null olurdu")
			}
		})
	}
}
