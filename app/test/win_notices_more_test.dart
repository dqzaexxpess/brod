// T-9: win_notices.dart, остаток: устойчивый разбор ответов демона. WinNotices вне Windows не создаётся: конструктор открывает DLL плагина.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/win_notices.dart';

void main() {
  test('NoticeOp.fromJson: пустой объект и неполные поля дают значения по умолчанию', () {
    final o = NoticeOp.fromJson({});
    expect((o.op, o.id, o.summary, o.body, o.timeoutMs), ('', 0, '', '', 0));
    expect(o.actions, isEmpty);
    final p = NoticeOp.fromJson(jsonDecode('{"op":"show","id":4.0,"actions":[{"key":"a"},{"label":"без ключа"},{}]}') as Map<String, dynamic>);
    expect(p.id, 4);
    expect(p.actions, [('a', ''), ('', 'без ключа'), ('', '')]);
  });

  test('NoticeBatch.fromJson: без полей — next 0 и пустой список; порядок операций сохраняется', () {
    final e = NoticeBatch.fromJson({});
    expect(e.next, 0);
    expect(e.ops, isEmpty);
    final b = NoticeBatch.fromJson({
      'next': 3,
      'ops': [
        {'op': 'show', 'id': 1},
        {'op': 'close', 'id': 1},
        {'op': 'show', 'id': 2},
      ],
    });
    expect(b.ops.map((o) => '${o.op}${o.id}'), ['show1', 'close1', 'show2']);
  });

  test('аргументы нажатия: ключ может содержать «|», нечисловой id и пустая строка отвергаются', () {
    expect(parseNoticeArgs(noticeArgs(9, 'a|b')), (9, 'a|b'));
    expect(parseNoticeArgs('x|key'), isNull);
    expect(parseNoticeArgs(''), isNull);
    expect(parseNoticeArgs('7'), isNull);
    expect(parseNoticeArgs('0|'), (0, ''));
  });
}
