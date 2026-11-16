package core

import "strings"

// Снимок таблицы токенов в памяти: главный токен и хэши токенов приложений. Authenticate зовут на каждый HTTP-запрос
// (CallerActive — на каждый такт долгих потоков), поэтому проверка не должна ходить в базу.
// Снимок привязан к версии настроек хранилища (Store.SettingsGen): любая запись настройки (выдача, перевыпуск, отзыв, смена главного
// токена, запись мимо ядра) сбрасывает его, поэтому отзыв действует со следующего же запроса. Хэш предъявленного токена
// считается на каждый запрос: иначе пришлось бы хранить токены в открытом виде.
type authSnap struct {
	gen    uint64
	master string
	apps   []appHash       // порядок произвольный; обходится целиком (постоянное время)
	names  map[string]bool // имена приложений с токеном
}

type appHash struct{ name, hash string }

// authSnapshot возвращает актуальный снимок, пересоздавая его при смене версии настроек.
func (c *Core) authSnapshot() (*authSnap, error) {
	g := c.st.SettingsGen() // до чтения: запись во время сборки оставит снимок с прежней версией, и он пересоберётся
	if p := c.auth.Load(); p != nil && p.gen == g {
		return p, nil
	}
	all, err := c.st.Settings()
	if err != nil {
		return nil, err
	}
	s := &authSnap{gen: g, master: all[SetAPIToken], names: map[string]bool{}}
	for k, v := range all {
		if name, ok := strings.CutPrefix(k, tokenPrefix); ok {
			s.apps = append(s.apps, appHash{name, v})
			s.names[name] = true
		}
	}
	c.auth.Store(s)
	return s, nil
}
