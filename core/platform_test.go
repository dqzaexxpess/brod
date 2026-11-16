package core

import "runtime"

// posixPerms — права файлов в стиле POSIX (0600, 0700) проверяемы: на Windows доступ задаётся ACL, а Mode().Perm() их не отражает.
var posixPerms = runtime.GOOS != "windows"

// killswitchDefault — значение настройки killswitch.enabled по умолчанию: киллсвитч реализован только на Linux.
var killswitchDefault = map[bool]string{true: "1", false: "0"}[runtime.GOOS == "linux"]
