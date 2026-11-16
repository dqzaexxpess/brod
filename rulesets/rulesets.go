// Package rulesets — встроенные списки маршрутизации для режима TUN «только заблокированное» (PLAN P6).
//
// Списки (формат sing-box `.srs`) поставляются внутри бинарника — снимок на момент сборки, работает без сети. Обновляются отдельно из
// публичной сборки runetfreedom/russia-v2ray-rules-dat (раз в 6 часов в источнике): скачивание с проверкой формата, разумных размеров
// и атомарной заменой файла; движок подхватывает изменения сам (локальный rule-set отслеживается по изменению файла).
// Источник — чужой ресурс: файлы читаются только парсером `.srs` и ни во что не исполняются; битый или подозрительный файл не принимается.
package rulesets

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sagernet/sing-box/common/srs"
)

//go:embed data/*.srs
var snapshot embed.FS

// Роли списков.
const (
	RoleProxy  = "proxy"  // заблокированное: идёт через туннель
	RoleDirect = "direct" // всегда напрямую (банки, Госуслуги, сервисы «только внутри РФ»)
	RoleRU     = "ru"     // российские сайты и адреса: напрямую в режиме «всё, кроме РФ»
)

// Set — один список.
type Set struct {
	Name string // имя файла без расширения (и тег rule-set'а в конфиге движка)
	Role string
	Path string // путь в репозитории источника
}

const (
	rawBase    = "https://raw.githubusercontent.com/runetfreedom/russia-v2ray-rules-dat/release/sing-box/"
	mirrorBase = "https://cdn.jsdelivr.net/gh/runetfreedom/russia-v2ray-rules-dat@release/sing-box/"
)

// Sets — используемые списки.
var Sets = []Set{
	{"geosite-ru-blocked", RoleProxy, "rule-set-geosite/geosite-ru-blocked.srs"},
	{"geoip-ru-blocked", RoleProxy, "rule-set-geoip/geoip-ru-blocked.srs"},
	{"geoip-ru-blocked-community", RoleProxy, "rule-set-geoip/geoip-ru-blocked-community.srs"},
	{"geosite-youtube", RoleProxy, "rule-set-geosite/geosite-youtube.srs"},
	{"geosite-telegram", RoleProxy, "rule-set-geosite/geosite-telegram.srs"},
	{"geoip-telegram", RoleProxy, "rule-set-geoip/geoip-telegram.srs"},
	{"geosite-category-bank-ru", RoleDirect, "rule-set-geosite/geosite-category-bank-ru.srs"},
	{"geosite-category-gov-ru", RoleDirect, "rule-set-geosite/geosite-category-gov-ru.srs"},
	{"geosite-ru-available-only-inside", RoleDirect, "rule-set-geosite/geosite-ru-available-only-inside.srs"},
	{"geoip-ru", RoleRU, "rule-set-geoip/geoip-ru.srs"},
	{"geosite-category-ru", RoleRU, "rule-set-geosite/geosite-category-ru.srs"},
}

// Names возвращает имена списков заданной роли.
func Names(role string) []string {
	var out []string
	for _, s := range Sets {
		if s.Role == role {
			out = append(out, s.Name)
		}
	}
	return out
}

// Path — путь файла списка в каталоге dir.
func Path(dir, name string) string { return filepath.Join(dir, name+".srs") }

// Validate проверяет, что данные — корректный `.srs` (разбирает их тем же парсером, что и движок).
func Validate(b []byte) error {
	if len(b) < 8 {
		return fmt.Errorf("слишком короткий файл (%d байт)", len(b))
	}
	if err := checkUnpackedLimits(b); err != nil {
		return err
	}
	if _, err := srs.Read(bytes.NewReader(b), false); err != nil {
		return fmt.Errorf("не rule-set формата srs: %w", err)
	}
	return nil
}

// Пределы распакованного списка: сжатый файл в несколько килобайт иначе разворачивается в миллионы пустых правил,
// по ~600 байт памяти на каждое. Реальные списки: правил верхнего уровня 1, распакованных байт до ~0,9 МиБ.
const (
	maxUnpacked = 4 << 20 // байт после распаковки zlib (запас ~4,5x над самым большим списком)
	maxRules    = 1024    // правил верхнего уровня
)

// checkUnpackedLimits распаковывает тело `.srs` не дальше maxUnpacked байт и сверяет заявленное число правил, не создавая правил.
func checkUnpackedLimits(b []byte) error {
	// заголовок: magic (3 байта) + версия (1 байт); версию и magic проверяет srs.Read.
	zr, err := zlib.NewReader(bytes.NewReader(b[4:]))
	if err != nil {
		return fmt.Errorf("не rule-set формата srs: %w", err)
	}
	defer zr.Close()
	lr := &io.LimitedReader{R: zr, N: maxUnpacked + 1}
	br := bufio.NewReader(lr)
	count, err := binary.ReadUvarint(br)
	if err != nil {
		return fmt.Errorf("не rule-set формата srs: %w", err)
	}
	if count > maxRules {
		return fmt.Errorf("в списке %d правил, предел %d", count, maxRules)
	}
	if _, err := io.Copy(io.Discard, br); err != nil && lr.N > 0 {
		return fmt.Errorf("не rule-set формата srs: %w", err)
	}
	if lr.N <= 0 {
		return fmt.Errorf("список после распаковки больше %d МиБ", maxUnpacked>>20)
	}
	return nil
}

// Install раскладывает встроенный снимок в dir: отсутствующие файлы создаются, существующие не трогаются
// (обновлённая копия новее снимка в бинарнике). Возвращает число созданных файлов.
func Install(dir string) (int, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	created := 0
	for _, s := range Sets {
		p := Path(dir, s.Name)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		b, err := snapshot.ReadFile("data/" + s.Name + ".srs")
		if err != nil {
			return created, fmt.Errorf("в снимке нет %s: %w", s.Name, err)
		}
		if err := writeAtomic(p, b); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Result — итог обновления.
type Result struct {
	Updated   []string // файлы, содержимое которых изменилось
	Unchanged int
	Failed    map[string]string // имя → причина (прежний файл остаётся)
}

// maxSize — предел размера файла: защита от подмены гигантским ответом.
const maxSize = 40 << 20

// Update скачивает списки (сначала основной адрес, затем зеркало) через client и заменяет изменившиеся. Новый файл принимается
// только если это корректный `.srs` допустимого состава (ValidateSet), не меньше 25% прежнего размера (обрезанный или подменённый ответ отбрасывается) и не больше maxSize.
func Update(ctx context.Context, client *http.Client, dir string) (Result, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if _, err := Install(dir); err != nil {
		return Result{}, err
	}
	res := Result{Failed: map[string]string{}}
	for _, s := range Sets {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		cur, _ := os.ReadFile(Path(dir, s.Name))
		var data []byte
		var lastErr error
		for _, base := range []string{rawBase, mirrorBase} {
			data, lastErr = fetch(ctx, client, base+s.Path)
			if lastErr == nil {
				if lastErr = ValidateSet(data, s.Name); lastErr == nil && len(cur) > 0 && len(data)*4 < len(cur) {
					lastErr = fmt.Errorf("новый файл (%d байт) втрое меньше прежнего (%d) — не принимается", len(data), len(cur))
				}
			}
			if lastErr == nil {
				break
			}
		}
		if lastErr != nil {
			res.Failed[s.Name] = lastErr.Error()
			continue
		}
		if sha256.Sum256(data) == sha256.Sum256(cur) {
			res.Unchanged++
			continue
		}
		if err := writeAtomic(Path(dir, s.Name), data); err != nil {
			res.Failed[s.Name] = err.Error()
			continue
		}
		res.Updated = append(res.Updated, s.Name)
	}
	return res, nil
}

func fetch(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSize {
		return nil, fmt.Errorf("ответ больше %d МБ", maxSize>>20)
	}
	return b, nil
}
