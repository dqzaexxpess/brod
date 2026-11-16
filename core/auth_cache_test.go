package core

import "testing"

// проверка токена идёт по снимку таблицы хэшей в памяти: пока настройки не менялись, снимок один и тот же (ни базы, ни разбора
// таблицы на запрос); любая запись настроек (выдача, отзыв, перевыпуск, смена главного токена, запись мимо ядра) пересоздаёт его,
// отзыв действует сразу.
func TestAuthSnapshotCachedAndInvalidated(t *testing.T) {
	c := startCore(t, t.TempDir())
	master := c.APIToken()
	if _, ok := c.Authenticate(master); !ok {
		t.Fatal("главный токен не принят")
	}
	snap := c.auth.Load()
	if snap == nil {
		t.Fatal("снимок токенов не создан")
	}
	for i := 0; i < 50; i++ {
		if _, ok := c.Authenticate("чужой-токен"); ok {
			t.Fatal("чужой токен принят")
		}
		if _, ok := c.Authenticate(master); !ok {
			t.Fatal("главный токен не принят")
		}
	}
	if c.auth.Load() != snap {
		t.Fatal("снимок пересоздавался без записи настроек: проверка токена снова читает настройки")
	}

	tok, err := c.CreateAppToken("app1", "user")
	if err != nil {
		t.Fatal(err)
	}
	if who, ok := c.Authenticate(tok); !ok || who != "api:app1" {
		t.Fatalf("выданный токен не принят сразу: %q %v", who, ok)
	}
	if !c.CallerActive("api:app1") || !c.CallerActive("user") {
		t.Fatal("CallerActive: действующий токен считается отозванным")
	}
	if err := c.RevokeAppToken("app1", "user"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Authenticate(tok); ok {
		t.Fatal("отозванный токен действует (кэш не сброшен)")
	}
	if c.CallerActive("api:app1") {
		t.Fatal("CallerActive: отозванный токен считается действующим (кэш не сброшен)")
	}
	// токен записан в настройки мимо CreateAppToken (например, восстановление копии): тоже виден сразу
	if err := c.st.SetSetting(tokenPrefix+"app2", hashToken("секрет-2")); err != nil {
		t.Fatal(err)
	}
	if who, ok := c.Authenticate("секрет-2"); !ok || who != "api:app2" {
		t.Fatalf("токен из прямой записи: %q %v", who, ok)
	}
	// смена главного токена
	if err := c.st.SetSetting(SetAPIToken, "новый-главный"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Authenticate(master); ok {
		t.Fatal("прежний главный токен действует")
	}
	if who, ok := c.Authenticate("новый-главный"); !ok || who != "user" {
		t.Fatalf("новый главный токен: %q %v", who, ok)
	}
}
