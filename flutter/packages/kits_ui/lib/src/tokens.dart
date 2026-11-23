import 'package:flutter/material.dart';

import 'tokens.g.dart';

/// Доступ к дизайн-токенам кита.
///
/// Токен называется так же, как в CSS (`--ck-accent-fill`), — поэтому поиск по
/// спецификации и по коду идёт одним словом. Значение остаётся строкой CSS:
/// виджет переводит его в тип Flutter тем методом, который ему нужен.
///
/// Токены приходят из [Theme]; [KitsTokens.of] отдаёт светлую тему, если
/// расширения в теме нет.
@immutable
class KitsTokens extends ThemeExtension<KitsTokens> {
  const KitsTokens(this.values);

  /// Светлая тема: значения из `theme.css`.
  static const KitsTokens light = KitsTokens(kitsTokensLight);

  /// Тёмная тема: те же имена, тёмная палитра.
  static const KitsTokens dark = KitsTokens(kitsTokensDark);

  /// Все токены темы по именам CSS.
  final Map<String, String> values;

  /// Токены текущей темы.
  static KitsTokens of(BuildContext context) =>
      Theme.of(context).extension<KitsTokens>() ?? light;

  /// Токены темы заданной яркости.
  static KitsTokens byBrightness(Brightness brightness) =>
      brightness == Brightness.dark ? dark : light;

  /// Имена всех токенов кита.
  static List<String> get names => kitsTokenNames;

  /// Значение токена как строка CSS.
  String raw(String name) {
    final value = values[name];
    if (value == null) {
      throw ArgumentError.value(name, 'name', 'токен не объявлен в теме кита');
    }
    return value;
  }

  /// Есть ли такой токен в теме.
  bool has(String name) => values.containsKey(name);

  /// Толщина кольца фокуса (`--ck-focus-width`).
  double get focusWidth => length('--ck-focus-width');

  /// Отступ кольца фокуса наружу (`--ck-focus-offset`).
  double get focusOffset => length('--ck-focus-offset');

  /// Цвет токена.
  Color color(String name) => _parseColor(raw(name), name);

  /// Длина токена в логических пикселях: `rem` считается по 16 px.
  double length(String name) => _parseLength(raw(name), name);

  /// Число токена без единиц: слои, множители строки.
  double number(String name) => double.parse(raw(name));

  /// Время токена.
  Duration duration(String name) => _parseDuration(raw(name), name);

  /// Тени токена (значение `box-shadow` целиком).
  List<BoxShadow> shadows(String name) {
    final value = raw(name);
    if (value == 'none') return const <BoxShadow>[];
    return _splitTopLevel(value).map((part) {
      final tokens = _splitWhitespaceTopLevel(part);
      if (tokens.length < 3) {
        throw ArgumentError.value(name, 'name', 'не разобрать тень «$value»');
      }
      final lengths = <double>[];
      String? colorText;
      for (final token in tokens) {
        final length = _tryLength(token);
        if (length != null) {
          lengths.add(length);
        } else {
          colorText = token;
        }
      }
      if (lengths.length < 2) {
        throw ArgumentError.value(name, 'name', 'не разобрать тень «$value»');
      }
      final offset = Offset(lengths[0], lengths[1]);
      final blur = lengths.length > 2 ? lengths[2] : 0.0;
      final spread = lengths.length > 3 ? lengths[3] : 0.0;
      return BoxShadow(
        color: _parseColor(colorText ?? 'transparent', name),
        offset: offset,
        blurRadius: blur,
        spreadRadius: spread,
      );
    }).toList(growable: false);
  }

  /// Первое семейство шрифта из значения `font-family`.
  String fontFamily(String name) {
    final first = _splitTopLevel(raw(name)).first.trim();
    return first.replaceAll('"', '').replaceAll("'", '');
  }

  /// Кривая движения токена.
  Curve curve(String name) => _parseCurve(raw(name));

  static Color _parseColor(String value, String name) {
    final text = value.trim();
    if (text == 'transparent') return const Color(0x00000000);
    if (text.startsWith('#')) {
      final hex = text.substring(1);
      switch (hex.length) {
        case 3:
          final r = int.parse(hex[0] * 2, radix: 16);
          final g = int.parse(hex[1] * 2, radix: 16);
          final b = int.parse(hex[2] * 2, radix: 16);
          return Color.fromARGB(255, r, g, b);
        case 6:
          return Color(0xFF000000 | int.parse(hex, radix: 16));
        case 8:
          // В CSS порядок #RRGGBBAA, во Flutter ARGB.
          final rgb = int.parse(hex.substring(0, 6), radix: 16);
          final alpha = int.parse(hex.substring(6), radix: 16);
          return Color((alpha << 24) | rgb);
      }
      throw ArgumentError.value(value, name, 'не разобрать цвет');
    }
    final match =
        RegExp(r'^rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)\s*(?:,\s*([\d.]+)\s*)?\)$')
            .firstMatch(text);
    if (match != null) {
      final r = double.parse(match.group(1)!).round();
      final g = double.parse(match.group(2)!).round();
      final b = double.parse(match.group(3)!).round();
      final a = match.group(4) == null
          ? 1.0
          : double.parse(match.group(4)!).clamp(0.0, 1.0);
      return Color.fromARGB((a * 255).round(), r, g, b);
    }
    throw ArgumentError.value(value, name, 'не разобрать цвет');
  }

  static double _parseLength(String value, String name) {
    final length = _tryLength(value.trim());
    if (length == null) {
      throw ArgumentError.value(value, name, 'не разобрать длину');
    }
    return length;
  }

  static double? _tryLength(String token) {
    final match = RegExp(r'^(-?[\d.]+)(px|rem)?$').firstMatch(token);
    if (match == null) return null;
    final number = double.parse(match.group(1)!);
    return match.group(2) == 'rem' ? number * 16 : number;
  }

  static Duration _parseDuration(String value, String name) {
    // Токен времени бывает составным (`150ms cubic-bezier(…)`): берём время.
    final match = RegExp(r'([\d.]+)(ms|s)\b').firstMatch(value);
    if (match != null) {
      final micros = match.group(2) == 's'
          ? (double.parse(match.group(1)!) * 1000000).round()
          : (double.parse(match.group(1)!) * 1000).round();
      return Duration(microseconds: micros);
    }
    throw ArgumentError.value(value, name, 'не разобрать время');
  }

  static Curve _parseCurve(String value) {
    final text = value.trim();
    final match = RegExp(
      r'cubic-bezier\(\s*([\d.]+)\s*,\s*(-?[\d.]+)\s*,\s*([\d.]+)\s*,\s*(-?[\d.]+)\s*\)',
    ).firstMatch(text);
    if (match != null) {
      return Cubic(
        double.parse(match.group(1)!),
        double.parse(match.group(2)!),
        double.parse(match.group(3)!),
        double.parse(match.group(4)!),
      );
    }
    if (text.contains('ease-out')) return Curves.easeOut;
    if (text.contains('ease-in')) return Curves.easeIn;
    if (text.contains('ease')) return Curves.ease;
    if (text.contains('linear')) return Curves.linear;
    return Curves.ease;
  }

  /// Делит значение по пробелам верхнего уровня: `rgba(…)` — один токен.
  static List<String> _splitWhitespaceTopLevel(String value) {
    final parts = <String>[];
    var depth = 0;
    final buffer = StringBuffer();
    for (final rune in value.runes) {
      final char = String.fromCharCode(rune);
      if (char == '(') depth++;
      if (char == ')') depth--;
      if (depth == 0 && (char == ' ' || char == '\t' || char == '\n')) {
        if (buffer.isNotEmpty) {
          parts.add(buffer.toString());
          buffer.clear();
        }
      } else {
        buffer.write(char);
      }
    }
    if (buffer.isNotEmpty) parts.add(buffer.toString());
    return parts;
  }

  /// Делит значение по запятым верхнего уровня: `rgba(…)` не считается.
  static List<String> _splitTopLevel(String value) {
    final parts = <String>[];
    var depth = 0;
    final buffer = StringBuffer();
    for (final rune in value.runes) {
      final char = String.fromCharCode(rune);
      if (char == '(') depth++;
      if (char == ')') depth--;
      if (char == ',' && depth == 0) {
        parts.add(buffer.toString());
        buffer.clear();
      } else {
        buffer.write(char);
      }
    }
    parts.add(buffer.toString());
    return parts;
  }

  @override
  KitsTokens copyWith({Map<String, String>? values}) =>
      KitsTokens(values ?? this.values);

  @override
  KitsTokens lerp(ThemeExtension<KitsTokens>? other, double t) {
    if (other is! KitsTokens || t >= 1) return this;
    if (t <= 0) return other;
    return KitsTokens(Map<String, String>.from(values));
  }

  @override
  bool operator ==(Object other) =>
      other is KitsTokens && other.values == values;

  @override
  int get hashCode => values.hashCode;
}
