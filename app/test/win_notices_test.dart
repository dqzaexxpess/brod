import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/win_notices.dart';

void main() {
  test('разбор ответа /v1/notifications', () {
    final b = NoticeBatch.fromJson({
      'next': 7,
      'ops': [
        {
          'seq': 6,
          'op': 'show',
          'id': 3,
          'summary': 'Узел деградирует',
          'body': 'NL · vless',
          'actions': [
            {'key': 'swap', 'label': 'Сменить'},
          ],
          'timeout_ms': -1000,
        },
        {'seq': 7, 'op': 'close', 'id': 2},
      ],
    });
    expect(b.next, 7);
    expect(b.ops.length, 2);
    expect(b.ops[0].actions.single, ('swap', 'Сменить'));
    expect(b.ops[0].timeoutMs, -1000);
    expect(b.ops[1].op, 'close');
    expect(NoticeBatch.fromJson({'next': 0, 'ops': []}).ops, isEmpty);
  });

  test('аргументы нажатия: id|ключ', () {
    expect(parseNoticeArgs(noticeArgs(12, 'swap')), (12, 'swap'));
    expect(parseNoticeArgs(noticeArgs(5, '')), (5, ''));
    expect(parseNoticeArgs('мусор'), isNull);
    expect(parseNoticeArgs(null), isNull);
    expect(parseNoticeArgs('|x'), isNull);
  });
}
