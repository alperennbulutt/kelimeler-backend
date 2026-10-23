package dbtest

import "testing"

type kayit struct {
	fatal, skip bool
}

func (*kayit) Helper()        {}
func (k *kayit) Fatal(...any) { k.fatal = true }
func (k *kayit) Skip(...any)  { k.skip = true }

func TestDSN(t *testing.T) {
	cases := []struct {
		name      string
		dsn, ci   string
		want      string
		wantFatal bool
		wantSkip  bool
	}{
		{"tanimli", "postgres://x", "true", "postgres://x", false, false},
		{"lokalde atlanir", "", "", "", false, true},
		{"CI=false atlanir", "", "false", "", false, true},
		{"CI'da basarisiz", "", "true", "", true, false},
		{"CI=1 basarisiz", "", "1", "", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(EnvVar, c.dsn)
			t.Setenv("CI", c.ci)
			k := &kayit{}
			if got := DSN(k); got != c.want {
				t.Errorf("DSN = %q, beklenen %q", got, c.want)
			}
			if k.fatal != c.wantFatal || k.skip != c.wantSkip {
				t.Errorf("fatal=%v skip=%v, beklenen fatal=%v skip=%v", k.fatal, k.skip, c.wantFatal, c.wantSkip)
			}
		})
	}
}
