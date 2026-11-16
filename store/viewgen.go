package store

import (
	"database/sql"
	"strings"
	"sync/atomic"
)

// Метка версии списка узлов: ответ GET /v1/nodes собирается из таблиц nodes/node_sources, проб, скорости, выхода и настроек
// (подписи, «скрывать небезопасные»). nodesGen покрывает только первые две, поэтому остальное считает ViewGen: он растёт при каждой
// записи через единственное соединение записи, кроме заведомо не влияющих таблиц (трафик, события, Telegram-прокси: пишутся постоянно
// и в списке узлов не видны). Новая таблица по умолчанию «влияет»: метка может только сбрасываться чаще, чем нужно, но не реже.

// NodesGen — версия таблиц узлов (растёт при любой записи в nodes и node_sources).
func (s *Store) NodesGen() uint64 { return s.nodesGen.Load() }

// ViewGen — версия данных списка узлов: любая запись, кроме шумных таблиц. Читать до сборки списка: запись, зафиксированная позже
// чтения, увеличит счётчик, и следующая метка будет другой.
func (s *Store) ViewGen() uint64 { return s.viewGen.Load() }

// Epoch — момент открытия базы (нс): счётчики после перезапуска начинаются с нуля и без него совпали бы с метками прежнего запуска.
func (s *Store) Epoch() int64 { return s.epoch }

// writeDB — соединение записи; каждая запись, не из шумных таблиц, увеличивает viewGen после выполнения (не до: читатель не должен
// получить новую метку со старыми данными).
type writeDB struct {
	*sql.DB
	gen *atomic.Uint64
}

// quietWrites — подстроки запросов, не влияющих на список узлов.
var quietWrites = []string{"traffic_daily", "INTO events(", "tg_proxies"}

func quiet(q string) bool {
	for _, k := range quietWrites {
		if strings.Contains(q, k) {
			return true
		}
	}
	return false
}

func (w *writeDB) Exec(q string, args ...any) (sql.Result, error) {
	r, err := w.DB.Exec(q, args...)
	if !quiet(q) {
		w.gen.Add(1)
	}
	return r, err
}

func (w *writeDB) Begin() (*writeTx, error) {
	tx, err := w.DB.Begin()
	if err != nil {
		return nil, err
	}
	return &writeTx{Tx: tx, gen: w.gen}, nil
}

// writeTx — транзакция записи: метка растёт после фиксации.
type writeTx struct {
	*sql.Tx
	gen *atomic.Uint64
}

func (t *writeTx) Commit() error {
	err := t.Tx.Commit()
	t.gen.Add(1)
	return err
}
