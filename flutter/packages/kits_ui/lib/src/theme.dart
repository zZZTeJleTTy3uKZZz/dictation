import 'package:flutter/material.dart';

import 'tokens.dart';

export 'tokens.dart';

/// Собирает [ThemeData] кита: токены, цвета Material и текстовые стили.
///
/// Виджеты кита берут всё из токенов через [KitsTokens.of], поэтому тема не
/// несёт ни вкусовщины, ни чисел: единственный источник — `theme.css`.
ThemeData kitsTheme({
  Brightness brightness = Brightness.light,
  String? fontFamily,
}) {
  final tokens = KitsTokens.byBrightness(brightness);
  final base = ThemeData(
    useMaterial3: true,
    brightness: brightness,
    extensions: <ThemeExtension<dynamic>>[tokens],
    fontFamily: fontFamily ?? tokens.fontFamily('--ck-font'),
    scaffoldBackgroundColor: tokens.color('--ck-bg'),
    canvasColor: tokens.color('--ck-bg'),
    dividerColor: tokens.color('--ck-divider'),
    visualDensity: VisualDensity.standard,
  );
  final colorScheme = base.colorScheme.copyWith(
    primary: tokens.color('--ck-accent-fill'),
    onPrimary: tokens.color('--ck-on-fill'),
    secondary: tokens.color('--ck-accent-strong'),
    surface: tokens.color('--ck-bg'),
    onSurface: tokens.color('--ck-text'),
    error: tokens.color('--ck-danger-strong'),
    onError: tokens.color('--ck-on-fill'),
    outline: tokens.color('--ck-border'),
  );
  return base.copyWith(
    colorScheme: colorScheme,
    textTheme: base.textTheme.apply(
      bodyColor: tokens.color('--ck-text'),
      displayColor: tokens.color('--ck-text'),
    ),
    splashFactory: InkSparkle.splashFactory,
    focusColor: tokens.color('--ck-focus'),
  );
}

/// Светлая тема кита.
ThemeData get kitsLightTheme => kitsTheme();

/// Тёмная тема кита.
ThemeData get kitsDarkTheme => kitsTheme(brightness: Brightness.dark);
