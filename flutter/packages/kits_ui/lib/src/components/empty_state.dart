import 'package:flutter/material.dart';

import '../foundation/kits_text.dart';
import '../tokens.dart';

/// Что означает пустой экран: данных ещё нет или поиск ничего не нашёл.
enum KitsEmptyStateKind { nothingYet, nothingFound }

/// Как стоять пустому состоянию: блоком в экране или строкой в списке.
enum KitsEmptyStateLayout { block, inline }

/// Пустое состояние кита: одна фраза и одно действие.
///
/// У «ничего не найдено» действия нет: новая запись — не ответ на пустой
/// поиск. Сброс фильтра живёт в тулбаре. Пустые слоты не рисуются.
class KitsEmptyState extends StatelessWidget {
  const KitsEmptyState({
    super.key,
    required this.text,
    this.kind = KitsEmptyStateKind.nothingYet,
    this.layout = KitsEmptyStateLayout.block,
    this.action,
    this.icon,
  });

  /// Экран, где данных ещё нет: значок, фраза и действие.
  const KitsEmptyState.nothingYet({
    super.key,
    this.text = 'Записей пока нет',
    this.layout = KitsEmptyStateLayout.block,
    this.action,
    this.icon,
  }) : kind = KitsEmptyStateKind.nothingYet;

  /// Пустой поиск: фраза без действия.
  const KitsEmptyState.nothingFound({
    super.key,
    this.text = 'Ничего не нашлось',
    this.layout = KitsEmptyStateLayout.block,
    this.icon,
  }) : kind = KitsEmptyStateKind.nothingFound,
       action = null;

  /// Единственная фраза: что здесь появится и как это получить.
  final String text;

  final KitsEmptyStateKind kind;
  final KitsEmptyStateLayout layout;

  /// Одно действие; у «ничего не найдено» не рисуется.
  final Widget? action;

  /// Значок над фразой (в inline — слева); по умолчанию свой для вида.
  final Widget? icon;

  static const Key iconKey = Key('kits-empty-icon');
  static const Key actionKey = Key('kits-empty-action');

  @override
  Widget build(BuildContext context) {
    final tokens = KitsTokens.of(context);
    final isInline = layout == KitsEmptyStateLayout.inline;
    final showAction = kind == KitsEmptyStateKind.nothingYet && action != null;
    final iconWidget =
        icon ??
        Icon(
          kind == KitsEmptyStateKind.nothingFound
              ? Icons.search_off
              : Icons.inbox_outlined,
          size: isInline ? 20 : 32,
          color: tokens.color('--ck-text-meta'),
        );

    final phrase = Flexible(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 420),
        child: Text(
          text,
          textAlign: isInline ? TextAlign.start : TextAlign.center,
          style: KitsType.of(
            context,
            font: '--ck-font-control',
            leading: '--ck-leading-relaxed',
            color: tokens.color('--ck-text-secondary'),
          ),
        ),
      ),
    );

    final actionWidget = showAction
        ? KeyedSubtree(key: actionKey, child: action!)
        : null;

    if (isInline) {
      return Padding(
        padding: EdgeInsets.symmetric(
          horizontal: tokens.length('--ck-space-4'),
          vertical: tokens.length('--ck-space-3'),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: <Widget>[
            KeyedSubtree(key: iconKey, child: iconWidget),
            SizedBox(width: tokens.length('--ck-space-3')),
            phrase,
            if (actionWidget != null) ...<Widget>[
              SizedBox(width: tokens.length('--ck-space-4')),
              actionWidget,
            ],
          ],
        ),
      );
    }

    return Padding(
      padding: EdgeInsets.symmetric(
        horizontal: tokens.length('--ck-space-4'),
        vertical: tokens.length('--ck-space-6'),
      ),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: <Widget>[
          KeyedSubtree(key: iconKey, child: iconWidget),
          SizedBox(height: tokens.length('--ck-space-3')),
          phrase,
          if (actionWidget != null) ...<Widget>[
            SizedBox(height: tokens.length('--ck-space-4')),
            actionWidget,
          ],
        ],
      ),
    );
  }
}
