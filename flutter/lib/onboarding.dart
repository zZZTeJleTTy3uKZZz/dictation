// Первый запуск: одна строка состояния и две большие кнопки — Groq или свой
// провайдер. Образец — добавление аккаунта в «Линии»: сначала выбор пути,
// потом ключ, и всё это на одном экране, без ленты шагов и вложенных карточек.
//
// ЧТО ГОВОРИТ ЧЕЛОВЕКУ. Только строка состояния вверху: что сделать сейчас,
// «Проверяем ключ…», «Готово — нажмите Ctrl+Space». Технические тексты
// («Method Not Allowed», HTTP-коды, detail из ответа) уходят в журнал и на
// экран не попадают — так решено в публичном контракте 19.11.2026, §5.
//
// ПРАВИЛА РАСПОЗНАВАНИЯ ЖИВУТ В GO. Окно не решает, что значит 403: служба
// отвечает готовой строкой `note`, окно её показывает. Проверка идёт сама,
// как только ключ вставлен; успешная проверка сразу сохраняется.
import 'dart:async';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'dictation.dart';
import 'dictation_ui.dart';

/// Как открыть внешнюю ссылку. В проверке подменяется, чтобы настоящий
/// браузер не поднимался и было видно, какой именно адрес окно показало.
typedef ExternalOpener = Future<void> Function(Uri target);

ExternalOpener openExternalUrl = _launchExternal;

Future<void> _launchExternal(Uri target) async {
  if (Platform.isWindows) {
    await Process.run('cmd', <String>['/c', 'start', '', target.toString()]);
  } else {
    await Process.run('xdg-open', <String>[target.toString()]);
  }
}

/// Итог проверки провайдера — как его назвала служба.
class ProviderCheck {
  const ProviderCheck({
    required this.ok,
    required this.kind,
    required this.note,
    this.detail = '',
    this.proxy = false,
  });

  factory ProviderCheck.of(Map<String, dynamic> raw) => ProviderCheck(
    ok: raw['ok'] == true,
    kind: raw['kind'] as String? ?? '',
    note: raw['note'] as String? ?? '',
    detail: raw['detail'] as String? ?? '',
    proxy: raw['proxy'] == true,
  );

  final bool ok;
  final String kind;
  final String note;
  final String detail;
  final bool proxy;
}

/// Экран первого запуска: выбор пути, ключ, автопроверка и «Готово».
class OnboardingPage extends StatefulWidget {
  const OnboardingPage({
    super.key,
    required this.client,
    required this.config,
    required this.initialHotkey,
    required this.onDone,
  });

  final DictationClient client;

  /// Настройки записи: при завершении они уходят в /config целиком.
  final Map<String, dynamic> config;

  /// Сочетание из настроек; по умолчанию Ctrl+Space.
  final String initialHotkey;

  /// Человек нажал «Готово»: окно перечитывает настройки и убирает онбординг.
  final Future<void> Function() onDone;

  @override
  State<OnboardingPage> createState() => _OnboardingPageState();
}

class _OnboardingPageState extends State<OnboardingPage> {
  /// Путь: groq или api. По умолчанию Groq — он проще всего.
  String path = 'groq';

  final key = TextEditingController();
  final url = TextEditingController();
  final model = TextEditingController(text: 'whisper-large-v3');
  final link = TextEditingController();

  bool busy = false;
  bool saving = false;
  bool done = false;
  String? error;
  ProviderCheck? result;

  /// Проверка вставленного ключа не ждёт нажатия кнопки, но и не идёт на
  /// каждую букву: небольшой таймер и признак уже проверенного значения.
  Timer? _autoTimer;
  String _checkedValue = '';

  /// Подсказка формата ссылки — от службы (ядро providercfg): формат выпускает
  /// она, и окно не должно застрять на прежней схеме. Ответа нет — запасная.
  String linkFormat = 'speech-provider://v1/…';

  /// Сочетание клавиш показывается в строке состояния, меняется в настройках.
  late String combination = widget.initialHotkey;

  @override
  void initState() {
    super.initState();
    _readFormat();
  }

  @override
  void dispose() {
    _autoTimer?.cancel();
    key.dispose();
    url.dispose();
    model.dispose();
    link.dispose();
    super.dispose();
  }

  /// Подсказка формата ссылки — от службы. Старая служба такого запроса не
  /// знает, и тогда остаётся запасная строка в поле.
  Future<void> _readFormat() async {
    try {
      final answer = await widget.client.request('/provider/link/format');
      final format = (answer['format'] as String? ?? '').trim();
      if (mounted && format.isNotEmpty) setState(() => linkFormat = format);
    } catch (_) {
      // Нечего показать — запасная строка уже стоит.
    }
  }

  /// Набранное значение готово к автопроверке.
  String get _typedValue => path == 'groq'
      ? key.text.trim()
      : '${url.text.trim()}|${model.text.trim()}|${key.text.trim()}';

  void _fieldChanged(String _) {
    final value = _typedValue;
    _autoTimer?.cancel();
    if (value == _checkedValue) return;
    final ready = path == 'groq'
        ? value.length >= 8
        : url.text.trim().isNotEmpty &&
              model.text.trim().isNotEmpty &&
              key.text.trim().isNotEmpty;
    if (!ready) return;
    _autoTimer = Timer(const Duration(milliseconds: 350), () {
      if (mounted) _check();
    });
  }

  /// Проверить провайдера настоящим запросом, при успехе сразу сохранить.
  Future<void> _check() async {
    if (busy || saving || done) return;
    _autoTimer?.cancel();
    final checked = _typedValue;
    if (checked == _checkedValue && result?.ok == true) return;
    setState(() {
      busy = true;
      error = null;
      result = null;
    });
    try {
      final answer = await widget.client.request('/provider/check', {
        'speech': path,
        if (path == 'api') 'url': url.text.trim(),
        if (path == 'api') 'model': model.text.trim(),
        if (key.text.trim().isNotEmpty) 'key': key.text.trim(),
      });
      if (!mounted) return;
      final check = ProviderCheck.of(answer);
      if (check.detail.isNotEmpty) {
        // Техническая часть ответа — только в журнал.
        debugPrint('[provider/check] ${check.detail}');
      }
      setState(() => result = check);
      if (!check.ok) {
        _checkedValue = '';
        return;
      }
      _checkedValue = checked;
      await _apply();
    } catch (failure) {
      debugPrint('[provider/check] $failure');
      _checkedValue = '';
      if (mounted) setState(() => error = _said(failure));
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  /// Сохранить проверенного провайдера — без «Далее» и второго экрана.
  Future<void> _apply() async {
    setState(() {
      saving = true;
      error = null;
    });
    try {
      await widget.client.request('/provider/apply', {
        'speech': path,
        if (path == 'api') 'url': url.text.trim(),
        if (path == 'api') 'model': model.text.trim(),
        if (key.text.trim().isNotEmpty) 'key': key.text.trim(),
      });
      if (mounted) setState(() => done = true);
    } catch (failure) {
      debugPrint('[provider/apply] $failure');
      if (mounted) setState(() => error = _said(failure));
    } finally {
      if (mounted) setState(() => saving = false);
    }
  }

  /// «Собрать ссылку»: адрес и модель превращаются в строку, которую можно
  /// перенести на другую машину. Ключ в неё не попадает никогда.
  Future<void> _makeLink() async {
    final rawURL = url.text.trim();
    final rawModel = model.text.trim();
    if (rawURL.isEmpty || rawModel.isEmpty) {
      setState(
        () => error =
            'Чтобы собрать ссылку, впишите адрес API и модель — ссылка перенесёт '
            'их без ключа, а ключ вписывают на новой машине.',
      );
      return;
    }
    setState(() {
      busy = true;
      error = null;
    });
    try {
      final answer = await widget.client.request('/provider/share', {
        'with_key': false,
      });
      final made = (answer['link'] as String? ?? '').trim();
      if (!mounted || made.isEmpty) return;
      setState(() => link.text = made);
      await Clipboard.setData(ClipboardData(text: made));
      if (mounted) lineToast(context, 'Ссылка собрана и скопирована');
    } catch (failure) {
      if (mounted) setState(() => error = _said(failure));
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  /// Вставить ссылку-конфигурацию: разбирает служба, окно только показывает
  /// поля. После этого автопроверка идёт обычным путём.
  Future<void> _pasteLink() async {
    final raw = link.text.trim();
    if (raw.isEmpty) {
      setState(() => error = 'Вставьте ссылку-конфигурацию.');
      return;
    }
    setState(() {
      busy = true;
      error = null;
      result = null;
    });
    try {
      final fresh = await widget.client.request('/provider/link', {
        'link': raw,
      });
      if (!mounted) return;
      setState(() {
        path = 'api';
        url.text = fresh['speech_url'] as String? ?? '';
        model.text = fresh['speech_model'] as String? ?? model.text;
      });
      await _check();
    } catch (failure) {
      if (mounted) setState(() => error = _said(failure));
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  /// Закончить: сохранить сочетание и убрать онбординг.
  Future<void> _finish() async {
    setState(() {
      busy = true;
      error = null;
    });
    try {
      await widget.client.request('/config', {
        ...widget.config,
        'hotkey': combination,
      });
      await widget.onDone();
    } catch (failure) {
      if (mounted) setState(() => error = _said(failure));
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  /// Человеку — одна человеческая строка.
  /// Технический текст ошибки уже ушёл в журнал в месте отказа.
  String _said(Object failure) {
    if (failure is HttpException) {
      debugPrint('[onboarding] ${failure.message}');
      return 'Проверить не удалось. Попробуйте ещё раз чуть позже.';
    }
    return 'Служба диктовки не ответила. Проверьте, что она запущена.';
  }

  /// Единственная строка состояния окна: что делать сейчас.
  String get _statusLine {
    final said = error;
    if (said != null && said.isNotEmpty) return said;
    if (saving) return 'Сохраняю…';
    if (busy) return 'Проверяем ключ…';
    if (done) return 'Готово — нажмите ${combination.replaceAll('+', ' + ')}';
    if (result != null && !result!.ok) return result!.note;
    if (path == 'groq') return 'Вставьте ключ Groq';
    return 'Укажите ссылку подключения или адрес, модель и ключ';
  }

  LineTone get _statusTone {
    if (error != null || (result != null && !result!.ok)) {
      return LineTone.danger;
    }
    if (done) return LineTone.success;
    if (busy || saving) return LineTone.info;
    return LineTone.info;
  }

  @override
  Widget build(BuildContext context) {
    final tokens = LineTokens.of(context);
    return LinePage(
      title: 'Первый запуск',
      description:
          'Выберите, чем распознавать речь: Groq или свой провайдер. '
          'Ключ хранится только на этом компьютере.',
      children: <Widget>[
        _status(tokens),
        const SizedBox(height: LineTokens.gap),
        Row(
          children: <Widget>[
            Expanded(
              child: _pathCard(
                tokens,
                chosen: path == 'groq',
                icon: Icons.bolt_rounded,
                title: 'Groq',
                hint: 'быстро, свой ключ Groq',
                choose: () => setState(() {
                  path = 'groq';
                  _checkedValue = '';
                  result = null;
                  done = false;
                }),
              ),
            ),
            const SizedBox(width: 12),
            Expanded(
              child: _pathCard(
                tokens,
                chosen: path == 'api',
                icon: Icons.dns_outlined,
                title: 'Свой провайдер',
                hint: 'ссылка подключения или свои поля',
                choose: () => setState(() {
                  path = 'api';
                  _checkedValue = '';
                  result = null;
                  done = false;
                }),
              ),
            ),
          ],
        ),
        const SizedBox(height: LineTokens.gap),
        if (path == 'groq') ..._groqFields(tokens) else ..._ownFields(tokens),
        const SizedBox(height: LineTokens.gap),
        Row(
          children: <Widget>[
            OutlinedButton(
              key: const Key('provider-check'),
              onPressed: busy || saving || done ? null : _check,
              child: Text(busy ? 'Проверяю…' : 'Проверить'),
            ),
            const Spacer(),
            if (done)
              FilledButton(
                onPressed: busy ? null : _finish,
                child: Text(busy ? 'Сохраняю…' : 'Готово'),
              ),
          ],
        ),
      ],
    );
  }

  /// Одна строка состояния: без плашек с техническими текстами.
  Widget _status(LineTokens tokens) {
    final tone = _statusTone;
    return Semantics(
      liveRegion: true,
      child: Container(
        width: double.infinity,
        key: const Key('onboarding-status'),
        padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 11),
        decoration: BoxDecoration(
          color: tokens.tone(tone).withValues(alpha: .10),
          borderRadius: BorderRadius.circular(LineTokens.fieldRadius),
          border: Border.all(color: tokens.tone(tone).withValues(alpha: .45)),
        ),
        child: Row(
          children: <Widget>[
            Icon(
              done ? Icons.check_circle_rounded : Icons.info_outline_rounded,
              size: 18,
              color: tokens.tone(tone),
            ),
            const SizedBox(width: 10),
            Expanded(
              child: Text(
                _statusLine,
                key: const Key('onboarding-status-text'),
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: lineText(
                  size: 14,
                  weight: FontWeight.w500,
                  color: tokens.ink,
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }

  /// Большая кнопка пути: нажимается целиком.
  Widget _pathCard(
    LineTokens tokens, {
    required bool chosen,
    required IconData icon,
    required String title,
    required String hint,
    required VoidCallback choose,
  }) => Material(
    color: chosen ? tokens.accent.withValues(alpha: .10) : tokens.tile,
    borderRadius: BorderRadius.circular(LineTokens.tileRadius),
    child: InkWell(
      onTap: choose,
      borderRadius: BorderRadius.circular(LineTokens.tileRadius),
      child: Container(
        padding: const EdgeInsets.all(18),
        decoration: BoxDecoration(
          borderRadius: BorderRadius.circular(LineTokens.tileRadius),
          border: Border.all(
            color: chosen ? tokens.accent : tokens.line,
            width: chosen ? 2 : 1,
          ),
        ),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: <Widget>[
            Row(
              children: <Widget>[
                Icon(
                  icon,
                  size: 26,
                  color: chosen ? tokens.accent : tokens.muted,
                ),
                const SizedBox(width: 10),
                Expanded(
                  child: Text(
                    title,
                    style: lineText(
                      size: 16,
                      weight: FontWeight.w600,
                      color: tokens.ink,
                    ),
                  ),
                ),
                Icon(
                  chosen
                      ? Icons.check_circle_rounded
                      : Icons.radio_button_unchecked,
                  size: 20,
                  color: chosen ? tokens.accent : tokens.line,
                ),
              ],
            ),
            const SizedBox(height: 8),
            Text(hint, style: lineText(size: 12, color: tokens.muted)),
          ],
        ),
      ),
    ),
  );

  List<Widget> _groqFields(LineTokens tokens) => <Widget>[
    LineBlock(
      title: 'Ключ Groq',
      // Отступ как у больших карточек выбора: заголовок и поле стоят по их
      // левому краю, а не сдвинуты внутрь плитки.
      padding: const EdgeInsets.all(18),
      children: <Widget>[
        _field(
          key,
          'Ключ Groq',
          obscure: true,
          hint: 'вставьте ключ из личного кабинета Groq',
          changed: _fieldChanged,
        ),
        const SizedBox(height: 10),
        Row(
          children: <Widget>[
            Text(
              'Где взять ключ: ',
              style: lineText(size: 13, color: tokens.muted),
            ),
            InkWell(
              key: const Key('groq-key-link'),
              onTap: () =>
                  openExternalUrl(Uri.parse('https://console.groq.com/keys')),
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
    ),
  ];

  List<Widget> _ownFields(LineTokens tokens) => <Widget>[
    LineBlock(
      title: 'Ссылка подключения',
      hint: 'Её присылают одной строкой: $linkFormat',
      children: <Widget>[
        _field(link, 'Вставьте ссылку подключения', hint: linkFormat),
        const SizedBox(height: 10),
        Wrap(
          spacing: 8,
          runSpacing: 8,
          children: <Widget>[
            OutlinedButton(
              onPressed: busy ? null : _pasteLink,
              child: const Text('Вставить ссылку'),
            ),
            OutlinedButton(
              onPressed: busy ? null : _makeLink,
              child: const Text('Собрать ссылку'),
            ),
          ],
        ),
      ],
    ),
    const SizedBox(height: LineTokens.gap),
    LineBlock(
      title: 'Или заполните поля',
      rows: false,
      children: <Widget>[
        _field(
          url,
          'Адрес API',
          hint: 'https://сервер/v1 — только https (http для localhost)',
          changed: _fieldChanged,
        ),
        const SizedBox(height: 10),
        _field(key, 'Ключ', obscure: true, changed: _fieldChanged),
        const SizedBox(height: 10),
        _field(
          model,
          'Модель распознавания',
          hint: 'например whisper-large-v3',
          changed: _fieldChanged,
        ),
      ],
    ),
  ];

  /// Поле ввода в духе окна: та же рамка, тот же радиус.
  Widget _field(
    TextEditingController controller,
    String label, {
    bool obscure = false,
    String? hint,
    ValueChanged<String>? changed,
  }) {
    final tokens = LineTokens.of(context);
    return TextField(
      controller: controller,
      obscureText: obscure,
      autocorrect: false,
      enableSuggestions: !obscure,
      onChanged: changed,
      style: lineText(size: 14, color: tokens.ink),
      decoration: InputDecoration(
        labelText: label,
        hintText: hint,
        hintStyle: lineText(size: 13, color: tokens.muted),
        labelStyle: lineText(size: 13, color: tokens.muted),
        filled: true,
        fillColor: tokens.ground,
        contentPadding: const EdgeInsets.symmetric(
          horizontal: 14,
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
