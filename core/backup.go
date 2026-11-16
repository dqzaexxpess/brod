package core

import (
	"brod/nodeopts"
	"encoding/json"
	"fmt"
	"time"

	"brod/store"
)

// Резервная копия настроек: правила маршрутизации, настройки демона, собственные серверы (с параметрами подключения — это секреты:
// файл копии нужно хранить так же аккуратно, как ссылки серверов) и названия узлов. Узлы из подписок не сохраняются — они вернутся обновлением.

// BackupVersion — версия формата копии.
const BackupVersion = 1

// BackupNode — собственный сервер в копии.
type BackupNode struct {
	ID         string          `json:"id"`
	Class      string          `json:"class"`
	Proto      string          `json:"proto"`
	Endpoint   bool            `json:"endpoint,omitempty"`
	Server     string          `json:"server"`
	Port       int             `json:"port"`
	Opts       json.RawMessage `json:"opts"`
	Notes      []string        `json:"notes,omitempty"`
	Insecure   bool            `json:"insecure,omitempty"`
	NoSecurity bool            `json:"no_security,omitempty"`
}

// Backup — содержимое резервной копии.
type Backup struct {
	Version  int               `json:"version"`
	Created  time.Time         `json:"created"`
	Settings map[string]string `json:"settings,omitempty"`
	Rules    []RouteRule       `json:"rules,omitempty"`
	// профили маршрутизации: имя активного (его правила — Rules) и остальные
	ActiveProfile string            `json:"active_profile,omitempty"`
	Profiles      []BackupProfile   `json:"profiles,omitempty"`
	Own           []BackupNode      `json:"own,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Sources       []BackupSource    `json:"sources,omitempty"` // добавленные пользователем подписки (адрес может содержать токен — секрет)
}

// BackupSource — пользовательский источник в копии.
type BackupSource struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Mirror   string `json:"mirror,omitempty"`
	Priority int    `json:"priority"`
	Enabled  bool   `json:"enabled"`
}

// BackupReport — итог восстановления.
type BackupReport struct {
	Settings int
	Rules    int
	Own      int
	Labels   int
	Sources  int
	Skipped  []string // что не удалось применить и почему
}

// ExportBackup собирает копию.
func (c *Core) ExportBackup() (Backup, error) {
	b := Backup{Version: BackupVersion, Created: c.cfg.Now(), Settings: map[string]string{}, Labels: map[string]string{}}
	all, err := c.st.Settings()
	if err != nil {
		return b, err
	}
	for k := range settingRules {
		if v, ok := all[k]; ok {
			b.Settings[k] = v
		}
	}
	if b.Rules, err = c.RouteRules(); err != nil {
		return b, err
	}
	if b.ActiveProfile, b.Profiles, err = c.backupProfiles(); err != nil {
		return b, err
	}
	if recs, err := c.st.Sources(); err == nil {
		for _, r := range recs {
			if r.UserAdded {
				b.Sources = append(b.Sources, BackupSource{Name: r.ID, URL: r.URL, Mirror: r.Mirror, Priority: r.Priority, Enabled: r.Enabled})
			}
		}
	}
	own, err := c.st.OwnIDs()
	if err != nil {
		return b, err
	}
	if len(own) > 0 {
		ids := make([]string, 0, len(own))
		for id := range own {
			ids = append(ids, id)
		}
		recs, err := c.st.Nodes(store.NodeFilter{IDs: ids, IncludeNoSecurity: true, WithOpts: true})
		if err != nil {
			return b, err
		}
		for _, r := range recs {
			b.Own = append(b.Own, BackupNode{ID: r.ID, Class: r.Class, Proto: r.Proto, Endpoint: r.Endpoint, Server: r.Server, Port: r.Port,
				Opts: json.RawMessage(r.Opts), Notes: r.Notes, Insecure: r.Insecure, NoSecurity: r.NoSecurity})
		}
	}
	labels, _ := c.st.NodeLabels()
	for id, l := range labels {
		if own[id] {
			b.Labels[id] = l
		}
	}
	return b, nil
}

// ImportBackup применяет копию поверх текущего состояния: настройки и правила заменяются, собственные серверы добавляются/обновляются
// (чужие не трогаются). Правила и настройки проходят обычную проверку; негодное пропускается с пояснением.
func (c *Core) ImportBackup(b Backup, initiator string) (BackupReport, error) {
	var rep BackupReport
	if b.Version < 1 || b.Version > BackupVersion {
		return rep, fmt.Errorf("%w: версия копии %d не поддерживается (нужна 1–%d)", ErrInvalid, b.Version, BackupVersion)
	}
	for k, v := range b.Settings {
		if err := c.SetSetting(k, v, initiator); err != nil {
			rep.Skipped = append(rep.Skipped, "настройка "+k+": "+err.Error())
			continue
		}
		rep.Settings++
	}
	if b.Rules != nil {
		if err := c.PutRouteRules(b.Rules, initiator); err != nil {
			rep.Skipped = append(rep.Skipped, "правила: "+err.Error())
		} else {
			rep.Rules = len(b.Rules)
		}
	}
	if b.ActiveProfile != "" || len(b.Profiles) > 0 {
		skipped, err := c.restoreProfiles(b.ActiveProfile, b.Profiles)
		rep.Skipped = append(rep.Skipped, skipped...)
		if err != nil {
			rep.Skipped = append(rep.Skipped, "профили: "+err.Error())
		}
	}
	for _, s := range b.Sources {
		if err := c.AddSource(s.Name, s.URL, s.Mirror, s.Priority, initiator); err != nil {
			rep.Skipped = append(rep.Skipped, "источник "+s.Name+": "+err.Error())
			continue
		}
		if !s.Enabled {
			_ = c.SetSourceEnabled(s.Name, false, initiator)
		}
		rep.Sources++
	}
	now := c.cfg.Now()
	for _, n := range b.Own {
		var probe map[string]any
		if n.ID == "" || json.Unmarshal(n.Opts, &probe) != nil || probe["type"] == nil {
			rep.Skipped = append(rep.Skipped, "сервер "+n.Server+": повреждённые параметры")
			continue
		}
		clean, err := nodeopts.Sanitize(n.Opts, nodeopts.Flags{}) // чужая копия не должна нести ничего, кроме полей из белого списка
		if err != nil {
			rep.Skipped = append(rep.Skipped, "сервер "+n.Server+": "+err.Error())
			continue
		}
		n.Opts = clean
		rec := store.NodeRec{ID: n.ID, Class: n.Class, Proto: n.Proto, Endpoint: n.Endpoint, Server: n.Server, Port: n.Port, Opts: []byte(n.Opts),
			Notes: n.Notes, Insecure: n.Insecure, NoSecurity: n.NoSecurity}
		if err := c.st.UpsertNodes(store.OwnSource, now.UnixNano(), []store.NodeRec{rec}, now); err != nil {
			rep.Skipped = append(rep.Skipped, "сервер "+n.Server+": "+err.Error())
			continue
		}
		_ = c.st.SetNodeFlag(n.ID, store.FlagPinned, true)
		_ = c.st.SetNodeFlag(n.ID, store.FlagFavorite, true)
		rep.Own++
	}
	for id, l := range b.Labels {
		if _, err := c.st.Node(id); err == nil && c.st.SetNodeLabel(id, l) == nil {
			rep.Labels++
		}
	}
	c.invalidateCounts()
	c.bus.Publish(store.Event{Kind: EvSetting, Initiator: initiator, Detail: fmt.Sprintf("восстановление из копии: настроек %d, правил %d, серверов %d, пропущено %d", rep.Settings, rep.Rules, rep.Own, len(rep.Skipped))})
	return rep, nil
}
