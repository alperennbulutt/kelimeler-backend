package observability

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestAppVersionFrom(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"", AppVersionLegacy},
		{"1.0.3+57", "1.0.3+57"},
		{"2.1.0-beta_1", "2.1.0-beta_1"},
		{"1.0 x", AppVersionInvalid},
		{"1.0\nuser=x", AppVersionInvalid},
		{"a=b", AppVersionInvalid},
		{strings.Repeat("1", maxAppVersionLen+1), AppVersionInvalid},
	}
	for _, c := range cases {
		app := fiber.New()
		var got string
		app.Get("/", func(ctx *fiber.Ctx) error {
			got = AppVersionFrom(ctx)
			return nil
		})
		req := httptest.NewRequest("GET", "/", nil)
		if c.header != "" {
			req.Header[AppVersionHeader] = []string{c.header}
		}
		if _, err := app.Test(req, -1); err != nil {
			t.Fatalf("istek başarısız: %v", err)
		}
		if got != c.want {
			t.Errorf("AppVersionFrom(%q) = %q, beklenen %q", c.header, got, c.want)
		}
	}
}
