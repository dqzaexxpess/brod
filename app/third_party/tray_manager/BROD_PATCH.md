Копия tray_manager 0.2.4 (MIT) с одной правкой: стабильный id иконки вместо случайного (lib/src/tray_manager.dart, поле 'id').
Причина: Plasma хранит видимость значка по id, со случайным id «всегда показывать» не запоминается.

Правка 2 (linux/tray_manager_plugin.cc, set_icon): абсолютный путь к PNG разбирается на каталог (`app_indicator_new_with_path` / `app_indicator_set_icon_theme_path`) и имя без расширения (`app_indicator_set_icon_full`). Причина: в IconName оказывался путь, IconThemePath был пуст, и Plasma показывала иконку только в момент смены, а в покое — пустое место.
