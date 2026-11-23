// Окно настроек MeetDigest на Flutter.
//
// Спайк: то же окно, что на WebView2, чтобы сравнить их честно — размером,
// памятью, скоростью запуска и временем разработки.
//
// Правил настроек здесь нет ни одного: окно спрашивает состояние у ядра на Go и
// пересылает ему просьбы. Всё, что оно решает само, — как это выглядит.
import 'package:flutter/material.dart';

import 'core.dart';
import 'notes_page.dart';
import 'overlay.dart';
import 'shell.dart';
import 'dictation.dart';

/// Две роли одной программы: окно настроек и плашка диктовки.
///
/// Разделение по флагу, а не по двум сборкам: вторая программа весила бы столько
/// же, сколько первая, а показывает три строки.
void main(List<String> args) {
  if (args.contains('--dictation')) {
    runApp(const DictationApp());
    return;
  }
  if (args.contains('--overlay')) {
    runApp(const OverlayApp());
    return;
  }
  // «--notes» открывает окно сразу на заметках: из лотка человек просит
  // именно их, и заставлять его щёлкать раздел после этого невежливо.
  runApp(SettingsApp(startAt: args.contains('--notes') ? 0 : 1));
}

class SettingsApp extends StatelessWidget {
  const SettingsApp({super.key, this.core, this.startAt = 0});

  /// startAt — с какого раздела открыть окно.
  final int startAt;

  /// Ядро приходит извне, чтобы проверки могли подставить своё: иначе окно в
  /// проверке полезло бы запускать настоящий meetdigest-core.exe, которого на
  /// машине сборки нет.
  final Core? core;

  @override
  Widget build(BuildContext context) {
    final made = core ?? Core.beside();
    return MaterialApp(
      title: 'MeetDigest',
      debugShowCheckedModeBanner: false,
      theme: _theme(Brightness.light),
      darkTheme: _theme(Brightness.dark),
      // Тему берём системную: окно не должно спорить с остальной Windows.
      themeMode: ThemeMode.system,
      home: Shell(
        startAt: startAt,
        sections: [
          Section(
            title: 'Заметки',
            icon: Icons.mic_none_rounded,
            body: (context) => NotesPage(core: made),
          ),
          Section(
            title: 'Настройки',
            icon: Icons.tune_rounded,
            body: (context) => SettingsPage(core: made),
          ),
        ],
      ),
    );
  }

  /// Тема: один зерновой цвет, из которого Material выводит всю палитру.
  ///
  /// Так же сделано в Hiddify: цвет задаётся один раз, а светлый и тёмный
  /// наборы получаются из него сами и остаются согласованными.
  ThemeData _theme(Brightness brightness) {
    final colors = ColorScheme.fromSeed(
      seedColor: const Color(0xFF2F6DF6),
      brightness: brightness,
    );
    return ThemeData(
      useMaterial3: true,
      colorScheme: colors,
      scaffoldBackgroundColor: colors.surface,
      splashFactory: InkSparkle.splashFactory,
      visualDensity: VisualDensity.compact,
    );
  }
}

class SettingsPage extends StatefulWidget {
  const SettingsPage({super.key, this.core});

  final Core? core;

  @override
  State<SettingsPage> createState() => _SettingsPageState();
}

class _SettingsPageState extends State<SettingsPage> {
  late final Core core = widget.core ?? Core.beside();
  Map<String, dynamic>? state;
  String? trouble;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final fresh = await core.show();
      setState(() {
        state = fresh;
        trouble = null;
      });
    } on CoreError catch (error) {
      setState(() => trouble = error.message);
    }
  }

  /// Попросить ядро и показать то, что оно вернуло.
  ///
  /// Ответ ядра — уже новое состояние, поэтому второй запрос не нужен; а отказ
  /// показываем прямо в окне: «нажал, и ничего не произошло» — худшее поведение.
  Future<void> _ask(String action, [String value = '']) async {
    try {
      final fresh = await core.set(action, value);
      setState(() {
        state = fresh;
        trouble = null;
      });
    } on CoreError catch (error) {
      setState(() => trouble = error.message);
      if (mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text(error.message)));
      }
    }
  }

  Future<void> _pickFolder(String title, String action) async {
    try {
      final chosen = await core.folder(title);
      if (chosen.isEmpty) return; // отказался — это не ошибка
      await _ask(action, chosen);
    } on CoreError catch (error) {
      setState(() => trouble = error.message);
    }
  }

  @override
  Widget build(BuildContext context) {
    final now = state;
    return switch ((now, trouble)) {
      (_, final String said) => _Trouble(said: said, again: _load),
      (null, _) => const Center(child: CircularProgressIndicator()),
      (final Map<String, dynamic> value, _) => _Rows(
        state: value,
        ask: _ask,
        pick: _pickFolder,
      ),
    };
  }
}

/// Отказ ядра во весь экран — когда показывать больше нечего.
class _Trouble extends StatelessWidget {
  const _Trouble({required this.said, required this.again});
  final String said;
  final VoidCallback again;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(24),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Icon(Icons.error_outline, size: 40),
            const SizedBox(height: 12),
            Text(said, textAlign: TextAlign.center),
            const SizedBox(height: 16),
            OutlinedButton(onPressed: again, child: const Text('Ещё раз')),
          ],
        ),
      ),
    );
  }
}

class _Rows extends StatelessWidget {
  const _Rows({required this.state, required this.ask, required this.pick});

  final Map<String, dynamic> state;
  final Future<void> Function(String action, [String value]) ask;
  final Future<void> Function(String title, String action) pick;

  @override
  Widget build(BuildContext context) {
    final qualities = Choice.all(state['qualities']);
    final screenQualities = Choice.all(state['screen_qualities']);
    final screens = Choice.all(state['screens']);
    final modes = Choice.all(state['modes']);
    final decisions = Choice.all(state['decisions']);
    final decided = App.all(state['decided']);

    return ListView(
      padding: const EdgeInsets.only(bottom: 28),
      children: [
        const _Heading('Откуда брать записи'),
        SwitchListTile(
          title: const Text('Телемост'),
          subtitle: Text(state['telemost_note'] as String),
          value: state['telemost'] as bool,
          onChanged: (_) => ask('telemost'),
        ),
        for (final folder in Folder.all(state['folders']))
          ListTile(
            leading: Icon(folder.provider ? Icons.cloud_done : Icons.folder),
            title: Text(folder.title),
            subtitle: Text(folder.path),
            // У папки провайдера кнопки «убрать» нет нарочно: она вернулась бы
            // сама при следующем обходе, и кнопка оказалась бы обманом.
            trailing: folder.provider
                ? null
                : IconButton(
                    icon: const Icon(Icons.close),
                    tooltip: 'убрать',
                    onPressed: () => ask('remove-folder', folder.path),
                  ),
          ),
        ListTile(
          leading: const Icon(Icons.add),
          title: const Text('Добавить папку'),
          subtitle: const Text(
            'любая папка, куда складываются записи разговоров',
          ),
          onTap: () => pick('Где лежат записи разговоров', 'add-folder'),
        ),

        const _Heading('Пока идёт запись'),
        SwitchListTile(
          title: const Text('Окно с таймером'),
          subtitle: const Text('плашка в углу экрана — видно, не отводя глаз'),
          value: state['window'] as bool,
          onChanged: (_) => ask('window'),
        ),
        SwitchListTile(
          title: const Text('Отметка в лотке'),
          subtitle: const Text('значок сервиса становится красным'),
          value: state['tray'] as bool,
          onChanged: (_) => ask('tray'),
        ),

        const _Heading('Запись'),
        _Pick(
          title: 'Качество',
          note: Choice.noteOf(qualities, state['quality'] as String),
          among: qualities,
          value: state['quality'] as String,
          change: (key) => ask('quality', key),
        ),
        SwitchListTile(
          title: const Text('Писать и экран'),
          subtitle: const Text(
            'для встреч, где показывают систему или документ',
          ),
          value: state['screen'] as bool,
          onChanged: (_) => ask('screen'),
        ),
        _Pick(
          title: 'Подробность экрана',
          note: Choice.noteOf(
            screenQualities,
            state['screen_quality'] as String,
          ),
          among: screenQualities,
          value: state['screen_quality'] as String,
          change: (key) => ask('screen-quality', key),
        ),
        _Pick(
          title: 'Какой экран писать',
          note: state['screens_note'] as String,
          among: screens,
          value: state['screen_target'] as String,
          change: (key) => ask('screen-target', key),
        ),
        ListTile(
          leading: const Icon(Icons.drive_file_move_outlined),
          title: const Text('Папка записей'),
          subtitle: Text(state['record_folder'] as String),
          onTap: () =>
              pick('Куда складывать записи разговоров', 'record-folder'),
        ),

        const _Heading('Разговоры на этом компьютере'),
        _Pick(
          title: 'Что делать со встречами',
          note:
              state['mode_note'] as String? ??
              Choice.noteOf(modes, state['mode'] as String),
          among: modes,
          value: state['mode'] as String,
          change: (key) => ask('mode', key),
        ),

        const _Heading('Решения по приложениям'),
        if (decided.isEmpty)
          const ListTile(
            title: Text('Решений пока нет'),
            subtitle: Text(
              'их принимают на плашке встречи — «всегда» или «никогда»',
            ),
          )
        else
          for (final app in decided)
            _Pick(
              title: app.title,
              note: app.program,
              among: decisions,
              value: app.decision,
              change: (key) => ask('app', '${app.program}=$key'),
            ),
      ],
    );
  }
}

class _Heading extends StatelessWidget {
  const _Heading(this.text);
  final String text;

  @override
  Widget build(BuildContext context) {
    final style = Theme.of(context).textTheme.labelMedium?.copyWith(
      letterSpacing: 1.1,
      color: Theme.of(context).colorScheme.primary,
    );
    return Padding(
      padding: const EdgeInsets.only(left: 16, right: 16, top: 22, bottom: 6),
      child: Text(text.toUpperCase(), style: style),
    );
  }
}

/// Строка с выпадающим выбором.
class _Pick extends StatelessWidget {
  const _Pick({
    required this.title,
    required this.note,
    required this.among,
    required this.value,
    required this.change,
  });

  final String title;
  final String note;
  final List<Choice> among;
  final String value;
  final Future<void> Function(String key) change;

  @override
  Widget build(BuildContext context) {
    // Неизвестное значение не подставляем в выбор: Flutter на это падает
    // исключением, а увидел бы человек пустое окно. Показываем как есть.
    final known = among.any((one) => one.key == value);
    return ListTile(
      title: Text(title),
      subtitle: note.isEmpty ? null : Text(note),
      trailing: DropdownButton<String>(
        value: known ? value : null,
        hint: Text(value),
        underline: const SizedBox.shrink(),
        items: [
          for (final one in among)
            DropdownMenuItem(value: one.key, child: Text(one.title)),
        ],
        onChanged: (chosen) {
          if (chosen != null) change(chosen);
        },
      ),
    );
  }
}
