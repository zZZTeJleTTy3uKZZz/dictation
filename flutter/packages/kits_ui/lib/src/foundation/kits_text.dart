import 'package:flutter/widgets.dart';

import '../tokens.dart';

/// Текстовые стили кита из токенов шкалы.
///
/// Компонент называет роль (`--ck-font-control`), а не число: поменять размер
/// всех кнопок — правка в `theme.css`, а не в восьми виджетах.
abstract final class KitsType {
  /// Стиль текста роли [font] с цветом [color] и межстрочным [leading].
  static TextStyle of(
    BuildContext context, {
    String font = '--ck-font-control',
    String leading = '--ck-leading-normal',
    Color? color,
    FontWeight? weight,
  }) {
    final tokens = KitsTokens.of(context);
    return TextStyle(
      fontFamily: tokens.fontFamily('--ck-font'),
      fontSize: tokens.length(font),
      height: tokens.number(leading),
      fontWeight: weight ?? FontWeight.w400,
      color: color ?? tokens.color('--ck-text'),
    );
  }
}
