// Плашка диктовки: узкая полоска внизу экрана, пока человек говорит.
//
// ЧТО НА НЕЙ И ПОЧЕМУ ТОЛЬКО ЭТО:
//   • живая волна — «тебя слышно»; без неё не понять, работает ли микрофон;
//   • время — сколько уже говоришь;
//   • «Стоп» — закончить и оставить надиктованное;
//   • крестик — отменить: остановиться и не сохранять.
// Ни распознанного текста, ни настроек тут нет нарочно: плашка висит поверх
// чужой работы, и каждая лишняя строка на ней — помеха. Текст человек видит там,
// куда он печатается.
//
// ВСЁ В ОДНУ СТРОКУ И БЕЗ ПОЛЕЙ ПО КРАЯМ. Первая версия рисовала скруглённую
// карточку внутри окна — и окно белым проступало вокруг неё, а содержимое из
// двух строк не влезало в высоту и обрезалось. Теперь плашка занимает окно
// целиком, а скругление делает само окно (SetWindowRgn).
//
// ФОКУС ОНА НЕ ЗАБИРАЕТ: человек диктует в своё окно. Кнопки нажимаются мышью,
// но окно активным не становится — это задано на уровне окна (WS_EX_NOACTIVATE).
import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

/// Состояние диктовки, как его присылает ядро.
class Moment {
  const Moment({
    this.stage = 'слушаю',
    this.whole = '',
    this.level = 0,
    this.seconds = 0,
    this.trouble = '',
  });

  factory Moment.from(Map<String, dynamic> raw) => Moment(
    stage: (raw['stage'] as String?) ?? 'слушаю',
    whole: (raw['whole'] as String?) ?? '',
    level: ((raw['level'] as num?) ?? 0).toDouble(),
    seconds: ((raw['seconds'] as num?) ?? 0).toDouble(),
    trouble: (raw['trouble'] as String?) ?? '',
  );

  final String stage;
  final String whole;
  final double level;
  final double seconds;
  final String trouble;

  bool get listening => stage == 'слушаю';
  bool get thinking => stage == 'распознаю';
  bool get typing => stage == 'печатаю';
  bool get done => stage == 'готово';
  bool get bad => stage == 'беда';
}

/// momentsFromStdin — события ядра приходят строками в стандартный ввод.
Stream<Moment> momentsFromStdin() => stdin
    .transform(utf8.decoder)
    .transform(const LineSplitter())
    .map((line) {
      try {
        return Moment.from(jsonDecode(line) as Map<String, dynamic>);
      } catch (_) {
        return null; // одна помеха — не повод гасить плашку посреди речи
      }
    })
    .where((one) => one != null)
    .cast<Moment>();

class OverlayApp extends StatelessWidget {
  const OverlayApp({super.key, this.moments, this.say});

  final Stream<Moment>? moments;

  /// say — как плашка отвечает ядру. По умолчанию — в стандартный вывод.
  final void Function(String word)? say;

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      debugShowCheckedModeBanner: false,
      theme: ThemeData(useMaterial3: true, brightness: Brightness.dark),
      home: OverlayPane(
        moments: moments ?? momentsFromStdin(),
        say: say ?? (word) => stdout.writeln(word),
      ),
    );
  }
}

class OverlayPane extends StatefulWidget {
  const OverlayPane({super.key, required this.moments, required this.say});
  final Stream<Moment> moments;
  final void Function(String word) say;

  @override
  State<OverlayPane> createState() => OverlayPaneState();
}

class OverlayPaneState extends State<OverlayPane> {
  static const _window = MethodChannel('meetdigest/window');

  Moment moment = const Moment();
  StreamSubscription<Moment>? listening;
  bool finishing = false;
  bool _told = false;
  bool _shown = false;

  @override
  void initState() {
    super.initState();
    listening = widget.moments.listen(
      (one) {
        if (one.stage == 'скрыть') {
          // Диктовка кончилась: показываем итог мгновение и прячемся.
          Future.delayed(const Duration(milliseconds: 700), () {
            if (mounted) setState(() => finishing = false);
            _hide();
          });
          return;
        }
        if (one.stage == 'готов') {
          // Ядро поднялось и греет модель — показываться пока незачем.
          return;
        }
        setState(() => moment = one);
        _show();
      },
      // Ядро замолчало насовсем — уходим вместе с ним.
      onDone: () => exit(0),
      onError: (_) => exit(0),
    );
  }

  /// _hide — спрятаться до следующей диктовки.
  ///
  /// Не выходим: поднять окно заново стоит пару секунд, и человек эти секунды
  /// ждал бы после каждого нажатия. Спрятанное окно показывается мгновенно.
  void _hide() {
    _shown = false;
    _window.invokeMethod('hideOverlay');
  }

  /// _show — показаться, не забирая фокус.
  void _show() {
    if (_shown) return;
    _shown = true;
    _window.invokeMethod('showOverlay');
  }

  void _stop() {
    setState(() => finishing = true);
    widget.say('stop');
  }

  /// Отмена: остановиться и не сохранять. Уже напечатанное вернуть нельзя —
  /// клавиши нажаты, — но всё, что не успело уйти, не уйдёт.
  void _cancel() {
    setState(() => finishing = true);
    widget.say('cancel');
  }

  @override
  void dispose() {
    listening?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    // Размер окна печатаем в поток ошибок при первой отрисовке: без этого
    // «содержимое не влезло» и «окно меньше, чем думает Flutter» выглядят
    // одинаково — обрезанной кнопкой у правого края.
    if (!_told) {
      _told = true;
      final size = MediaQuery.of(context).size;
      final ratio = MediaQuery.of(context).devicePixelRatio;
      stderr.writeln(
        'плашка: ${size.width.toStringAsFixed(0)}x'
        '${size.height.toStringAsFixed(0)} точек, масштаб $ratio',
      );
    }
    final tint = moment.bad
        ? const Color(0xFFFF6B7F)
        : (moment.thinking || moment.typing
              ? const Color(0xFF6FA0FF)
              : const Color(0xFF2ED47A));

    return Material(
      // Плашка занимает окно целиком: скругление делает само окно, а всё, что
      // рисуется здесь, должно доходить до краёв — иначе окно проступает белым.
      color: const Color(0xFF17171C),
      child: Row(
        children: [
          const SizedBox(width: 14),
          _Wave(level: moment.level, tint: tint, alive: moment.listening),
          const SizedBox(width: 12),
          Expanded(
            child: Text(
              _title(),
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(
                color: tint,
                fontSize: 13.5,
                fontWeight: FontWeight.w600,
                fontFeatures: const [FontFeature.tabularFigures()],
              ),
            ),
          ),
          const SizedBox(width: 8),
          _Stop(onPressed: finishing ? null : _stop),
          const SizedBox(width: 4),
          _Cross(onPressed: finishing ? null : _cancel),
          const SizedBox(width: 8),
        ],
      ),
    );
  }

  String _title() {
    if (moment.bad) return moment.trouble.isEmpty ? 'Не вышло' : moment.trouble;
    if (finishing) return 'Заканчиваю…';
    if (moment.done) return 'Готово';
    if (moment.thinking) return 'Распознаю…';
    if (moment.typing) return 'Печатаю';
    return 'Записываю · ${_clock(moment.seconds)}';
  }

  String _clock(double seconds) {
    final whole = seconds.round();
    return '${whole ~/ 60}:${(whole % 60).toString().padLeft(2, '0')}';
  }
}

/// Кнопка «Стоп» — закончить и оставить текст.
///
/// Знаки нарисованы линиями, а не взяты из шрифта значков: на плашке их всего
/// два, а зависимость от шрифта уже один раз оставила кнопки пустыми
/// прямоугольниками.
class _Stop extends StatefulWidget {
  const _Stop({required this.onPressed});
  final VoidCallback? onPressed;

  @override
  State<_Stop> createState() => _StopState();
}

class _StopState extends State<_Stop> {
  bool hot = false;

  @override
  Widget build(BuildContext context) {
    const blue = Color(0xFF2F6DF6);
    return MouseRegion(
      cursor: SystemMouseCursors.click,
      onEnter: (_) => setState(() => hot = true),
      onExit: (_) => setState(() => hot = false),
      child: GestureDetector(
        onTap: widget.onPressed,
        child: AnimatedContainer(
          duration: const Duration(milliseconds: 120),
          height: 34,
          padding: const EdgeInsets.symmetric(horizontal: 14),
          decoration: BoxDecoration(
            color: hot ? const Color(0xFF4A82FF) : blue,
            borderRadius: BorderRadius.circular(10),
          ),
          child: Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              const CustomPaint(size: Size(11, 11), painter: _SquareGlyph()),
              const SizedBox(width: 7),
              const Text(
                'Стоп',
                style: TextStyle(
                  color: Colors.white,
                  fontSize: 13,
                  fontWeight: FontWeight.w600,
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

/// Крестик — отмена: остановиться и не сохранять.
///
/// Красный всегда, а не только под мышью: это единственное на плашке действие,
/// после которого сказанное пропадает, и оно должно отличаться от «Стоп» ещё до
/// того, как человек наведёт курсор.
class _Cross extends StatefulWidget {
  const _Cross({required this.onPressed});
  final VoidCallback? onPressed;

  @override
  State<_Cross> createState() => _CrossState();
}

class _CrossState extends State<_Cross> {
  bool hot = false;

  @override
  Widget build(BuildContext context) {
    const red = Color(0xFFD7263D);
    return Tooltip(
      message: 'Отменить — не сохранять',
      waitDuration: const Duration(milliseconds: 500),
      child: MouseRegion(
        cursor: SystemMouseCursors.click,
        onEnter: (_) => setState(() => hot = true),
        onExit: (_) => setState(() => hot = false),
        child: GestureDetector(
          onTap: widget.onPressed,
          child: AnimatedContainer(
            duration: const Duration(milliseconds: 120),
            width: 34,
            height: 34,
            decoration: BoxDecoration(
              color: hot ? const Color(0xFFE8384F) : red,
              borderRadius: BorderRadius.circular(10),
            ),
            child: const Center(
              child: CustomPaint(size: Size(13, 13), painter: _CrossGlyph()),
            ),
          ),
        ),
      ),
    );
  }
}

/// Белый квадрат — знак «стоп».
class _SquareGlyph extends CustomPainter {
  const _SquareGlyph();

  @override
  void paint(Canvas canvas, Size size) {
    canvas.drawRRect(
      RRect.fromRectAndRadius(Offset.zero & size, const Radius.circular(2)),
      Paint()..color = Colors.white,
    );
  }

  @override
  bool shouldRepaint(covariant CustomPainter old) => false;
}

/// Белый крестик — знак отмены.
class _CrossGlyph extends CustomPainter {
  const _CrossGlyph();

  @override
  void paint(Canvas canvas, Size size) {
    final ink = Paint()
      ..color = Colors.white
      ..strokeWidth = 2
      ..strokeCap = StrokeCap.round;
    canvas.drawLine(Offset.zero, Offset(size.width, size.height), ink);
    canvas.drawLine(Offset(size.width, 0), Offset(0, size.height), ink);
  }

  @override
  bool shouldRepaint(covariant CustomPainter old) => false;
}

/// Живая волна: показывает, что микрофон слышит.
///
/// Столбики помнят прошлые значения и уезжают влево — так видно именно речь, а
/// один прыгающий столбик читался бы как индикатор загрузки.
class _Wave extends StatefulWidget {
  const _Wave({required this.level, required this.tint, required this.alive});
  final double level;
  final Color tint;
  final bool alive;

  @override
  State<_Wave> createState() => _WaveState();
}

class _WaveState extends State<_Wave> {
  static const count = 12;
  final List<double> bars = List.filled(count, 0.05);
  Timer? beat;

  @override
  void initState() {
    super.initState();
    // Двигаем волну сами: события приходят десять раз в секунду, а волна должна
    // выглядеть живой и между ними.
    beat = Timer.periodic(const Duration(milliseconds: 80), (_) {
      if (!mounted) return;
      setState(() {
        for (var index = 0; index < count - 1; index++) {
          bars[index] = bars[index + 1];
        }
        // Громкость речи почти всегда в нижней части шкалы — растягиваем низ,
        // иначе полоски еле шевелятся и выглядят сломанными.
        bars[count - 1] = widget.alive
            ? (widget.level * 7).clamp(0.06, 1.0)
            : 0.06;
      });
    });
  }

  @override
  void dispose() {
    beat?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return SizedBox(
      width: 54,
      height: 24,
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.center,
        children: [
          for (var index = 0; index < count; index++) ...[
            Expanded(
              child: AnimatedContainer(
                duration: const Duration(milliseconds: 90),
                height: 3 + 21 * bars[index],
                decoration: BoxDecoration(
                  color: widget.tint.withValues(alpha: .3 + .7 * bars[index]),
                  borderRadius: BorderRadius.circular(1.5),
                ),
              ),
            ),
            if (index < count - 1) const SizedBox(width: 2),
          ],
        ],
      ),
    );
  }
}
