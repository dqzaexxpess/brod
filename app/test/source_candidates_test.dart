// Экран «Найденные источники» (автопоиск, «мягкий карантин», группы зеркал) и вход в него со страницы источников.
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:brod/src/api.dart';
import 'package:brod/src/candidates_page.dart';
import 'package:brod/src/model.dart';
import 'package:brod/src/sources_page.dart';

class _Api implements DaemonApi {
  _Api({this.groups = const [], this.failGroups = false, this.scanError});

  List<CandidateGroupDto> groups;
  bool failGroups;
  ApiException? scanError;
  final calls = <String>[];

  @override
  Future<List<SourceDto>> sources() async => const [SourceDto(name: 'alpha', host: 'a.example', enabled: true, userAdded: false, lastCount: 12)];

  @override
  Future<Map<String, String>> settings() async => {};

  @override
  Future<List<CandidateGroupDto>> candidateGroups() async {
    if (failGroups) throw ApiException(404, 'нет маршрута');
    return groups;
  }

  // добавление убирает только указанного кандидата; группа без представителя берёт первого члена
  @override
  Future<String> addCandidate(String id) async {
    calls.add('add:$id');
    final out = <CandidateGroupDto>[];
    for (final g in groups) {
      final all = [g.representative, ...g.members].where((c) => c.id != id).toList();
      if (all.isNotEmpty) out.add(CandidateGroupDto(id: g.id, representative: all.first, members: all.skip(1).toList(), size: all.length));
    }
    groups = out;
    return 'owner/repo/sub';
  }

  @override
  Future<void> dismissCandidateGroup(String id) async {
    calls.add('dismissGroup:$id');
    groups = [for (final g in groups) if (g.id != id) g];
  }

  @override
  Future<String> scanCandidates({bool allowMetered = false}) async {
    calls.add('scan');
    if (scanError != null) throw scanError!;
    groups = [...groups, const CandidateGroupDto(id: 'gnew', representative: CandidateDto(id: 'new000000000', name: 'x/y/fresh', links: 77, schemes: {'vless': 77}, daysSeen: 1))];
    return 'Просмотрено репозиториев: 3, кандидатов: 1 (новых 1)';
  }

  @override
  void close() {}

  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError('${invocation.memberName}');
}

Future<void> _open(WidgetTester tester, Widget page, {Size size = const Size(900, 1800)}) async {
  await tester.binding.setSurfaceSize(size);
  addTearDown(() => tester.binding.setSurfaceSize(null));
  await tester.pumpWidget(MaterialApp(home: page));
  await tester.pumpAndSettle();
}

const _rep = CandidateDto(id: 'aaaaaaaaaaaa', name: 'sevcator/m1n1-5ub-1', links: 10000, schemes: {'trojan': 500, 'vless': 9500}, daysSeen: 3, sampled: 40, sampleOk: 14, groupSize: 3);
const _mir1 = CandidateDto(id: 'bbbbbbbbbbbb', name: 'pie/m1n1-5ub-1-long-name', links: 9990, schemes: {'vless': 9990}, daysSeen: 2, sampled: 20, sampleOk: 2, groupSize: 3);
const _mir2 = CandidateDto(id: 'cccccccccccc', name: 'pie/m1n1-5ub-2', links: 9000, schemes: {'ss': 9000}, daysSeen: 1, groupSize: 3);
const _solo = CandidateDto(id: 'dddddddddddd', name: 'owner/repo/other', links: 500, schemes: {'ss': 500}, daysSeen: 1);
const _groupA = CandidateGroupDto(id: 'gA', representative: _rep, members: [_mir1, _mir2], size: 3, totalLinksMax: 10000);
const _groupB = CandidateGroupDto(id: 'gB', representative: _solo);

void main() {
  testWidgets('группы: представитель с именем, ссылками, схемами и живостью; члены свёрнуты', (tester) async {
    final m = AppModel(_Api(groups: [_groupA, _groupB]), pollEvery: const Duration(hours: 1));
    await _open(tester, CandidatesPage(model: m));
    expect(find.text('sevcator/m1n1-5ub-1'), findsOneWidget);
    expect(find.text('10000 ссылок · vless 9500, trojan 500'), findsOneWidget);
    expect(find.text('3 дн. наблюдения, живых 14 из 40'), findsOneWidget);
    expect(find.text('ещё 2 похожих'), findsOneWidget);
    expect(find.text('owner/repo/other'), findsOneWidget);
    expect(find.text('1 дн. наблюдения, без проверки'), findsOneWidget);
    // члены скрыты до раскрытия
    expect(find.text('pie/m1n1-5ub-1-long-name'), findsNothing);
    expect(find.text('Скрыть группу'), findsOneWidget);
    expect(find.text('Скрыть'), findsOneWidget); // одиночный кандидат
    expect(find.text('Добавить'), findsNWidgets(2));
  });

  testWidgets('раскрытие: члены с кнопкой «Добавить», «Свернуть» прячет их', (tester) async {
    final api = _Api(groups: [_groupA]);
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await _open(tester, CandidatesPage(model: m));
    await tester.tap(find.text('ещё 2 похожих'));
    await tester.pumpAndSettle();
    expect(find.text('pie/m1n1-5ub-1-long-name'), findsOneWidget);
    expect(find.text('pie/m1n1-5ub-2'), findsOneWidget);
    expect(find.text('9990 ссылок · 2 дн. наблюдения, живых 2 из 20'), findsOneWidget);
    expect(find.text('Добавить'), findsNWidgets(3));
    // «Добавить» у члена добавляет именно его, а не представителя
    await tester.tap(find.byKey(const Key('add-cccccccccccc')));
    await tester.pumpAndSettle();
    expect(api.calls, ['add:cccccccccccc']);
    expect(find.text('pie/m1n1-5ub-2'), findsNothing);
    expect(find.text('sevcator/m1n1-5ub-1'), findsOneWidget);
    await tester.tap(find.text('Свернуть'));
    await tester.pumpAndSettle();
    expect(find.text('pie/m1n1-5ub-1-long-name'), findsNothing);
    expect(find.text('ещё 1 похожий'), findsOneWidget);
  });

  testWidgets('«Добавить» у представителя добавляет только его, «Скрыть группу» скрывает группу целиком', (tester) async {
    final api = _Api(groups: [_groupA, _groupB]);
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await _open(tester, CandidatesPage(model: m));
    await tester.tap(find.byKey(const Key('add-aaaaaaaaaaaa')));
    await tester.pumpAndSettle();
    expect(api.calls, ['add:aaaaaaaaaaaa']);
    // представителем стал следующий член: группа осталась
    expect(find.text('sevcator/m1n1-5ub-1'), findsNothing);
    expect(find.text('pie/m1n1-5ub-1-long-name'), findsOneWidget);
    await tester.tap(find.byKey(const Key('dismiss-gA')));
    await tester.pumpAndSettle();
    expect(api.calls, contains('dismissGroup:gA'));
    expect(find.text('pie/m1n1-5ub-1-long-name'), findsNothing);
    await tester.tap(find.byKey(const Key('dismiss-gB')));
    await tester.pumpAndSettle();
    expect(find.text('Пока ничего не найдено'), findsOneWidget);
  });

  testWidgets('пустое состояние и «Искать сейчас»', (tester) async {
    final api = _Api();
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await _open(tester, CandidatesPage(model: m));
    expect(find.text('Пока ничего не найдено'), findsOneWidget);
    await tester.tap(find.byKey(const Key('scanNow')));
    await tester.pumpAndSettle();
    expect(api.calls, contains('scan'));
    expect(find.text('x/y/fresh'), findsOneWidget);
  });

  testWidgets('409 на проходе (платная сеть) — понятное сообщение, не сырой текст демона', (tester) async {
    final api = _Api(scanError: ApiException(409, 'сеть мобильная или с оплатой за трафик: скачивание не начато без согласия'));
    final m = AppModel(api, pollEvery: const Duration(hours: 1));
    await _open(tester, CandidatesPage(model: m));
    await tester.tap(find.byKey(const Key('scanNow')));
    await tester.pumpAndSettle();
    final t = m.takeToast();
    expect(t, isNotNull);
    expect(t!.$2, isTrue);
    expect(t.$1, contains('Wi-Fi'));
    expect(t.$1, isNot(contains('скачивание не начато')));
    expect(AppModel.scanConflictText('обновление уже идёт'), contains('уже идёт'));
  });

  for (final size in const [Size(360, 740), Size(1400, 900)]) {
    testWidgets('ширина ${size.width.toInt()}: без переполнения, раскрытие и кнопки доступны', (tester) async {
      final m = AppModel(_Api(groups: [_groupA, _groupB]), pollEvery: const Duration(hours: 1));
      await _open(tester, CandidatesPage(model: m), size: size);
      expect(find.text('ещё 2 похожих'), findsOneWidget);
      await tester.tap(find.text('ещё 2 похожих'));
      await tester.pumpAndSettle();
      expect(find.text('pie/m1n1-5ub-2'), findsOneWidget);
      expect(find.byKey(const Key('dismiss-gA')), findsOneWidget);
      expect(find.byKey(const Key('add-aaaaaaaaaaaa')), findsOneWidget);
      expect(tester.takeException(), isNull); // RenderFlex overflow попал бы сюда
      if (size.width > 1000) {
        // широкое окно: колонка ограничена и не растягивается на всю ширину
        expect(tester.getSize(find.byType(CandidateGroupCard).first).width, lessThanOrEqualTo(720));
      }
    });
  }

  testWidgets('страница источников: плитка «Найденные источники (N групп)» открывает экран', (tester) async {
    final m = AppModel(_Api(groups: [_groupA, _groupB]), pollEvery: const Duration(hours: 1));
    await _open(tester, SourcesPage(model: m));
    expect(find.text('Найденные источники (2 репозитория)'), findsOneWidget);
    // секции кандидатов на странице источников больше нет
    expect(find.text('sevcator/m1n1-5ub-1'), findsNothing);
    expect(find.text('Искать'), findsNothing);
    await tester.tap(find.byKey(const Key('candidatesEntry')));
    await tester.pumpAndSettle();
    expect(find.text('sevcator/m1n1-5ub-1'), findsOneWidget);
    expect(find.text('Искать'), findsOneWidget);
  });

  testWidgets('старый демон без маршрута групп — плитки нет, страница работает', (tester) async {
    final m = AppModel(_Api(failGroups: true), pollEvery: const Duration(hours: 1));
    await _open(tester, SourcesPage(model: m));
    expect(find.byKey(const Key('candidatesEntry')), findsNothing);
    expect(find.textContaining('Найденные источники'), findsNothing);
    expect(find.textContaining('alpha'), findsOneWidget);
  });

  testWidgets('наборы одного репозитория собраны под одной строкой и раскрываются', (tester) async {
    const a = CandidateDto(id: 'r1', name: 'o/repo/m1n1-5ub-1', repo: 'o/repo', links: 10000, schemes: {'vless': 10000}, daysSeen: 2, sampled: 20, sampleOk: 10);
    const b = CandidateDto(id: 'r2', name: 'o/repo/m1n1-5ub-2', repo: 'o/repo', links: 6000, schemes: {'vless': 6000}, daysSeen: 2, sampled: 20, sampleOk: 2);
    const c = CandidateDto(id: 'r3', name: 'x/other/file', repo: 'x/other', links: 100, schemes: {'ss': 100}, daysSeen: 1);
    final groups = [
      const CandidateGroupDto(id: 'g1', representative: a),
      const CandidateGroupDto(id: 'g2', representative: b),
      const CandidateGroupDto(id: 'g3', representative: c),
    ];
    final buckets = bucketByRepo(groups);
    expect(buckets.length, 2);
    expect(buckets.first.repo, 'o/repo'); // проверенный репозиторий выше непроверенного
    expect(buckets.first.links, 16000);
    expect(buckets.first.liveShare, closeTo(0.3, 1e-9));

    final m = AppModel(_Api(groups: groups), pollEvery: const Duration(hours: 1));
    await _open(tester, CandidatesPage(model: m));
    expect(find.text('o/repo'), findsOneWidget);
    expect(find.textContaining('2 набора'), findsOneWidget);
    expect(find.text('o/repo/m1n1-5ub-1'), findsNothing); // свёрнуто
    expect(find.text('x/other/file'), findsOneWidget); // одиночный репозиторий без обёртки
    await tester.tap(find.byKey(const Key('repo-toggle-o/repo')));
    await tester.pumpAndSettle();
    expect(find.text('o/repo/m1n1-5ub-1'), findsOneWidget);
    expect(find.text('o/repo/m1n1-5ub-2'), findsOneWidget);
  });

  test('склонения', () {
    String g(int n) => ruPlural(n, 'группа', 'группы', 'групп');
    expect([for (final n in [0, 1, 2, 4, 5, 11, 12, 21, 22, 25, 111]) g(n)], ['групп', 'группа', 'группы', 'группы', 'групп', 'групп', 'групп', 'группа', 'группы', 'групп', 'групп']);
  });

  test('DTO разбирают ответ демона', () {
    final c = CandidateDto.fromJson({'id': 'abc', 'name': 'n', 'links': 5, 'schemes': {'vless': 4, 'ss': 1}, 'days_seen': 2, 'sampled': 20, 'sample_ok': 7, 'sample_days': 1, 'group': 'g1', 'group_size': 3});
    expect(c.schemes, {'vless': 4, 'ss': 1});
    expect(c.sampleOk, 7);
    expect((c.group, c.groupSize), ('g1', 3));
    expect(schemesText(c.schemes), 'vless 4, ss 1');
    final g = CandidateGroupDto.fromJson({
      'id': 'g1',
      'representative': {'id': 'a', 'name': 'a'},
      'members': [
        {'id': 'b', 'name': 'b'}
      ],
      'size': 2,
      'total_links_max': 10000
    });
    expect((g.id, g.representative.id, g.members.single.id, g.size, g.totalLinksMax), ('g1', 'a', 'b', 2, 10000));
    // демон до групп: поля group нет
    expect(CandidateDto.fromJson({'id': 'x', 'name': 'x'}).groupSize, 1);
  });
}
