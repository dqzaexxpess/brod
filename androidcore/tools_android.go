//go:build android

package androidcore

// gomobile bind ищет пакет bind форка в модуле проекта: без этого импорта модуль не попадает в go.mod (так же сделано в cmd/internal/build_libbox движка).
import _ "github.com/sagernet/gomobile"
