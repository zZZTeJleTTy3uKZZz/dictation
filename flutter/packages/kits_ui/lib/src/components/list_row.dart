import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../foundation/kits_focus_ring.dart';
import '../foundation/kits_text.dart';
import '../tokens.dart';
import 'button.dart';
import 'confirm.dart';

/// Плотность строки списка: высота из токенов строк.
enum KitsListRowDensity { compact, regular, list }

/// Кнопка-значок в слоте действий строки: всегда с подписью-подсказкой.
@immutable
class KitsListRowAction {
  const KitsListRowAction({
    required this.label,
    required this.icon,
    this.onClick,
    this.danger = false,
  });

  final String label;
  final Widget icon;
  final VoidCallback? onClick;
  final bool danger;
}

/// Пункт меню «⋯»: остальные действия и опасное живут здесь.
@immutable
class KitsRowMenuItem {
  const KitsRowMenuItem({
    required this.label,
    this.code,
    this.onSelect,
    this.danger = false,
  });

  final String label;
  final String? code;
  final VoidCallback? onSelect;
  final bool danger;
}

/// Строка списка кита: голова, главная и вторая строки, хвост и действие.
///
/// Нажимается вся строка, хвост живёт вне нажатия: кнопка в хвосте не
/// срабатывает вместе со строкой. Кольцо фокуса рисуется внутрь — иначе его
/// срежет край прокручиваемой колонки.
///
/// Если заданы и [onTap], и [onToggle], Enter и Пробел раскрывают строку, а
/// клик мышью вызывает [onTap]; кнопка «Развернуть»/«Свернуть» делает то же,
/// что Enter. На ширине меньше 480 px на виду остаётся одна кнопка-значок
/// (первая), остальные действия и раскрытие уходят в меню «⋯».
class KitsListRow extends StatefulWidget {
  const KitsListRow({
    super.key,
    this.leading,
    required this.title,
    this.detail,
    this.meta,
    this.trailing,
    this.onTap,
    this.url,
    this.selected = false,
    this.danger = false,
    this.disabled = false,
    this.detailMultiline = false,
    this.trailingShrinks = false,
    this.density = KitsListRowDensity.regular,
    this.focusNode,
    this.actions = const <KitsListRowAction>[],
    this.menu = const <KitsRowMenuItem>[],
    this.onMenu,
    this.onDelete,
    this.deleteTitle = 'Удалить запись?',
    this.deleteConsequences = const <String>[
      'Запись исчезнет из списка',
      'Восстановить её будет нельзя',
    ],
    this.expanded = false,
    this.onToggle,
    this.showExpandAction = true,
  });

  /// Голова строки: значок, лицо, знак канала.
  final Widget? leading;

  /// Главное: имя или название.
  final String title;

  /// Вторая строка: должность, пояснение.
  final String? detail;

  /// Справа сверху мелко: время, счёт.
  final String? meta;

  /// Хвост: тумблер, бейдж, кнопка «⋯» — вне нажатия.
  final Widget? trailing;

  /// Нажатие на строку-кнопку.
  final VoidCallback? onTap;

  /// Строка-ссылка: адрес, который открывает хозяин.
  final String? url;

  final bool selected;
  final bool danger;
  final bool disabled;

  /// Вторая строка переносится, иначе обрезается одной строкой.
  final bool detailMultiline;

  /// Текст в хвосте уступает место заголовку (для кнопок — не нужно).
  final bool trailingShrinks;

  final KitsListRowDensity density;
  final FocusNode? focusNode;

  /// До трёх кнопок-значков справа; на узком экране на виду первая.
  final List<KitsListRowAction> actions;

  /// Пункты меню «⋯»; пустое меню не рисует кнопку.
  final List<KitsRowMenuItem> menu;

  /// Выбор пункта меню, если у пункта нет своего обработчика.
  final ValueChanged<KitsRowMenuItem>? onMenu;

  /// Удаление после опасного подтверждения; без него Delete ничего не делает.
  final Future<void> Function()? onDelete;
  final String deleteTitle;
  final List<String> deleteConsequences;

  /// Строка раскрыта: меняет только значок «Свернуть» и подсказку. Высота
  /// плашки не меняется — полный текст хозяин кладёт под строкой.
  final bool expanded;

  /// Просьба раскрыть или свернуть строку; включает Enter, кнопку-значок и
  /// пункт «Развернуть»/«Свернуть» в меню на узком экране.
  final ValueChanged<bool>? onToggle;

  /// Рисовать кнопку «Развернуть»/«Свернуть» в слоте действий.
  final bool showExpandAction;

  static const Key leadingKey = Key('kits-list-row-leading');
  static const Key detailKey = Key('kits-list-row-detail');
  static const Key metaKey = Key('kits-list-row-meta');
  static const Key trailingKey = Key('kits-list-row-trailing');
  static const Key actionsKey = Key('kits-list-row-actions');
  static const Key menuKey = Key('kits-list-row-menu');
  static const Key expandKey = Key('kits-list-row-expand');

  @override
  State<KitsListRow> createState() => _KitsListRowState();
}

class _KitsListRowState extends State<KitsListRow> {
  FocusNode? _ownedFocus;
  bool _hovered = false;
  bool _showFocus = false;

  bool get _interactive =>
      !widget.disabled &&
      (widget.onTap != null ||
          widget.url != null ||
          widget.onToggle != null ||
          widget.onDelete != null);

  FocusNode get _focusNode =>
      widget.focusNode ??
      (_ownedFocus ??= FocusNode(debugLabel: 'KitsListRow'));

  @override
  void dispose() {
    _ownedFocus?.dispose();
    super.dispose();
  }

  double _height(KitsTokens tokens) => switch (widget.density) {
    KitsListRowDensity.compact => tokens.length('--ck-row-compact'),
    KitsListRowDensity.regular => tokens.length('--ck-row-regular'),
    KitsListRowDensity.list => tokens.length('--ck-row-list'),
  };

  void _toggle() => widget.onToggle?.call(!widget.expanded);

  void _activate() {
    // Enter и Пробел на строке с раскрытием раскрывают её; клик мышью ниже
    // по-прежнему зовёт onTap, если он задан.
    if (widget.onToggle != null) {
      _toggle();
    } else {
      widget.onTap?.call();
    }
  }

  Future<void> _askDelete() async {
    final remove = widget.onDelete;
    if (remove == null) return;
    await askDanger(
      context,
      title: widget.deleteTitle,
      text: widget.title,
      consequences: widget.deleteConsequences,
      onConfirm: remove,
    );
  }

  Future<void> _openMenu(List<KitsRowMenuItem> items) async {
    if (items.isEmpty) return;
    final box = context.findRenderObject();
    final overlay = Overlay.of(context).context.findRenderObject();
    if (box is! RenderBox || overlay is! RenderBox) return;
    final rect = Rect.fromPoints(
      box.localToGlobal(Offset.zero, ancestor: overlay),
      box.localToGlobal(box.size.bottomRight(Offset.zero), ancestor: overlay),
    );
    final picked = await showMenu<KitsRowMenuItem>(
      context: context,
      position: RelativeRect.fromRect(rect, Offset.zero & overlay.size),
      items: <PopupMenuEntry<KitsRowMenuItem>>[
        for (final item in items)
          PopupMenuItem<KitsRowMenuItem>(
            value: item,
            child: Text(
              item.label,
              style: KitsType.of(
                context,
                font: '--ck-font-control',
                color: item.danger
                    ? KitsTokens.of(context).color('--ck-danger-strong')
                    : KitsTokens.of(context).color('--ck-text'),
              ),
            ),
          ),
      ],
    );
    if (picked == null) return;
    if (picked.onSelect != null) {
      picked.onSelect!();
    } else {
      widget.onMenu?.call(picked);
    }
  }

  String _titleFont() => switch (widget.density) {
    KitsListRowDensity.compact => '--ck-font-control',
    KitsListRowDensity.regular => '--ck-font-row',
    KitsListRowDensity.list => '--ck-font-text',
  };

  double _leadWidth(KitsTokens tokens) => switch (widget.density) {
    KitsListRowDensity.compact => tokens.length('--ck-avatar-sm'),
    KitsListRowDensity.regular => tokens.length('--ck-row-lead'),
    KitsListRowDensity.list => tokens.length('--ck-avatar-xl'),
  };

  @override
  Widget build(BuildContext context) {
    assert(widget.actions.length <= 3, 'на виду не больше трёх кнопок-значков');
    final tokens = KitsTokens.of(context);
    final background = widget.selected
        ? tokens.color('--ck-accent-soft')
        : (_hovered && _interactive
              ? tokens.color('--ck-bg-hover')
              : const Color(0x00000000));
    final titleColor = widget.danger
        ? tokens.color('--ck-danger-strong')
        : tokens.color('--ck-text');

    Widget main = Row(
      children: <Widget>[
        if (widget.leading != null) ...<Widget>[
          SizedBox(
            key: KitsListRow.leadingKey,
            width: _leadWidth(tokens),
            child: widget.leading,
          ),
          SizedBox(width: tokens.length('--ck-row-gap')),
        ],
        Expanded(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: <Widget>[
              // Заголовок всегда одна строка, даже раскрытым: иначе длинная
              // первая строка растягивает плашку, и кнопки уезжают в центр
              // высокой строки. Полный текст раскрытая строка показывает сама
              // (тело под плашкой), а не разворачивает заголовок.
              Text(
                widget.title,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: KitsType.of(
                  context,
                  font: _titleFont(),
                  color: titleColor,
                ),
              ),
              if (widget.detail != null)
                Text(
                  widget.detail!,
                  key: KitsListRow.detailKey,
                  maxLines: widget.detailMultiline ? null : 1,
                  overflow: widget.detailMultiline
                      ? null
                      : TextOverflow.ellipsis,
                  style: KitsType.of(
                    context,
                    font: '--ck-font-hint',
                    color: tokens.color('--ck-text-secondary'),
                  ),
                ),
            ],
          ),
        ),
      ],
    );

    if (_interactive) {
      main = FocusableActionDetector(
        focusNode: _focusNode,
        onShowFocusHighlight: (value) => setState(() => _showFocus = value),
        mouseCursor: SystemMouseCursors.click,
        shortcuts: <ShortcutActivator, Intent>{
          if (widget.onDelete != null)
            const SingleActivator(LogicalKeyboardKey.delete): _DeleteRowIntent(),
          if (widget.expanded)
            const SingleActivator(LogicalKeyboardKey.escape):
                _CollapseRowIntent(),
        },
        actions: <Type, Action<Intent>>{
          ActivateIntent: CallbackAction<ActivateIntent>(
            onInvoke: (_) {
              _activate();
              return null;
            },
          ),
          _DeleteRowIntent: CallbackAction<_DeleteRowIntent>(
            onInvoke: (_) {
              _askDelete();
              return null;
            },
          ),
          _CollapseRowIntent: CallbackAction<_CollapseRowIntent>(
            onInvoke: (_) {
              if (widget.expanded) _toggle();
              return null;
            },
          ),
        },
        child: MouseRegion(
          onEnter: (_) => setState(() => _hovered = true),
          onExit: (_) => setState(() => _hovered = false),
          cursor: SystemMouseCursors.click,
          child: GestureDetector(
            behavior: HitTestBehavior.opaque,
            onTap: widget.onToggle != null && widget.onTap == null
                ? _toggle
                : widget.onTap,
            child: main,
          ),
        ),
      );
      main = KitsFocusRing(focused: _showFocus, inset: true, child: main);
    }

    Widget content = LayoutBuilder(
      builder: (context, constraints) => _surface(
        context,
        tokens,
        background,
        main,
        narrow: constraints.maxWidth < 480,
      ),
    );

    if (widget.disabled) {
      content = Opacity(opacity: 0.5, child: content);
    }

    return Semantics(
      container: true,
      button: _interactive,
      link: widget.url != null,
      enabled: !widget.disabled,
      selected: widget.selected,
      label: widget.title,
      onTap: _interactive ? _activate : null,
      child: content,
    );
  }

  Widget _surface(
    BuildContext context,
    KitsTokens tokens,
    Color background,
    Widget main, {
    required bool narrow,
  }) {
    final visible = narrow && widget.actions.length > 1
        ? widget.actions.take(1).toList(growable: false)
        : widget.actions;
    final menuItems = <KitsRowMenuItem>[
      if (narrow)
        for (final action in widget.actions.skip(visible.length))
          KitsRowMenuItem(
            label: action.label,
            onSelect: action.onClick,
            danger: action.danger,
          ),
      ...widget.menu,
      if (narrow && widget.onToggle != null && widget.showExpandAction)
        KitsRowMenuItem(
          label: widget.expanded ? 'Свернуть' : 'Развернуть',
          onSelect: _toggle,
        ),
    ];
    final showExpand =
        !narrow && widget.onToggle != null && widget.showExpandAction;
    final hasActions =
        visible.isNotEmpty || showExpand || menuItems.isNotEmpty;

    return Container(
      constraints: BoxConstraints(minHeight: _height(tokens)),
      padding: EdgeInsets.symmetric(
        horizontal: tokens.length('--ck-space-3'),
        vertical: tokens.length('--ck-space-2'),
      ),
      decoration: BoxDecoration(
        color: background,
        borderRadius: BorderRadius.circular(tokens.length('--ck-radius-row')),
      ),
      child: Row(
        children: <Widget>[
          Expanded(child: main),
          if (hasActions) ...<Widget>[
            SizedBox(width: tokens.length('--ck-space-2')),
            KeyedSubtree(
              key: KitsListRow.actionsKey,
              child: Row(
                mainAxisSize: MainAxisSize.min,
                children: <Widget>[
                  for (final action in visible)
                    KitsButton(
                      variant: action.danger
                          ? KitsButtonVariant.iconDanger
                          : KitsButtonVariant.iconDefault,
                      size: KitsButtonSize.sm,
                      semanticLabel: action.label,
                      iconContent: action.icon,
                      disabled: widget.disabled,
                      onClick: action.onClick,
                    ),
                  if (showExpand)
                    KitsButton(
                      key: KitsListRow.expandKey,
                      variant: KitsButtonVariant.iconDefault,
                      size: KitsButtonSize.sm,
                      semanticLabel:
                          widget.expanded ? 'Свернуть' : 'Развернуть',
                      iconContent: Icon(
                        widget.expanded
                            ? Icons.keyboard_arrow_up
                            : Icons.keyboard_arrow_down,
                        size: 18,
                      ),
                      disabled: widget.disabled,
                      onClick: _toggle,
                    ),
                  if (menuItems.isNotEmpty)
                    KitsButton(
                      key: KitsListRow.menuKey,
                      variant: KitsButtonVariant.iconDefault,
                      size: KitsButtonSize.sm,
                      semanticLabel: 'Ещё',
                      iconContent: const Icon(Icons.more_horiz, size: 18),
                      disabled: widget.disabled,
                      onClick: () => _openMenu(menuItems),
                    ),
                ],
              ),
            ),
          ],
          if (widget.meta != null) ...<Widget>[
            SizedBox(width: tokens.length('--ck-space-2')),
            Text(
              widget.meta!,
              key: KitsListRow.metaKey,
              style: KitsType.of(
                context,
                font: '--ck-font-micro',
                color: tokens.color('--ck-text-meta'),
              ),
            ),
          ],
          if (widget.trailing != null) ...<Widget>[
            SizedBox(width: tokens.length('--ck-space-2')),
            widget.trailingShrinks
                ? Flexible(
                    child: KeyedSubtree(
                      key: KitsListRow.trailingKey,
                      child: widget.trailing!,
                    ),
                  )
                : KeyedSubtree(
                    key: KitsListRow.trailingKey,
                    child: widget.trailing!,
                  ),
          ],
        ],
      ),
    );
  }
}

class _DeleteRowIntent extends Intent {
  const _DeleteRowIntent();
}

class _CollapseRowIntent extends Intent {
  const _CollapseRowIntent();
}
