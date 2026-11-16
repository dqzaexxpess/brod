package rulesets

import "runtime"

// posixPerms — права файлов в стиле POSIX (0600, 0700) проверяемы: на Windows доступ задаётся ACL, а Mode().Perm() их не отражает.
var posixPerms = runtime.GOOS != "windows"
