//go:build windows

package killswitch

import "os"

// На Windows прежнего расположения не было (legacyMarker пуст): проверка владельца не нужна.
func ownedByCurrentUser(os.FileInfo) bool { return true }
