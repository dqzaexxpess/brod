package core

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// свой список был доменным и стал адресным — обновление не принимается, прежний файл и причина остаются; тот же вид принимается.
func TestRefreshUserSetsRejectsKindChange(t *testing.T) {
	var body atomic.Value
	body.Store(srcSet)
	c, srv := userSetRig(t, &body)
	ctx := t.Context()
	set, err := c.AddUserSet(ctx, srv.URL+"/list.json", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(c.userSetPath(set.ID))
	var cidrs []string
	for i := range 40 { // не меньше четверти прежнего размера
		cidrs = append(cidrs, fmt.Sprintf(`"10.%d.0.0/16"`, i))
	}
	body.Store(`{"version":3,"rules":[{"ip_cidr":[` + strings.Join(cidrs, ",") + `]}]}`)
	ch, err := c.RefreshUserSets(ctx, "test")
	if err != nil || len(ch) != 0 {
		t.Fatalf("смена вида принята: %v %v", ch, err)
	}
	after, _ := os.ReadFile(c.userSetPath(set.ID))
	if string(after) != string(before) {
		t.Fatal("файл заменён списком другого вида")
	}
	if us := c.UserSets(); len(us) != 1 || !strings.Contains(us[0].Error, "вид списка изменился") {
		t.Fatalf("причина не записана: %+v", us)
	}
	body.Store(`{"version":3,"rules":[{"domain_suffix":["example.org","example.net","example.com"]}]}`)
	if ch, err := c.RefreshUserSets(ctx, "test"); err != nil || len(ch) != 1 {
		t.Fatalf("тот же вид не принят: %v %v", ch, err)
	}
	if us := c.UserSets(); us[0].Error != "" {
		t.Fatalf("ошибка не снята: %q", us[0].Error)
	}
}
