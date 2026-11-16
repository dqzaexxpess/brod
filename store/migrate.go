package store

import (
	"database/sql"
	"fmt"
)

// migrations применяются по порядку; версия базы хранится в PRAGMA user_version. Менять уже выпущенные шаги нельзя —
// только добавлять новые в конец. Шаг 1 идемпотентен (IF NOT EXISTS): базы, созданные до появления миграций
// (user_version = 0), проходят его без потерь.
var migrations = []string{
	// 1. результаты проверок (Ф2)
	`
CREATE TABLE IF NOT EXISTS probes (
  node     TEXT NOT NULL,   -- sha1 ключа дедупликации
  cls      TEXT NOT NULL DEFAULT '',  -- класс узла: протокол/транспорт/безопасность (для априорных долей успеха)
  net      TEXT NOT NULL,   -- идентификатор сети (NetID)
  level    INTEGER NOT NULL,
  target   TEXT NOT NULL DEFAULT '',
  ok       INTEGER NOT NULL,
  class    TEXT NOT NULL DEFAULT '',
  latency_ms INTEGER NOT NULL DEFAULT 0,
  bytes    INTEGER NOT NULL DEFAULT 0,
  speed_kbps REAL NOT NULL DEFAULT 0,
  at       INTEGER NOT NULL  -- unix-секунды
);
CREATE INDEX IF NOT EXISTS probes_node_net ON probes(node, net, at);
CREATE INDEX IF NOT EXISTS probes_net_at ON probes(net, at);`,
	// 2. состояние демона (Ф3): источники, узлы, настройки, журнал событий, цели, выбор
	`
CREATE TABLE sources (
  id         TEXT PRIMARY KEY,           -- имя источника
  url        TEXT NOT NULL,              -- может содержать токен: наружу (CLI/API) не отдаётся без явной просьбы
  mirror     TEXT NOT NULL DEFAULT '',
  priority   INTEGER NOT NULL DEFAULT 1,
  enabled    INTEGER NOT NULL DEFAULT 1,
  user_added INTEGER NOT NULL DEFAULT 0,
  last_fetch INTEGER NOT NULL DEFAULT 0, -- unix-секунды последней попытки
  last_ok    INTEGER NOT NULL DEFAULT 0, -- последней удачной
  last_err   TEXT NOT NULL DEFAULT '',
  last_count INTEGER NOT NULL DEFAULT 0, -- ссылок в последней удачной загрузке
  added_at   INTEGER NOT NULL
);
CREATE TABLE nodes (
  id          TEXT PRIMARY KEY,          -- NodeID: sha1 ключа дедупликации
  cls         TEXT NOT NULL DEFAULT '',
  proto       TEXT NOT NULL DEFAULT '',
  endpoint    INTEGER NOT NULL DEFAULT 0, -- 1: endpoint sing-box (WireGuard/AWG), а не outbound
  server      TEXT NOT NULL DEFAULT '',
  port        INTEGER NOT NULL DEFAULT 0,
  opts        BLOB NOT NULL,             -- JSON outbound'а sing-box без tag; содержит секреты
  notes       TEXT NOT NULL DEFAULT '[]',
  insecure    INTEGER NOT NULL DEFAULT 0,
  no_security INTEGER NOT NULL DEFAULT 0,
  favorite    INTEGER NOT NULL DEFAULT 0,
  pinned      INTEGER NOT NULL DEFAULT 0,
  first_seen  INTEGER NOT NULL,
  last_seen   INTEGER NOT NULL
);
-- Узел хранится один раз, а присутствие в источниках — отдельно: дедуплицированный узел бывает сразу в нескольких.
CREATE TABLE node_sources (
  node    TEXT NOT NULL,
  source  TEXT NOT NULL,
  present INTEGER NOT NULL DEFAULT 1,    -- 0: источник перестал отдавать узел (узел при этом остаётся, пока он активен/закреплён)
  stamp   INTEGER NOT NULL,              -- метка обновления, в котором узел последний раз встретился
  PRIMARY KEY (node, source)
);
CREATE INDEX node_sources_source ON node_sources(source, stamp);
CREATE TABLE settings (k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE events (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  at        INTEGER NOT NULL,            -- unix-миллисекунды
  kind      TEXT NOT NULL,
  initiator TEXT NOT NULL DEFAULT '',    -- user | auto | api:<приложение>
  node      TEXT NOT NULL DEFAULT '',
  target    TEXT NOT NULL DEFAULT '',
  detail    TEXT NOT NULL DEFAULT ''
);
CREATE TABLE targets (
  id      TEXT PRIMARY KEY,
  owner   TEXT NOT NULL,                 -- builtin | имя приложения
  title   TEXT NOT NULL DEFAULT '',
  spec    TEXT NOT NULL,                 -- JSON: правила, проверка, политика, K, допустимые кандидаты
  created INTEGER NOT NULL,
  updated INTEGER NOT NULL
);
CREATE TABLE selection (
  scope     TEXT PRIMARY KEY,            -- "active" или "target:<id>"
  node      TEXT NOT NULL DEFAULT '',    -- NodeID либо "direct"/"warp"
  fallbacks TEXT NOT NULL DEFAULT '[]',  -- JSON: упорядоченные запасные
  set_at    INTEGER NOT NULL,
  by        TEXT NOT NULL DEFAULT ''
);`,
	// 3. сведения о подписке, которые сообщает сам источник (название, период обновления, квота)
	`
ALTER TABLE sources ADD COLUMN title TEXT NOT NULL DEFAULT '';
ALTER TABLE sources ADD COLUMN update_hours INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sources ADD COLUMN used_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sources ADD COLUMN total_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sources ADD COLUMN expire INTEGER NOT NULL DEFAULT 0;`,
	// 4. что видят сайты на выходе узла: адрес, страна, поведенческая «чистота» (не зависит от сети пользователя — адрес выхода один)
	`
CREATE TABLE exit_info (
  node      TEXT PRIMARY KEY,
  ip        TEXT NOT NULL DEFAULT '',
  country   TEXT NOT NULL DEFAULT '',   -- ISO alpha-2 из ответа Cloudflare
  colo      TEXT NOT NULL DEFAULT '',
  google_ok INTEGER NOT NULL DEFAULT -1, -- 1 без капчи, 0 капча, -1 не проверялось
  site_ok   INTEGER NOT NULL DEFAULT -1, -- 1 строгий сайт открылся, 0 испытание/отказ, -1 не проверялось
  detail    TEXT NOT NULL DEFAULT '',
  at        INTEGER NOT NULL            -- unix-секунды
);`,
	// 5. скорость скачивания через узел (замер ~2 МБ с Cloudflare), КБ/с
	`
CREATE TABLE node_speed (
  node TEXT PRIMARY KEY,
  kbps REAL NOT NULL,
  at   INTEGER NOT NULL
);`,
	// 6. трафик по дням (все соединения, байт): статистика в окне «Монитор»
	`
CREATE TABLE traffic_daily (
  day  TEXT PRIMARY KEY,   -- YYYY-MM-DD по местному времени
  up   INTEGER NOT NULL DEFAULT 0,
  down INTEGER NOT NULL DEFAULT 0,
  vpn_up   INTEGER NOT NULL DEFAULT 0,  -- из них через VPN (активный узел и цели)
  vpn_down INTEGER NOT NULL DEFAULT 0
);`,
	// 7. прокси для Telegram (MTProto, SOCKS5): из источников и свои; результат последней проверки хранится в строке
	`
CREATE TABLE tg_proxies (
  id         TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,                 -- mtproto | socks5
  server     TEXT NOT NULL,
  port       INTEGER NOT NULL,
  secret     TEXT NOT NULL DEFAULT '',      -- hex в каноническом виде (MTProto)
  user       TEXT NOT NULL DEFAULT '',
  pass       TEXT NOT NULL DEFAULT '',
  source     TEXT NOT NULL DEFAULT '',      -- источник, из которого пришёл (пусто у своих)
  stamp      INTEGER NOT NULL DEFAULT 0,    -- метка обновления источника, в котором прокси встретился последний раз
  present    INTEGER NOT NULL DEFAULT 1,    -- 0: источник перестал отдавать прокси
  favorite   INTEGER NOT NULL DEFAULT 0,
  own        INTEGER NOT NULL DEFAULT 0,    -- добавлен пользователем: из базы не пропадает
  ok         INTEGER NOT NULL DEFAULT 0,    -- результат последней проверки
  latency_ms INTEGER NOT NULL DEFAULT 0,
  checked_at INTEGER NOT NULL DEFAULT 0,    -- 0: ещё не проверялся
  fails      INTEGER NOT NULL DEFAULT 0,    -- подряд проваленных проверок
  first_seen INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL
);
CREATE INDEX tg_proxies_source ON tg_proxies(source, stamp);
CREATE INDEX tg_proxies_state ON tg_proxies(ok, latency_ms);`,
	// 8. кандидаты в источники (автопоиск, «мягкий карантин»): лежат отдельно от sources и nodes. Ссылок узлов и секретов здесь нет —
	// только счётчики и схемы; выборка для проверки живёт в памяти.
	`
CREATE TABLE source_candidates (
  url         TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  repo        TEXT NOT NULL DEFAULT '',
  first_seen  INTEGER NOT NULL,
  last_seen   INTEGER NOT NULL,
  last_day    TEXT NOT NULL DEFAULT '',     -- день (YYYY-MM-DD) последнего наблюдения: дни считаются по разным датам, а не по числу проходов
  days_seen   INTEGER NOT NULL DEFAULT 1,
  links       INTEGER NOT NULL DEFAULT 0,   -- ссылок узлов в последней загрузке
  schemes     TEXT NOT NULL DEFAULT '{}',   -- JSON: схема -> число ссылок
  sampled     INTEGER NOT NULL DEFAULT 0,   -- накопительно: проверено ссылок из выборок
  sample_ok   INTEGER NOT NULL DEFAULT 0,   -- накопительно: из проверенных ответило
  sample_days INTEGER NOT NULL DEFAULT 0,   -- дней, в которые брали выборку
  last_sample INTEGER NOT NULL DEFAULT 0,   -- unix-секунды последней выборки
  dismissed   INTEGER NOT NULL DEFAULT 0,
  added       INTEGER NOT NULL DEFAULT 0
);`,
	// 9. отпечаток набора ссылок кандидата (bottom-k sketch: до 64 усечённых солёных хэшей по 8 байт, до 512 байт): по нему кандидаты-зеркала
	// группируются. Только хэши; ссылок и секретов здесь нет. NULL — отпечатка ещё нет (кандидат до миграции, пока его не увидит проход).
	`ALTER TABLE source_candidates ADD COLUMN sketch BLOB;`,
}

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("база создана более новой версией (схема %d, поддерживается %d)", v, len(migrations))
	}
	for i := v; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("миграция %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
