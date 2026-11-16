package rulesets

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/sagernet/sing-box/common/srs"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
)

// Свои (удалённые) списки по URL: пользователь добавляет их сам, поэтому подписи нет, но принимаются они так же строго, как встроенные:
// только https, размер ограничен, формат проверяется разбором тем же парсером, что у движка, и файл ни во что не исполняется.

// MaxRemoteSize — предел размера свёрнутого списка по URL.
const MaxRemoteSize = 20 << 20

// FetchRemote скачивает список по url и возвращает его в бинарном формате srs. Принимает бинарный `.srs` и исходный JSON
// (`{"version":…,"rules":[…]}`, как у sing-box): исходный компилируется в srs. prevSize > 0 включает защиту: ответ меньше четверти прежнего
// отбрасывается (обрезанный или подменённый).
func FetchRemote(ctx context.Context, c *http.Client, url string, prevSize int) ([]byte, error) {
	if !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("нужен https-адрес")
	}
	b, err := fetch(ctx, c, url)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxRemoteSize {
		return nil, fmt.Errorf("список больше %d МБ", MaxRemoteSize>>20)
	}
	out, err := compile(b)
	if err != nil {
		return nil, err
	}
	if prevSize > 0 && len(out)*4 < prevSize {
		return nil, fmt.Errorf("новый список (%d байт) втрое меньше прежнего (%d) — не принимается", len(out), prevSize)
	}
	return out, nil
}

// compile приводит данные к srs: бинарный проверяется разбором, исходный JSON компилируется.
func compile(b []byte) ([]byte, error) {
	if t := bytes.TrimSpace(b); len(t) > 0 && t[0] == '{' {
		var compat option.PlainRuleSetCompat
		if err := json.Unmarshal(t, &compat); err != nil {
			return nil, fmt.Errorf("не rule-set формата JSON: %w", err)
		}
		plain, err := compat.Upgrade()
		if err != nil {
			return nil, fmt.Errorf("rule-set: %w", err)
		}
		if len(plain.Rules) == 0 {
			return nil, fmt.Errorf("в списке нет правил")
		}
		var buf bytes.Buffer
		if err := srs.Write(&buf, plain, C.RuleSetVersionCurrent); err != nil {
			return nil, fmt.Errorf("компиляция списка: %w", err)
		}
		b = buf.Bytes()
	}
	if err := ValidateUser(b); err != nil { // пределы + состав: только доменный или только адресный список, без «совпадает со всем»
		return nil, err
	}
	return b, nil
}

// WriteFile атомарно записывает файл списка.
func WriteFile(path string, b []byte) error { return writeAtomic(path, b) }
