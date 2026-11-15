// Package dbtest, entegrasyon testlerinin gerçek PostgreSQL adresini okur.
//
// TEST_DATABASE_URL tanımlı değilse testler lokalde atlanır; CI'da ise
// başarısız olur. Satır kilidi, CASCADE ve gün dönümü gibi asıl riskli mantık
// yalnızca bu testlerde sınanır; CI'da sessizce atlanırlarsa "yeşil" sonuç
// yanıltıcı olur.
package dbtest

import (
	"os"
	"strconv"
)

// EnvVar, entegrasyon testlerinin veritabanı adresini taşıyan değişkendir.
const EnvVar = "TEST_DATABASE_URL"

// TB, *testing.T'nin burada kullanılan kısmıdır.
type TB interface {
	Helper()
	Fatal(args ...any)
	Skip(args ...any)
}

// DSN, TEST_DATABASE_URL'i döner. Tanımlı değilse CI ortamında (CI=true) testi
// başarısız sayar, aksi halde atlar.
func DSN(t TB) string {
	t.Helper()
	if dsn := os.Getenv(EnvVar); dsn != "" {
		return dsn
	}
	if inCI() {
		t.Fatal("CI ortamında " + EnvVar + " tanımlı değil: entegrasyon testleri atlanamaz")
		return ""
	}
	t.Skip(EnvVar + " tanımlı değil, entegrasyon testi atlanıyor")
	return ""
}

// inCI, GitHub Actions gibi CI ortamlarının tanımladığı CI değişkenine bakar.
func inCI() bool {
	ci, _ := strconv.ParseBool(os.Getenv("CI"))
	return ci
}
