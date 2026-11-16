package core

import (
	"testing"

	"brod/engine"
	"brod/nodeopts"
	"brod/store"
)

// Регрессия: пробный узел строится тем же outboundJSON, что и боевой, и при check.via_warp (по умолчанию включён) несёт поле detour
// (цепочка через WARP, его ставит ядро). Белый список движка обязан его пропускать для проб так же, как для постоянных узлов: иначе
// отклонялись все проверки узлов (L2, node-check, скорость, страна выхода) с классом other.
func TestProbeOptsViaWarpAcceptedByEngineWhitelist(t *testing.T) {
	for _, opts := range []string{
		`{"type":"vless","server":"example.com","server_port":443,"uuid":"00000000-0000-0000-0000-000000000000"}`,
		`{"type":"shadowsocks","server":"example.com","server_port":8388,"method":"aes-128-gcm","password":"x"}`,
	} {
		rec := store.NodeRec{Opts: []byte(opts)}
		for _, viaWarp := range []bool{false, true} {
			raw, err := outboundJSON(rec, "n-test", viaWarp)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := nodeopts.Sanitize(raw, engine.OutboundFlags); err != nil {
				t.Errorf("via_warp=%v: белый список отклонил опции, которые собирает само ядро: %v", viaWarp, err)
			}
		}
	}
}
