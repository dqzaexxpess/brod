//go:build linux

package supervisor

import "testing"

// На любой машине список не должен включать lo и свой интерфейс, а имена — быть без повторов и отсортированы.
func TestForeignTunnelsExcludesOwnAndLoopback(t *testing.T) {
	all := foreignTunnels("")
	for _, n := range all {
		if n == "lo" {
			t.Fatal("lo не туннель")
		}
	}
	if len(all) > 0 {
		own := all[0]
		for _, n := range foreignTunnels(own) {
			if n == own {
				t.Fatalf("свой интерфейс %q не должен попадать в список", own)
			}
		}
	}
	for i := 1; i < len(all); i++ {
		if all[i-1] >= all[i] {
			t.Fatalf("список должен быть отсортирован без повторов: %v", all)
		}
	}
}
