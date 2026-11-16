package core

import (
	"context"
	"strings"
	"testing"
)

const groupReadme = "[known](BASE/o1/r1/main/known.txt) [a](BASE/o1/r1/main/mir-a.txt) [b](BASE/o1/r1/main/mir-bb.txt) [c](BASE/o1/r1/main/mir-c.txt) [z](BASE/o1/r1/main/zzz.txt)"

// mirrorFiles — три зеркала одного набора (100 общих ссылок; у b ещё 10 своих, у c 5 своих: настоящий Жаккар 0.91-0.95, с запасом над порогом 0.8) и один посторонний файл.
func mirrorFiles(e *discoverEnv) {
	base := discList(100, "mir")
	e.setFile("/o1/r1/main/README.md", groupReadme)
	e.setFile("/o1/r1/main/mir-a.txt", base)
	e.setFile("/o1/r1/main/mir-bb.txt", base+discList(10, "bx"))
	e.setFile("/o1/r1/main/mir-c.txt", strings.ReplaceAll(base, "#mir", "#копия")+discList(5, "cx"))
	e.setFile("/o1/r1/main/zzz.txt", discList(100, "zzz"))
}

func groupNames(g GroupView) string {
	names := []string{g.Representative.Name}
	for _, m := range g.Members {
		names = append(names, m.Name)
	}
	return strings.Join(names, ",")
}

func TestCandidateGroupsMirrors(t *testing.T) {
	e := newDiscoverEnv(t, nil, false, nil)
	mirrorFiles(e)
	if _, err := e.c.ScanCandidates(context.Background(), "user", false); err != nil {
		t.Fatal(err)
	}
	groups, err := e.c.CandidateGroups()
	if err != nil {
		t.Fatal(err)
	}
	var mir, zzz *GroupView
	for i := range groups {
		switch {
		case groups[i].Size == 3:
			mir = &groups[i]
		case strings.HasSuffix(groups[i].Representative.Name, "/zzz"):
			zzz = &groups[i]
		}
	}
	if mir == nil || zzz == nil || zzz.Size != 1 || len(zzz.Members) != 0 {
		t.Fatalf("группы: %+v", groups)
	}
	// живость нет (карантин не шёл): представитель — с большим числом ссылок
	if !strings.HasSuffix(mir.Representative.Name, "/mir-bb") || len(mir.Members) != 2 || mir.TotalLinksMax != 110 {
		t.Errorf("представитель/члены: %s, max %d", groupNames(*mir), mir.TotalLinksMax)
	}
	if mir.ID == "" || mir.ID == zzz.ID {
		t.Errorf("идентификаторы групп: %q %q", mir.ID, zzz.ID)
	}
	list, _ := e.c.Candidates()
	if len(list) != len(groups[0].Members)+len(groups[1].Members)+len(groups) {
		t.Errorf("Candidates и группы расходятся: %d", len(list))
	}
	for _, v := range list {
		want := 1
		if strings.Contains(v.Name, "/mir-") {
			want = 3
		}
		if v.GroupSize != want || v.Group == "" {
			t.Errorf("%s: group %q size %d", v.Name, v.Group, v.GroupSize)
		}
		if strings.Contains(v.Name, "/mir-") && v.Group != mir.ID {
			t.Errorf("%s вне группы зеркал", v.Name)
		}
	}
	// в базе только хэши: ссылок и узлов нет
	recs, _ := e.c.st.Candidates()
	for _, r := range recs {
		if len(r.Sketch) == 0 || len(r.Sketch) > 512 || strings.Contains(string(r.Sketch), "11111111") {
			t.Errorf("отпечаток %s: %d байт", r.Name, len(r.Sketch))
		}
	}
	if salt, err := e.c.discoverSalt(); err != nil || len(salt) < 16 {
		t.Errorf("соль: %v %d", err, len(salt))
	}
}

func TestRepresentativeByLivenessThenLinksThenShortName(t *testing.T) {
	mk := func(name string, links, sampled, ok int) CandidateView {
		return CandidateView{ID: name, Name: name, Links: links, Sampled: sampled, SampleOK: ok}
	}
	sk := []uint64{1, 2, 3}
	pick := func(vs ...CandidateView) string {
		// порядок как в visibleCandidates: по candBefore
		for i := range vs {
			for j := i + 1; j < len(vs); j++ {
				if candBefore(vs[j], vs[i]) {
					vs[i], vs[j] = vs[j], vs[i]
				}
			}
		}
		g := groupCandidates(vs, [][]uint64{sk, sk, sk}[:len(vs)])
		if len(g) != 1 {
			t.Fatalf("групп %d", len(g))
		}
		return g[0].Representative.Name
	}
	if got := pick(mk("a/long-name", 500, 20, 20), mk("b", 9000, 20, 5), mk("c", 100, 0, 0)); got != "a/long-name" {
		t.Errorf("живость: %s", got)
	}
	if got := pick(mk("a", 100, 20, 10), mk("b", 900, 20, 10)); got != "b" {
		t.Errorf("при равной живости больше ссылок: %s", got)
	}
	if got := pick(mk("longer-name", 100, 20, 10), mk("short", 100, 20, 10)); got != "short" {
		t.Errorf("при равенстве короче имя: %s", got)
	}
}

func TestDismissGroupHidesAllAndNewSimilarMemberInherits(t *testing.T) {
	e := newDiscoverEnv(t, nil, false, nil)
	mirrorFiles(e)
	ctx := context.Background()
	if _, err := e.c.ScanCandidates(ctx, "user", false); err != nil {
		t.Fatal(err)
	}
	groups, _ := e.c.CandidateGroups()
	var mirID string
	for _, g := range groups {
		if g.Size == 3 {
			mirID = g.ID
		}
	}
	if _, err := e.c.DismissGroup("ffffffffffff", "user"); err == nil {
		t.Error("неизвестная группа должна дать ошибку")
	}
	n, err := e.c.DismissGroup(mirID, "user")
	if err != nil || n != 3 {
		t.Fatalf("скрыто %d: %v", n, err)
	}
	list, _ := e.c.Candidates()
	if len(list) != 1 || !strings.HasSuffix(list[0].Name, "/zzz") {
		t.Fatalf("после скрытия группы: %+v", list)
	}
	// появляется ещё одно зеркало (чуть отличается) и посторонний новый файл
	e.setFile("/o1/r1/main/README.md", groupReadme+" [d](BASE/o1/r1/main/mir-d.txt) [n](BASE/o1/r1/main/newone.txt)")
	e.setFile("/o1/r1/main/mir-d.txt", discList(100, "mir")+discList(5, "dx"))
	e.setFile("/o1/r1/main/newone.txt", discList(100, "other"))
	rep, err := e.c.ScanCandidates(ctx, "user", false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.New != 2 || rep.Inherited != 1 {
		t.Errorf("итог: %+v", rep)
	}
	list, _ = e.c.Candidates()
	var names []string
	for _, v := range list {
		names = append(names, v.Name)
	}
	if len(list) != 2 || strings.Contains(strings.Join(names, " "), "mir-d") || !strings.Contains(strings.Join(names, " "), "newone") {
		t.Errorf("новый похожий член должен наследовать «скрыто», посторонний — нет: %v", names)
	}
}
