// Оболочка окна: своя полоса заголовка и боковое меню.
//
// ПОЧЕМУ СВОЙ ЗАГОЛОВОК. Системный заголовок Windows нельзя ни покрасить, ни
// занять чем-то полезным — он всегда чужая полоса над приложением. Свой
// заголовок становится частью интерфейса: в нём живут название, состояние и
// кнопки, и он не спорит с содержимым по цвету.
//
// Кнопки нарисованы здесь, а работу за них делает система — через канал
// «meetdigest/window» (свернуть, развернуть, закрыть, тащить окно).
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

/// Окно — то, что умеет только система.
class Window {
  static const _channel = MethodChannel('meetdigest/window');

  /// Развёрнуто ли окно сейчас. Меняется и после кнопки, и когда систему
  /// разворачивает сама: Aero Snap, Win+стрелка, двойной клик по рамке. Кнопки
  /// читают его, а не помнят своё состояние, иначе подпись расходилась бы с
  /// окном.
  static final ValueNotifier<bool> maximized = ValueNotifier<bool>(false);

  static bool _listening = false;

  /// Подписка на изменение размера, которое присылает нативная часть. Вызывать
  /// можно сколько угодно: подписка одна.
  static void listen() {
    if (_listening) return;
    _listening = true;
    _channel.setMethodCallHandler((call) async {
      if (call.method == 'maximizedChanged') {
        maximized.value = call.arguments == true;
      }
      return null;
    });
    // Без канала (другая платформа, тесты) состояние остаётся «обычное»: кнопка
    // работает, просто не знает, что окно развёрнуто. Ошибку не показываем.
    isMaximized()
        .then<void>((now) {
          maximized.value = now;
        })
        .catchError((Object _) {});
  }

  /// drag — тащить окно за нашу полосу заголовка.
  static Future<void> drag() => _channel.invokeMethod('drag');
  static Future<void> minimize() => _channel.invokeMethod('minimize');
  /// Переключает «развернуть / свернуть в окно» и возвращает новое состояние.
  static Future<bool> maximize() async {
    final now = (await _channel.invokeMethod<bool>('maximize')) ?? false;
    maximized.value = now;
    return now;
  }
  static Future<void> close() => _channel.invokeMethod('close');
  static Future<bool> isMaximized() async =>
      (await _channel.invokeMethod<bool>('isMaximized')) ?? false;
}

/// Раздел окна.
class Section {
  const Section({required this.title, required this.icon, required this.body});
  final String title;
  final IconData icon;
  final Widget Function(BuildContext context) body;
}

class Shell extends StatefulWidget {
  const Shell({
    super.key,
    required this.sections,
    this.trailing,
    this.startAt = 0,
  });
  final List<Section> sections;

  /// startAt — раздел, с которого открывается окно.
  final int startAt;

  /// trailing — что показать в правой части заголовка (состояние, отметки).
  final Widget? trailing;

  @override
  State<Shell> createState() => _ShellState();
}

class _ShellState extends State<Shell> {
  late int chosen = widget.startAt.clamp(0, widget.sections.length - 1);

  @override
  Widget build(BuildContext context) {
    final colors = Theme.of(context).colorScheme;
    return Scaffold(
      body: Column(
        children: [
          _TitleBar(
            title: widget.sections[chosen].title,
            trailing: widget.trailing,
          ),
          Expanded(
            child: Row(
              children: [
                _Rail(
                  sections: widget.sections,
                  chosen: chosen,
                  choose: (index) => setState(() => chosen = index),
                ),
                Container(
                  width: 1,
                  color: colors.outlineVariant.withValues(alpha: .5),
                ),
                Expanded(
                  child: AnimatedSwitcher(
                    duration: const Duration(milliseconds: 160),
                    child: KeyedSubtree(
                      key: ValueKey(chosen),
                      child: widget.sections[chosen].body(context),
                    ),
                  ),
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

/// Полоса заголовка: название, состояние и кнопки окна.
class _TitleBar extends StatefulWidget {
  const _TitleBar({required this.title, this.trailing});
  final String title;
  final Widget? trailing;

  @override
  State<_TitleBar> createState() => _TitleBarState();
}

class _TitleBarState extends State<_TitleBar> {
  @override
  void initState() {
    super.initState();
    Window.listen();
  }

  @override
  Widget build(BuildContext context) {
    final colors = Theme.of(context).colorScheme;
    return SizedBox(
      height: 44,
      child: DecoratedBox(
        decoration: BoxDecoration(
          color: colors.surfaceContainerHigh,
          border: Border(
            bottom: BorderSide(
              color: colors.outlineVariant.withValues(alpha: .5),
            ),
          ),
        ),
        child: Row(
          children: [
            Expanded(
              // Вся полоса — «ручка» для перетаскивания: так ведёт себя любое
              // окно Windows, и человек делает это не задумываясь.
              child: GestureDetector(
                behavior: HitTestBehavior.opaque,
                onPanStart: (_) => Window.drag(),
                onDoubleTap: Window.maximize,
                child: Padding(
                  padding: const EdgeInsets.only(left: 14),
                  child: Row(
                    children: [
                      Icon(Icons.mic_rounded, size: 17, color: colors.primary),
                      const SizedBox(width: 8),
                      Text(
                        'MeetDigest',
                        style: TextStyle(
                          fontWeight: FontWeight.w600,
                          fontSize: 13.5,
                          color: colors.onSurface,
                        ),
                      ),
                      const SizedBox(width: 10),
                      Text(
                        widget.title,
                        style: TextStyle(
                          fontSize: 13,
                          color: colors.onSurfaceVariant,
                        ),
                      ),
                      const Spacer(),
                      if (widget.trailing != null) widget.trailing!,
                      const SizedBox(width: 10),
                    ],
                  ),
                ),
              ),
            ),
            _WindowButton(
              icon: Icons.remove,
              onPressed: Window.minimize,
              tooltip: 'Свернуть',
            ),
            // Подпись и значок идут от состояния окна: в развёрнутом окне кнопка
            // возвращает его в обычный размер.
            ValueListenableBuilder<bool>(
              valueListenable: Window.maximized,
              builder: (context, full, _) => _WindowButton(
                icon: full ? Icons.filter_none : Icons.crop_square,
                iconSize: full ? 13 : 15,
                onPressed: Window.maximize,
                tooltip: full ? 'Свернуть в окно' : 'Развернуть',
              ),
            ),
            _WindowButton(
              icon: Icons.close,
              onPressed: Window.close,
              danger: true,
              tooltip: 'Закрыть',
            ),
          ],
        ),
      ),
    );
  }
}

class _WindowButton extends StatefulWidget {
  const _WindowButton({
    required this.icon,
    required this.onPressed,
    required this.tooltip,
    this.danger = false,
    this.iconSize = 16,
  });
  final IconData icon;
  final VoidCallback onPressed;
  final String tooltip;
  final bool danger;
  final double iconSize;

  @override
  State<_WindowButton> createState() => _WindowButtonState();
}

class _WindowButtonState extends State<_WindowButton> {
  bool hot = false;

  @override
  Widget build(BuildContext context) {
    final colors = Theme.of(context).colorScheme;
    // Красная подсветка только у «закрыть» — как везде в Windows: рука тянется
    // туда не глядя, и цвет здесь работает предупреждением.
    final ground = widget.danger
        ? const Color(0xFFC42B1C)
        : colors.onSurface.withValues(alpha: .08);
    return Tooltip(
      message: widget.tooltip,
      waitDuration: const Duration(milliseconds: 700),
      child: MouseRegion(
        onEnter: (_) => setState(() => hot = true),
        onExit: (_) => setState(() => hot = false),
        child: GestureDetector(
          onTap: widget.onPressed,
          child: Container(
            width: 46,
            height: 44,
            color: hot ? ground : Colors.transparent,
            alignment: Alignment.center,
            child: Icon(
              widget.icon,
              size: widget.iconSize,
              color: hot && widget.danger
                  ? Colors.white
                  : colors.onSurfaceVariant,
            ),
          ),
        ),
      ),
    );
  }
}

/// Боковое меню разделов.
class _Rail extends StatelessWidget {
  const _Rail({
    required this.sections,
    required this.chosen,
    required this.choose,
  });
  final List<Section> sections;
  final int chosen;
  final ValueChanged<int> choose;

  @override
  Widget build(BuildContext context) {
    final colors = Theme.of(context).colorScheme;
    return Container(
      width: 176,
      color: colors.surfaceContainerLow,
      padding: const EdgeInsets.symmetric(vertical: 10, horizontal: 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          for (var index = 0; index < sections.length; index++)
            _RailItem(
              section: sections[index],
              chosen: index == chosen,
              onTap: () => choose(index),
            ),
        ],
      ),
    );
  }
}

class _RailItem extends StatelessWidget {
  const _RailItem({
    required this.section,
    required this.chosen,
    required this.onTap,
  });
  final Section section;
  final bool chosen;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final colors = Theme.of(context).colorScheme;
    return Padding(
      padding: const EdgeInsets.only(bottom: 4),
      child: Material(
        color: chosen ? colors.secondaryContainer : Colors.transparent,
        borderRadius: BorderRadius.circular(9),
        child: InkWell(
          onTap: onTap,
          borderRadius: BorderRadius.circular(9),
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
            child: Row(
              children: [
                Icon(
                  section.icon,
                  size: 18,
                  color: chosen
                      ? colors.onSecondaryContainer
                      : colors.onSurfaceVariant,
                ),
                const SizedBox(width: 10),
                Text(
                  section.title,
                  style: TextStyle(
                    fontSize: 13.5,
                    fontWeight: chosen ? FontWeight.w600 : FontWeight.w400,
                    color: chosen
                        ? colors.onSecondaryContainer
                        : colors.onSurfaceVariant,
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
