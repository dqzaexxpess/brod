import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/country.dart';

void main() {
  test('флаги и названия', () {
    expect(flagOf('DE'), '🇩🇪');
    expect(flagOf('nl'), '🇳🇱');
    expect(flagOf('XX'), '🌐');
    expect(flagOf(''), '🌐');
    expect(countryName('FI'), 'Финляндия');
    expect(countryName('ZZ'), 'ZZ');
    expect(countryName('IR'), 'Иран', reason: 'в списке узлов страна не должна показываться кодом');
    expect(countryName('ir'), 'Иран');
    expect(countryName('PS'), 'Палестина');
    expect(countryName(''), 'Неизвестно');
  });

  test('где флаги-эмодзи отключены, вместо флага — код страны', () {
    flagEmojiOverride = false;
    addTearDown(() => flagEmojiOverride = null);
    expect(flagOf('de'), 'DE');
    expect(flagOf('XX'), '🌐');
    expect(flagOf(''), '🌐');
  });

  test('таблица названий полная: каждая страна ISO названа по-русски, а не кодом', () {
    var named = 0;
    for (var a = 65; a <= 90; a++) {
      for (var b = 65; b <= 90; b++) {
        final code = String.fromCharCodes([a, b]);
        if (countryName(code) != code) named++;
      }
    }
    expect(named, greaterThanOrEqualTo(245));
  });
}
