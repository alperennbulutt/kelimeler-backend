package services

import "testing"

func TestIndexOfLevel(t *testing.T) {
	if got := IndexOfLevel("A1"); got != 0 {
		t.Errorf("IndexOfLevel(A1) = %d, beklenen 0", got)
	}
	if got := IndexOfLevel("C2"); got != 5 {
		t.Errorf("IndexOfLevel(C2) = %d, beklenen 5", got)
	}
	if got := IndexOfLevel("D1"); got != -1 {
		t.Errorf("IndexOfLevel(D1) = %d, bilinmeyen seviye -1 dönmeli", got)
	}
}

func TestAdvanceGroup(t *testing.T) {
	cases := []struct {
		name        string
		level       string
		group       int
		maxGroup    int
		wantLevel   string
		wantGroup   int
		wantChanged bool
	}{
		{
			name:  "seviye içinde bir sonraki gruba geçer",
			level: "A1", group: 3, maxGroup: 12,
			wantLevel: "A1", wantGroup: 4, wantChanged: false,
		},
		{
			name:  "seviyenin son grubu bitince sonraki seviyenin 1. grubuna geçer",
			level: "A1", group: 12, maxGroup: 12,
			wantLevel: "A2", wantGroup: 1, wantChanged: true,
		},
		{
			name:  "B2 bitince C1'e geçer",
			level: "B2", group: 28, maxGroup: 28,
			wantLevel: "C1", wantGroup: 1, wantChanged: true,
		},
		{
			// C2 son seviyedir: bitince group=0 ile "hepsi tamamlandı" işaretlenir.
			name:  "C2 son grubu bitince group 0 doner",
			level: "C2", group: 20, maxGroup: 20,
			wantLevel: "C2", wantGroup: 0, wantChanged: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lvl, grp, changed := AdvanceGroup(c.level, c.group, c.maxGroup)
			if lvl != c.wantLevel || grp != c.wantGroup || changed != c.wantChanged {
				t.Errorf("AdvanceGroup(%s, %d, %d) = (%s, %d, %v), beklenen (%s, %d, %v)",
					c.level, c.group, c.maxGroup, lvl, grp, changed,
					c.wantLevel, c.wantGroup, c.wantChanged)
			}
		})
	}
}
