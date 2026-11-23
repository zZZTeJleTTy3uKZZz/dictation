import 'package:flutter/material.dart';

import '../foundation/kits_focus_ring.dart';
import '../foundation/kits_text.dart';
import '../foundation/kits_tip.dart';
import '../tokens.dart';

/// Тон метки и фишки: смысл важнее цвета.
enum KitsBadgeTone { neutral, accent, success, warning, danger, info }

/// Вид метки: мягкая заливка, сплошная или рамкой.
enum KitsBadgeAppearance { soft, solid, outline }

/// Размер метки.
enum KitsBadgeSize { xs, sm }

/// Размер фишки.
enum KitsChipSize { xs, sm }

class _Tone {
  const _Tone({
    required this.soft,
    required this.strong,
    required this.fill,
    required this.border,
  });

  final Color soft;
  final Color strong;
  final Color fill;
  final Color border;
}

_Tone _toneOf(KitsTokens tokens, KitsBadgeTone tone) => switch (tone) {
  KitsBadgeTone.neutral => _Tone(
    soft: tokens.color('--ck-bg-second'),
    strong: tokens.color('--ck-text'),
    fill: tokens.color('--ck-text'),
    border: tokens.color('--ck-border'),
  ),
  KitsBadgeTone.accent => _Tone(
    soft: tokens.color('--ck-accent-soft'),
    strong: tokens.color('--ck-accent-strong'),
    fill: tokens.color('--ck-accent-fill'),
    border: tokens.color('--ck-accent-border'),
  ),
  KitsBadgeTone.success => _Tone(
    soft: tokens.color('--ck-success-soft'),
    strong: tokens.color('--ck-success'),
    fill: tokens.color('--ck-success-fill'),
    border: tokens.color('--ck-success-border'),
  ),
  KitsBadgeTone.warning => _Tone(
    soft: tokens.color('--ck-warning-soft'),
    strong: tokens.color('--ck-warning'),
    fill: tokens.color('--ck-warning-fill'),
    border: tokens.color('--ck-warning-border'),
  ),
  KitsBadgeTone.danger => _Tone(
    soft: tokens.color('--ck-danger-soft'),
    strong: tokens.color('--ck-danger-strong'),
    fill: tokens.color('--ck-danger-fill'),
    border: tokens.color('--ck-danger-border'),
  ),
  KitsBadgeTone.info => _Tone(
    soft: tokens.color('--ck-info-soft'),
    strong: tokens.color('--ck-info'),
    fill: tokens.color('--ck-info-fill'),
    border: tokens.color('--ck-info-border'),
  ),
};

/// Метка состояния: тон, точка и счётчик.
///
/// Без текста и без счётчика метка не рисуется. Счётчик больше 99 показывается
/// «99+», а диктору читается полное число. [dotOnly] требует [label]: без слова
/// метка бессмысленна и на экране, и у диктора.
class KitsBadge extends StatelessWidget {
  const KitsBadge({
    super.key,
    this.text,
    this.count,
    this.label,
    this.tone = KitsBadgeTone.neutral,
    this.appearance = KitsBadgeAppearance.soft,
    this.size = KitsBadgeSize.sm,
    this.dot = false,
    this.dotOnly = false,
    this.icon,
  }) : assert(
         !dotOnly || label != null,
         'метка только точкой обязана иметь label',
       );

  final String? text;

  /// Число вместо текста; больше 99 — «99+».
  final int? count;

  /// Доступное имя: обязательно для [dotOnly], иначе берётся из текста.
  final String? label;

  final KitsBadgeTone tone;
  final KitsBadgeAppearance appearance;
  final KitsBadgeSize size;
  final bool dot;
  final bool dotOnly;
  final Widget? icon;

  static const Key countKey = Key('kits-badge-count');
  static const Key dotKey = Key('kits-badge-dot');

  @override
  Widget build(BuildContext context) {
    final tokens = KitsTokens.of(context);
    if (dotOnly) {
      return KitsTip(
        message: label!,
        child: Semantics(
          label: label,
          child: SizedBox(
            width: 10,
            height: 10,
            child: DecoratedBox(
              key: dotKey,
              decoration: BoxDecoration(
                color: _toneOf(tokens, tone).strong,
                shape: BoxShape.circle,
              ),
            ),
          ),
        ),
      );
    }

    if ((text == null || text!.isEmpty) && count == null) {
      return const SizedBox.shrink();
    }

    final colors = _toneOf(tokens, tone);
    final height = size == KitsBadgeSize.xs ? 18.0 : 22.0;
    final background = switch (appearance) {
      KitsBadgeAppearance.soft => colors.soft,
      KitsBadgeAppearance.solid => colors.fill,
      KitsBadgeAppearance.outline => const Color(0x00000000),
    };
    final foreground = switch (appearance) {
      KitsBadgeAppearance.soft => colors.strong,
      KitsBadgeAppearance.solid => tokens.color('--ck-on-fill'),
      KitsBadgeAppearance.outline => colors.strong,
    };
    final textValue = count == null ? text! : (count! > 99 ? '99+' : '$count');
    final semanticsValue = count == null ? null : count.toString();

    Widget content = Row(
      mainAxisSize: MainAxisSize.min,
      children: <Widget>[
        if (dot) ...<Widget>[
          SizedBox(
            key: dotKey,
            width: 6,
            height: 6,
            child: DecoratedBox(
              decoration: BoxDecoration(
                color: foreground,
                shape: BoxShape.circle,
              ),
            ),
          ),
          SizedBox(width: tokens.length('--ck-space-1')),
        ],
        if (icon != null) ...<Widget>[
          IconTheme.merge(
            data: IconThemeData(size: 12, color: foreground),
            child: icon!,
          ),
          SizedBox(width: tokens.length('--ck-space-1')),
        ],
        Text(
          textValue,
          key: count == null ? null : countKey,
          maxLines: 1,
          softWrap: false,
          overflow: TextOverflow.ellipsis,
          style: KitsType.of(
            context,
            font: '--ck-font-micro',
            leading: '--ck-leading-tight',
            color: foreground,
            weight: FontWeight.w500,
          ),
        ),
      ],
    );

    return Semantics(
      label: label ?? text,
      value: semanticsValue,
      child: Container(
        height: height,
        padding: EdgeInsets.symmetric(
          horizontal: tokens.length('--ck-space-2'),
        ),
        decoration: BoxDecoration(
          color: background,
          border: appearance == KitsBadgeAppearance.outline
              ? Border.all(color: colors.border)
              : null,
          borderRadius: BorderRadius.circular(
            tokens.length('--ck-radius-full'),
          ),
        ),
        child: Align(
          alignment: Alignment.center,
          widthFactor: 1,
          heightFactor: 1,
          child: content,
        ),
      ),
    );
  }
}

/// Фишка кита: пометка, переключатель или снимаемая фишка фильтра.
///
/// Крестик снятия — отдельная кнопка рядом с ярлыком, а не кнопка в кнопке:
/// он снимает фишку и не переключает фильтр. У снимаемой фишки два шага
/// табуляции — сама фишка (если она переключатель) и крестик.
class KitsChip extends StatefulWidget {
  const KitsChip({
    super.key,
    required this.label,
    this.icon,
    this.selected = false,
    this.onClick,
    this.onRemove,
    this.disabled = false,
    this.size = KitsChipSize.sm,
  });

  final String label;
  final Widget? icon;
  final bool selected;

  /// Нажатие фишки-переключателя; без него и без [onRemove] фишка — ярлык.
  final VoidCallback? onClick;

  /// Снятие фишки: отдельная кнопка-крестик с именем «Снять: <label>».
  final VoidCallback? onRemove;

  final bool disabled;
  final KitsChipSize size;

  static const Key removeKey = Key('kits-chip-remove');

  @override
  State<KitsChip> createState() => _KitsChipState();
}

class _KitsChipState extends State<KitsChip> {
  FocusNode? _toggleFocus;
  FocusNode? _removeFocus;
  bool _hovered = false;
  bool _toggleRing = false;
  bool _removeRing = false;

  bool get _interactive =>
      !widget.disabled && (widget.onClick != null || widget.onRemove != null);

  FocusNode get _toggleNode =>
      _toggleFocus ??= FocusNode(debugLabel: 'KitsChipToggle');

  FocusNode get _removeNode =>
      _removeFocus ??= FocusNode(debugLabel: 'KitsChipRemove');

  @override
  void dispose() {
    _toggleFocus?.dispose();
    _removeFocus?.dispose();
    super.dispose();
  }

  void _remove() {
    widget.onRemove?.call();
    // После снятия фокус уходит на соседнюю фишку или в поле, а не на body.
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted) return;
      FocusScope.of(context).nextFocus();
    });
  }

  @override
  Widget build(BuildContext context) {
    final tokens = KitsTokens.of(context);
    final height = widget.size == KitsChipSize.xs ? 22.0 : 28.0;
    final background = widget.selected
        ? tokens.color('--ck-accent-fill')
        : tokens.color('--ck-field-bg');
    final foreground = widget.selected
        ? tokens.color('--ck-on-fill')
        : tokens.color('--ck-text');

    Widget label = Row(
      mainAxisSize: MainAxisSize.min,
      children: <Widget>[
        if (widget.icon != null) ...<Widget>[
          IconTheme.merge(
            data: IconThemeData(size: 14, color: foreground),
            child: widget.icon!,
          ),
          SizedBox(width: tokens.length('--ck-space-1')),
        ],
        Flexible(
          child: Text(
            widget.label,
            maxLines: 1,
            softWrap: false,
            overflow: TextOverflow.ellipsis,
            style: KitsType.of(
              context,
              font: '--ck-font-hint',
              leading: '--ck-leading-tight',
              color: foreground,
              weight: FontWeight.w500,
            ),
          ),
        ),
      ],
    );

    Widget surface = Container(
      height: height,
      padding: EdgeInsets.symmetric(horizontal: tokens.length('--ck-space-2')),
      decoration: BoxDecoration(
        color: widget.selected
            ? background
            : (_hovered && _interactive
                  ? tokens.color('--ck-field-bg-hover')
                  : background),
        borderRadius: BorderRadius.circular(tokens.length('--ck-radius-full')),
      ),
      child: Align(
        alignment: Alignment.center,
        widthFactor: 1,
        heightFactor: 1,
        child: label,
      ),
    );

    if (widget.onClick != null) {
      surface = FocusableActionDetector(
        focusNode: _toggleNode,
        enabled: !widget.disabled,
        onShowFocusHighlight: (value) => setState(() => _toggleRing = value),
        actions: <Type, Action<Intent>>{
          ActivateIntent: CallbackAction<ActivateIntent>(
            onInvoke: (_) {
              if (!widget.disabled) widget.onClick?.call();
              return null;
            },
          ),
        },
        child: GestureDetector(
          behavior: HitTestBehavior.opaque,
          onTap: widget.disabled
              ? null
              : () {
                  _toggleNode.requestFocus();
                  widget.onClick?.call();
                },
          child: surface,
        ),
      );
      surface = KitsFocusRing(
        focused: _toggleRing,
        borderRadius: BorderRadius.circular(tokens.length('--ck-radius-full')),
        child: surface,
      );
      surface = Semantics(
        button: true,
        enabled: !widget.disabled,
        toggled: widget.selected,
        label: widget.label,
        child: surface,
      );
    }

    Widget chip = MouseRegion(
      cursor: _interactive
          ? SystemMouseCursors.click
          : SystemMouseCursors.basic,
      onEnter: (_) => setState(() => _hovered = true),
      onExit: (_) => setState(() => _hovered = false),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: <Widget>[
          surface,
          if (widget.onRemove != null) ...<Widget>[
            SizedBox(width: tokens.length('--ck-space-1')),
            KitsTip(
              message: 'Снять: ${widget.label}',
              child: FocusableActionDetector(
                focusNode: _removeNode,
                enabled: !widget.disabled,
                onShowFocusHighlight: (value) =>
                    setState(() => _removeRing = value),
                actions: <Type, Action<Intent>>{
                  ActivateIntent: CallbackAction<ActivateIntent>(
                    onInvoke: (_) {
                      if (!widget.disabled) _remove();
                      return null;
                    },
                  ),
                },
                child: GestureDetector(
                  key: KitsChip.removeKey,
                  behavior: HitTestBehavior.opaque,
                  onTap: widget.disabled ? null : _remove,
                  child: Semantics(
                    button: true,
                    enabled: !widget.disabled,
                    label: 'Снять: ${widget.label}',
                    child: SizedBox(
                      width: 20,
                      height: 20,
                      child: Icon(
                        Icons.close,
                        size: 14,
                        color: widget.selected
                            ? tokens.color('--ck-on-fill')
                            : tokens.color('--ck-text-secondary'),
                      ),
                    ),
                  ),
                ),
              ),
            ),
            if (_removeRing) const SizedBox.shrink(),
          ],
        ],
      ),
    );

    if (widget.onRemove != null && _removeRing) {
      chip = Stack(
        clipBehavior: Clip.none,
        children: <Widget>[
          chip,
          Positioned(
            right: 0,
            top: 0,
            bottom: 0,
            child: IgnorePointer(
              child: KitsFocusRing(
                focused: true,
                borderRadius: BorderRadius.circular(
                  tokens.length('--ck-radius-full'),
                ),
                child: const SizedBox(width: 20, height: 20),
              ),
            ),
          ),
        ],
      );
    }

    if (widget.disabled) chip = Opacity(opacity: 0.5, child: chip);
    return chip;
  }
}
