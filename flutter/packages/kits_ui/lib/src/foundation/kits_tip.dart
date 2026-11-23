import 'package:flutter/material.dart';

import '../tokens.dart';
import 'kits_text.dart';

/// Подсказка кита: всплывает над элементом и повторяет доступное имя.
///
/// Браузерного `title` во Flutter нет вовсе, поэтому подсказка — единственный
/// способ показать подпись значка глазам и диктору одновременно.
class KitsTip extends StatelessWidget {
  const KitsTip({
    super.key,
    required this.message,
    required this.child,
    this.preferBelow = false,
  });

  final String message;
  final Widget child;
  final bool preferBelow;

  @override
  Widget build(BuildContext context) {
    final tokens = KitsTokens.of(context);
    return Tooltip(
      message: message,
      preferBelow: preferBelow,
      waitDuration: Duration.zero,
      textStyle: KitsType.of(
        context,
        font: '--ck-font-hint',
        color: tokens.color('--ck-bg'),
      ),
      decoration: BoxDecoration(
        color: tokens.color('--ck-text'),
        borderRadius: BorderRadius.circular(tokens.length('--ck-radius-chip')),
      ),
      child: child,
    );
  }
}
