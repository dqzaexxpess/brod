package core

import (
	"encoding/json"
	"fmt"
	"strings"

	"brod/aggregator"
	"brod/store"
	"brod/translator"
)

// Собственные серверы пользователя (свой VPS и т. п.): добавляются ссылкой или полным конфигом, живут в служебном источнике «own»
// (его никто не обновляет), закрепляются и попадают в избранное — не чистятся и всплывают наверх списка.

const maxOwnAdd = 50

// OwnReport — итог добавления.
type OwnReport struct {
	Added      int // новых узлов
	Duplicates int // уже были в базе (становятся собственными)
	Failed     int // ссылок не разобрано
	IDs        []string
}

// AddOwn добавляет серверы из текста: ссылки по одной в строке, весь список в base64 либо полный конфиг (sing-box/Clash/Xray).
func (c *Core) AddOwn(text, initiator string) (OwnReport, error) {
	links := aggregator.ExtractLinks([]byte(text))
	links = append(links, translator.WGQuickLinks(text)...) // конфиги wg-quick (WireGuard, AmneziaWG) с секциями [Interface] и [Peer]
	if len(links) == 0 {
		return OwnReport{}, fmt.Errorf("%w: серверы не найдены (нужны ссылки vless://, ss://, trojan:// и т. п., готовый конфиг или файл WireGuard/AmneziaWG с секциями [Interface] и [Peer])", ErrInvalid)
	}
	if len(links) > maxOwnAdd {
		return OwnReport{}, fmt.Errorf("%w: за раз не больше %d серверов", ErrInvalid, maxOwnAdd)
	}
	nodes := parseLinks(links)
	rep := OwnReport{Failed: len(links) - len(nodes)}
	if len(nodes) == 0 {
		return rep, fmt.Errorf("%w: ни одна ссылка не разобрана", ErrInvalid)
	}
	existing, _ := c.st.Nodes(store.NodeFilter{IDs: idsOf(nodes), IncludeNoSecurity: true})
	had := map[string]bool{}
	for _, r := range existing {
		had[r.ID] = true
	}
	now := c.cfg.Now()
	if err := c.st.UpsertNodes(store.OwnSource, now.UnixNano(), nodes, now); err != nil {
		return rep, err
	}
	for _, n := range nodes {
		_ = c.st.SetNodeFlag(n.ID, store.FlagPinned, true)
		_ = c.st.SetNodeFlag(n.ID, store.FlagFavorite, true)
		rep.IDs = append(rep.IDs, n.ID)
		if had[n.ID] {
			rep.Duplicates++
		} else {
			rep.Added++
		}
	}
	c.invalidateCounts()
	c.bus.Publish(store.Event{Kind: EvNode, Initiator: initiator, Detail: fmt.Sprintf("свои серверы: добавлено %d, уже были %d, не разобрано %d", rep.Added, rep.Duplicates, rep.Failed)})
	return rep, nil
}

func idsOf(nodes []store.NodeRec) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.ID
	}
	return out
}

// RemoveOwn убирает собственный сервер. Нельзя убрать тот, через который сейчас идёт трафик.
func (c *Core) RemoveOwn(id, initiator string) error {
	own, err := c.st.OwnIDs()
	if err != nil {
		return err
	}
	if !own[id] {
		return fmt.Errorf("%w: это не собственный сервер", ErrInvalid)
	}
	c.mu.Lock()
	_, needed := c.needed()[id]
	c.mu.Unlock()
	if needed {
		return fmt.Errorf("%w: сервер сейчас используется (активный или цель) — сначала выберите другой", ErrBusy)
	}
	if err := c.st.DeleteOwn(id); err != nil {
		return err
	}
	// у цепочки в движке остались звенья: сначала снимается выход (gc), затем вспомогательные звенья
	c.mu.Lock()
	c.gc()
	c.mu.Unlock()
	c.dropChainHelpers(id)
	c.invalidateCounts()
	c.bus.Publish(store.Event{Kind: EvNode, Initiator: initiator, Node: id, Detail: "свой сервер удалён"})
	return nil
}

// EditOwnReq — что менять у собственного сервера: название (для любого узла — SetNodeLabel), адрес, порт.
type EditOwnReq struct {
	Label  *string
	Server *string
	Port   *int
}

// SetNodeLabel задаёт пользовательское название любого узла (пустое — снимает). Видно в списке и на главной.
func (c *Core) SetNodeLabel(id, label, initiator string) error {
	label = strings.TrimSpace(label)
	if len([]rune(label)) > 60 {
		return fmt.Errorf("%w: название длиннее 60 символов", ErrInvalid)
	}
	if _, err := c.st.Node(id); err != nil {
		return err
	}
	if err := c.st.SetNodeLabel(id, label); err != nil {
		return err
	}
	c.bus.Publish(store.Event{Kind: EvNode, Initiator: initiator, Node: id, Detail: "название узла изменено"})
	return nil
}

// EditOwn меняет собственный сервер на месте: название, адрес, порт. Остальные параметры (uuid, пароль, SNI…) меняются заменой ссылкой
// (ReplaceOwn). Сервер, через который идёт трафик, менять нельзя: движок держит его параметры — сначала выберите другой.
func (c *Core) EditOwn(id string, req EditOwnReq, initiator string) error {
	own, err := c.st.OwnIDs()
	if err != nil {
		return err
	}
	if !own[id] {
		return fmt.Errorf("%w: это не собственный сервер", ErrInvalid)
	}
	if req.Label != nil {
		if err := c.SetNodeLabel(id, *req.Label, initiator); err != nil {
			return err
		}
	}
	if req.Server == nil && req.Port == nil {
		return nil
	}
	rec, err := c.st.Node(id)
	if err != nil {
		return err
	}
	if isChain(rec) {
		return fmt.Errorf("%w: у цепочки нет своего адреса: звенья задаются при создании (название менять можно)", ErrInvalid)
	}
	if rec.Endpoint {
		return fmt.Errorf("%w: адрес WireGuard-узла меняется заменой ссылкой", ErrInvalid)
	}
	c.mu.Lock()
	_, needed := c.needed()[id]
	c.mu.Unlock()
	if needed {
		return fmt.Errorf("%w: сервер сейчас используется (активный или цель) — выберите другой узел, измените этот и верните", ErrBusy)
	}
	var opts map[string]any
	if err := json.Unmarshal(rec.Opts, &opts); err != nil {
		return err
	}
	server, port := rec.Server, rec.Port
	if req.Server != nil {
		server = strings.TrimSpace(*req.Server)
		if server == "" || strings.ContainsAny(server, " /\\\"'@") {
			return fmt.Errorf("%w: недопустимый адрес сервера", ErrInvalid)
		}
	}
	if req.Port != nil {
		port = *req.Port
		if port < 1 || port > 65535 {
			return fmt.Errorf("%w: порт вне диапазона 1–65535", ErrInvalid)
		}
	}
	opts["server"], opts["server_port"] = server, port
	raw, err := json.Marshal(opts)
	if err != nil {
		return err
	}
	if err := c.st.UpdateNodeAddr(id, server, port, raw); err != nil {
		return err
	}
	// прежние результаты проверок относились к другому адресу
	c.bus.Publish(store.Event{Kind: EvNode, Initiator: initiator, Node: id, Detail: "адрес собственного сервера изменён"})
	return nil
}

// ReplaceOwn заменяет собственный сервер новой ссылкой (меняется что угодно: uuid, пароль, SNI…), сохраняя название. Возвращает id нового узла.
func (c *Core) ReplaceOwn(id, text, initiator string) (string, error) {
	own, err := c.st.OwnIDs()
	if err != nil {
		return "", err
	}
	if !own[id] {
		return "", fmt.Errorf("%w: это не собственный сервер", ErrInvalid)
	}
	c.mu.Lock()
	_, needed := c.needed()[id]
	c.mu.Unlock()
	if needed {
		return "", fmt.Errorf("%w: сервер сейчас используется — сначала выберите другой узел", ErrBusy)
	}
	labels, _ := c.st.NodeLabels()
	rep, err := c.AddOwn(text, initiator)
	if err != nil {
		return "", err
	}
	if len(rep.IDs) != 1 {
		return "", fmt.Errorf("%w: для замены нужна ровно одна ссылка", ErrInvalid)
	}
	if rep.IDs[0] != id {
		if err := c.st.DeleteOwn(id); err != nil {
			return "", err
		}
		if l := labels[id]; l != "" {
			_ = c.st.SetNodeLabel(rep.IDs[0], l)
			_ = c.st.SetNodeLabel(id, "")
		}
		c.invalidateCounts()
	}
	return rep.IDs[0], nil
}
