// Package warp регистрирует устройство в Cloudflare WARP и строит конфиг wireguard-endpoint'а для sing-box-lx
// (обычный WG или AmneziaWG с клиентской маскировкой под QUIC).
//
// Секреты (приватный ключ, токен аккаунта) возвращаются вызывающему и сохраняются им только в work/ (в git не идут).
// Регистрация — внешнее действие (запросы к api.cloudflareclient.com), выполняется только по явной команде.
package warp

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	apiBase = "https://api.cloudflareclient.com/v0a2158"
	// Эти заголовки ожидает API потребительского клиента (как у официального приложения).
	clientVersion = "a-6.3-2158"
	userAgent     = "okhttp/3.12.1"
)

// Account — зарегистрированное устройство. Содержит секреты.
type Account struct {
	ID            string    `json:"id"`
	Token         string    `json:"token"`
	PrivateKey    string    `json:"private_key"`
	PublicKey     string    `json:"public_key"`
	PeerPublicKey string    `json:"peer_public_key"`
	AddrV4        string    `json:"addr_v4"`
	AddrV6        string    `json:"addr_v6"`
	ClientID      string    `json:"client_id"` // base64 из трёх байт: «reserved» для обычного WG
	EndpointHost  string    `json:"endpoint_host"`
	CreatedAt     time.Time `json:"created_at"`
}

// GenerateKey создаёт пару X25519 (base64, как у WireGuard).
func GenerateKey() (priv, pub string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(k.Bytes()), base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

func do(ctx context.Context, c *http.Client, method, url, token string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("CF-Client-Version", clientVersion)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s %s: HTTP %d: %.200s", method, url, resp.StatusCode, string(data))
	}
	return data, nil
}

// Register регистрирует новое устройство и включает WARP. c может быть nil: тогда обычный запрос, а если сеть режет TLS по SNI
// api.cloudflareclient.com (см. nosni.go) — повтор без SNI с проверкой сертификата.
func Register(ctx context.Context, c *http.Client) (*Account, error) {
	if c == nil {
		a, err := register(ctx, &http.Client{Timeout: 6 * time.Second})
		if err == nil {
			return a, nil
		}
		if a2, err2 := register(ctx, noSNIClient(10*time.Second)); err2 == nil {
			return a2, nil
		} else {
			return nil, fmt.Errorf("%w (запасной путь без SNI: %v)", err, err2)
		}
	}
	return register(ctx, c)
}

func register(ctx context.Context, c *http.Client) (*Account, error) {
	priv, pub, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	data, err := do(ctx, c, http.MethodPost, apiBase+"/reg", "", map[string]any{
		"key": pub, "install_id": "", "fcm_token": "", "model": "PC", "serial_number": "", "locale": "en_US",
		"tos": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	})
	if err != nil {
		return nil, fmt.Errorf("регистрация: %w", err)
	}
	var r struct {
		ID     string `json:"id"`
		Token  string `json:"token"`
		Config struct {
			ClientID string `json:"client_id"`
			Peers    []struct {
				PublicKey string `json:"public_key"`
				Endpoint  struct {
					Host string `json:"host"`
				} `json:"endpoint"`
			} `json:"peers"`
			Interface struct {
				Addresses struct {
					V4 string `json:"v4"`
					V6 string `json:"v6"`
				} `json:"addresses"`
			} `json:"interface"`
		} `json:"config"`
	}
	if err := json.Unmarshal(data, &r); err != nil || r.ID == "" || len(r.Config.Peers) == 0 {
		return nil, fmt.Errorf("неожиданный ответ регистрации (id=%q, peers=%d)", r.ID, len(r.Config.Peers))
	}
	// включить WARP на устройстве (без этого пир может не принимать рукопожатие)
	if _, err := do(ctx, c, http.MethodPatch, apiBase+"/reg/"+r.ID, r.Token, map[string]any{"warp_enabled": true}); err != nil {
		return nil, fmt.Errorf("включение WARP: %w", err)
	}
	return &Account{
		ID: r.ID, Token: r.Token, PrivateKey: priv, PublicKey: pub,
		PeerPublicKey: r.Config.Peers[0].PublicKey, AddrV4: r.Config.Interface.Addresses.V4, AddrV6: r.Config.Interface.Addresses.V6,
		ClientID: r.Config.ClientID, EndpointHost: r.Config.Peers[0].Endpoint.Host, CreatedAt: time.Now().UTC(),
	}, nil
}

// Reserved возвращает три «reserved»-байта из client_id (нужны WARP при обычном WireGuard; AWG-маскировка их не использует).
func (a *Account) Reserved() []uint8 {
	b, err := base64.StdEncoding.DecodeString(a.ClientID)
	if err != nil || len(b) < 3 {
		return nil
	}
	return []uint8{b[0], b[1], b[2]}
}

// Options — параметры генерируемого endpoint'а.
type Options struct {
	Tag      string // тег endpoint'а (по умолчанию "warp")
	Endpoint string // host (по умолчанию engage.cloudflareclient.com)
	Port     uint16 // порт (по умолчанию 2408)
	MTU      uint32 // по умолчанию 1280
	// AWG: если Mask != "", включается AmneziaWG с клиентской маскировкой (только клиентские параметры: сервер WARP
	// обфускацию заголовков не понимает).
	Mask           string // quic | dns | stun | sip (пусто = обычный WireGuard)
	MaskHost       string // домен для маскировки (SNI при quic); по умолчанию www.google.com
	Browser        string // chrome | firefox | curl (только quic)
	Jc, Jmin, Jmax uint32
	// IPv4Only: только IPv4 (адрес интерфейса и allowed_ips без IPv6). На Windows отправка рукопожатия падала с `wsasendmsg: invalid pointer
	// address`, в том числе на IPv6-адрес через udp4-сокет; без IPv6 в конфиге таких отправок нет.
	IPv4Only bool
}

// Endpoint строит JSON-объект wireguard-endpoint'а sing-box-lx.
func (a *Account) Endpoint(o Options) map[string]any {
	if o.Tag == "" {
		o.Tag = "warp"
	}
	if o.Endpoint == "" {
		o.Endpoint = "engage.cloudflareclient.com"
	}
	if o.Port == 0 {
		o.Port = 2408
	}
	if o.MTU == 0 {
		o.MTU = 1280
	}
	addrs := []string{a.AddrV4 + "/32"}
	allowed := []string{"0.0.0.0/0", "::/0"}
	if o.IPv4Only {
		allowed = []string{"0.0.0.0/0"}
	} else if a.AddrV6 != "" {
		addrs = append(addrs, a.AddrV6+"/128")
	}
	peer := map[string]any{
		"address": o.Endpoint, "port": o.Port, "public_key": a.PeerPublicKey,
		"allowed_ips": allowed, "persistent_keepalive_interval": 25,
	}
	ep := map[string]any{
		"type": "wireguard", "tag": o.Tag, "mtu": o.MTU, "address": addrs, "private_key": a.PrivateKey,
		"peers": []any{peer},
	}
	if o.Mask == "" {
		if r := a.Reserved(); r != nil {
			peer["reserved"] = r
		}
		return ep
	}
	ep["jc"], ep["jmin"], ep["jmax"] = pick(o.Jc, 4), pick(o.Jmin, 40), pick(o.Jmax, 70)
	ep["ip"] = o.Mask
	if o.Mask == "quic" {
		ep["id"] = firstNonEmpty(o.MaskHost, "www.google.com")
		ep["ib"] = firstNonEmpty(o.Browser, "chrome")
	} else if o.MaskHost != "" {
		ep["id"] = o.MaskHost
	}
	return ep
}

func pick(v, d uint32) uint32 {
	if v == 0 {
		return d
	}
	return v
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}
