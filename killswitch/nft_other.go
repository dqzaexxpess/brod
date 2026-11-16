//go:build !linux

package killswitch

import "errors"

// Киллсвитч на nftables есть только на Linux (Windows — отдельный этап: WFP).
func (n *Nft) run(string, ...string) (string, error) {
	return "", errors.New("киллсвитч не поддерживается на этой системе")
}
