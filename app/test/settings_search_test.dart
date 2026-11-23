import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/settings_search.dart';

void main() {
  const all = [
    SearchEntry('Киллсвитч', 'connection', 'блокировка утечки интернет без vpn kill switch'),
    SearchEntry('Раздавать прокси в локальную сеть', 'routing', 'lan телевизор консоль socks http'),
    SearchEntry('Мои серверы', 'nodes', 'свой сервер ссылка vless цепочка прокси', hint: 'Свой сервер по ссылке'),
  ];

  test('пустой запрос ничего не даёт', () {
    expect(searchSettings('  ', all), isEmpty);
  });

  test('находит по названию и по синониму, ё и регистр не мешают', () {
    expect(searchSettings('КИЛЛ', all).single.section, 'connection');
    expect(searchSettings('kill switch', all).single.title, 'Киллсвитч');
    expect(searchSettings('телевизор', all).single.section, 'routing');
  });

  test('нужны все слова запроса', () {
    expect(searchSettings('прокси цепочка', all).single.title, 'Мои серверы');
    expect(searchSettings('прокси банан', all), isEmpty);
  });

  test('совпадение в названии выше, чем в ключевых словах', () {
    final r = searchSettings('прокси', all);
    expect(r.first.title, 'Раздавать прокси в локальную сеть');
    expect(r.length, 2);
  });

  test('окончания не мешают: «серверы» находит «сервер», «обновления» — «обновление»', () {
    const e = [SearchEntry('Мои серверы', 'nodes', 'свой сервер'), SearchEntry('Обновление приложения', 'about', 'обновить')];
    expect(searchSettings('серверы', e).single.title, 'Мои серверы');
    expect(searchSettings('обновления', e).single.title, 'Обновление приложения');
    expect(searchSettings('сервера', e).single.title, 'Мои серверы');
  });
}
