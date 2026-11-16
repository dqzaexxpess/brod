package supervisor

import "regexp"

// winTunNameRe — имена туннельных адаптеров Windows (Throne: throne-tun, WireGuard: имя конфигурации, wintun, OpenVPN TAP).
// «Teredo Tunneling Pseudo-Interface», isatap и 6to4 есть в системе всегда и туннелем «весь трафик» не являются: «tun» должно стоять
// отдельным словом (граница — начало, «-», «_», пробел), «Tunneling» под правило не попадает.
var winTunNameRe = regexp.MustCompile(`(?i)(^|[-_ ])(tun|tap|wg|wintun|wireguard|utun)([-_ 0-9]|$)|tun$|tap-windows`)

func isWinTunName(name string) bool { return winTunNameRe.MatchString(name) }
