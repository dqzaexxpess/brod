package ota

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Манифест релизов приложения (OTA, этап 2): тот же принцип, что у списка источников. Документ releases.json публикуется с подписью
// releases.json.sig (ed25519, ключ вшит в приложение); клиент принимает его только с верной подписью, строгим разбором и «версией публикации»
// не ниже уже виденной (откат старым подписанным файлом невозможен). Сами файлы релизов (APK, архивы) подписью не покрыты напрямую:
// манифест несёт их SHA-256 и размер, а клиент сверяет скачанное с ними (VerifyAsset), поэтому хранить файлы можно где угодно.

// ReleasesPublicKey — открытый ключ подписи манифеста релизов (base64). Отдельный от PublicKey: компрометация ключа списка источников не даёт
// подписать приложение, и наоборот. Закрытая часть — ~/.config/brod/releases-signing.key (вне репозитория, резервная копия за владельцем).
const ReleasesPublicKey = "jNVer/OvSB1HbMUL4At27UZ/vtZ0wY1iGmWTnVfAGCw="

// ReleasesKey возвращает вшитый открытый ключ релизов.
func ReleasesKey() ed25519.PublicKey {
	b, _ := base64.StdEncoding.DecodeString(ReleasesPublicKey)
	return ed25519.PublicKey(b)
}

const (
	ReleasesFile    = "releases.json"
	ReleasesSigFile = ReleasesFile + sigSuffix
	MaxNotes        = 4000
	MaxAssetBytes   = 512 << 20 // 512 МиБ: APK ~90 МБ, архивы десктопа не больше
)

// Platforms — допустимые ключи платформ в манифесте.
var Platforms = []string{"android-arm64", "linux-amd64", "windows-amd64"}

// Asset — файл релиза для платформы.
type Asset struct {
	URL    string `json:"url"`
	Mirror string `json:"mirror,omitempty"`
	SHA256 string `json:"sha256"` // 64 шестнадцатеричных знака, нижний регистр
	Size   int64  `json:"size"`   // байт: клиент не скачивает больше заявленного
}

// Release — выпуск приложения.
type Release struct {
	App    string           `json:"app"`             // версия приложения, например «0.4.0»
	Notes  string           `json:"notes,omitempty"` // что изменилось, обычным текстом
	Assets map[string]Asset `json:"assets"`          // по ключам Platforms
}

// ReleasesDoc — подписанный манифест. Version растёт при каждой публикации (не путать с версией приложения App).
type ReleasesDoc struct {
	Version int     `json:"version"`
	Created string  `json:"created,omitempty"`
	Release Release `json:"release"`
}

var (
	appVersionRe = regexp.MustCompile(`^(\d{1,4})\.(\d{1,4})\.(\d{1,4})$`)
	sha256Re     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ParseReleases разбирает и проверяет манифест; ошибка — документ отвергается целиком.
func ParseReleases(doc []byte) (*ReleasesDoc, error) { return ParseReleasesWith(doc, false) }

// ParseReleasesWith — ParseReleases; с loopback=true адреса файлов могут быть http на сам компьютер (только для проверки с локального сервера).
func ParseReleasesWith(doc []byte, loopback bool) (*ReleasesDoc, error) {
	if len(doc) == 0 || len(doc) > MaxDocBytes {
		return nil, fmt.Errorf("размер документа %d вне допустимого (1…%d)", len(doc), MaxDocBytes)
	}
	var d ReleasesDoc
	dec := json.NewDecoder(strings.NewReader(string(doc)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("не манифест релизов: %w", err)
	}
	if d.Version <= 0 {
		return nil, errors.New("не указана версия публикации")
	}
	if _, ok := parseAppVersion(d.Release.App); !ok {
		return nil, fmt.Errorf("версия приложения %q: нужен вид 1.2.3", d.Release.App)
	}
	if len([]rune(d.Release.Notes)) > MaxNotes {
		return nil, fmt.Errorf("описание длиннее %d знаков", MaxNotes)
	}
	if len(d.Release.Assets) == 0 {
		return nil, errors.New("в релизе нет файлов")
	}
	known := map[string]bool{}
	for _, p := range Platforms {
		known[p] = true
	}
	for p, a := range d.Release.Assets {
		if !known[p] {
			return nil, fmt.Errorf("неизвестная платформа %q", p)
		}
		if err := checkURL(a.URL, true, loopback); err != nil {
			return nil, fmt.Errorf("%s: адрес: %w", p, err)
		}
		if err := checkURL(a.Mirror, false, loopback); err != nil {
			return nil, fmt.Errorf("%s: зеркало: %w", p, err)
		}
		if !sha256Re.MatchString(a.SHA256) {
			return nil, fmt.Errorf("%s: sha256 должен быть 64 шестнадцатеричных знака в нижнем регистре", p)
		}
		if a.Size <= 0 || a.Size > MaxAssetBytes {
			return nil, fmt.Errorf("%s: размер %d вне допустимого (1…%d)", p, a.Size, MaxAssetBytes)
		}
	}
	return &d, nil
}

func parseAppVersion(s string) ([3]int, bool) {
	m := appVersionRe.FindStringSubmatch(s)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, true
}

// NewerThan сообщает, что версия приложения из манифеста строго новее current (вид «1.2.3», допускается суффикс после дефиса у текущей:
// «0.3.0-android» → 0.3.0). Непонятная текущая версия новее ничего не считается: лучше не предлагать обновление, чем предложить не то.
func (r Release) NewerThan(current string) bool {
	cur := current
	if i := strings.IndexByte(cur, '-'); i >= 0 {
		cur = cur[:i]
	}
	c, ok := parseAppVersion(cur)
	if !ok {
		return false
	}
	n, ok := parseAppVersion(r.App)
	if !ok {
		return false
	}
	for i := range n {
		if n[i] != c[i] {
			return n[i] > c[i]
		}
	}
	return false
}

// VerifyAsset читает файл релиза из r и сверяет размер и SHA-256 с манифестом. Читает не больше заявленного размера + 1 байт, поэтому
// подсунутый «бесконечный» ответ не съест диск и память: лишний байт — ошибка.
func VerifyAsset(r io.Reader, a Asset) error {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, a.Size+1))
	if err != nil {
		return fmt.Errorf("чтение файла: %w", err)
	}
	if n != a.Size {
		return fmt.Errorf("размер файла %d не совпадает с заявленным %d", n, a.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		return errors.New("SHA-256 файла не совпадает с манифестом")
	}
	return nil
}
