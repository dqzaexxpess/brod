//go:build linux

package netwatch

import (
	"context"
	"time"

	"golang.org/x/sys/unix"
)

// Watch подписывается на события netlink о ссылках, адресах и маршрутах. Канал закрывается при отмене контекста.
func Watch(ctx context.Context) (<-chan struct{}, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return nil, err
	}
	groups := uint32(unix.RTMGRP_LINK | unix.RTMGRP_IPV4_IFADDR | unix.RTMGRP_IPV6_IFADDR | unix.RTMGRP_IPV4_ROUTE | unix.RTMGRP_IPV6_ROUTE)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: groups}); err != nil {
		unix.Close(fd)
		return nil, err
	}
	// чтение с таймаутом, чтобы цикл замечал отмену контекста
	tv := unix.NsecToTimeval(int64(500 * time.Millisecond))
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		unix.Close(fd)
		return nil, err
	}
	raw := make(chan struct{}, 1)
	go func() {
		defer close(raw)
		defer unix.Close(fd)
		buf := make([]byte, 1<<16)
		for ctx.Err() == nil {
			n, _, err := unix.Recvfrom(fd, buf, 0)
			if err != nil {
				if err == unix.EAGAIN || err == unix.EINTR {
					continue
				}
				return
			}
			if n > 0 {
				select {
				case raw <- struct{}{}:
				default:
				}
			}
		}
	}()
	return Debounce(ctx, raw, 700*time.Millisecond), nil
}
