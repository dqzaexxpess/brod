#ifndef RUNNER_MENU_THEME_H_
#define RUNNER_MENU_THEME_H_

#include <string>

// Applies the app theme to native popup menus (the tray menu): "dark", "light" or "system".
// Win32 menus stay light unless the process opts into dark mode through uxtheme.dll; the entry
// points are undocumented (exported by ordinal only) but stable since Windows 10 1903 and used
// by Explorer, Notepad++ and others. On older builds this is a no-op. Returns false if unsupported.
bool SetMenuTheme(const std::string& mode);

#endif  // RUNNER_MENU_THEME_H_
