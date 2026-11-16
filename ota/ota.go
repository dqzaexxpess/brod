// Package ota — подписанные обновления без доверия к каналу: список источников подписок (и, дальше, манифест релизов) публикуется в
// публичном репозитории вместе с подписью ed25519; клиент принимает документ только с верной подписью ключа, вшитого в программу.
// Подмена канала (взлом репозитория, перехват) без закрытого ключа ничего не даст: список источников решает, чьи узлы клиент возьмёт.
package ota

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// PublicKey — открытый ключ издателя (base64, 32 байта). Закрытая часть хранится у владельца вне репозитория
// (~/.config/brod/ota-signing.key, см. cmd/otasign); потеря ключа означает выпуск приложений с новым открытым ключом.
const PublicKey = "sXyEgNh66O+orP+JCOMk+rEdrfsyPqFKNWm98asb4aM="

// Пределы документа: защита от «ловушки» (раздутый список, чужие адреса).
const (
	MaxDocBytes   = 1 << 20
	MaxSources    = 500
	MaxNameLen    = 120
	maxPriority   = 100
	sigSuffix     = ".sig"
	SourcesFile   = "sources.json"
	SourcesSigFil = SourcesFile + sigSuffix
)

// Verify проверяет подпись ed25519 (base64 в sig, с пробелами и переводами строк по краям) над точными байтами документа.
func Verify(pub ed25519.PublicKey, doc, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return errors.New("подпись повреждена (ожидается base64 от 64 байт)")
	}
	if !ed25519.Verify(pub, doc, raw) {
		return errors.New("подпись не сходится с ключом издателя")
	}
	return nil
}

// DefaultKey возвращает вшитый открытый ключ.
func DefaultKey() ed25519.PublicKey {
	b, _ := base64.StdEncoding.DecodeString(PublicKey)
	return ed25519.PublicKey(b)
}

// SourceEntry — источник подписки в списке издателя.
type SourceEntry struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Mirror   string `json:"mirror,omitempty"`
	Priority int    `json:"priority"`
}

// SourcesDoc — подписанный список источников. Version растёт при каждой публикации: клиент не принимает старее уже применённого
// (иначе перехваченный старый, но подписанный файл откатил бы список).
type SourcesDoc struct {
	Version int           `json:"version"`
	Created string        `json:"created,omitempty"`
	Sources []SourceEntry `json:"sources"`
	Retired []string      `json:"retired,omitempty"` // источники, которые издатель убрал: у клиента они отключаются
}

// ParseSources разбирает и проверяет список; ошибка — документ целиком отвергается.
func ParseSources(doc []byte) (*SourcesDoc, error) {
	if len(doc) == 0 || len(doc) > MaxDocBytes {
		return nil, fmt.Errorf("размер документа %d вне допустимого (1…%d)", len(doc), MaxDocBytes)
	}
	var d SourcesDoc
	dec := json.NewDecoder(strings.NewReader(string(doc)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("не список источников: %w", err)
	}
	if d.Version <= 0 {
		return nil, errors.New("не указана версия списка")
	}
	if len(d.Sources) == 0 || len(d.Sources) > MaxSources {
		return nil, fmt.Errorf("число источников %d вне допустимого (1…%d)", len(d.Sources), MaxSources)
	}
	seen := map[string]bool{}
	for i, s := range d.Sources {
		if n := len([]rune(s.Name)); n == 0 || n > MaxNameLen {
			return nil, fmt.Errorf("источник %d: название вне допустимого (1…%d знаков)", i, MaxNameLen)
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("источник %q указан дважды", s.Name)
		}
		seen[s.Name] = true
		if s.Priority < 0 || s.Priority > maxPriority {
			return nil, fmt.Errorf("источник %q: приоритет %d вне 0…%d", s.Name, s.Priority, maxPriority)
		}
		if err := checkHTTPS(s.URL, true); err != nil {
			return nil, fmt.Errorf("источник %q: адрес: %w", s.Name, err)
		}
		if err := checkHTTPS(s.Mirror, false); err != nil {
			return nil, fmt.Errorf("источник %q: зеркало: %w", s.Name, err)
		}
	}
	for _, name := range d.Retired {
		if name == "" || len([]rune(name)) > MaxNameLen {
			return nil, errors.New("в списке снятых источников пустое или слишком длинное название")
		}
		if seen[name] {
			return nil, fmt.Errorf("источник %q одновременно в списке и в снятых", name)
		}
	}
	return &d, nil
}

func checkHTTPS(raw string, required bool) error { return checkURL(raw, required, false) }

// IsLoopbackHost сообщает, что хост — это сама машина (адрес 127.0.0.0/8, ::1 или localhost).
func IsLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// checkURL требует https; с loopback=true допускает ещё http на адрес самой машины (проверка обновления с локального сервера).
func checkURL(raw string, required, loopback bool) error {
	if raw == "" {
		if required {
			return errors.New("пусто")
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return errors.New("нужен https-адрес без логина и пароля")
	}
	if u.Scheme == "https" || (loopback && u.Scheme == "http" && IsLoopbackHost(u.Hostname())) {
		return nil
	}
	return errors.New("нужен https-адрес без логина и пароля")
}
