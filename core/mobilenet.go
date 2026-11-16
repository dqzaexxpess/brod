package core

import (
	"encoding/binary"
	"net/netip"
	"strings"

	"brod/mobile"
)

// Мобильная сеть: узлы, чей адрес входит в белые списки операторов, проверяются первыми, а проверки идут с малой параллельностью
//. Оценка по SNI требует опций узла и в отборе кандидатов не используется: адрес — более жёсткий признак.

const (
	mobileBoost         = 400 // добавка к баллу кандидата, чей адрес в белом списке (баллы истории и класса — десятки и сотни)
	mobileCheckParallel = 12  // одновременных проверок в платной сети (в обычной — CheckParallel)
)

// isMobileNet — текущая сеть мобильная: идентификатор сети Android «cell:<оператор>» или явная метка «mobile…» (VPNCLIENT_NETLABEL при раздаче с телефона).
func isMobileNet(netID string) bool {
	return strings.HasPrefix(netID, "cell:") || strings.HasPrefix(netID, "mobile")
}

// mobileListed сообщает, входит ли IPv4-адрес (из лёгкой записи узла) в белые списки; без загруженных списков всегда false.
func mobileListed(l *mobile.Lists, ip4 uint32) bool {
	if l == nil || ip4 == 0 {
		return false
	}
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], ip4)
	return l.IP(netip.AddrFrom4(b))
}

// checkParallel — сколько проверок идёт одновременно: в платной сети (трафик, батарея, подозрительная активность) заметно меньше.
func (c *Core) checkParallel() int {
	n := c.cfg.CheckParallel
	if c.meteredNow() && n > mobileCheckParallel {
		return mobileCheckParallel
	}
	return n
}
