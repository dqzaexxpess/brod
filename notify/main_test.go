package notify_test

import (
	"os"
	"testing"

	"brod/nodeopts"
)

type noCleanup struct{}

func (noCleanup) Cleanup(func()) {}

// Узлы-заглушки тестов (type direct) не проходят белый список nodeopts; разрешаем их на весь тестовый бинарник.
func TestMain(m *testing.M) {
	nodeopts.AllowTypeForTests(noCleanup{}, "direct")
	os.Exit(m.Run())
}
