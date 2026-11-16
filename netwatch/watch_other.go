//go:build !linux

package netwatch

import (
	"context"
	"errors"
)

// Watch на этой системе не поддерживается: вызывающий опрашивает сеть сам.
func Watch(ctx context.Context) (<-chan struct{}, error) {
	return nil, errors.New("netwatch: подписка на смену сети не поддерживается")
}
