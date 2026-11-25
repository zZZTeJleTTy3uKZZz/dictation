import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'package:kits_ui/kits_ui.dart';

import 'dictation_ui.dart';
import 'hotkey_capture.dart';
import 'onboarding.dart';
import 'shell.dart';

/// Ошибка обновления человеку — ровно строка из §4 публичного контракта.
/// Код службы уходит в журнал и на экран не попадает.
const String updateFailureLine =
    'Не удалось проверить обновления — нет связи с GitHub';

/// Строка человеку по причине неудачной проверки обновления.
///
/// Причину даёт ядро в поле `reason`: 404 — релиз ещё не опубликован, сеть и
/// таймаут — нет связи, прочие HTTP-коды — ошибка сервера с кодом. Без причины
/// (ядро старой версии или иной сбой) остаётся прежняя строка §4.
String updateFailureText(Map<String, dynamic>? update) {
  final reason = update?['reason'] as String? ?? '';
  final status = (update?['http_status'] as num?)?.toInt() ?? 0;
  return switch (reason) {
    'not_published' => 'Обновлений пока нет: релиз ещё не опубликован',
    'server_error' =>
      status > 0
          ? 'Сервер обновлений ответил ошибкой (код $status)'
          : 'Сервер обновлений ответил ошибкой',
    _ => updateFailureLine,
  };
}

/// Связь с окном и службой диктовки.
///
/// Правил распознавания здесь нет ни одного: чем разбирать речь, решает служба
/// диктовки (GET/POST /settings), а что уже сказано и что не разобралось — она
/// же, своим /state и /retry. Окно спрашивает и пересылает.
///
/// Ядро настроек (meetdigest-core.exe) окну диктовки больше не нужно: в пакете
/// диктовки этой программы нет, и настройки распознавания служба отдаёт сама.
/// Пункт выбора в настройках: ключ для нас, название и объяснение для человека.
///
/// Форма та же, что у ответа службы (`{key,title,note}`), и окно её не толкует:
/// какие провайдеры бывают, решает Go.
class SettingChoice {
  const SettingChoice(this.key, this.title, this.note);

  factory SettingChoice.from(Map<String, dynamic> raw) => SettingChoice(
    raw['key'] as String? ?? '',
    raw['title'] as String? ?? '',
    raw['note'] as String? ?? '',
  );

  final String key;
  final String title;
  final String note;

  static List<SettingChoice> all(dynamic raw) => ((raw as List?) ?? const [])
      .whereType<Map>()
      .map((one) => SettingChoice.from(Map<String, dynamic>.from(one)))
      .toList();
}

/// Частые поля кадра: уровень микрофона, время записи и последний текст.
///
/// Они меняются несколько раз в секунду, и перестраивать из-за них страницу с
/// историей — это и есть подлагивание окна. Слушает их только тот виджет,
/// которому они нужны: значок записи в полосе окна и подсказка о копировании.
@immutable
class DictationLive {
  const DictationLive({this.level = 0, this.seconds = 0, this.transcript = ''});

  final double level;
  final int seconds;
  final String transcript;
}

class DictationClient {
  DictationClient({String? directory})
    : directory =
          directory ?? '${Platform.environment['LOCALAPPDATA']}\\Dictation';
  final String directory;
  final HttpClient client = HttpClient()
    ..connectionTimeout = const Duration(seconds: 2);

  /// Сколько ждать ответа: у каждой просьбы свой срок.
  ///
  /// У повтора свой срок: служба пробует облако несколько раз с паузами, и
  /// пяти секунд ей мало. Оборванное ожидание выглядело бы как отказ, хотя
  /// разбор ещё идёт.
  static Duration _deadline(String path) {
    if (path == '/retry') return const Duration(minutes: 3);
    return const Duration(seconds: 5);
  }

  Future<HttpClientResponse> _open(
    String path,
    Map<String, dynamic>? body, {
    String? method,
  }) async {
    // Разделитель берём у системы: в работе это Windows, а проверки запускают
    // тот же клиент на Linux и пишут endpoint.json рядом с собой.
    final endpoint = jsonDecode(
      await File('$directory${Platform.pathSeparator}endpoint.json')
          .readAsString(),
    ) as Map<String, dynamic>;
    final url = Uri.parse('${endpoint['url']}$path');
    if (url.host != '127.0.0.1') {
      throw const FormatException('Invalid local endpoint');
    }
    // METHOD задаём явно для действий без тела: служба регистрирует их как
    // POST, а старый клиент слал GET и получал «Method Not Allowed». Наличие
    // тела не должно решать, какой глагол у просьбы.
    final request = await client.openUrl(
      method ?? (body == null ? 'GET' : 'POST'),
      url,
    );
    request.headers.set(
      HttpHeaders.authorizationHeader,
      'Bearer ${endpoint['token']}',
    );
    if (body != null) {
      request.headers.contentType = ContentType.json;
      request.write(jsonEncode(body));
    }
    final response = await request.close().timeout(_deadline(path));
    if (response.statusCode != 200) {
      // Причину служба пишет словами для человека («распознать не удалось —
      // попробуйте ещё раз чуть позже»). Показывать «действие не выполнено»
      // вместо неё значило бы прятать единственное, что человеку нужно.
      throw HttpException(await utf8.decoder.bind(response).join());
    }
    return response;
  }

  Future<Map<String, dynamic>> request(
    String path, [
    Map<String, dynamic>? body,
  ]) async =>
      jsonDecode(await utf8.decoder.bind(await _open(path, body)).join())
          as Map<String, dynamic>;

  /// Действие без полей: POST с пустым JSON, а не GET.
  ///
  /// Служба регистрирует такие ручки как POST; GET на них отвечает
  /// 405 Method Not Allowed, и человек видел технический текст вместо дела.
  Future<Map<String, dynamic>> post(
    String path, [
    Map<String, dynamic> body = const <String, dynamic>{},
  ]) async => jsonDecode(
    await utf8.decoder.bind(await _open(path, body, method: 'POST')).join(),
  ) as Map<String, dynamic>;

  /// Поток состояния без истории (`/events?lite=1`).
  ///
  /// Служба шлёт снимок каждые 150 мс, и в полном виде он весит около 255 КБ на
  /// сто строк истории. Окну история в каждом кадре не нужна: она приходит
  /// отдельно, по `history_rev`. Старая служба запрос `lite` не знает и отвечает
  /// полным снимком — тогда история берётся из него, как раньше.
  Stream<Map<String, dynamic>> events() async* {
    final response = await _open('/events?lite=1', null);
    await for (final line
        in response.transform(utf8.decoder).transform(const LineSplitter())) {
      if (line.startsWith('data: ')) {
        yield jsonDecode(line.substring(6)) as Map<String, dynamic>;
      }
    }
  }

  /// История целиком и её ревизия (`GET /history`): `{"rev": n, "history":[]}`.
  ///
  /// Читается только тогда, когда `history_rev` в потоке изменился: иначе окно
  /// перестраивало бы весь список на каждый кадр записи.
  Future<Map<String, dynamic>> history() => request('/history');

  /// Состояние обновления по протоколу update/1 (`GET /update`).
  ///
  /// Окно не ведёт своё состояние обновления: скачивание, проверку суммы и
  /// установку делает служба, а окно только показывает её ответ. Иначе два
  /// состояния разошлись бы в том, что уже скачано.
  Future<Map<String, dynamic>> update() => request('/update');

  /// Действие обновления (`check`, `download`, `install`, `cancel`).
  ///
  /// Все четыре ручки службы — POST, хотя тела у них нет.
  Future<Map<String, dynamic>> updateAction(String action) =>
      post('/update/$action');

  void close() => client.close(force: true);
}

class DictationApp extends StatefulWidget {
  const DictationApp({super.key, this.client});
  final DictationClient? client;

  @override
  State<DictationApp> createState() => _DictationAppState();
}

class _DictationAppState extends State<DictationApp> {
  bool? dark;

  /// Тема кита: цвета, радиусы и шрифт берутся из токенов @kits/ui.
  ///
  /// Онбординг ещё живёт на виджетах «Линии», и его палитра берётся по
  /// яркости темы: отдельного расширения ThemeData для этого не нужно.
  ThemeData theme(Brightness brightness) => kitsTheme(brightness: brightness);

  @override
  Widget build(BuildContext context) => MaterialApp(
    title: 'Диктовка',
    debugShowCheckedModeBanner: false,
    theme: theme(Brightness.light),
    darkTheme: theme(Brightness.dark),
    themeMode: dark == null
        ? ThemeMode.system
        : dark!
        ? ThemeMode.dark
        : ThemeMode.light,
    home: DictationWindow(
      client: widget.client,
      onTheme: (value) {
        if (value != dark && mounted) setState(() => dark = value);
      },
    ),
  );
}

/// Значок записи в полосе окна: точка, время и три полоски уровня.
///
/// Подписан на `DictationLive` и перестраивает только себя — кадр микрофона не
/// трогает ни страницу, ни историю.
class _LiveBadge extends StatelessWidget {
  const _LiveBadge({required this.live, required this.recording});

  final ValueListenable<DictationLive> live;
  final bool recording;

  @override
  Widget build(BuildContext context) {
    if (!recording) return const SizedBox.shrink();
    final tokens = LineTokens.of(context);
    return ValueListenableBuilder<DictationLive>(
      valueListenable: live,
      builder: (context, value, _) {
        final level = value.level.clamp(0.0, 1.0).toDouble();
        return Row(
          mainAxisSize: MainAxisSize.min,
          children: <Widget>[
            Container(
              width: 7,
              height: 7,
              decoration: BoxDecoration(
                color: tokens.danger,
                shape: BoxShape.circle,
              ),
            ),
            const SizedBox(width: 6),
            Text(
              lineClock(value.seconds),
              style: lineText(size: 12, color: tokens.muted, tabular: true),
            ),
            const SizedBox(width: 8),
            SizedBox(
              height: 14,
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.end,
                children: <Widget>[
                  for (var i = 0; i < 3; i++)
                    Container(
                      width: 3,
                      height: 4 + level * (i == 1 ? 10 : 7),
                      margin: const EdgeInsets.only(right: 2),
                      decoration: BoxDecoration(
                        color: tokens.danger.withValues(alpha: .7),
                        borderRadius: BorderRadius.circular(1.5),
                      ),
                    ),
                ],
              ),
            ),
          ],
        );
      },
    );
  }
}

/// Сколько раз собрана строка истории.
///
/// Проверки этим следят, что кадр уровня не перестраивает список; в работе
/// счётчик ни на что не влияет.
@visibleForTesting
int historyRowBuilds = 0;

/// Промежуток под строкой истории: плашка выше него — сама строка.
const double _historyRowGap = 6;

/// Ширина колонки истории: текст записи читается в строку, а не обрезается
/// многоточием. Узкое окно колонку всё равно ужимает (ConstrainedBox).
const double _historyColumnWidth = 1100;

/// Ширина колонки настроек: там строки короче, широкая колонка их растягивала бы.
const double _settingsColumnWidth = 760;

/// Подпись строки, которую служба остановила в фоне. Это не ошибка: звук
/// сохранён, просто запись закрылась из-за тишины.
const String _autostopNote = 'Остановлено в фоне: 5 минут без голоса';

/// Срок, после которого звук готовой записи удаляется; текст остаётся. Ноль —
/// «никогда». Список тот же, что в службе (audioKeepChoices в
/// internal/localdict/audio_keep.go): служба проверяет значение и отказывает
/// чужому.
class AudioKeepChoice {
  const AudioKeepChoice(this.days, this.label);

  final int days;
  final String label;
}

const List<AudioKeepChoice> audioKeepChoices = <AudioKeepChoice>[
  AudioKeepChoice(0, 'Никогда'),
  AudioKeepChoice(1, '1 день'),
  AudioKeepChoice(7, '1 неделя'),
  AudioKeepChoice(14, '14 дней'),
  AudioKeepChoice(30, '1 месяц'),
  AudioKeepChoice(90, '3 месяца'),
];

/// Объём словами: килобайты до мегабайта, дальше мегабайты и гигабайты, с
/// запятой, как принято в русских текстах («92,8 МБ»).
String formatStorageBytes(int bytes) {
  const kilo = 1024;
  const mega = kilo * 1024;
  const giga = mega * 1024;
  String withComma(double value) =>
      value.toStringAsFixed(1).replaceAll('.', ',');
  if (bytes < mega) return '${(bytes / kilo).round()} КБ';
  if (bytes < giga) return '${withComma(bytes / mega)} МБ';
  return '${withComma(bytes / giga)} ГБ';
}

/// Слово «запись» в нужной форме: 1 запись, 2 записи, 5 записей, 11 записей.
/// Одиннадцать–четырнадцать — всегда «записей», хотя заканчиваются на 1–4.
String recordsWordRu(int count) {
  final last = count % 10;
  final tens = count % 100;
  if (last == 1 && tens != 11) return 'запись';
  if (last >= 2 && last <= 4 && (tens < 12 || tens > 14)) return 'записи';
  return 'записей';
}

const List<String> _monthsGenitive = <String>[
  'января',
  'февраля',
  'марта',
  'апреля',
  'мая',
  'июня',
  'июля',
  'августа',
  'сентября',
  'октября',
  'ноября',
  'декабря',
];

/// Дата словами: «11 ноября».
String russianDay(DateTime day) =>
    '${day.day} ${_monthsGenitive[day.month - 1]}';

/// Строка об объёме: «Занято: 92,8 МБ, 200 записей, старейшая — 11 ноября».
/// Пустое хранилище называем прямо, без пустых «0 КБ».
String storageSummary(int bytes, int count, DateTime? oldest) {
  if (count == 0) return 'Записей со звуком нет';
  return <String>[
    'Занято: ${formatStorageBytes(bytes)}',
    '$count ${recordsWordRu(count)}',
    if (oldest != null) 'старейшая — ${russianDay(oldest)}',
  ].join(', ');
}

/// Плашка записи истории фиксированной высоты. Высота задана токеном строки:
/// свёрнутая и раскрытая плашка одинаковы, поэтому закрепление не прыгает.
class _HistoryRowDelegate extends SliverPersistentHeaderDelegate {
  const _HistoryRowDelegate({required this.extent, required this.child});

  final double extent;
  final Widget child;

  @override
  double get minExtent => extent;

  @override
  double get maxExtent => extent;

  @override
  Widget build(
    BuildContext context,
    double shrinkOffset,
    bool overlapsContent,
  ) => child;

  @override
  bool shouldRebuild(covariant _HistoryRowDelegate oldDelegate) =>
      oldDelegate.extent != extent || oldDelegate.child != child;
}

class DictationWindow extends StatefulWidget {
  const DictationWindow({super.key, this.client, this.onTheme});
  final DictationClient? client;
  final ValueChanged<bool>? onTheme;

  @override
  State<DictationWindow> createState() => _DictationWindowState();
}

class _DictationWindowState extends State<DictationWindow> {
  late final client = widget.client ?? DictationClient();

  Map<String, dynamic>? state;
  Map<String, dynamic>? settings;
  String? error;
  String? settingsError;
  StreamSubscription<Map<String, dynamic>>? subscription;
  Timer? retry;
  bool acting = false;

  /// Пока служба расшифровывает запись, историю спрашивают чаще: разбор может
  /// кончиться без заметной смены ревизии, и окно держало бы «расшифровывается…»
  /// до следующей записи. Таймер один на всё окно и живёт, только пока такие
  /// строки есть.
  Timer? transcribePoll;

  /// Раскрытые строки истории: человек читает полный текст по клику, а не
  /// видит простыню в списке. Живёт, пока он не свернёт строку.
  final Set<String> opened = <String>{};

  /// Окно потеряло службу. Отдельно от ошибки действия: ошибку соединения
  /// нужно снять, как только состояние снова пришло, а «не сохранилось» —
  /// пусть человек прочитает.
  bool _offline = false;

  /// Звук записи, которую сейчас повторяют: у её кнопки своё «распознаю…».
  String? retrying;

  /// Раздел: 0 — история, 1 — настройки. Больше в окне ничего нет.
  int page = 0;

  /// Онбординг пройден в этом запуске окна: настройки службы могли обновиться
  /// ещё до нажатия «Готово», и показывать первый запуск снова незачем.
  bool _onboardingPassed = false;

  /// Итог «Проверить» в настройках: чем ответил сервис. Прокси в интерфейсе
  /// убран (решение владельца 19.11): техническую причину пишем в журнал, а
  /// человеку показываем только готовую строку `note`.
  ProviderCheck? _providerCheck;
  bool _checkingProvider = false;

  /// История хранится отдельно от состояния: в lite-потоке её нет, и окно
  /// забирает её по `history_rev`, а не перестраивает список на каждый кадр.
  List<Map<String, dynamic>> history = const <Map<String, dynamic>>[];

  /// Ревизия показанной истории. null — служба старая: она присылает историю
  /// прямо в состоянии, и окно работает с ней как раньше.
  int? historyRev;
  bool _historyLoaded = false;
  bool _historyLoading = false;
  int? _wantedRev;
  bool _historyDirty = false;
  String? historyError;

  /// Фильтр списка по статусу: all / done / error / pending. Фильтр окна, а не
  /// службы: она отдаёт всю историю, и перечитывать её ради смены взгляда
  /// значило бы спрашивать то же самое ещё раз.
  String historyFilter = 'all';

  /// Выделенные строки для массовых действий. Идентификатор тот же, по которому
  /// служба удаляет и повторяет запись; у старых строк это время.
  final Set<String> selected = <String>{};

  /// Режим выделения: строки выбираются, а не раскрываются. Один клик «Готово»
  /// в панели массовых действий возвращает обычный список.
  bool selectMode = false;

  /// Состояние обновления из лёгкого кадра: тонкая плашка истории и карточка
  /// «О программе» показывают одно и то же, а не каждая своё.
  Map<String, dynamic>? update;

  /// Пока идёт проверка, скачивание или установка, окно перечитывает /update
  /// по таймеру: поток состояния шлёт обновление редко, а проценты нужны живые.
  Timer? updatePoll;

  /// Поиск по истории ведёт окно, а не служба: она отдаёт всю историю.
  String historyQuery = '';

  /// Частые поля кадра (уровень, секунды, последний текст) — отдельно от
  /// `state`. Кадр уровня обновляет только этот Notifier, а страница и список
  /// истории остаются нетронутыми.
  final ValueNotifier<DictationLive> _live = ValueNotifier(
    const DictationLive(),
  );

  /// Снимок состояния без частых полей и истории: по нему видно, нужно ли
  /// перестраивать страницу.
  Map<String, dynamic>? _core;

  Map<String, dynamic> get config =>
      Map<String, dynamic>.from(state?['config'] ?? {});
  String get stage => state?['stage'] as String? ?? 'idle';
  bool get recording => stage == 'recording';
  bool get active => recording || stage == 'processing';

  /// Настройки распознавания, как их отдаёт служба; пусто, пока их нет.
  Map<String, dynamic> get view => settings ?? const <String, dynamic>{};

  /// Выбранный провайдер: groq или api. Пусто у службы значит
  /// умолчание (Groq); окно это не переписывает, а только показывает.
  String get speech => (view['speech'] as String?)?.trim() ?? '';

  /// Язык диктовки. Пусто у службы значит русский — так решено в Go, и окно
  /// это не переписывает, а только показывает.
  String get language {
    final value = (view['speech_language'] as String?)?.trim() ?? '';
    return value.isEmpty ? 'ru' : value;
  }

  @override
  void initState() {
    super.initState();
    _connect();
    _loadSettings();
    // Кнопка «Развернуть» следит за состоянием окна, в том числе когда систему
    // разворачивает сама (двойной клик, Win+стрелка).
    Window.listen();
  }

  /// Принять снимок состояния из потока.
  ///
  /// Кадр уровня, секунд и текста не перестраивает страницу: он живёт в
  /// `_live`, а страница обновляется только тогда, когда изменилось что-то
  /// ещё. История приходит по ревизии и никогда — вместе с кадром.
  void receive(Map<String, dynamic> fresh) {
    if (!mounted) return;
    // Свежий снимок кладём сразу, без перерисовки: частые поля нужны тем, кто
    // читает их в следующий раз, а страницу перестроит только смена остальных.
    state = fresh;
    var updateChanged = false;
    final rawUpdate = fresh['update'];
    if (rawUpdate is Map) {
      final parsed = Map<String, dynamic>.from(rawUpdate);
      if (!_sameJson(parsed, update)) {
        update = parsed;
        _logUpdateFailure(parsed);
        updateChanged = true;
        _pollUpdate();
      }
    }
    _live.value = DictationLive(
      level: (fresh['level'] as num? ?? 0).toDouble(),
      seconds: (fresh['seconds'] as num? ?? 0).toInt(),
      transcript: fresh['transcript'] as String? ?? '',
    );

    final rev = fresh['history_rev'];
    final rawHistory = fresh['history'];
    final core = _coreOf(fresh);
    final coreChanged = _core == null || !_sameJson(core, _core);
    var historyChanged = false;
    if (rev is int) {
      // Новый договор: история приходит отдельной просьбой. Ревизия могла не
      // однажды смениться, пока окно не смотрело, поэтому забираем её и по
      // первому кадру после подключения.
      if (rev != historyRev) _wantHistory(rev);
    } else if (rawHistory is List) {
      // Старая служба: истории в ревизии нет, берём её из состояния. Сравнение
      // нужно и здесь: полный снимок приходит теми же 150 мс, а история в нём
      // почти всегда одна и та же.
      final parsed = newestFirst(
        rawHistory
            .whereType<Map>()
            .map((one) => Map<String, dynamic>.from(one))
            .toList(growable: false),
      );
      if (!_sameHistory(parsed, history)) {
        history = parsed;
        historyChanged = true;
      }
    }

    if (coreChanged || _offline || updateChanged) {
      _core = core;
      setState(() {
        // Служба кладёт те же настройки в /state полем settings: пока поток
        // состояния жив, выбранный способ и язык не отстают от службы, даже
        // если отдельную просьбу окно не повторяло.
        final same = fresh['settings'];
        if (same is Map) {
          settings = Map<String, dynamic>.from(same);
        }
        if (_offline) {
          _offline = false;
          error = null;
        }
      });
    } else if (historyChanged) {
      setState(() {});
    }
    watchTranscribing();
    if (fresh['dark'] is bool) widget.onTheme?.call(fresh['dark'] as bool);
  }

  /// Пока в истории есть расшифровываемые записи, окно опрашивает службу само:
  /// ревизия истории в потоке может не смениться (старая служба о ней не знает
  /// вовсе), и «расшифровывается…» осталось бы на экране.
  void watchTranscribing() {
    final waiting = history.any(
      (entry) => _statusKind(entry) == 'transcribing',
    );
    if (!waiting) {
      transcribePoll?.cancel();
      transcribePoll = null;
      return;
    }
    transcribePoll ??= Timer.periodic(
      const Duration(seconds: 1),
      (_) => _loadHistory(),
    );
  }

  /// Снимок без полей, которые меняются каждый кадр.
  Map<String, dynamic> _coreOf(Map<String, dynamic> fresh) {
    final core = Map<String, dynamic>.from(fresh);
    for (final key in const <String>['level', 'seconds', 'transcript']) {
      core.remove(key);
    }
    core.remove('history');
    return core;
  }

  /// Сравнение состояния по значению: карты и списки из JSON приходят новыми
  /// объектами, и `==` у них ссылочное.
  bool _sameJson(dynamic left, dynamic right) {
    if (left is Map && right is Map) {
      if (left.length != right.length) return false;
      for (final key in left.keys) {
        if (!right.containsKey(key) || !_sameJson(left[key], right[key])) {
          return false;
        }
      }
      return true;
    }
    if (left is List && right is List) {
      if (left.length != right.length) return false;
      for (var i = 0; i < left.length; i++) {
        if (!_sameJson(left[i], right[i])) return false;
      }
      return true;
    }
    return left == right;
  }

  /// Сравнение истории для старой службы: поля строки, которые окно видит.
  bool _sameHistory(
    List<Map<String, dynamic>> left,
    List<Map<String, dynamic>> right,
  ) {
    if (left.length != right.length) return false;
    const fields = <String>[
      'at',
      'text',
      'seconds',
      'destination',
      'audio',
      'status',
      'attempts',
      'next_retry_at',
      'error',
    ];
    for (var i = 0; i < left.length; i++) {
      for (final field in fields) {
        if (!_sameJson(left[i][field], right[i][field])) return false;
      }
    }
    return true;
  }

  /// Новые сверху. Служба отдаёт список в этом порядке, но окно не должно от
  /// неё зависеть: старая служба и поддельная в проверке могут прислать как
  /// угодно, а история читается сверху вниз.
  List<Map<String, dynamic>> newestFirst(List<Map<String, dynamic>> entries) {
    final indexed = <(int, Map<String, dynamic>)>[];
    for (var index = 0; index < entries.length; index++) {
      indexed.add((index, entries[index]));
    }
    indexed.sort((left, right) {
      final leftAt = DateTime.tryParse(left.$2['at'] as String? ?? '');
      final rightAt = DateTime.tryParse(right.$2['at'] as String? ?? '');
      if (leftAt != null && rightAt != null && leftAt != rightAt) {
        return rightAt.compareTo(leftAt);
      }
      return left.$1.compareTo(right.$1);
    });
    return <Map<String, dynamic>>[for (final item in indexed) item.$2];
  }

  /// Запросить историю, когда ревизия сменилась.
  void _wantHistory(int rev) {
    _wantedRev = rev;
    _historyDirty = true;
    if (_historyLoading) return;
    _loadHistory();
  }

  /// `GET /history`: только по смене `history_rev`, не вместе с кадром.
  Future<void> _loadHistory() async {
    if (_historyLoading) return;
    _historyLoading = true;
    _historyDirty = false;
    try {
      final answer = await client.history();
      if (!mounted) return;
      final raw = answer['history'];
      final rev = answer['rev'];
      setState(() {
        if (raw is List) {
          history = newestFirst(
            raw
                .whereType<Map>()
                .map((one) => Map<String, dynamic>.from(one))
                .toList(growable: false),
          );
          _historyLoaded = true;
        }
        // Ответ мог отстать от потока: тогда ревизию берём из ответа, а
        // недостающую — из запрошенной, и ниже дочитаем.
        historyRev = rev is int ? rev : _wantedRev;
        _wantedRev = historyRev;
        historyError = null;
      });
    } catch (failure) {
      // История — не повод прятать всё окно: настройки и состояние остаются.
      // Ревизию запоминаем, чтобы не просить историю на каждом кадре; при
      // переходе в раздел и при следующей смене ревизии попробуем снова.
      if (mounted) {
        setState(() {
          historyError = _reason(failure);
          historyRev = _wantedRev;
        });
      }
    } finally {
      _historyLoading = false;
      // Пока читали, ревизия могла уйти вперёд — тогда читаем ещё раз, но
      // только если что-то действительно изменилось.
      final wanted = _wantedRev;
      if (mounted &&
          (_historyDirty || (wanted != null && wanted != historyRev))) {
        _loadHistory();
      }
    }
    if (mounted) watchTranscribing();
  }

  /// Причина отказа словами для человека: своя фраза — только когда сказать
  /// нечего. Служба объясняет отказ по-русски («нет такого способа
  /// распознавания: …»), и повторять её общими словами незачем.
  String _reason(Object failure, {String? path}) {
    if (failure is HttpException) {
      final said = failure.message.trim();
      if (said.isNotEmpty) {
        // Служба часто отвечает готовой русской фразой — её стоит показать как
        // есть. Техническое тело (HTML, код ответа, «Method Not Allowed»,
        //StackTrace) человеку не нужно: оно уходит только в журнал.
        final technical = RegExp(
          r'<(html|body|pre)|\b[45][0-9]{2}\b|Method Not Allowed|'
          r'Exception|StackTrace|\bdetail\s*=',
          caseSensitive: false,
        );
        if (!technical.hasMatch(said) &&
            said.contains(RegExp(r'[а-яА-ЯёЁ]')) &&
            said.length <= 300) {
          return said;
        }
      }
      debugPrint('[http] $said');
      return 'Служба диктовки не выполнила просьбу. Попробуйте ещё раз.';
    }
    if (failure is SocketException) {
      return 'Служба диктовки не отвечает. Настройки станут доступны, когда она '
          'запустится.';
    }
    if (failure is FileSystemException) {
      return 'Служба диктовки ещё не запускалась: нет файла endpoint.json.';
    }
    if (failure is TimeoutException) {
      return 'Служба диктовки не ответила вовремя.';
    }
    if (path == '/config') {
      return 'Настройка не сохранилась. Попробуйте ещё раз.';
    }
    if (path != null) {
      return 'Действие не выполнено. Проверьте состояние приложения.';
    }
    return 'Служба диктовки не ответила.';
  }

  Future<void> _connect() async {
    try {
      receive(await client.request('/state?lite=1'));
    } catch (failure) {
      if (mounted) {
        setState(() {
          error = _reason(failure);
          _offline = true;
        });
      }
      if (!mounted) return;
      final executable =
          '${File(Platform.resolvedExecutable).parent.path}\\dictation.exe';
      if (await File(executable).exists()) {
        await Process.start(executable, [], mode: ProcessStartMode.detached);
      }
    }
    if (!mounted) return;
    _listen();
  }

  void _listen() {
    subscription = client.events().listen(
      receive,
      onError: (_) => reconnect(),
      onDone: reconnect,
      cancelOnError: true,
    );
  }

  void reconnect() {
    if (!mounted || retry?.isActive == true) return;
    // Причина уже сказана при подключении: не заменяем её общим «пробую»,
    // иначе человек прочитает «нет endpoint.json» и тут же потеряет его.
    if (state == null && error == null) {
      setState(() {
        error = 'Служба диктовки не отвечает. Пробую снова…';
        _offline = true;
      });
    }
    retry = Timer(const Duration(seconds: 2), () {
      if (mounted) _listen();
    });
  }

  /// Прочитать настройки распознавания у службы (GET /settings). Зовётся при
  /// открытии окна и при переходе в настройки: настройки меняет и значок в
  /// лотке, и второе окно, а показывать запомненное значило бы врать о том,
  /// что действует сейчас.
  Future<void> _loadSettings() async {
    try {
      final fresh = await client.request('/settings');
      if (!mounted) return;
      setState(() {
        settings = fresh;
        settingsError = null;
      });
    } catch (failure) {
      if (!mounted) return;
      setState(() => settingsError = _reason(failure));
    }
  }

  /// Просьба к службе диктовки. Ответ — уже новое состояние, поэтому
  /// перерисовывать ничего не надо; `said` — что сказать тостом, когда вышло.
  Future<void> action(
    String path, [
    Map<String, dynamic> body = const {},
    String? said,
  ]) async {
    setState(() {
      acting = true;
      error = null;
    });
    try {
      receive(await client.request(path, body));
      if (said != null && mounted) lineToast(context, said);
    } catch (failure) {
      // Сырое тело ответа — только в журнал. На экран human-строка из _reason:
      // 5xx и HTML не должны превращаться в «Method Not Allowed» в окне.
      if (failure is HttpException) {
        debugPrint('[action] $path: ${failure.message}');
      }
      if (!mounted) return;
      setState(() => error = _reason(failure, path: path));
    } finally {
      if (mounted) setState(() => acting = false);
    }
  }

  /// Просьба к службе: она решает, что законно, и отвечает новыми настройками.
  ///
  /// Просьба идёт полем нового договора (`{"speech":"api"}`,
  /// `{"speech-language":"en"}`): окно называет то, что меняет, и не присылает
  /// всё состояние целиком — между открытием окна и нажатием его мог поменять
  /// кто-то ещё. Отказ показываем словами службы, а не своей догадкой.
  Future<void> _askSettings(Map<String, dynamic> fields, [String? said]) async {
    setState(() {
      acting = true;
      settingsError = null;
    });
    try {
      final fresh = await client.request('/settings', fields);
      if (!mounted) return;
      setState(() => settings = fresh);
      if (said != null && mounted) lineToast(context, said);
    } catch (failure) {
      if (mounted) setState(() => settingsError = _reason(failure));
    } finally {
      // Без сброса acting переключатель языка и выбор провайдера остаются
      // заблокированными после первой же просьбы: вернуться одним нажатием
      // (§6 контракта) уже нельзя. Проверка на это — test/language_test.dart.
      if (mounted) setState(() => acting = false);
    }
  }

  /// Срок хранения звука уходит службе вместе с остальными настройками, как и
  /// пауза музыки: служба проверяет значение и отвечает новым состоянием.
  Future<void> _askAudioKeep(int days) {
    final label = audioKeepChoices
        .firstWhere((choice) => choice.days == days)
        .label;
    return action(
      '/config',
      <String, dynamic>{...config, 'audio_keep_days': days},
      'Срок хранения звука: $label',
    );
  }

  /// «Очистить сейчас»: та же уборка, что и автоматическая, — сразу, по сроку
  /// из настроек. Объём пересчитывается заново: служба сбрасывает кэш сама.
  Future<void> _cleanAudioNow() async {
    setState(() {
      acting = true;
      error = null;
    });
    try {
      final answer = await client.post('/storage/audio-clean');
      final removed = (answer['removed'] as num?)?.toInt() ?? 0;
      final freed = (answer['freed_bytes'] as num?)?.toInt() ?? 0;
      final fresh = await client.request('/settings');
      if (!mounted) return;
      setState(() => settings = fresh);
      lineToast(
        context,
        removed == 0
            ? 'Звука старше срока нет'
            : 'Звук убран у $removed ${recordsWordRu(removed)}, '
                  'освобождено ${formatStorageBytes(freed)}',
      );
    } catch (failure) {
      // Отказ службы (например, идёт восстановление) показываем её словами.
      if (mounted) setState(() => error = _reason(failure));
    } finally {
      if (mounted) setState(() => acting = false);
    }
  }

  /// «Поделиться»: ссылка-конфигурация провайдера.
  Future<void> _shareProvider() async {
    await showDialog<void>(
      context: context,
      builder: (_) => _ShareProviderDialog(client: client),
    );
  }

  /// «Вставить ссылку»: разбирает и применяет её служба.
  Future<void> _pasteProviderLink() async {
    final fresh = await showDialog<Map<String, dynamic>>(
      context: context,
      builder: (_) => _PasteLinkDialog(client: client),
    );
    if (fresh == null || !mounted) return;
    setState(() {
      settings = fresh;
    });
    lineToast(context, 'Провайдер взят из ссылки');
  }

  /// «Проверить» рядом со способом распознавания.
  ///
  /// Ответ называет причину словами службы. Техническую часть (`detail`,
  /// HTTP-коды) пишем в журнал: на экране остаётся только человеческая строка.
  /// Прокси в интерфейсе убран — вернётся отдельным модулем.
  Future<void> _checkProvider() async {
    setState(() {
      _checkingProvider = true;
      settingsError = null;
      _providerCheck = null;
    });
    try {
      final answer = await client.request('/provider/check', <String, dynamic>{
        'speech': speech,
        'url': view['speech_url'] as String? ?? '',
        'model': view['speech_model'] as String? ?? '',
      });
      if (!mounted) return;
      final check = ProviderCheck.of(answer);
      if (check.detail.isNotEmpty) {
        debugPrint('[provider/check] ${check.detail}');
      }
      setState(() => _providerCheck = check);
      if (check.ok && mounted) lineToast(context, check.note);
    } catch (failure) {
      if (mounted) setState(() => settingsError = _reason(failure));
    } finally {
      if (mounted) {
        setState(() {
          _checkingProvider = false;
          acting = false;
        });
      }
    }
  }

  Future<void> changeHotkey() async {
    Timer? lease;
    Future<void> renewal = Future.value();
    String? failure;
    setState(() {
      acting = true;
      error = null;
    });
    try {
      receive(await client.request('/hotkey-capture', {'active': true}));
      if (!mounted) return;
      lease = Timer.periodic(const Duration(seconds: 5), (_) {
        renewal = renewal.then(
          (_) => client
              .request('/hotkey-capture', {'active': true})
              .then<void>((_) {}, onError: (_) {}),
        );
      });
      var choosing = true;
      while (choosing && mounted) {
        if (!mounted) break;
        choosing = false;
        final combination = await showDialog<String>(
          context: context,
          builder: (_) => HotkeyCaptureDialog(check: _checkHotkey),
        );
        if (combination == null || !mounted) break;
        final status = await _checkHotkey(combination);
        if (!mounted) break;
        if (status != null && status.byIme) {
          final release = await _askReleaseIME(status, combination);
          if (!mounted) break;
          if (release == true) {
            await _releaseIME(combination);
            break;
          }
          choosing = true;
          continue;
        }
        if (status != null && (status.byOther || status.pending)) {
          failure = status.note.isNotEmpty
              ? status.note
              : status.label(combination);
          choosing = true;
          continue;
        }
        try {
          receive(
            await client.request('/config', {...config, 'hotkey': combination}),
          );
        } catch (_) {
          failure = 'Это сочетание уже занято. Прежнее сочетание сохранено.';
        }
        break;
      }
    } catch (_) {
      failure = 'Не удалось начать выбор сочетания. Повторите попытку.';
    } finally {
      lease?.cancel();
      await renewal;
      try {
        final fresh = await client.request('/hotkey-capture', {
          'active': false,
        });
        if (mounted) receive(fresh);
      } catch (_) {
        failure ??=
            'Не удалось включить горячую клавишу. Повторите выбор сочетания.';
      }
      if (mounted) {
        setState(() {
          acting = false;
          error = failure;
        });
      }
    }
  }

  /// Спросить у службы, свободно ли сочетание и кто его держит.
  Future<HotkeyStatus?> _checkHotkey(String combination) async {
    try {
      return HotkeyStatus.of(
        await client.request('/hotkey-check', {'hotkey': combination}),
      );
    } catch (_) {
      return null;
    }
  }

  /// Согласие на освобождение системного переключения ввода.
  ///
  /// Клавишу у IME забираем только с прямого согласия: после этого Ctrl+Space
  /// перестаёт переключать ввод, пока Диктовка установлена.
  Future<bool?> _askReleaseIME(
    HotkeyStatus status,
    String combination,
  ) => showDialog<bool>(
    context: context,
    builder: (context) => AlertDialog(
      title: const Text('Освободить системное сочетание?'),
      content: Text(
        '$combination занято системным переключением ввода '
        '(китайский/японский IME).\n\n'
        'Освободить его для Диктовки? Прежнее сочетание можно будет вернуть '
        'в настройках или при удалении программы.',
      ),
      actions: <Widget>[
        TextButton(
          onPressed: () => Navigator.pop(context, false),
          child: const Text('Выбрать другое'),
        ),
        FilledButton(
          onPressed: () => Navigator.pop(context, true),
          child: const Text('Освободить'),
        ),
      ],
    ),
  );

  /// Освободить системное сочетание и забрать его Диктовке.
  Future<void> _releaseIME(String combination) async {
    try {
      final answer = await client.request('/hotkey-ime/release', {
        'hotkey': combination,
      });
      receive(await client.request('/state'));
      final note = (answer['note'] as String? ?? '').trim();
      if (mounted) {
        await _showHotkeyNote(
          note.isEmpty
              ? 'Системное сочетание освобождено. Ctrl+Space заработает после '
                    'выхода и входа в Windows.'
              : note,
        );
      }
    } catch (_) {
      if (mounted) {
        setState(
          () => error =
              'Не удалось освободить сочетание. Прежнее сочетание сохранено.',
        );
      }
    }
  }

  /// Вернуть системное сочетание, которое Диктовка забрала у IME.
  Future<void> restoreSystemHotkey() async {
    setState(() => acting = true);
    try {
      final answer = await client.request(
        '/hotkey-ime/restore',
        const <String, dynamic>{},
      );
      receive(await client.request('/state'));
      final note = (answer['note'] as String? ?? '').trim();
      if (mounted) {
        await _showHotkeyNote(
          note.isEmpty ? 'Системное сочетание возвращено.' : note,
        );
      }
    } catch (_) {
      if (mounted) {
        setState(() => error = 'Не удалось вернуть системное сочетание.');
      }
    } finally {
      if (mounted) setState(() => acting = false);
    }
  }

  Future<void> _showHotkeyNote(String note) => showDialog<void>(
    context: context,
    builder: (context) => AlertDialog(
      title: const Text('Сочетание клавиш'),
      content: Text(note),
      actions: <Widget>[
        FilledButton(
          onPressed: () => Navigator.pop(context),
          child: const Text('Понятно'),
        ),
      ],
    ),
  );

  @override
  void dispose() {
    retry?.cancel();
    transcribePoll?.cancel();
    updatePoll?.cancel();
    _keepCount.dispose();
    subscription?.cancel();
    _live.dispose();
    client.close();
    super.dispose();
  }

  /// Подключиться ещё раз: человек мог только что запустить службу (или
  /// закрыть её крестиком — она живёт в лотке). Пробуем оба канала сразу.
  Future<void> _reconnect() async {
    retry?.cancel();
    await subscription?.cancel();
    subscription = null;
    if (!mounted) return;
    setState(() {
      error = null;
      settingsError = null;
      _offline = false;
      // После обрыва ревизия могла уйти вперёд: историю перечитаем заново.
      _historyLoaded = false;
      _wantedRev = null;
      historyRev = null;
    });
    await _connect();
    await _loadSettings();
  }

  void _openPage(int index) {
    setState(() => page = index);
    // Настройки читаем при заходе: их меняет и значок в лотке, и второе окно.
    // Там же — состояние обновления: старая служба поля `update` в кадре не
    // шлёт, и карточка должна честно показать «проверить», а не пустоту.
    if (index == 1) {
      _loadSettings();
      _loadUpdate();
    }
    // Прошлая попытка прочитать историю не удалась — повторяем при заходе.
    if (index == 0 && !_historyLoaded && historyRev != null) {
      _wantHistory(historyRev!);
    }
  }

  /// Прочитать состояние обновления отдельной просьбой (GET /update).
  Future<void> _loadUpdate() async {
    try {
      final answer = await client.update();
      if (!mounted) return;
      _logUpdateFailure(answer);
      setState(() => update = answer);
      _pollUpdate();
    } catch (_) {
      // Старая служба ручки не знает: карточка остаётся в покое.
    }
  }

  /// Скопировать текст: буфером обмена владеет служба, окно только просит.
  Future<void> copyText(String text) =>
      action('/copy', <String, dynamic>{'text': text}, 'Скопировано');

  /// Первый запуск: провайдер не настроен вовсе.
  ///
  /// Старая служба поля `onboarding` не знает и его не присылает — тогда окно
  /// работает как раньше. Пока настройки не прочитаны, онбординг не показываем:
  /// иначе он мелькал бы на каждом открытии окна.
  bool get _showOnboarding =>
      settings != null && view['onboarding'] == true && !_onboardingPassed;

  @override
  Widget build(BuildContext context) => Scaffold(
    body: Column(
      children: <Widget>[
        _titleBar(),
        const Divider(height: 1),
        if (_showOnboarding)
          Expanded(
            child: OnboardingPage(
              client: client,
              config: config,
              initialHotkey:
                  (view['hotkey'] as String?)?.trim() ?? 'Ctrl+Space',
              onDone: _finishOnboarding,
            ),
          )
        else ...<Widget>[_nav(), Expanded(child: _page())],
      ],
    ),
  );

  /// Онбординг закончен: перечитать настройки и открыть их же.
  Future<void> _finishOnboarding() async {
    if (!mounted) return;
    setState(() {
      _onboardingPassed = true;
      page = 1;
    });
    await _loadSettings();
  }

  /// Полоса окна: перетаскивание и три кнопки. Это рамка окна, а не страница,
  /// поэтому она не переезжает при переходах между разделами.
  Widget _titleBar() => SizedBox(
    height: 44,
    child: Row(
      children: <Widget>[
        Expanded(
          child: GestureDetector(
            behavior: HitTestBehavior.opaque,
            onPanStart: (_) => Window.drag(),
            // Двойной клик по полосе, как у любого окна Windows: разворачивает
            // или возвращает размер.
            onDoubleTap: Window.maximize,
            child: Padding(
              padding: const EdgeInsets.only(left: 20),
              child: Row(
                children: <Widget>[
                  Icon(
                    Icons.graphic_eq_rounded,
                    color: LineTokens.of(context).accent,
                    size: 20,
                  ),
                  const SizedBox(width: 10),
                  const Text(
                    'Диктовка',
                    style: TextStyle(fontWeight: FontWeight.w600),
                  ),
                  const SizedBox(width: 12),
                  // Уровень и секунды приходят много раз в секунду. Их слушает
                  // только этот значок: страница под ним не перестраивается.
                  _LiveBadge(live: _live, recording: recording),
                ],
              ),
            ),
          ),
        ),
        IconButton(
          tooltip: 'Свернуть',
          onPressed: Window.minimize,
          icon: const Icon(Icons.remove_rounded, size: 18),
        ),
        ValueListenableBuilder<bool>(
          valueListenable: Window.maximized,
          builder: (context, full, _) => IconButton(
            tooltip: full ? 'Свернуть в окно' : 'Развернуть',
            onPressed: Window.maximize,
            icon: Icon(
              full ? Icons.filter_none_rounded : Icons.crop_square_rounded,
              size: full ? 13 : 15,
            ),
          ),
        ),
        IconButton(
          tooltip: 'Закрыть окно · диктовка останется в трее',
          onPressed: Window.close,
          icon: const Icon(Icons.close_rounded, size: 18),
        ),
      ],
    ),
  );

  /// Разделы окна по порядку: что уже сказано и что можно изменить. Третьего
  /// лишнего нет: «Главной» и «Доработок LLM» в окне не осталось.
  static const List<String> _navTitles = <String>['История', 'Настройки'];

  /// Полоса вкладок занимает ровно ту же ширину, что и колонка содержимого:
  /// на развёрнутом окне обе стоят по центру и не расходятся краями.
  Widget _nav() => Padding(
    padding: const EdgeInsets.symmetric(horizontal: 16),
    child: Center(
      child: ConstrainedBox(
        // Полоса повторяет колонку открытого раздела: история шире настроек.
        constraints: BoxConstraints(
          maxWidth: page == 0 ? _historyColumnWidth : _settingsColumnWidth,
        ),
        child: Padding(
          padding: const EdgeInsets.symmetric(vertical: 10),
          child: Row(
            children: <Widget>[
              for (var i = 0; i < _navTitles.length; i++)
                Expanded(child: _navItem(i)),
            ],
          ),
        ),
      ),
    ),
  );

  /// Разделы — капсулы: выбранная подкрашена акцентом в 12 %, как баннер.
  Widget _navItem(int index) {
    final tokens = LineTokens.of(context);
    final chosen = page == index;
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 3),
      child: Material(
        color: chosen
            ? tokens.accent.withValues(alpha: .12)
            : Colors.transparent,
        borderRadius: BorderRadius.circular(LineTokens.pill),
        child: InkWell(
          onTap: () => _openPage(index),
          borderRadius: BorderRadius.circular(LineTokens.pill),
          mouseCursor: SystemMouseCursors.click,
          hoverColor: Colors.transparent,
          highlightColor: Colors.transparent,
          child: SizedBox(
            height: 36,
            child: Center(
              child: Text(
                _navTitles[index],
                style: lineText(
                  size: 14,
                  weight: chosen ? FontWeight.w500 : FontWeight.w400,
                  color: chosen ? tokens.accent : tokens.muted,
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }

  /// Что показать в окне: история или настройки.
  ///
  /// История — свой ленивый список, а не Column внутри общего ListView: иначе
  /// все сто строк строились бы на каждый кадр. Настройки — обычная колонка
  /// LinePage: их немного, и прокручивать в них нечего.
  Widget _page() => page == 0 ? _historyPage() : _settingsPage();

  /// Настройки: способ разбора, запись, микрофон, язык и вставка.
  // ── настройки ────────────────────────────────────────────────────────────

  /// Настройки: способ разбора, запись, микрофон, язык и «О программе».
  Widget _settingsPage() {
    final down = state == null;
    final trouble = error ?? (state?['trouble'] as String? ?? '');
    // Причина, по которой окно пустое. Когда служба не отвечает, общее «нет
    // соединения» бесполезно: человеку нужно знать, что она не запущена, и
    // что делать дальше. Пока первая просьба ещё не ответила, «не отвечает»
    // было бы неправдой: это не отказ, а ожидание.
    final connecting = down && trouble.isEmpty && settingsError == null;
    final reason = trouble.isNotEmpty
        ? trouble
        : down
        ? settingsError ?? 'Подключаюсь к диктовке…'
        : '';
    return ListView(
      padding: const EdgeInsets.fromLTRB(16, 20, 16, 32),
      children: <Widget>[
        _column(
          Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: <Widget>[
              if (reason.isNotEmpty) ...<Widget>[
                _notice(
                  reason,
                  connecting ? KitsBadgeTone.info : KitsBadgeTone.danger,
                ),
                if (down && !connecting) ...<Widget>[
                  const SizedBox(height: 12),
                  Align(
                    alignment: Alignment.centerLeft,
                    child: KitsButton(
                      label: 'Подключиться ещё раз',
                      size: KitsButtonSize.sm,
                      disabled: acting,
                      onClick: _reconnect,
                    ),
                  ),
                ],
                const SizedBox(height: 12),
              ],
              if (down)
                const KitsEmptyState(
                  text: 'Настройки появятся, когда служба ответит.',
                  icon: Icon(Icons.tune_rounded, size: 32),
                )
              else
                _settings(),
            ],
          ),
        ),
      ],
    );
  }

  /// Сообщение в строку: в ките такого компонента нет, поэтому только токены.
  Widget _notice(String text, KitsBadgeTone tone) {
    final tokens = KitsTokens.of(context);
    final background = switch (tone) {
      KitsBadgeTone.danger => tokens.color('--ck-danger-soft'),
      KitsBadgeTone.warning => tokens.color('--ck-warning-soft'),
      KitsBadgeTone.success => tokens.color('--ck-success-soft'),
      KitsBadgeTone.info => tokens.color('--ck-info-soft'),
      _ => tokens.color('--ck-bg-second'),
    };
    final foreground = switch (tone) {
      KitsBadgeTone.danger => tokens.color('--ck-danger-strong'),
      KitsBadgeTone.warning => tokens.color('--ck-warning'),
      KitsBadgeTone.success => tokens.color('--ck-success'),
      KitsBadgeTone.info => tokens.color('--ck-info'),
      _ => tokens.color('--ck-text'),
    };
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 9),
      decoration: BoxDecoration(
        color: background,
        borderRadius: BorderRadius.circular(
          tokens.length('--ck-radius-control'),
        ),
      ),
      child: Text(
        text,
        style: KitsType.of(
          context,
          font: '--ck-font-hint',
          leading: '--ck-leading-normal',
          color: foreground,
        ),
      ),
    );
  }

  /// Раздел настроек: заголовок и плитка с содержимым.
  Widget _section(String title, List<Widget> children, {Widget? trailing}) {
    final tokens = KitsTokens.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: <Widget>[
          Padding(
            padding: const EdgeInsets.only(left: 2, bottom: 6),
            child: Row(
              children: <Widget>[
                Expanded(
                  child: Text(
                    title,
                    style: KitsType.of(
                      context,
                      font: '--ck-font-control',
                      weight: FontWeight.w600,
                      color: tokens.color('--ck-text'),
                    ),
                  ),
                ),
                if (trailing != null) trailing,
              ],
            ),
          ),
          Container(
            decoration: BoxDecoration(
              color: tokens.color('--ck-bg-second'),
              borderRadius: BorderRadius.circular(
                tokens.length('--ck-radius-card'),
              ),
            ),
            padding: const EdgeInsets.all(4),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: children,
            ),
          ),
        ],
      ),
    );
  }

  Widget _settings() {
    final hotkey = (config['hotkey'] as String? ?? 'Ctrl+Space').replaceAll(
      '+',
      ' + ',
    );
    final hotkeyNote = (state?['hotkey_note'] as String? ?? '').trim();
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: <Widget>[
        if (settingsError != null) ...<Widget>[
          _notice(
            'Настройки распознавания недоступны: ${settingsError!}',
            KitsBadgeTone.warning,
          ),
          const SizedBox(height: 12),
        ],
        _recognition(),
        _section('Управление записью', <Widget>[
          _settingsRow(
            icon: Icons.keyboard_rounded,
            title: 'Горячая клавиша',
            detail: hotkeyNote.isEmpty ? hotkey : '$hotkey · $hotkeyNote',
            trailing: KitsButton(
              label: 'Изменить',
              variant: KitsButtonVariant.quiet,
              size: KitsButtonSize.sm,
              disabled: acting || active,
              onClick: changeHotkey,
            ),
          ),
          if (state?['hotkey_released'] == true)
            _settingsRow(
              icon: Icons.restore_rounded,
              title: 'Системное сочетание',
              detail: 'Ctrl+Space освобождён для Диктовки',
              trailing: KitsButton(
                label: 'Вернуть',
                variant: KitsButtonVariant.quiet,
                size: KitsButtonSize.sm,
                disabled: acting,
                onClick: restoreSystemHotkey,
              ),
            ),
          _pauseMediaRow(),
        ]),
        _microphone(),
        _audioStorage(),
        _language(),
        _about(),
      ],
    );
  }

  /// Строка настройки на ките: значок, имя, пояснение и хвост.
  Widget _settingsRow({
    required IconData icon,
    required String title,
    String? detail,
    Widget? trailing,
  }) {
    final tokens = KitsTokens.of(context);
    return KitsListRow(
      density: KitsListRowDensity.compact,
      leading: Icon(icon, size: 18, color: tokens.color('--ck-text-secondary')),
      title: title,
      detail: detail,
      detailMultiline: true,
      // Кнопки действий стоят справа одной колонкой: на узком окне они не
      // растягивают строку и не разъезжаются по разным краям.
      trailing: trailing == null
          ? null
          : Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.end,
              children: <Widget>[trailing],
            ),
      // Хвост не сжимается: иначе короткая кнопка «Изменить» остаётся у левого
      // края свободного места, и действия разных строк не выравниваются.
      trailingShrinks: false,
    );
  }

  // Пауза играющего медиа на время диктовки. Служба старой версии поля не
  // знает и не присылает его — тогда считаем включённым, как и она сама.
  Widget _pauseMediaRow() => Padding(
    padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: <Widget>[
        KitsSwitch(
          label: 'Пауза музыки и видео',
          value: config['pause_media'] != false,
          onChanged: acting || active
              ? null
              : (value) => action(
                  '/config',
                  <String, dynamic>{...config, 'pause_media': value},
                  value
                      ? 'Музыка и видео будут вставать на паузу'
                      : 'Музыка и видео не трогаются',
                ),
        ),
        const SizedBox(height: 2),
        Text(
          config['pause_media'] == false
              ? 'Всё, что играет, продолжает играть во время диктовки.'
              : 'Пока вы диктуете, плеер встаёт на паузу и потом продолжает сам.',
          style: KitsType.of(
            context,
            font: '--ck-font-hint',
            color: KitsTokens.of(context).color('--ck-text-secondary'),
          ),
        ),
      ],
    ),
  );

  /// Микрофон: состояние словами службы и две кнопки в одной строке.
  ///
  /// Отдельного баннера с тем же самым нет: «не отвечает» и «доступен» уже
  /// стоят подписью у строки, и повторять их плиткой было бы мусором.
  Widget _microphone() {
    final found = state?['microphone'] as String? ?? 'unchecked';
    final ready = found == 'ready';
    final failed = found == 'error';
    final hint = failed
        ? 'не отвечает — проверьте, что он подключён и не занят другой программой'
        : ready
        ? 'доступен — речь будет слышно'
        : found == 'checking'
        ? 'слушаю микрофон…'
        : 'ещё не проверен';
    // Оба действия в одной строке справа; под узким окном строка уменьшается
    // целиком, а не разъезжается стопкой.
    final actions = FittedBox(
      fit: BoxFit.scaleDown,
      alignment: Alignment.centerRight,
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: <Widget>[
          _actionButton(
            'Проверить',
            acting || active ? null : () => action('/microphone'),
          ),
          const SizedBox(width: 8),
          _actionButton(
            'Проверить разрешения',
            acting ? null : () => action('/permissions'),
          ),
        ],
      ),
    );
    return _section('Микрофон', <Widget>[
      _settingsRow(
        icon: Icons.mic_none_rounded,
        title: 'Проверка микрофона',
        detail: hint,
      ),
      Padding(
        padding: const EdgeInsets.fromLTRB(12, 0, 12, 6),
        child: Align(alignment: Alignment.centerRight, child: actions),
      ),
    ]);
  }

  /// Способ распознавания: провайдеры, которых назвала служба.
  ///
  /// Хранение звука: сколько занято, через сколько дней звук уходит и кнопка
  /// «Очистить сейчас». Текст записей остаётся всегда: удаляется только звук.
  Widget _audioStorage() {
    final busy = acting || active;
    final storage = view['storage'];
    final figures = storage is Map
        ? Map<String, dynamic>.from(storage)
        : const <String, dynamic>{};
    final bytes = (figures['recordings_bytes'] as num?)?.toInt() ?? 0;
    final count = (figures['recordings_count'] as num?)?.toInt() ?? 0;
    final oldest = DateTime.tryParse(
      figures['oldest_at'] as String? ?? '',
    )?.toLocal();
    final days = (config['audio_keep_days'] as num?)?.toInt() ?? 0;
    return _section('Хранение звука', <Widget>[
      Padding(
        padding: const EdgeInsets.fromLTRB(12, 6, 12, 0),
        child: Text(storageSummary(bytes, count, oldest)),
      ),
      _settingsRow(
        icon: Icons.auto_delete_rounded,
        title: 'Удалять звук старше',
        trailing: SizedBox(
          width: 150,
          child: KitsSelect(
            sections: <KitsSelectSection>[
              KitsSelectSection(
                options: <KitsSelectOption>[
                  for (final choice in audioKeepChoices)
                    KitsSelectOption(id: '${choice.days}', label: choice.label),
                ],
              ),
            ],
            value: '$days',
            disabled: busy,
            onChanged: (value) {
              final picked = int.tryParse(value);
              if (picked != null && picked != days) _askAudioKeep(picked);
            },
          ),
        ),
      ),
      Padding(
        padding: const EdgeInsets.fromLTRB(12, 0, 12, 4),
        child: Text(
          'Текст записей остаётся, удаляется только звук.',
          style: KitsType.of(
            context,
            font: '--ck-font-hint',
            color: KitsTokens.of(context).color('--ck-text-secondary'),
          ),
        ),
      ),
      Padding(
        padding: const EdgeInsets.fromLTRB(8, 0, 8, 8),
        child: Align(
          alignment: Alignment.centerLeft,
          // «Никогда» по сроку означает, что удалять по возрасту нечего:
          // кнопка тогда не нужна и не работает.
          child: _actionButton(
            'Очистить сейчас',
            busy || days == 0 ? null : _cleanAudioNow,
          ),
        ),
      ),
    ]);
  }

  /// Своих правил у окна нет: список приходит полем `speeches` (ключ для нас,
  /// название и объяснение для человека), выбранный — полем `speech`, а строку
  /// «чем разбирается» пишет Go. Ключа провайдера здесь нет и быть не должно:
  /// он лежит в providers.json и вводится в другом окне, а в этом окне ему не
  /// место. Местной модели и сравнения способов больше нет.
  Widget _recognition() {
    final choices = SettingChoice.all(view['speeches']);
    final check = _providerCheck;
    final children = <Widget>[
      if (choices.isEmpty)
        _settingsRow(
          icon: Icons.help_outline_rounded,
          title: 'Способ распознавания пока недоступен',
          // Две разные причины не должны звучать одинаково: служба могла
          // не ответить вовсе, а могла ответить старой сборкой, где
          // выбора ещё нет. Человеку нужно знать, обновлять ли программу.
          detail: settings == null
              ? (settingsError ?? 'служба диктовки не ответила')
              : 'служба диктовки старее окна: выбора способа в ней нет',
        )
      else
        for (final one in choices) _choiceRow(one),
      if (check != null && !check.ok)
        Padding(
          padding: const EdgeInsets.fromLTRB(12, 4, 12, 8),
          child: _notice(check.note, KitsBadgeTone.danger),
        ),
      if (settings != null)
        Padding(
          padding: const EdgeInsets.fromLTRB(8, 0, 8, 8),
          // Две отдельные кнопки, а не пара тихих ссылок: это действия, а не
          // пояснение, и их должно быть видно рядом с провайдерами. Wrap даёт
          // им перенестись на узком окне, не вылезая за карточку.
          child: Wrap(
            spacing: 8,
            runSpacing: 8,
            children: <Widget>[
              _actionButton('Поделиться', acting ? null : _shareProvider),
              _actionButton(
                'Вставить ссылку',
                acting ? null : _pasteProviderLink,
              ),
            ],
          ),
        ),
    ];
    return _section(
      'Способ распознавания',
      children,
      trailing: settings == null
          ? null
          : KitsButton(
              label: _checkingProvider ? 'Проверяю…' : 'Проверить ключ',
              variant: KitsButtonVariant.quiet,
              size: KitsButtonSize.sm,
              loading: _checkingProvider,
              disabled: _checkingProvider,
              onClick: _checkProvider,
            ),
    );
  }

  /// Кнопка действия внутри плитки: тихая, но настоящая кнопка кита.
  Widget _actionButton(String label, VoidCallback? onClick) => KitsButton(
    label: label,
    variant: KitsButtonVariant.quiet,
    size: KitsButtonSize.sm,
    disabled: onClick == null,
    onClick: onClick,
  );

  /// Строка провайдера: нажимается целиком, галочка справа.
  ///
  /// Если служба присылает `providers`, в строке видно, задан ли ключ, и есть
  /// кнопка «Настроить»/«Изменить» для вписывания, замены и удаления ключа.
  Widget _choiceRow(SettingChoice one) {
    final slot = _providerSlot(one.key);
    return KitsListRow(
      density: KitsListRowDensity.compact,
      leading: Icon(_speechIcon(one.key), size: 18),
      title: one.title,
      detail: slot == null ? one.note : '${one.note} · ${_keyState(slot)}',
      detailMultiline: true,
      onTap: acting
          ? null
          : () {
              // Способ меняет только служба, и повторное нажатие ничего не
              // ломает: просьба называет поле и значение.
              if (one.key != speech) {
                _askSettings(<String, dynamic>{'speech': one.key});
              }
            },
      trailing: Row(
        mainAxisSize: MainAxisSize.min,
        children: <Widget>[
          if (slot != null) ...<Widget>[
            _actionButton(
              slot['key_set'] == true ? 'Изменить' : 'Настроить',
              acting ? null : () => _configureProvider(slot),
            ),
            const SizedBox(width: 8),
          ],
          _chosenMark(one.key == speech),
        ],
      ),
    );
  }

  /// Запись провайдера из `providers` службы. Старая служба поля не знает —
  /// тогда null, и кнопок настройки в окне нет вовсе.
  Map<String, dynamic>? _providerSlot(String key) {
    final raw = view['providers'];
    if (raw is! List) return null;
    for (final one in raw) {
      if (one is Map && one['key'] == key) {
        return Map<String, dynamic>.from(one);
      }
    }
    return null;
  }

  /// Признак ключа для человека: только «задан» или «нет», значения не видно.
  String _keyState(Map<String, dynamic> slot) =>
      slot['key_set'] == true ? 'ключ задан' : 'ключа нет';

  /// «Настроить»/«Изменить»: диалог ключа. Служба отвечает свежими настройками,
  /// окно их берёт и говорит, что сделано.
  Future<void> _configureProvider(Map<String, dynamic> slot) async {
    final result = await showDialog<Map<String, dynamic>>(
      context: context,
      builder: (_) => _ProviderKeyDialog(client: client, slot: slot),
    );
    if (result == null || !mounted) return;
    setState(() => settings = result['fresh'] as Map<String, dynamic>);
    lineToast(context, result['said'] as String? ?? '');
  }

  /// Значок провайдера по ключу службы. Незнакомый ключ — общий значок
  /// распознавания: окно не должно падать на новой сборке службы.
  IconData _speechIcon(String key) => switch (key) {
    'groq' => Icons.bolt_rounded,
    'api' => Icons.dns_outlined,
    _ => Icons.cloud_outlined,
  };

  /// Выбранный пункт — галочка; невыбранный — пустой кружок, чтобы строка
  /// оставалась нажимаемой целиком.
  Widget _chosenMark(bool chosen) {
    final tokens = KitsTokens.of(context);
    return Icon(
      chosen ? Icons.check_circle_rounded : Icons.radio_button_unchecked,
      size: 20,
      color: chosen
          ? tokens.color('--ck-accent-fill')
          : tokens.color('--ck-border'),
    );
  }

  /// Язык диктовки. Служба хранит его строкой и не перечисляет варианты: язык —
  /// не список режимов, и выдумывать за него нечего. Пусто значит русский.
  Widget _language() {
    final current = language;
    // Переключатель, а не выпадающий список: вернуться к прежнему языку можно
    // одним нажатием. Незнакомый код службы показываем тем же сегментом, чтобы
    // он не пропал из вида и его можно было сменить в один клик.
    final codes = <String>[
      'ru',
      'en',
      if (current != 'ru' && current != 'en') current,
    ];
    return _section('Язык диктовки', <Widget>[
      Padding(
        padding: const EdgeInsets.fromLTRB(12, 6, 12, 8),
        // Обычная строка настроек: значок и переключатель идут от левого края,
        // а не по центру плитки. Фиксированная ширина не даёт сегментам
        // растянуться на всю строку и центрироваться внутри неё.
        child: Row(
          children: <Widget>[
            Icon(
              Icons.translate_rounded,
              size: 18,
              color: KitsTokens.of(context).color('--ck-text-secondary'),
            ),
            const SizedBox(width: 10),
            SizedBox(
              // Ширина по содержимому: «Русский» и «English» читаются целиком,
              // а не «Русск…». Незнакомый код службы получает своё место.
              width: (codes.length * 96 + 24).toDouble(),
              // Смена не трогает текущую фразу: служба держит язык, с которого
              // она началась, и применяет новый к следующей.
              child: KitsSegmentedControl(
                segments: <KitsSegment>[
                  for (final code in codes)
                    KitsSegment(id: code, label: _languageName(code)),
                ],
                value: current,
                label: 'Язык распознавания',
                size: KitsSegmentSize.sm,
                disabled: acting || settings == null,
                onChanged: (value) {
                  if (value != current) {
                    _askSettings(<String, dynamic>{'speech-language': value});
                  }
                },
              ),
            ),
            const SizedBox(width: 10),
            Flexible(
              child: Text(
                'со следующей фразы',
                overflow: TextOverflow.ellipsis,
                style: KitsType.of(
                  context,
                  font: '--ck-font-micro',
                  color: KitsTokens.of(context).color('--ck-text-meta'),
                ),
              ),
            ),
          ],
        ),
      ),
    ]);
  }

  /// Язык словами. Незнакомый код показывается как есть: окно не должно
  /// выдавать «Русский» за значение, которого не знает.
  String _languageName(String code) => switch (code) {
    'ru' => 'Русский',
    'en' => 'English',
    _ => code,
  };

  // ── обновление ───────────────────────────────────────────────────────────

  /// «О программе»: версия и карточка обновления протокола update/1.
  ///
  /// Установку делает служба, а не окно: она ждёт конца диктовки и запускает
  /// установщик скрытым окном. Поэтому карточка читает состояние службы
  /// (GET /update) и зовёт её действия, а не kits_update: своё состояние в
  /// окне разошлось бы с тем, что уже скачано и поставлено.
  Widget _about() {
    final version = (state?['version'] as String? ?? '').trim();
    final status = _updateStatus();
    final failed = status == KitsUpdateStatus.failed;
    final updateKnown = update != null;
    return _section('О программе', <Widget>[
      _settingsRow(
        icon: Icons.info_outline_rounded,
        title: version.isEmpty ? 'Версия' : 'Версия $version',
        detail:
            'Обновления проверяются службой при запуске и раз в шесть часов.',
      ),
      if (failed) ...<Widget>[
        // §4 контракта: одна строка человеку, код — только в журнал. Карточка
        // кита в failed печатает «Код: …», поэтому на этом состояние её нет.
        Padding(
          padding: const EdgeInsets.fromLTRB(12, 0, 12, 4),
          child: _notice(updateFailureText(update), KitsBadgeTone.danger),
        ),
        Align(
          alignment: Alignment.centerRight,
          child: KitsButton(
            key: const Key('update-retry'),
            label: 'Повторить',
            size: KitsButtonSize.sm,
            onClick: _retryUpdate,
          ),
        ),
      ] else
        Padding(
          padding: const EdgeInsets.fromLTRB(4, 0, 4, 4),
          child: KitsUpdateCard(
            key: const ValueKey<String>('about-update'),
            status: status,
            version: update?['version'] as String?,
            notes: update?['notes'] as String?,
            received: (update?['received'] as num?)?.toInt(),
            total: (update?['total'] as num?)?.toInt(),
            mandatory: update?['mandatory'] == true,
            onCheck: () => _updateAction('check'),
            onDownload: () => _updateAction('download'),
            onCancel: () => _updateAction('cancel'),
            onInstall: () => _updateAction('install'),
            onRetry: _retryUpdate,
          ),
        ),
      if (update?['deferred'] == true)
        Padding(
          padding: const EdgeInsets.fromLTRB(12, 0, 12, 8),
          child: _notice(
            (update?['message'] as String? ?? '').isEmpty
                ? 'Идёт запись: установка начнётся после её конца.'
                : update!['message'] as String,
            KitsBadgeTone.warning,
          ),
        ),
      if (!updateKnown)
        Padding(
          padding: const EdgeInsets.fromLTRB(12, 0, 12, 8),
          child: Text(
            'Служба ещё не сказала о обновлении.',
            style: KitsType.of(
              context,
              font: '--ck-font-micro',
              color: KitsTokens.of(context).color('--ck-text-meta'),
            ),
          ),
        ),
    ]);
  }

  /// Состояние службы в состояние карточки. Незнакомое (старая служба) —
  /// покой: карточка предложит проверить, а не притворится готовой.
  KitsUpdateStatus _updateStatus() => switch (update?['status'] as String?) {
    'checking' => KitsUpdateStatus.checking,
    'up_to_date' => KitsUpdateStatus.upToDate,
    'available' => KitsUpdateStatus.available,
    'downloading' => KitsUpdateStatus.downloading,
    'ready' => KitsUpdateStatus.ready,
    'installing' => KitsUpdateStatus.installing,
    'failed' => KitsUpdateStatus.failed,
    _ => KitsUpdateStatus.idle,
  };

  /// Код и техническое сообщение службы уходят в журнал, а не на экран.
  void _logUpdateFailure(Map<String, dynamic>? fresh) {
    if (fresh == null || fresh['status'] != 'failed') return;
    debugPrint(
      '[update] code=${fresh['code'] ?? ''} message=${fresh['message'] ?? ''}',
    );
  }

  /// Действие обновления: ответ службы сразу становится состоянием карточки;
  /// пока оно длится, окно перечитывает /update по таймеру.
  Future<void> _updateAction(String action) async {
    setState(() {
      acting = true;
      error = null;
    });
    try {
      final answer = await client.updateAction(action);
      if (!mounted) return;
      _logUpdateFailure(answer);
      setState(() => update = answer);
      _pollUpdate();
    } catch (failure) {
      if (mounted) setState(() => error = _reason(failure));
    } finally {
      if (mounted) setState(() => acting = false);
    }
  }

  /// Повтор последнего шага: ошибку установки повторяет установка, ошибку
  /// скачивания — скачивание, прочую — проверка манифеста.
  void _retryUpdate() {
    final code = update?['code'] as String?;
    if (code == 'install_failed' && update?['status'] == 'ready') {
      _updateAction('install');
    } else if (code == 'download_failed' ||
        code == 'checksum_mismatch' ||
        code == 'size_mismatch') {
      _updateAction('download');
    } else {
      _updateAction('check');
    }
  }

  /// Перечитывать /update, пока идёт работа: поток состояния шлёт обновление
  /// редко, а проценты и переходы состояний нужны живые.
  void _pollUpdate() {
    updatePoll?.cancel();
    final status = update?['status'];
    if (status != 'checking' &&
        status != 'downloading' &&
        status != 'installing') {
      return;
    }
    updatePoll = Timer(const Duration(milliseconds: 700), () async {
      if (!mounted) return;
      try {
        final fresh = await client.update();
        if (!mounted) return;
        _logUpdateFailure(fresh);
        setState(() => update = fresh);
        _pollUpdate();
      } catch (_) {
        // Служба не ответила — следующий кадр состояния всё равно придёт.
      }
    });
  }

  // ── история ──────────────────────────────────────────────────────────────

  /// Страница истории: шапка, плашка обновления и тулбар кита — первые куски
  /// `CustomScrollView`, за ними записи.
  ///
  /// Компромисс: в отличие от прежнего ленивого `ListView.builder`, все записи
  /// здесь строятся сразу. Лениво строить группы слайверов с закреплением
  /// штатными средствами нельзя (SliverList принимает только боксы), поэтому
  /// на сотню записей платим лишними построениями при перестроении истории, а
  /// не при каждом кадре: кадры уровня записи истории не трогают. Если список
  /// начнёт тормозить, переходить на кастомный sliver.
  ///
  /// Каждая запись — своя группа слайверов. Раскрытая запись закрепляет плашку
  /// вверху области прокрутки, пока открыт её текст: свернуть можно в любой
  /// момент, не возвращаясь к началу. Закрепление живёт внутри группы, поэтому
  /// следующая запись сдвигает предыдущую, как заголовки таблиц.
  Widget _historyPage() {
    final shown = _shownHistory();
    final head = <Widget>[
      _historyHeader(),
      if (_updateBanner() != null) _updateBanner()!,
      const SizedBox(height: 10),
      _toolbar(),
      if (_bulkBar() != null) _bulkBar()!,
      const SizedBox(height: 6),
    ];
    return CustomScrollView(
      slivers: <Widget>[
        const SliverToBoxAdapter(child: SizedBox(height: 20)),
        for (final part in head) _pageBox(part),
        if (shown.isEmpty)
          _pageBox(
            history.isEmpty
                ? const KitsEmptyState(
                    text:
                        'Записей пока нет. Продиктуйте — сочетание клавиш '
                        'работает и при закрытом окне.',
                    icon: Icon(Icons.history_rounded, size: 32),
                  )
                : KitsEmptyState.nothingFound(
                    text: historyFilter == 'error'
                        ? 'Ошибок нет: все диктовки распознаны.'
                        : 'Ничего не нашлось по запросу или фильтру.',
                  ),
          ),
        for (final (index, entry) in shown.indexed)
          _historySliver(index, entry),
        const SliverToBoxAdapter(child: SizedBox(height: 32)),
      ],
    );
  }

  /// Один кусок страницы истории: по бокам 16, содержимое в колонке окна.
  Widget _pageBox(Widget child) => SliverToBoxAdapter(
    child: Padding(
      padding: const EdgeInsets.symmetric(horizontal: 16),
      child: _column(child, maxWidth: _historyColumnWidth),
    ),
  );

  /// Запись истории: закрепляемая плашка и, если раскрыта, текст под ней.
  ///
  /// Плашка всегда `pinned: true`: у свёрнутой записи в группе только плашка,
  /// закреплять нечего — она не залипает над соседями. Раскрытие лишь добавляет
  /// тело в группу. Тип слайвера не меняется, поэтому элемент плашки и её
  /// состояние (фокус, наведение) сохраняются при клике на «Развернуть».
  ///
  /// Высота плашки растёт вместе с масштабом текста системы: при крупном шрифте
  /// фиксированная высота обрезала бы строку и давала переполнение.
  Widget _historySliver(int index, Map<String, dynamic> entry) {
    final tokens = KitsTokens.of(context);
    final expanded = opened.contains(_historyId(entry)) && !selectMode;
    final base = tokens.length('--ck-row-regular');
    final scaled = MediaQuery.textScalerOf(context).scale(base);
    final rowHeight = scaled > base ? scaled : base;
    return SliverMainAxisGroup(
      key: ValueKey<String>('history-$index-${entry['at']}-${entry['audio']}'),
      slivers: <Widget>[
        SliverPersistentHeader(
          pinned: true,
          delegate: _HistoryRowDelegate(
            extent: rowHeight + _historyRowGap,
            // Непрозрачный фон: текст прокручиваемой записи не просвечивает
            // сквозь закреплённую плашку.
            child: ColoredBox(
              color: tokens.color('--ck-bg'),
              child: Padding(
                padding: const EdgeInsets.symmetric(horizontal: 16),
                child: _column(
                  SizedBox(
                    height: rowHeight + _historyRowGap,
                    child: Padding(
                      padding: const EdgeInsets.only(bottom: _historyRowGap),
                      child: _historyRow(entry),
                    ),
                  ),
                  maxWidth: _historyColumnWidth,
                ),
              ),
            ),
          ),
        ),
        if (expanded)
          _pageBox(
            Padding(
              padding: const EdgeInsets.only(bottom: _historyRowGap),
              child: _historyBody(entry),
            ),
          ),
      ],
    );
  }

  /// Содержимое в колонке окна: на широком экране оно по центру и не тянется.
  Widget _column(Widget child, {double maxWidth = _settingsColumnWidth}) =>
      Center(
        child: ConstrainedBox(
          constraints: BoxConstraints(maxWidth: maxWidth),
          child: child,
        ),
      );

  /// Шапка истории: название и то, что мешает истории читаться.
  ///
  /// «Очистить» отдельной кнопки больше нет: решение об удалении живёт в
  /// «Подчистить» одним действием с вариантами.
  Widget _historyHeader() {
    final tokens = KitsTokens.of(context);
    final trouble = error ?? (state?['trouble'] as String? ?? '');
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: <Widget>[
        Text(
          'История',
          style: KitsType.of(
            context,
            font: '--ck-font-page',
            weight: FontWeight.w600,
            color: tokens.color('--ck-text'),
          ),
        ),
        if (trouble.isNotEmpty) ...<Widget>[
          const SizedBox(height: 8),
          _notice(trouble, KitsBadgeTone.danger),
          if (state == null) ...<Widget>[
            const SizedBox(height: 8),
            Align(
              alignment: Alignment.centerLeft,
              child: KitsButton(
                label: 'Подключиться ещё раз',
                size: KitsButtonSize.sm,
                disabled: acting,
                onClick: _reconnect,
              ),
            ),
          ],
        ] else if (state == null) ...<Widget>[
          const SizedBox(height: 8),
          _notice('Подключаюсь к диктовке…', KitsBadgeTone.info),
        ],
        if (historyError != null) ...<Widget>[
          const SizedBox(height: 8),
          _notice(
            'Историю не удалось прочитать: $historyError',
            KitsBadgeTone.warning,
          ),
        ],
      ],
    );
  }

  /// Плашка обновления: тонкая строка над списком, когда версия уже есть.
  ///
  /// Подробности и кнопки — в «Настройки → О программе»; здесь только «есть
  /// новее» и переход туда, иначе история перестала бы быть историей.
  Widget? _updateBanner() {
    final status = update?['status'] as String? ?? '';
    if (status != 'available' &&
        status != 'downloading' &&
        status != 'ready' &&
        status != 'installing') {
      return null;
    }
    final tokens = KitsTokens.of(context);
    final version = (update?['version'] as String? ?? '').trim();
    final text = switch (status) {
      'available' => 'Доступна версия ${version.isEmpty ? 'новее' : version}',
      'downloading' =>
        'Скачиваю обновление ${KitsUpdateCard.percentOf((update?['received'] as num?)?.toInt(), (update?['total'] as num?)?.toInt())}%',
      'ready' =>
        'Обновление ${version.isEmpty ? '' : '$version '}готово к установке',
      _ => 'Устанавливаю обновление…',
    };
    return Container(
      margin: const EdgeInsets.only(top: 10),
      padding: const EdgeInsets.fromLTRB(12, 6, 6, 6),
      decoration: BoxDecoration(
        color: tokens.color('--ck-accent-soft'),
        borderRadius: BorderRadius.circular(tokens.length('--ck-radius-row')),
      ),
      child: Row(
        children: <Widget>[
          Icon(
            Icons.system_update_alt_rounded,
            size: 16,
            color: tokens.color('--ck-accent-strong'),
          ),
          const SizedBox(width: 8),
          Expanded(
            child: Text(
              text,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: KitsType.of(
                context,
                font: '--ck-font-hint',
                color: tokens.color('--ck-text'),
              ),
            ),
          ),
          KitsButton(
            label: 'Открыть',
            variant: KitsButtonVariant.quiet,
            size: KitsButtonSize.sm,
            onClick: () => _openPage(1),
          ),
        ],
      ),
    );
  }

  /// Тулбар занимает всю ширину колонки: поиск, фильтры и действия стоят одной
  /// строкой над списком, как в «Линии». Дополнительного сужения до 620 здесь
  /// нет — оно включало в ките узкий режим и складывало полосу стопкой.
  Widget _toolbar() => _historyFilters();

  /// Полоса истории: поиск, фильтр по статусу, выделение и подчистка.
  ///
  /// Счётчики фильтра считает окно по уже прочитанной истории: служба отдаёт
  /// весь список, и перечитывать его ради смены взгляда значило бы спрашивать
  /// одно и то же.
  String _suffix(int? count) => count == null || count == 0 ? '' : ' $count';

  Widget _historyFilters() {
    final counts = <String, int>{};
    for (final entry in history) {
      final status = _entryStatus(entry);
      counts[status] = (counts[status] ?? 0) + 1;
    }
    int? countOf(String status) {
      final n = counts[status] ?? 0;
      return n > 0 ? n : null;
    }

    return KitsListToolbar(
      // Боковые отступы полосы минимальны: при 680 точках она всё равно должна
      // оставаться одной строкой, а не складываться в ките стопкой.
      padding: const EdgeInsets.symmetric(horizontal: 4, vertical: 8),
      searchValue: historyQuery,
      searchPlaceholder: 'Поиск по истории',
      onSearchChange: (value) => setState(() => historyQuery = value),
      // Фильтр — одна кнопка с перечнем, как в «Линии»: четыре всегда видимых
      // сегмента не оставляют места поиску и действиям на узком окне.
      extra: SizedBox(
        width: 124,
        child: KitsSelect(
          sections: <KitsSelectSection>[
            KitsSelectSection(
              options: <KitsSelectOption>[
                // «Все» считает всю историю, и остановки в фоне тоже: они
                // в «Готово» и «Ошибка» не попадают.
                KitsSelectOption(
                  id: 'all',
                  label: 'Все${_suffix(history.length)}',
                ),
                KitsSelectOption(
                  id: 'done',
                  label: 'Готово${_suffix(countOf('done'))}',
                ),
                KitsSelectOption(
                  id: 'error',
                  label: 'Ошибка${_suffix(countOf('error'))}',
                ),
                KitsSelectOption(
                  id: 'pending',
                  label: 'Буфер${_suffix(countOf('pending'))}',
                ),
              ],
            ),
          ],
          value: historyFilter,
          placeholder: 'Фильтры',
          onChanged: (value) => setState(() => historyFilter = value),
        ),
      ),
      actions: <Widget>[
        KitsButton(
          label: selectMode ? 'Готово' : 'Выбрать',
          variant: KitsButtonVariant.quiet,
          size: KitsButtonSize.sm,
          onClick: () => setState(() {
            selectMode = !selectMode;
            if (!selectMode) selected.clear();
          }),
        ),
        if (historyFilter == 'error' && countOf('error') != null)
          KitsButton(
            label: 'Повторить все',
            variant: KitsButtonVariant.secondary,
            size: KitsButtonSize.sm,
            disabled: acting,
            onClick: _retryFailed,
          ),
        KitsButton(
          label: 'Подчистить',
          variant: KitsButtonVariant.secondary,
          size: KitsButtonSize.sm,
          disabled: acting || history.isEmpty,
          onClick: _cleanupHistory,
        ),
      ],
    );
  }

  /// Панель массовых действий над выделенными строками.
  ///
  /// Появляется в один клик и уходит им же: список history не становится
  /// бесконечным набором чекбоксов, которые мешают читать записи.
  Widget? _bulkBar() {
    if (!selectMode) return null;
    final tokens = KitsTokens.of(context);
    final chosen = selected.isNotEmpty;
    return Container(
      margin: const EdgeInsets.only(bottom: 8),
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 6),
      decoration: BoxDecoration(
        color: tokens.color('--ck-accent-soft'),
        borderRadius: BorderRadius.circular(tokens.length('--ck-radius-row')),
      ),
      child: Wrap(
        spacing: 6,
        runSpacing: 6,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: <Widget>[
          Text(
            chosen ? 'Выбрано: ${selected.length}' : 'Отметьте записи',
            style: KitsType.of(
              context,
              font: '--ck-font-hint',
              color: tokens.color('--ck-text'),
            ),
          ),
          KitsButton(
            label: 'Копировать',
            size: KitsButtonSize.sm,
            variant: KitsButtonVariant.quiet,
            disabled: !chosen,
            onClick: _bulkCopy,
          ),
          KitsButton(
            label: 'Распознать заново',
            size: KitsButtonSize.sm,
            variant: KitsButtonVariant.quiet,
            disabled: !chosen,
            onClick: _bulkRetranscribe,
          ),
          KitsButton(
            label: 'Удалить',
            size: KitsButtonSize.sm,
            variant: KitsButtonVariant.quiet,
            disabled: !chosen || acting,
            onClick: _bulkDelete,
          ),
        ],
      ),
    );
  }

  /// Статус строки в словаре окна: один на весь список и на фильтр.
  String _entryStatus(Map<String, dynamic> entry) =>
      switch (_statusKind(entry)) {
        'error' => 'error',
        'retrying' || 'transcribing' => 'pending',
        // Остановка в фоне — не готово и не ошибка: в «Готово» и «Ошибка» не
        // попадает, видна только во «Все».
        'autostop' => 'autostop',
        _ => _failedEntry(entry) ? 'error' : 'done',
      };

  void _toggleSelected(String id) => setState(() {
    if (!selected.remove(id)) selected.add(id);
  });

  /// Скопировать тексты всех выделенных записей одной строкой.
  Future<void> _bulkCopy() async {
    final texts = history
        .where((entry) => selected.contains(_historyId(entry)))
        .map((entry) => (entry['text'] as String? ?? '').trim())
        .where((text) => text.isNotEmpty)
        .toList();
    if (texts.isEmpty) {
      lineToast(context, 'В выделенных записях нет текста');
      return;
    }
    await copyText(texts.join('\n\n'));
    if (!mounted) return;
    lineToast(context, 'Скопировано записей: ${texts.length}');
  }

  /// Массовый повтор: служба разбирает сохранённый звук каждой записи.
  Future<void> _bulkRetranscribe() async {
    final entries = _selectedEntries();
    if (entries.isEmpty) return;
    setState(() {
      acting = true;
      error = null;
    });
    var queued = 0;
    try {
      for (final entry in entries) {
        final audio = (entry['audio'] as String? ?? '').trim();
        if (audio.isEmpty || _statusKind(entry) == 'transcribing') continue;
        await client.request('/history/retranscribe', {
          'id': _historyId(entry),
        });
        queued++;
      }
      _reloadHistory();
      if (mounted) {
        lineToast(context, 'В очереди на повтор: $queued');
      }
    } catch (failure) {
      if (mounted) setState(() => error = _reason(failure));
    } finally {
      if (mounted) setState(() => acting = false);
    }
  }

  /// Массовое удаление с опасным подтверждением: звук уходит вместе с текстом.
  Future<void> _bulkDelete() async {
    final entries = _selectedEntries();
    if (entries.isEmpty) return;
    final approved = await askDanger(
      context,
      title: 'Удалить выбранные записи?',
      consequences: <String>[
        'Записи исчезнут из истории.',
        'Звук выбранных записей тоже удалится с диска.',
      ],
      confirmLabel: 'Удалить',
    );
    if (approved != true || !mounted) return;
    setState(() {
      acting = true;
      error = null;
    });
    var removed = 0;
    try {
      for (final entry in entries) {
        await client.request('/history/delete', {'id': _historyId(entry)});
        removed++;
      }
      selected.clear();
      selectMode = false;
      _reloadHistory();
      if (mounted) lineToast(context, 'Удалено записей: $removed');
    } catch (failure) {
      if (mounted) setState(() => error = _reason(failure));
    } finally {
      if (mounted) setState(() => acting = false);
    }
  }

  List<Map<String, dynamic>> _selectedEntries() => history
      .where((entry) => selected.contains(_historyId(entry)))
      .toList(growable: false);

  /// Записи, которые видит человек при нынешнем фильтре и поиске.
  List<Map<String, dynamic>> _shownHistory() {
    final query = historyQuery.trim().toLowerCase();
    return history
        .where(
          (entry) =>
              historyFilter == 'all' || _entryStatus(entry) == historyFilter,
        )
        .where((entry) {
          if (query.isEmpty) return true;
          final text = (entry['text'] as String? ?? '').toLowerCase();
          return text.contains(query);
        })
        .toList(growable: false);
  }

  /// Неудачная ли строка. `error` и `retrying` — прямой ответ службы; старые
  /// записи без статуса узнаются по `destination: failed` и пустому тексту со
  /// звуком, как их и показывала прежняя версия окна.
  ///
  /// `autostop` сюда намеренно не входит: остановка в фоне — не ошибка, и
  /// «повторить все неудачные» её не трогает.
  bool _failedEntry(Map<String, dynamic> entry) {
    final kind = _statusKind(entry);
    if (kind == 'error' || kind == 'retrying') return true;
    if (kind != null) return false;
    final text = (entry['text'] as String? ?? '').trim();
    final audio = (entry['audio'] as String? ?? '').trim();
    return entry['destination'] == 'failed' ||
        (text.isEmpty && audio.isNotEmpty);
  }

  /// «Повторить все неудачные»: служба ставит в очередь все строки с ошибкой и
  /// звуком, а строкам без звука оставляет причину пропуска.
  Future<void> _retryFailed() async {
    setState(() {
      acting = true;
      error = null;
    });
    try {
      final answer = await client.post('/history/retry-failed');
      final queued = (answer['queued'] as num? ?? 0).toInt();
      final skipped = (answer['skipped'] as num? ?? 0).toInt();
      _reloadHistory();
      if (mounted) {
        lineToast(
          context,
          skipped > 0
              ? 'В очереди: $queued. Без звука пропущено: $skipped.'
              : 'В очереди на повтор: $queued.',
          tone: queued > 0 ? LineTone.success : LineTone.warning,
        );
      }
    } catch (failure) {
      if (mounted) setState(() => error = _reason(failure));
    } finally {
      if (mounted) setState(() => acting = false);
    }
  }

  /// Перечитать историю после просьбы, которая меняет её не через `/state`.
  void _reloadHistory() {
    _historyLoaded = false;
    final rev = historyRev ?? _wantedRev;
    if (rev != null) {
      _wantHistory(rev);
    } else {
      _loadHistory();
    }
  }

  /// «Подчистить»: старше 7 или 30 дней, оставить последние N — или всё.
  ///
  /// Варианты называет окно, а решение об удалении звука принимает служба:
  /// у неё история, журналы и пути. Окно присылает только условие. Подтверждение
  /// опасное (askDanger): убранные записи и звук не вернуть.
  Future<void> _cleanupHistory() async {
    final choice = await _askCleanupKind();
    if (choice == null || !mounted) return;
    if (choice == 'all') {
      final approved = await askDanger(
        context,
        title: 'Очистить историю?',
        consequences: <String>[
          'Сохранённые тексты будут удалены с этого устройства.',
          'Записи звука, если они есть, останутся в папке диктовки.',
        ],
        confirmLabel: 'Очистить',
      );
      if (approved != true || !mounted) return;
      await action('/clear-history', const {}, 'История очищена');
      return;
    }
    final Map<String, dynamic>? body;
    if (choice == 'older7') {
      body = <String, dynamic>{'older_than_days': 7};
    } else if (choice == 'older30') {
      body = <String, dynamic>{'older_than_days': 30};
    } else {
      final count = int.tryParse(_keepCount.text.trim());
      if (count == null || count <= 0) {
        if (mounted) {
          lineToast(
            context,
            'Сколько записей оставить? Нужно число больше нуля.',
            tone: LineTone.warning,
          );
        }
        return;
      }
      body = <String, dynamic>{'keep_last': count};
    }
    final approved = await askDanger(
      context,
      title: 'Подчистить историю?',
      text: switch (choice) {
        'older7' => 'Удалить записи старше 7 дней',
        'older30' => 'Удалить записи старше 30 дней',
        _ => 'Оставить последние ${_keepCount.text.trim()} записей',
      },
      consequences: <String>[
        'Убранные записи и их звук удалятся с диска.',
        'Восстановление их не вернёт.',
      ],
      confirmLabel: 'Подчистить',
    );
    if (approved != true || !mounted) return;
    setState(() {
      acting = true;
      error = null;
    });
    try {
      final answer = await client.request('/history/cleanup', body);
      final removed = (answer['removed'] as num? ?? 0).toInt();
      _reloadHistory();
      if (mounted) {
        lineToast(context, 'Убрано записей: $removed.', tone: LineTone.success);
      }
    } catch (failure) {
      if (mounted) setState(() => error = _reason(failure));
    } finally {
      if (mounted) setState(() => acting = false);
    }
  }

  /// Сколько записей оставить при подчистке; поле живёт между открытиями.
  final _keepCount = TextEditingController(text: '50');

  /// Выбор варианта подчистки: возвращает id варианта или null.
  Future<String?> _askCleanupKind() {
    var choice = 'older7';
    return showDialog<String>(
      context: context,
      builder: (context) => StatefulBuilder(
        builder: (context, setLocal) => AlertDialog(
          title: const Text('Подчистить историю'),
          content: SingleChildScrollView(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: <Widget>[
                Wrap(
                  spacing: 8,
                  runSpacing: 8,
                  children: <Widget>[
                    for (final one in <(String, String)>[
                      ('older7', 'Старше 7 дней'),
                      ('older30', 'Старше 30 дней'),
                      ('keep', 'Оставить последние'),
                      ('all', 'Всё'),
                    ])
                      KitsChip(
                        label: one.$2,
                        selected: choice == one.$1,
                        onClick: () => setLocal(() => choice = one.$1),
                      ),
                  ],
                ),
                const SizedBox(height: 12),
                KitsTextField(
                  label: 'Сколько записей оставить',
                  controller: _keepCount,
                  disabled: choice != 'keep',
                  placeholder: 'например, 50',
                  variant: KitsFieldVariant.field,
                ),
              ],
            ),
          ),
          actions: <Widget>[
            KitsButton(
              label: 'Отменить',
              variant: KitsButtonVariant.quiet,
              onClick: () => Navigator.pop(context),
            ),
            KitsButton(
              label: choice == 'all' ? 'Очистить' : 'Подчистить',
              variant: KitsButtonVariant.primary,
              onClick: () => Navigator.pop(context, choice),
            ),
          ],
        ),
      ),
    );
  }

  /// Имя записи, по которому её удаляют и расшифровывают заново. Служба
  /// постарше поля `id` не знает — тогда имя записи и есть её время.
  String _historyId(Map<String, dynamic> entry) {
    final id = (entry['id'] as String? ?? '').trim();
    return id.isNotEmpty ? id : (entry['at'] as String? ?? '').trim();
  }

  /// Состояние строки, которое надо показать отдельно: retrying (ждёт
  /// повтора), transcribing (разбор идёт), error (не вышло), autostop
  /// (остановлено в фоне без голоса — это не ошибка). null — обычная запись.
  /// Прежняя ветка писала unsent и failed: они читаются как retrying и error,
  /// чтобы окно работало и со службой постарше.
  String? _statusKind(Map<String, dynamic> entry) => switch (entry['status']) {
    'retrying' || 'unsent' => 'retrying',
    'transcribing' => 'transcribing',
    'error' || 'failed' => 'error',
    'autostop' => 'autostop',
    _ => null,
  };

  /// Что написано на пометке строки: она живёт в подсказке значка, а не
  /// отдельной плашкой — плашек в списке больше нет.
  String _statusLabel(
    Map<String, dynamic> entry,
    String? kind,
    String text,
    String audio,
  ) {
    switch (kind) {
      case 'retrying':
        final at = _timeOf(entry['next_retry_at']);
        return at.isEmpty ? 'ждёт повтора' : 'ждёт повтора в $at';
      case 'transcribing':
        return 'расшифровывается…';
      case 'error':
        final said = (entry['error'] as String? ?? '').trim();
        return said.isEmpty ? 'ошибка' : 'ошибка: $said';
      case 'autostop':
        return _autostopNote;
      default:
        return _statusOf(entry, text, audio);
    }
  }

  /// Первая строка текста: в свёрнутой строке больше не помещается.
  String _firstLine(String text) {
    final stop = text.indexOf('\n');
    return (stop < 0 ? text : text.substring(0, stop)).trim();
  }

  /// ЧЧ:ММ из ISO-времени; пусто, если время не разобрать.
  String _timeOf(dynamic value) {
    final at = DateTime.tryParse(value as String? ?? '')?.toLocal();
    if (at == null) return '';
    return '${at.hour.toString().padLeft(2, '0')}:'
        '${at.minute.toString().padLeft(2, '0')}';
  }

  /// Запись истории строкой кита.
  ///
  /// В заголовке — первая строка текста (или честная замена, когда текста нет),
  /// справа время. Состояние — значок слева и бейдж: строка читается как
  /// список, а не карточка с подписями. Раскрытие показывает полный текст
  /// мелким и подробности записи; «⋯» — «Расшифровать заново» и «Удалить».
  Widget _historyRow(Map<String, dynamic> entry) {
    historyRowBuilds++;
    final tokens = KitsTokens.of(context);
    final id = _historyId(entry);
    final text = (entry['text'] as String? ?? '').trim();
    final audio = (entry['audio'] as String? ?? '').trim();
    final kind = _statusKind(entry);
    final chosen = selected.contains(id);
    final expanded = opened.contains(id) && !selectMode;
    final busy = audio.isNotEmpty && retrying == audio;
    final line = _firstLine(text);
    final note = line.isNotEmpty
        ? line
        : _historyPlaceholder(entry, kind, text, audio);
    final canRetry = audio.isNotEmpty && kind != 'transcribing';
    final when = lineHistoryTime(entry['at']);
    final badge = _statusBadge(entry, kind);
    // Фиксированные слоты справа: время и бейдж занимают одно и то же место в
    // каждой строке, поэтому действия тоже заканчиваются на одной вертикали —
    // независимо от того, есть у записи бейдж или нет.
    final right = Row(
      mainAxisSize: MainAxisSize.min,
      children: <Widget>[
        SizedBox(
          width: 150,
          child: badge == null
              ? null
              : FittedBox(
                  fit: BoxFit.scaleDown,
                  alignment: Alignment.centerRight,
                  child: badge,
                ),
        ),
        const SizedBox(width: 6),
        SizedBox(
          width: 42,
          child: Text(
            when,
            textAlign: TextAlign.right,
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: KitsType.of(
              context,
              font: '--ck-font-micro',
              color: tokens.color('--ck-text-meta'),
            ),
          ),
        ),
      ],
    );
    // Одна и та же колонка действий во всех строках: «Копировать» есть и у
    // записи без текста, но не нажимается. Поэтому кнопки, дата и бейдж
    // заканчиваются на одной вертикали, а текст обрезается одинаково.
    final actions = <KitsListRowAction>[
      KitsListRowAction(
        label: 'Копировать',
        icon: const Icon(Icons.content_copy_rounded, size: 18),
        onClick: acting || text.isEmpty ? null : () => copyText(text),
      ),
    ];
    final menu = <KitsRowMenuItem>[
      if (canRetry)
        KitsRowMenuItem(
          label: busy ? 'Распознаю…' : 'Расшифровать заново',
          onSelect: acting || busy ? null : () => _retranscribe(entry),
        ),
      KitsRowMenuItem(
        label: 'Удалить',
        danger: true,
        onSelect: acting ? null : () => _deleteHistory(entry),
      ),
    ];
    return KitsListRow(
      leading: selectMode
          ? Icon(
              chosen ? Icons.check_box : Icons.check_box_outline_blank,
              size: 18,
              color: chosen
                  ? tokens.color('--ck-accent-fill')
                  : tokens.color('--ck-text-secondary'),
            )
          : _statusMark(entry, kind, text, audio),
      title: note,
      trailing: right,
      selected: chosen,
      onTap: selectMode ? () => _toggleSelected(id) : null,
      actions: actions,
      menu: menu,
      expanded: expanded,
      showExpandAction: !selectMode,
      onToggle: selectMode ? null : (_) => _toggleRow(id),
    );
  }

  /// Тело раскрытой записи: полный текст, подробности и путь к звуку. Строит
  /// его [_historySliver] под закреплённой плашкой.
  Widget _historyBody(Map<String, dynamic> entry) {
    final tokens = KitsTokens.of(context);
    final text = (entry['text'] as String? ?? '').trim();
    final audio = (entry['audio'] as String? ?? '').trim();
    return Container(
      margin: const EdgeInsets.only(top: 2),
      padding: const EdgeInsets.fromLTRB(14, 10, 14, 12),
      decoration: BoxDecoration(
        color: tokens.color('--ck-bg-second'),
        borderRadius: BorderRadius.circular(tokens.length('--ck-radius-row')),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: <Widget>[
          if (text.isEmpty)
            Text(
              'Текста в записи нет.',
              style: KitsType.of(
                context,
                font: '--ck-font-hint',
                color: tokens.color('--ck-text-secondary'),
              ),
            )
          else
            SelectableText(
              text,
              style: KitsType.of(
                context,
                font: '--ck-font-control',
                leading: '--ck-leading-relaxed',
                color: tokens.color('--ck-text'),
              ),
            ),
          if (_historyMeta(entry).isNotEmpty) ...<Widget>[
            const SizedBox(height: 8),
            Text(
              _historyMeta(entry),
              style: KitsType.of(
                context,
                font: '--ck-font-micro',
                color: tokens.color('--ck-text-meta'),
              ),
            ),
          ],
          if (audio.isNotEmpty) ...<Widget>[
            const SizedBox(height: 2),
            Tooltip(
              message: audio,
              child: Text(
                'Звук: $audio',
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: KitsType.of(
                  context,
                  font: '--ck-font-micro',
                  color: tokens.color('--ck-text-meta'),
                ),
              ),
            ),
          ],
        ],
      ),
    );
  }

  /// Раскрыть или свернуть строку.
  void _toggleRow(String id) => setState(() {
    if (!opened.remove(id)) opened.add(id);
  });

  /// Бейдж состояния строки: у готовой записи его нет, у прочих — слово
  /// службы, а не значок без подписи.
  Widget? _statusBadge(Map<String, dynamic> entry, String? kind) {
    final (text, tone) = switch (kind) {
      'error' => ('ошибка', KitsBadgeTone.danger),
      'retrying' => ('ждёт повтора', KitsBadgeTone.warning),
      'autostop' => ('фоновая остановка', KitsBadgeTone.warning),
      'transcribing' => ('расшифровка', KitsBadgeTone.info),
      _ => (
        _statusOf(
                  entry,
                  entry['text'] as String? ?? '',
                  entry['audio'] as String? ?? '',
                ) ==
                'восстановлено после сбоя'
            ? 'восстановлено'
            : '',
        KitsBadgeTone.neutral,
      ),
    };
    if (text.isEmpty) return null;
    return KitsBadge(text: text, tone: tone, size: KitsBadgeSize.xs);
  }

  /// Значок состояния слева у ВСЕХ строк: готовые тоже отмечены галочкой,
  /// чтобы список читался одинаково, а не смешанными строками с отступом.
  /// Состояние пишется в подсказке — отдельными плашками список не замусорен.
  Widget _statusMark(
    Map<String, dynamic> entry,
    String? kind,
    String text,
    String audio,
  ) {
    final tokens = KitsTokens.of(context);
    final (icon, color) = switch (kind) {
      'error' => (
        Icons.error_outline_rounded,
        tokens.color('--ck-danger-strong'),
      ),
      'retrying' => (Icons.schedule_rounded, tokens.color('--ck-warning')),
      'autostop' => (
        Icons.pause_circle_outline_rounded,
        tokens.color('--ck-warning'),
      ),
      'transcribing' => (
        Icons.autorenew_rounded,
        tokens.color('--ck-accent-fill'),
      ),
      _ => _plainStatusMark(entry, text, audio, tokens),
    };
    return Tooltip(
      message: _statusLabel(entry, kind, text, audio),
      child: Icon(icon, size: 16, color: color),
    );
  }

  /// Значок для строки без отдельного состояния: готово, восстановлено,
  /// не распознано или пусто — как их назвала служба.
  (IconData, Color) _plainStatusMark(
    Map<String, dynamic> entry,
    String text,
    String audio,
    KitsTokens tokens,
  ) => switch (_statusOf(entry, text, audio)) {
    'восстановлено после сбоя' => (
      Icons.restore_rounded,
      tokens.color('--ck-accent-fill'),
    ),
    'не распознано' => (
      Icons.help_outline_rounded,
      tokens.color('--ck-warning'),
    ),
    'пусто' => (Icons.notes_rounded, tokens.color('--ck-text-meta')),
    _ => (Icons.check_circle_outline_rounded, tokens.color('--ck-success')),
  };

  /// Что стоит в строке, когда текста ещё нет.
  String _historyPlaceholder(
    Map<String, dynamic> entry,
    String? kind,
    String text,
    String audio,
  ) {
    switch (kind) {
      case 'transcribing':
        return 'Расшифровывается…';
      case 'retrying':
        return 'Ждёт повтора — текст появится сам.';
      case 'error':
        return 'Не распознано — звук сохранён.';
      case 'autostop':
        return _autostopNote;
    }
    if (text.isEmpty && audio.isNotEmpty) {
      return 'Не распознано — звук сохранён.';
    }
    if (text.isEmpty && audio.isEmpty) return 'Пустая запись';
    return '';
  }

  /// Мелкие подробности раскрытой строки: длительность, что стало с текстом,
  /// номер попытки. Пути к звуку стоят отдельной строкой ниже.
  String _historyMeta(Map<String, dynamic> entry) {
    final parts = <String>[];
    final seconds = (entry['seconds'] as num? ?? 0).toInt();
    if (seconds > 0) parts.add('$seconds с');
    final where = switch (entry['destination']) {
      'inserted' => 'вставлено в поле',
      'clipboard' => 'осталось в буфере обмена',
      'restored' => 'восстановлено после сбоя',
      'failed' => 'не распознано',
      _ => '',
    };
    if (where.isNotEmpty) parts.add(where);
    if (_statusKind(entry) == 'retrying') {
      final attempts = (entry['attempts'] as num? ?? 0).toInt();
      parts.add('попытка ${attempts < 1 ? 1 : attempts}');
    }
    return parts.join(' · ');
  }

  /// «Расшифровать заново»: служба разбирает сохранённый звук ещё раз.
  Future<void> _retranscribe(Map<String, dynamic> entry) async {
    final audio = (entry['audio'] as String? ?? '').trim();
    if (audio.isEmpty) return;
    setState(() {
      retrying = audio;
      error = null;
    });
    try {
      // Новая служба знает запись по `id` и умеет /history/retranscribe. У
      // старой записи имени нет — тогда работает прежняя просьба по звуку, и
      // строка не остаётся без повтора после обновления одного окна.
      final followed = (entry['status'] as String? ?? '').isNotEmpty;
      final answer = followed
          ? await client.request('/history/retranscribe', {
              'id': _historyId(entry),
            })
          : await client.request('/retry', {'audio': audio});
      if (!mounted) return;
      // Новая служба отвечает не состоянием, а итогом повтора; историю после
      // него всё равно надо перечитать: строка перестала быть неудачной.
      if (answer.containsKey('stage') ||
          answer.containsKey('history') ||
          answer.containsKey('history_rev')) {
        receive(answer);
      }
      _reloadHistory();
      lineToast(context, 'Расшифровываю заново');
    } catch (failure) {
      if (mounted) setState(() => error = _reason(failure));
    } finally {
      if (mounted) setState(() => retrying = null);
    }
  }

  /// Удалить запись: спрашиваем, потому что вместе с ней уходит и звук.
  Future<void> _deleteHistory(Map<String, dynamic> entry) async {
    final audio = (entry['audio'] as String? ?? '').trim();
    final approved = await askDanger(
      context,
      title: 'Удалить запись?',
      consequences: <String>[
        'Запись исчезнет из истории.',
        if (audio.isNotEmpty) 'Запись звука тоже удалится с диска.',
      ],
    );
    if (approved != true || !mounted) return;
    await action('/history/delete', {
      'id': _historyId(entry),
    }, 'Запись удалена');
  }

  /// Статус строки. «Не распознано» ставит служба (destination failed), а
  /// старые записи того же вида узнаются по пустому тексту со звуком: до
  /// признака failed отказ в историю не попадал, но файлы оставались.
  String _statusOf(Map<String, dynamic> entry, String text, String audio) {
    final destination = entry['destination'] as String? ?? '';
    if (destination == 'failed' || (text.isEmpty && audio.isNotEmpty)) {
      return 'не распознано';
    }
    // Восстановленная запись названа делом, а не словом «готово»: человек
    // должен видеть, что разговор вернулся после сбоя.
    if (destination == 'restored' || entry['recovered'] == true) {
      return 'восстановлено после сбоя';
    }
    // Запись без слов и без звука: показывать «готово» было бы неправдой.
    if (text.isEmpty && audio.isEmpty) return 'пусто';
    return 'готово';
  }
}

/// «Поделиться»: ссылка-конфигурация провайдера.
///
/// Ключ в ссылке — по желанию и по умолчанию выключен: пересланная в мессенджер
/// строка с ключом остаётся там навсегда, а без ключа ссылка переносит только
/// адрес и модель, и человек вписывает свой ключ сам.
class _ShareProviderDialog extends StatefulWidget {
  const _ShareProviderDialog({required this.client});

  final DictationClient client;

  @override
  State<_ShareProviderDialog> createState() => _ShareProviderDialogState();
}

class _ShareProviderDialogState extends State<_ShareProviderDialog> {
  bool withKey = false;
  bool busy = true;
  String? link;
  String? error;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    setState(() {
      busy = true;
      error = null;
    });
    try {
      final answer = await widget.client.request('/provider/share', {
        'with_key': withKey,
      });
      if (mounted) {
        setState(() => link = answer['link'] as String? ?? '');
      }
    } catch (failure) {
      if (mounted) {
        setState(
          () => error = failure is HttpException
              ? lineSentence(failure.message)
              : 'Ссылка не собралась: служба не ответила.',
        );
      }
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
    title: const Text('Поделиться провайдером'),
    content: SizedBox(
      width: 420,
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: <Widget>[
          SwitchListTile(
            contentPadding: EdgeInsets.zero,
            value: withKey,
            title: const Text('В ссылке будет ключ'),
            subtitle: const Text(
              'По умолчанию без ключа: так ссылку не страшно переслать.',
            ),
            onChanged: busy
                ? null
                : (value) {
                    setState(() => withKey = value);
                    _load();
                  },
          ),
          const SizedBox(height: 8),
          if (busy)
            const LinearProgressIndicator(minHeight: 2)
          else if (error != null)
            Text(
              error!,
              style: lineText(
                size: 13,
                height: 1.45,
                color: LineTokens.of(context).danger,
              ),
            )
          else
            Container(
              width: double.infinity,
              padding: const EdgeInsets.all(12),
              decoration: BoxDecoration(
                color: LineTokens.of(context).ground,
                borderRadius: BorderRadius.circular(LineTokens.fieldRadius),
                border: Border.all(color: LineTokens.of(context).line),
              ),
              child: SelectableText(
                link ?? '',
                style: lineText(size: 12, height: 1.45),
              ),
            ),
        ],
      ),
    ),
    actions: <Widget>[
      TextButton(
        onPressed: () async {
          // Буфер обмена — для мессенджера: ссылку пересылают строкой.
          if (link != null) {
            await Clipboard.setData(ClipboardData(text: link!));
            if (context.mounted) lineToast(context, 'Ссылка скопирована');
            return;
          }
          Navigator.pop(context);
        },
        child: Text(link == null || busy ? 'Закрыть' : 'В буфер обмена'),
      ),
      FilledButton(
        onPressed: link == null || busy ? null : () => Navigator.pop(context),
        child: Text(link == null || busy ? 'Закрыть' : 'Готово'),
      ),
    ],
  );
}

/// «Вставить ссылку»: разбирает и применяет служба.
class _PasteLinkDialog extends StatefulWidget {
  const _PasteLinkDialog({required this.client});

  final DictationClient client;

  @override
  State<_PasteLinkDialog> createState() => _PasteLinkDialogState();
}

class _PasteLinkDialogState extends State<_PasteLinkDialog> {
  final link = TextEditingController();
  bool busy = false;
  String? error;

  /// Подсказка формата. Строка — от службы (ядра providercfg), а не своя:
  /// формат ссылки выпускает она, и окно не должно застрять на прежней схеме
  /// (так и вышло со старой dictation://). Ответа нет — запасная строка.
  String linkFormat = 'speech-provider://v1/…';

  @override
  void initState() {
    super.initState();
    _readFormat();
  }

  Future<void> _readFormat() async {
    try {
      final answer = await widget.client.request('/provider/link/format');
      final format = (answer['format'] as String? ?? '').trim();
      if (mounted && format.isNotEmpty) setState(() => linkFormat = format);
    } catch (_) {
      // Старая служба такого запроса не знает: остаётся запасная строка.
    }
  }

  @override
  void dispose() {
    link.dispose();
    super.dispose();
  }

  Future<void> _apply() async {
    setState(() {
      busy = true;
      error = null;
    });
    try {
      final fresh = await widget.client.request('/provider/link', {
        'link': link.text.trim(),
      });
      if (mounted) Navigator.pop(context, fresh);
    } catch (failure) {
      if (mounted) {
        setState(
          () => error = failure is HttpException
              ? lineSentence(failure.message)
              : 'Служба диктовки не ответила.',
        );
      }
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final tokens = LineTokens.of(context);
    return AlertDialog(
      title: const Text('Вставить ссылку'),
      content: SizedBox(
        width: 420,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: <Widget>[
            TextField(
              controller: link,
              autocorrect: false,
              enableSuggestions: false,
              decoration: InputDecoration(
                labelText: 'Ссылка-конфигурация',
                hintText: linkFormat,
                hintStyle: lineText(size: 12, color: tokens.muted),
                labelStyle: lineText(size: 13, color: tokens.muted),
                filled: true,
                fillColor: tokens.ground,
                isDense: true,
                contentPadding: const EdgeInsets.symmetric(
                  horizontal: 12,
                  vertical: 12,
                ),
                border: OutlineInputBorder(
                  borderRadius: BorderRadius.circular(LineTokens.fieldRadius),
                  borderSide: BorderSide(color: tokens.line),
                ),
                enabledBorder: OutlineInputBorder(
                  borderRadius: BorderRadius.circular(LineTokens.fieldRadius),
                  borderSide: BorderSide(color: tokens.line),
                ),
                focusedBorder: OutlineInputBorder(
                  borderRadius: BorderRadius.circular(LineTokens.fieldRadius),
                  borderSide: BorderSide(color: tokens.accent, width: 2),
                ),
              ),
            ),
            if (error != null) ...<Widget>[
              const SizedBox(height: 10),
              Text(
                error!,
                style: lineText(size: 13, height: 1.45, color: tokens.danger),
              ),
            ],
          ],
        ),
      ),
      actions: <Widget>[
        TextButton(
          onPressed: () => Navigator.pop(context),
          child: const Text('Отмена'),
        ),
        FilledButton(
          onPressed: busy ? null : _apply,
          child: Text(busy ? 'Применяю…' : 'Применить'),
        ),
      ],
    );
  }
}

/// Настройка ключа прямо в окне: вписать, заменить или удалить.
///
/// Ключ здесь только вводится и уходит службе. После сохранения его нигде не
/// показываем, а поле очищается при закрытии: окно хранит ключ лишь пока
/// диалог открыт. Адрес и модель своего провайдера приходят из `providers`
/// службы; сам ключ служба наружу не отдаёт никогда.
class _ProviderKeyDialog extends StatefulWidget {
  const _ProviderKeyDialog({required this.client, required this.slot});

  final DictationClient client;

  /// Запись `providers` службы: key, title, key_set, url, model, own_url.
  final Map<String, dynamic> slot;

  @override
  State<_ProviderKeyDialog> createState() => _ProviderKeyDialogState();
}

class _ProviderKeyDialogState extends State<_ProviderKeyDialog> {
  final url = TextEditingController();
  final model = TextEditingController();
  final key = TextEditingController();
  bool busy = false;
  bool showKey = false;
  String? error;
  ProviderCheck? check;

  String get _speech => widget.slot['key'] as String? ?? '';

  /// Свой провайдер: адрес и модель видны и правятся. У Groq адрес прописан.
  bool get _own => _speech == 'api';

  bool get _keySet => widget.slot['key_set'] == true;

  /// Своего провайдера можно удалить и без ключа, если сохранён адрес: forget
  /// очищает адрес и модель. У Groq адрес прописан всегда, поэтому там удаление
  /// зависит только от ключа.
  bool get _canForget =>
      _keySet ||
      (_own && (widget.slot['url'] as String? ?? '').trim().isNotEmpty);

  @override
  void initState() {
    super.initState();
    url.text = widget.slot['url'] as String? ?? '';
    model.text = widget.slot['model'] as String? ?? '';
  }

  @override
  void dispose() {
    url.dispose();
    model.dispose();
    key.dispose();
    super.dispose();
  }

  /// Тело просьбы. Пустой ключ не посылаем: служба оставит сохранённый, и
  /// смена одной модели ключ не потеряет.
  Map<String, dynamic> _fields() => <String, dynamic>{
    'speech': _speech,
    if (_own) 'url': url.text.trim(),
    if (_own) 'model': model.text.trim(),
    if (key.text.trim().isNotEmpty) 'key': key.text.trim(),
  };

  /// Ключ обязателен, если его ещё нет; адрес и модель — у своего провайдера.
  String? _missing() {
    if (_own && (url.text.trim().isEmpty || model.text.trim().isEmpty)) {
      return 'Впишите адрес и модель: без них провайдер не сохранить.';
    }
    if (!_keySet && key.text.trim().isEmpty) {
      return _own
          ? 'Вставьте ключ: без него провайдер не заработает.'
          : 'Вставьте ключ Groq: без него распознавание через Groq не заработает.';
    }
    return null;
  }

  /// Ответ службы словами службы; технический текст в поле не выводим.
  String _said(Object failure) {
    if (failure is HttpException) {
      final said = lineSentence(failure.message);
      if (said.isNotEmpty) return said;
    }
    return 'Служба диктовки не ответила.';
  }

  /// «Проверить»: настоящий запрос к провайдеру, как в онбординге.
  Future<void> _check() async {
    final problem =
        _own && (url.text.trim().isEmpty || model.text.trim().isEmpty)
        ? 'Впишите адрес и модель, чтобы проверить провайдера.'
        : null;
    if (problem != null) {
      setState(() => error = problem);
      return;
    }
    setState(() {
      busy = true;
      error = null;
      check = null;
    });
    try {
      final answer = await widget.client.request('/provider/check', _fields());
      if (!mounted) return;
      final result = ProviderCheck.of(answer);
      if (result.detail.isNotEmpty) {
        // Техническая часть ответа — только в журнал, не на экран.
        debugPrint('[provider/check] ${result.detail}');
      }
      setState(() => check = result);
    } catch (failure) {
      if (mounted) setState(() => error = _said(failure));
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  /// «Сохранить»: POST /provider/apply, затем свежие настройки от службы.
  Future<void> _save() async {
    final problem = _missing();
    if (problem != null) {
      setState(() => error = problem);
      return;
    }
    setState(() {
      busy = true;
      error = null;
    });
    try {
      await widget.client.request('/provider/apply', _fields());
      final fresh = await widget.client.request('/settings');
      if (mounted) {
        Navigator.pop(context, <String, dynamic>{
          'fresh': fresh,
          'said': 'Провайдер сохранён',
        });
      }
    } catch (failure) {
      if (mounted) setState(() => error = _said(failure));
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  /// «Удалить»: после подтверждения стирает ключ (и у своего — адрес и модель).
  Future<void> _forget() async {
    final approved = await askDanger(
      context,
      title: _own ? 'Удалить свой провайдер?' : 'Удалить ключ Groq?',
      consequences: _own
          ? <String>[
              'Ключ, адрес и модель будут стёрты.',
              'Диктовка через свой провайдер не заработает, пока его не настроить снова.',
            ]
          : <String>[
              'Ключ Groq будет стёрт.',
              'Диктовка через Groq не заработает, пока ключ не введён снова.',
            ],
      confirmLabel: 'Удалить',
    );
    if (approved != true || !mounted) return;
    setState(() {
      busy = true;
      error = null;
    });
    try {
      await widget.client.request('/provider/forget', {'speech': _speech});
      final fresh = await widget.client.request('/settings');
      if (mounted) {
        Navigator.pop(context, <String, dynamic>{
          'fresh': fresh,
          'said': _own ? 'Свой провайдер удалён' : 'Ключ Groq удалён',
        });
      }
    } catch (failure) {
      if (mounted) setState(() => error = _said(failure));
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final tokens = LineTokens.of(context);
    final title = _own ? 'Свой провайдер' : 'Ключ Groq';
    return AlertDialog(
      title: Text(title),
      content: SizedBox(
        width: 420,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: <Widget>[
            if (!_own)
              Padding(
                padding: const EdgeInsets.only(bottom: 10),
                child: Text(
                  'Адрес Groq уже прописан — нужен только ключ.',
                  style: lineText(size: 13, color: tokens.muted),
                ),
              ),
            if (_own) ...<Widget>[
              _field(url, 'Адрес (URL)', hint: 'https://сервер/v1'),
              const SizedBox(height: 10),
              _field(model, 'Модель', hint: 'например whisper-large-v3'),
              const SizedBox(height: 10),
            ],
            _field(
              key,
              _own ? 'Ключ' : 'Ключ Groq',
              hint: 'вставьте ключ',
              // Подсказка про пустое поле видна всегда, а не только в фокусе:
              // без неё человек не поймёт, что пустой ключ ничего не меняет.
              helper: _keySet ? 'оставьте пустым, чтобы не менять' : null,
              obscure: !showKey,
              suffix: IconButton(
                tooltip: showKey ? 'скрыть' : 'показать',
                icon: Icon(
                  showKey
                      ? Icons.visibility_off_outlined
                      : Icons.visibility_outlined,
                  size: 18,
                ),
                onPressed: () => setState(() => showKey = !showKey),
              ),
            ),
            const SizedBox(height: 8),
            Text(
              _keySet ? 'Ключ задан.' : 'Ключа нет.',
              style: lineText(size: 12, color: tokens.muted),
            ),
            if (!_own) ...<Widget>[
              const SizedBox(height: 10),
              Row(
                children: <Widget>[
                  Text(
                    'Где взять ключ: ',
                    style: lineText(size: 13, color: tokens.muted),
                  ),
                  InkWell(
                    key: const Key('groq-key-link'),
                    onTap: () => openExternalUrl(
                      Uri.parse('https://console.groq.com/keys'),
                    ),
                    child: Text(
                      'Создать ключ в Groq',
                      style: lineText(
                        size: 13,
                        color: tokens.accent,
                        weight: FontWeight.w500,
                      ).copyWith(decoration: TextDecoration.underline),
                    ),
                  ),
                ],
              ),
            ],
            if (check != null) ...<Widget>[
              const SizedBox(height: 10),
              Text(
                check!.note,
                style: lineText(
                  size: 13,
                  height: 1.45,
                  color: check!.ok ? tokens.accent : tokens.danger,
                ),
              ),
            ],
            if (error != null) ...<Widget>[
              const SizedBox(height: 10),
              Text(
                error!,
                style: lineText(size: 13, height: 1.45, color: tokens.danger),
              ),
            ],
          ],
        ),
      ),
      actions: <Widget>[
        if (_canForget)
          TextButton(
            onPressed: busy ? null : _forget,
            child: Text('Удалить', style: TextStyle(color: tokens.danger)),
          ),
        TextButton(
          onPressed: () => Navigator.pop(context),
          child: const Text('Отмена'),
        ),
        OutlinedButton(
          onPressed: busy ? null : _check,
          child: const Text('Проверить'),
        ),
        FilledButton(
          onPressed: busy ? null : _save,
          child: Text(busy ? 'Сохраняю…' : 'Сохранить'),
        ),
      ],
    );
  }

  /// Поле в духе окна: та же рамка и радиус, что у ссылки подключения.
  Widget _field(
    TextEditingController controller,
    String label, {
    String? hint,
    String? helper,
    bool obscure = false,
    Widget? suffix,
  }) {
    final tokens = LineTokens.of(context);
    return TextField(
      controller: controller,
      obscureText: obscure,
      autocorrect: false,
      enableSuggestions: !obscure,
      style: lineText(size: 14, color: tokens.ink),
      decoration: InputDecoration(
        labelText: label,
        hintText: hint,
        helperText: helper,
        helperStyle: lineText(size: 12, color: tokens.muted),
        hintStyle: lineText(size: 13, color: tokens.muted),
        labelStyle: lineText(size: 13, color: tokens.muted),
        suffixIcon: suffix,
        filled: true,
        fillColor: tokens.ground,
        isDense: true,
        contentPadding: const EdgeInsets.symmetric(
          horizontal: 12,
          vertical: 12,
        ),
        border: OutlineInputBorder(
          borderRadius: BorderRadius.circular(LineTokens.fieldRadius),
          borderSide: BorderSide(color: tokens.line),
        ),
        enabledBorder: OutlineInputBorder(
          borderRadius: BorderRadius.circular(LineTokens.fieldRadius),
          borderSide: BorderSide(color: tokens.line),
        ),
        focusedBorder: OutlineInputBorder(
          borderRadius: BorderRadius.circular(LineTokens.fieldRadius),
          borderSide: BorderSide(color: tokens.accent, width: 2),
        ),
      ),
    );
  }
}
