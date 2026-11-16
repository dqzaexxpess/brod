// Package buildinfo — номер версии программ демона в одном месте: его берут brodd (версия в журнале и API) и ресурсы Windows-файлов
// (VERSIONINFO в brodd.exe и brodctl.exe, buildinfo/winres). При смене номера ресурсы пересобираются: go run ./buildinfo/winres/gen.
package buildinfo

// Version — номер версии демона и CLI (без платформы).
const Version = "0.3.0"

// Version — единственный источник номера версии: им пользуются brodd, ядро Android, ресурсы Windows,
// и в той же версии (X.Y.Z) выпускается приложение: app/pubspec.yaml `version: X.Y.Z+N` (N — номер сборки, на Android versionCode, при
// каждом выпуске растёт). Тест TestVersionMatchesPubspec следит за совпадением. Сравнение версий в OTA (ota.Release.NewerThan) требует вид X.Y.Z.
