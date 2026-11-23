import 'dart:io';

import 'package:flutter/material.dart';

// Оформление «Диктовки» по образцу «Линии».
//
// Здесь только вид: палитра, радиусы, размеры, шрифт и сборка ThemeData. Ни
// одного правила настроек — что можно включить и что из чего следует, решает
// ядро на Go, а окно показывает и пересылает просьбы.
//
// Почему токены отдельным ThemeExtension, а не ColorScheme: в «Линии» тёмная
// тема отличается не светлотой, а другим акцентом (синий в темноте слепит), и
// смысловые цвета в обеих темах одинаковы. Из зернового цвета такое не вывести.

/// Тона сообщений: ошибка, предупреждение, сведения, успех.
enum LineTone { danger, warning, info, success }

/// Палитра, радиусы, размеры и шрифт «Линии» для обеих тем.
@immutable
class LineTokens extends ThemeExtension<LineTokens> {
  const LineTokens({
    required this.ground,
    required this.tile,
    required this.line,
    required this.muted,
    required this.accent,
    required this.danger,
    required this.warn,
    required this.ok,
    required this.ink,
    required this.toast,
  });

  /// Фон окна и страницы.
  final Color ground;

  /// Раздел, карточка, поле в меню.
  final Color tile;

  /// Рамки и разделители.
  final Color line;

  /// Пояснения, подсказки, мета.
  final Color muted;

  /// Кнопка, заголовок плитки, фокус.
  final Color accent;

  /// Ошибка и необратимое действие.
  final Color danger;

  /// «Сохранили, но не всё».
  final Color warn;

  /// «Получилось».
  final Color ok;

  /// Обычный текст. В палитре «Линии» его нет — там он берётся из темы
  /// браузера, — а нам нужно назвать его явно.
  final Color ink;

  /// Заливка тоста: капсула должна отличаться от земли в обеих темах.
  final Color toast;

  static const LineTokens light = LineTokens(
    ground: Color(0xFFF4F4F5),
    tile: Color(0xFFFFFFFF),
    line: Color(0xFFDADCE0),
    muted: Color(0xFF707579),
    accent: Color(0xFF3390EC),
    danger: Color(0xFFE53935),
    warn: Color(0xFFF59E0B),
    ok: Color(0xFF10B981),
    ink: Color(0xFF1B1B1F),
    toast: Color(0xFFFFFFFF),
  );

  static const LineTokens dark = LineTokens(
    ground: Color(0xFF0F0F0F),
    tile: Color(0xFF212121),
    line: Color(0xFF303030),
    muted: Color(0xFFAAAAAA),
    accent: Color(0xFF8774E1),
    danger: Color(0xFFFF595A),
    warn: Color(0xFFF59E0B),
    ok: Color(0xFF10B981),
    ink: Color(0xFFF2F2F2),
    toast: Color(0xFF2B2B2B),
  );

  /// Радиусы: поле и кнопка 12, плитка и баннер 16, строка 14, окно вопроса 20.
  static const double fieldRadius = 12;
  static const double tileRadius = 16;
  static const double rowRadius = 14;
  static const double windowRadius = 20;

  /// Тумблер, капсула, бейдж, тост — полностью круглые.
  static const double pill = 999;

  /// Колонка содержимого по центру.
  static const double columnWidth = 760;

  /// Строка списка и поле ввода.
  static const double rowHeight = 48;
  static const double controlHeight = 40;

  /// Между плитками.
  static const double gap = 12;

  static const String fontFamily = 'Segoe UI Variable Text';
  static const List<String> fontFallback = <String>['Segoe UI'];

  static LineTokens of(BuildContext context) =>
      Theme.of(context).extension<LineTokens>() ??
      (Theme.of(context).brightness == Brightness.dark ? dark : light);

  /// Цвет тона сообщения.
  Color tone(LineTone tone) => switch (tone) {
    LineTone.danger => danger,
    LineTone.warning => warn,
    LineTone.info => accent,
    LineTone.success => ok,
  };

  /// Тень плитки: только намёк на край, плитка отличается цветом.
  static const List<BoxShadow> tileShadow = <BoxShadow>[
    BoxShadow(color: Color(0x0A000000), blurRadius: 2, offset: Offset(0, 1)),
  ];

  /// Тень тоста — как у окна вопроса.
  static const List<BoxShadow> toastShadow = <BoxShadow>[
    BoxShadow(color: Color(0x2E000000), blurRadius: 24, offset: Offset(0, 4)),
  ];

  @override
  LineTokens copyWith({
    Color? ground,
    Color? tile,
    Color? line,
    Color? muted,
    Color? accent,
    Color? danger,
    Color? warn,
    Color? ok,
    Color? ink,
    Color? toast,
  }) => LineTokens(
    ground: ground ?? this.ground,
    tile: tile ?? this.tile,
    line: line ?? this.line,
    muted: muted ?? this.muted,
    accent: accent ?? this.accent,
    danger: danger ?? this.danger,
    warn: warn ?? this.warn,
    ok: ok ?? this.ok,
    ink: ink ?? this.ink,
    toast: toast ?? this.toast,
  );

  @override
  LineTokens lerp(covariant LineTokens? other, double t) {
    if (other == null) return this;
    return LineTokens(
      ground: Color.lerp(ground, other.ground, t)!,
      tile: Color.lerp(tile, other.tile, t)!,
      line: Color.lerp(line, other.line, t)!,
      muted: Color.lerp(muted, other.muted, t)!,
      accent: Color.lerp(accent, other.accent, t)!,
      danger: Color.lerp(danger, other.danger, t)!,
      warn: Color.lerp(warn, other.warn, t)!,
      ok: Color.lerp(ok, other.ok, t)!,
      ink: Color.lerp(ink, other.ink, t)!,
      toast: Color.lerp(toast, other.toast, t)!,
    );
  }
}

/// Текст «Линии»: шрифт с запасным и, где нужно, табличные числа.
TextStyle lineText({
  double? size,
  FontWeight? weight,
  Color? color,
  double? height,
  bool tabular = false,
}) => TextStyle(
  fontFamily: LineTokens.fontFamily,
  fontFamilyFallback: LineTokens.fontFallback,
  fontSize: size,
  fontWeight: weight,
  color: color,
  height: height,
  fontFeatures: tabular
      ? const <FontFeature>[FontFeature.tabularFigures()]
      : null,
);

/// Когда была запись: сегодня — «12:31», иначе дата «04.11».
///
/// Сегодняшняя дата в каждой строке — мусор: за день она одна и та же. Год
/// добавляется только к чужому году, чтобы «04.11» не притворялось нынешним.
String lineHistoryTime(dynamic value) {
  final at = DateTime.tryParse(value as String? ?? '')?.toLocal();
  if (at == null) return '';
  String two(int number) => number.toString().padLeft(2, '0');
  final now = DateTime.now();
  if (at.year == now.year && at.month == now.month && at.day == now.day) {
    return '${two(at.hour)}:${two(at.minute)}';
  }
  final date = '${two(at.day)}.${two(at.month)}';
  return at.year == now.year ? date : '$date.${two(at.year % 100)}';
}

/// Сколько длилась запись: «00:27».
String lineClock(dynamic value) {
  final seconds = (value as num? ?? 0).toInt();
  return '${(seconds ~/ 60).toString().padLeft(2, '0')}:${(seconds % 60).toString().padLeft(2, '0')}';
}

class LinePage extends StatelessWidget {
  const LinePage({
    super.key,
    required this.title,
    this.description,
    this.actions = const <Widget>[],
    required this.children,
  });

  final String title;
  final String? description;
  final List<Widget> actions;
  final List<Widget> children;

  @override
  Widget build(BuildContext context) {
    final tokens = LineTokens.of(context);
    return ListView(
      padding: const EdgeInsets.fromLTRB(24, 20, 24, 32),
      children: <Widget>[
        Center(
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: LineTokens.columnWidth),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: <Widget>[
                Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: <Widget>[
                    Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: <Widget>[
                          Text(
                            title,
                            style: lineText(
                              size: 20,
                              weight: FontWeight.w600,
                              color: tokens.ink,
                            ),
                          ),
                          if (description != null) ...<Widget>[
                            const SizedBox(height: 6),
                            Text(
                              description!,
                              style: lineText(
                                size: 14,
                                height: 1.45,
                                color: tokens.muted,
                              ),
                            ),
                          ],
                        ],
                      ),
                    ),
                    if (actions.isNotEmpty) ...<Widget>[
                      const SizedBox(width: 12),
                      Row(mainAxisSize: MainAxisSize.min, children: actions),
                    ],
                  ],
                ),
                const SizedBox(height: 20),
                ...children,
              ],
            ),
          ),
        ),
      ],
    );
  }
}

/// Плитка раздела: 16 радиуса, заголовок акцентом 15/600, пояснение под ней.
class LineBlock extends StatelessWidget {
  const LineBlock({
    super.key,
    this.title,
    this.trailing,
    this.hint,
    this.rows = false,
    this.padding,
    required this.children,
  });

  final String? title;
  final Widget? trailing;

  /// Пояснение к группе — под плиткой, мелким тихим.
  final String? hint;

  /// Плитка со строками: внутри 6, а строки держат свои 12.
  final bool rows;
  final EdgeInsetsGeometry? padding;
  final List<Widget> children;

  @override
  Widget build(BuildContext context) {
    final tokens = LineTokens.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: <Widget>[
        Container(
          decoration: BoxDecoration(
            color: tokens.tile,
            borderRadius: BorderRadius.circular(LineTokens.tileRadius),
            boxShadow: LineTokens.tileShadow,
          ),
          padding:
              padding ??
              (rows
                  ? const EdgeInsets.symmetric(vertical: 6)
                  : const EdgeInsets.all(16)),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: <Widget>[
              if (title != null)
                Padding(
                  padding: rows
                      ? const EdgeInsets.fromLTRB(12, 8, 12, 4)
                      : EdgeInsets.zero,
                  child: Row(
                    children: <Widget>[
                      Expanded(
                        child: Text(
                          title!,
                          style: lineText(
                            size: 15,
                            weight: FontWeight.w600,
                            color: tokens.accent,
                          ),
                        ),
                      ),
                      ?trailing,
                    ],
                  ),
                ),
              if (title != null && !rows) const SizedBox(height: 10),
              ...children,
            ],
          ),
        ),
        if (hint != null)
          Padding(
            padding: const EdgeInsets.fromLTRB(4, 6, 4, 0),
            child: Text(
              hint!,
              style: lineText(size: 12, height: 1.45, color: tokens.muted),
            ),
          ),
      ],
    );
  }
}

/// Строка списка: 48 в высоту, радиус 14, значок 20, хвост справа.
class LineNotice extends StatelessWidget {
  const LineNotice(
    this.text, {
    super.key,
    this.tone = LineTone.info,
    this.icon,
  });

  final String text;
  final LineTone tone;
  final IconData? icon;

  @override
  Widget build(BuildContext context) {
    final tokens = LineTokens.of(context);
    final color = tokens.tone(tone);
    return Container(
      decoration: BoxDecoration(
        color: color.withValues(alpha: .10),
        borderRadius: BorderRadius.circular(LineTokens.tileRadius),
      ),
      padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 12),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: <Widget>[
          Icon(icon ?? lineToneIcon(tone), size: 20, color: color),
          const SizedBox(width: 12),
          Expanded(
            child: Text(
              text,
              style: lineText(size: 14, height: 1.4, color: tokens.ink),
            ),
          ),
        ],
      ),
    );
  }
}

/// Значок тона: единственное, что окрашено в баннере и тосте.
IconData lineToneIcon(LineTone tone) => switch (tone) {
  LineTone.danger => Icons.error_outline_rounded,
  LineTone.warning => Icons.warning_amber_rounded,
  LineTone.info => Icons.info_outline_rounded,
  LineTone.success => Icons.check_circle_outline_rounded,
};

/// Пустое состояние: значок, короткая фраза и действие, если оно есть.
class LineAdvanced extends StatelessWidget {
  const LineAdvanced({
    super.key,
    this.title = 'Дополнительно',
    this.initiallyExpanded = false,
    required this.children,
  });

  final String title;
  final bool initiallyExpanded;
  final List<Widget> children;

  @override
  Widget build(BuildContext context) {
    final tokens = LineTokens.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: <Widget>[
        const Divider(height: 1),
        Theme(
          // ExpansionTile рисует рамки Material — в «Линии» секция плоская.
          data: Theme.of(context).copyWith(dividerColor: Colors.transparent),
          child: ExpansionTile(
            title: Text(title, style: lineText(size: 16, color: tokens.ink)),
            initiallyExpanded: initiallyExpanded,
            tilePadding: EdgeInsets.zero,
            childrenPadding: const EdgeInsets.only(bottom: 12),
            backgroundColor: Colors.transparent,
            collapsedBackgroundColor: Colors.transparent,
            shape: const Border(),
            collapsedShape: const Border(),
            iconColor: tokens.muted,
            collapsedIconColor: tokens.muted,
            expansionAnimationStyle: const AnimationStyle(
              duration: Duration(milliseconds: 180),
              curve: Curves.easeOut,
            ),
            children: <Widget>[
              Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: children,
              ),
            ],
          ),
        ),
      ],
    );
  }
}

/// Отказ ядра словами человека.
///
/// Свой отказ служба пишет по-русски и объясняет причину: «нет такого способа
/// распознавания», «аудиофайл не найден». Пересказывать его своими словами —
/// значит потерять причину, поэтому тело ответа показывается как есть. Своя
/// строка остаётся на случай, когда ответа не было вовсе.
String apiFailure(Object error) {
  if (error is HttpException && error.message.trim().isNotEmpty) {
    return lineSentence(error.message);
  }
  return 'Не удалось обратиться к API. Проверьте подключение.';
}

/// Фраза ядра отдельной строкой: его причины начинаются со строчной буквы
/// («не удалось распознать: …»), а в окне стоят целой строкой.
String lineSentence(String text) {
  final trimmed = text.trim();
  if (trimmed.isEmpty) return '';
  return trimmed[0].toUpperCase() + trimmed.substring(1);
}

/// Тост: снизу по центру, капсула, живёт 5 секунд, больше трёх не копится.
void lineToast(
  BuildContext context,
  String text, {
  LineTone tone = LineTone.success,
}) {
  final messenger = ScaffoldMessenger.maybeOf(context);
  if (messenger == null) return;
  final tokens = LineTokens.of(context);
  // Новый тост заменяет прежний: очередь тостов показывала бы «сохранено»
  // через пять секунд после нажатия — то есть уже неправду.
  messenger
    ..removeCurrentSnackBar()
    ..showSnackBar(
      SnackBar(
        duration: const Duration(seconds: 5),
        padding: EdgeInsets.zero,
        content: Center(
          child: Container(
            padding: const EdgeInsets.symmetric(horizontal: 18, vertical: 12),
            decoration: BoxDecoration(
              color: tokens.toast,
              borderRadius: BorderRadius.circular(LineTokens.pill),
              boxShadow: LineTokens.toastShadow,
            ),
            child: Row(
              mainAxisSize: MainAxisSize.min,
              children: <Widget>[
                Icon(lineToneIcon(tone), size: 18, color: tokens.tone(tone)),
                const SizedBox(width: 10),
                Flexible(
                  child: Text(
                    text,
                    style: lineText(size: 14, color: tokens.ink),
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
}

/// Вопрос человеку — вызовом, а не виджетом в дереве.
///
/// Необратимое действие показывается списком последствий и подтверждается
/// словами «Да, я уверен»: «Удалить» нажимают не глядя.
