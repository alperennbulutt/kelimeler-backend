// Package web, binary'ye gömülü statik sayfaları (gizlilik politikası gibi)
// tutar. Coolify'de ayrı bir statik site kaynağı açmamak için sayfalar
// doğrudan API uygulaması tarafından servis edilir.
package web

import _ "embed"

// PrivacyPolicyHTML, mağaza listelemelerinde verilen gizlilik politikası sayfası.
// Kaynağı Flutter reposundaki docs/privacy-policy.html ile aynı içeriktir;
// güncellerken iki kopyayı birlikte güncelle.
//
//go:embed privacy-policy.html
var PrivacyPolicyHTML []byte
