#include "menu_theme.h"

#include <windows.h>

namespace {

// uxtheme.dll, ordinal 135 since build 18362 (1903).
enum class PreferredAppMode { Default, AllowDark, ForceDark, ForceLight, Max };
using SetPreferredAppModeFn = PreferredAppMode(WINAPI*)(PreferredAppMode);
// uxtheme.dll, ordinal 136: re-reads the menu theme for already created menus.
using FlushMenuThemesFn = void(WINAPI*)();

DWORD WindowsBuild() {
  using RtlGetVersionFn = LONG(WINAPI*)(PRTL_OSVERSIONINFOW);
  HMODULE ntdll = GetModuleHandleW(L"ntdll.dll");
  if (!ntdll) return 0;
  auto get_version = reinterpret_cast<RtlGetVersionFn>(GetProcAddress(ntdll, "RtlGetVersion"));
  if (!get_version) return 0;
  RTL_OSVERSIONINFOW info = {sizeof(info)};
  return get_version(&info) == 0 ? info.dwBuildNumber : 0;
}

}  // namespace

bool SetMenuTheme(const std::string& mode) {
  static const bool supported = WindowsBuild() >= 18362;
  if (!supported) return false;
  static HMODULE uxtheme = LoadLibraryExW(L"uxtheme.dll", nullptr, LOAD_LIBRARY_SEARCH_SYSTEM32);
  if (!uxtheme) return false;
  auto set_mode = reinterpret_cast<SetPreferredAppModeFn>(GetProcAddress(uxtheme, MAKEINTRESOURCEA(135)));
  auto flush = reinterpret_cast<FlushMenuThemesFn>(GetProcAddress(uxtheme, MAKEINTRESOURCEA(136)));
  if (!set_mode) return false;
  PreferredAppMode m = PreferredAppMode::AllowDark;  // "system": follow the Windows app theme
  if (mode == "dark") m = PreferredAppMode::ForceDark;
  if (mode == "light") m = PreferredAppMode::ForceLight;
  set_mode(m);
  if (flush) flush();
  return true;
}
