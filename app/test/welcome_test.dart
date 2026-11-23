import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/model.dart';
import 'package:brod/src/welcome_page.dart';

class _Api implements DaemonApi {
  _Api(this.state);
  WarpStateDto state;

  @override
  Future<WarpStateDto> warpState() async => state;

  @override
  void close() {}

  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError('${invocation.memberName}');
}

Future<void> _open(WidgetTester t, WarpStateDto s, VoidCallback onDone) async {
  await t.pumpWidget(MaterialApp(home: WelcomeFlow(model: AppModel(_Api(s)), onDone: onDone)));
  await t.pump();
}

void main() {
  testWidgets('мастер: четыре шага, WARP работает — кнопок починки нет', (t) async {
    var done = 0;
    await _open(t, const WarpStateDto(enabled: true), () => done++);
    expect(find.text('Добро пожаловать в Brod'), findsOneWidget);
    expect(find.textContaining('Серверы из бесплатных подписок чужие'), findsOneWidget, reason: 'предупреждение о доверии к чужим серверам на первом шаге');
    await t.tap(find.text('Дальше'));
    await t.pump();
    expect(find.textContaining('WARP работает'), findsOneWidget);
    expect(find.text('Подобрать для этой сети'), findsNothing);
    await t.tap(find.text('Дальше'));
    await t.pump();
    expect(find.text('Подключиться к лучшему узлу'), findsOneWidget);
    expect(find.textContaining('Переключать сам'), findsOneWidget, reason: 'в мастере сказано, что автозамену узла можно включить');
    await t.tap(find.text('Дальше'));
    await t.pump();
    expect(find.text('Что где'), findsOneWidget);
    await t.tap(find.text('Готово'));
    expect(done, 1);
  });

  testWidgets('мастер: WARP не отвечает — предлагается подбор; не создан — повтор регистрации; «Пропустить» закрывает', (t) async {
    var done = 0;
    await _open(t, const WarpStateDto(enabled: true, down: true), () => done++);
    await t.tap(find.text('Дальше'));
    await t.pump();
    expect(find.text('Подобрать для этой сети'), findsOneWidget);
    await t.pumpWidget(const SizedBox());
    await _open(t, const WarpStateDto(enabled: false), () => done++);
    await t.tap(find.text('Дальше'));
    await t.pump();
    expect(find.text('Создать аккаунт ещё раз'), findsOneWidget);
    await t.tap(find.text('Пропустить'));
    expect(done, 1);
  });
}
