package core

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Набор правил node-servers: домены серверов узлов, зарегистрированных в движке. Их имена всегда резолвятся «local» (мимо туннеля).
//
// Зачем: цепочка WARP → узел (и любой другой дозвон, где домен резолвит не сам исходящий, а WireGuard-эндпоинт) спрашивает DNS-роутер без
// явного сервера, то есть по правилам dns.rules. В режимах «всё через выход» итоговый сервер — «remote» (DoH через активный узел), а узлу для
// дозвона нужен тот же домен: DNS query loopback in transport[remote], весь трафик виснет. Правило «домены серверов → local» первым
// в dns.rules разрывает петлю. Список меняется во время работы (узлы переключаются без перезапуска движка), поэтому это файл локального
// rule-set: движок следит за ним и перечитывает при замене (так же обновляются встроенные списки, см. rulesets).
const (
	nodeServersTag  = "node-servers"
	nodeServersFile = "node-servers.json"
	// движок не принимает пустой набор: в нём всегда есть служебное имя
	nodeServersDummy = "node-servers.invalid"
	nodeServersMax   = 256
	// пауза после записи, чтобы движок успел перечитать файл до первого дозвона
	nodeServersSettle = 250 * time.Millisecond
)

// nodeServers хранит домены серверов и пишет файл набора правил.
type nodeServers struct {
	mu    sync.Mutex
	path  string
	order []string
	have  map[string]bool
}

func newNodeServers(path string) *nodeServers {
	ns := &nodeServers{path: path, have: map[string]bool{}}
	_ = ns.write() // файл должен существовать к запуску движка
	return ns
}

// note добавляет домен сервера (IP-адреса резолвить не нужно). Возвращает true, если список изменился.
func (n *nodeServers) note(host string) bool {
	if n == nil || host == "" {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.have[host] {
		return false
	}
	n.have[host] = true
	n.order = append(n.order, host)
	if len(n.order) > nodeServersMax { // самые старые узлы уже вряд ли активны
		delete(n.have, n.order[0])
		n.order = n.order[1:]
	}
	if err := n.write(); err != nil {
		delete(n.have, host)
		n.order = n.order[:len(n.order)-1]
		return false
	}
	return true
}

// write атомарно заменяет файл набора правил (движок отслеживает замену файла).
func (n *nodeServers) write() error {
	domains := append([]string{nodeServersDummy}, n.order...)
	raw, err := json.Marshal(map[string]any{"version": 2, "rules": []any{map[string]any{"domain": domains}}})
	if err != nil {
		return err
	}
	tmp := n.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, n.path)
}

// nodeServersPath — путь файла в каталоге состояния.
func nodeServersPath(stateDir string) string { return filepath.Join(stateDir, nodeServersFile) }
