import 'package:flutter/material.dart';

import '../foundation/kits_focus_ring.dart';
import '../foundation/kits_text.dart';
import '../foundation/kits_tip.dart';
import '../tokens.dart';

/// Вариант кнопки: тон говорит, что она значит.
enum KitsButtonVariant {
  primary,
  secondary,
  quiet,
  danger,
  link,
  iconDefault,
  iconAccent,
  iconDanger,
  iconOnDark,
}

/// Размер кнопки: высота из токенов контролов.
enum KitsButtonSize { sm, md, lg }

/// Кнопка кита — обычная и кнопка-значок.
///
/// Один вид на все действия: тон из [variant], высота из [size], загрузка
/// запрещает повторное нажатие. Кнопка-значок обязана иметь подпись
/// [semanticLabel]: она уходит диктору и в подсказку.
class KitsButton extends StatefulWidget {
  const KitsButton({
    super.key,
    this.label,
    this.semanticLabel,
    this.icon,
    this.iconContent,
    this.badge,
    this.variant = KitsButtonVariant.secondary,
    this.size = KitsButtonSize.md,
    this.loading = false,
    this.disabled = false,
    this.block = false,
    this.wrap = false,
    this.onClick,
    this.focusNode,
  }) : assert(
          semanticLabel != null ||
              (variant != KitsButtonVariant.iconDefault &&
                  variant != KitsButtonVariant.iconAccent &&
                  variant != KitsButtonVariant.iconDanger &&
                  variant != KitsButtonVariant.iconOnDark),
          'кнопка-значок обязана иметь подпись: semanticLabel',
        );

  /// Подпись кнопки; у кнопки-значка её заменяет [semanticLabel].
  final String? label;

  /// Подпись для диктора и подсказки: обязательна у кнопки-значка.
  final String? semanticLabel;

  /// Значок перед подписью; при загрузке его заменяет крутилка.
  final Widget? icon;

  /// Содержимое кнопки-значка.
  final Widget? iconContent;

  /// Счётчик на кнопке-значке; `null` и ноль не рисуются.
  final Widget? badge;

  final KitsButtonVariant variant;
  final KitsButtonSize size;

  /// Кнопка занята: недоступна, вместо значка крутилка.
  final bool loading;

  final bool disabled;

  /// Растянуть на ширину родителя.
  final bool block;

  /// Разрешить перенос подписи.
  final bool wrap;

  final VoidCallback? onClick;
  final FocusNode? focusNode;

  /// Ключ крутилки загрузки — по нему проверка отличает занятую кнопку.
  static const Key spinnerKey = Key('kits-button-spinner');

  static bool _isIconVariant(KitsButtonVariant variant) =>
      variant == KitsButtonVariant.iconDefault ||
      variant == KitsButtonVariant.iconAccent ||
      variant == KitsButtonVariant.iconDanger ||
      variant == KitsButtonVariant.iconOnDark;

  @override
  State<KitsButton> createState() => _KitsButtonState();
}

class _KitsButtonState extends State<KitsButton> {
  FocusNode? _ownedFocus;
  bool _hovered = false;
  bool _showFocus = false;

  FocusNode get _focusNode =>
      widget.focusNode ?? (_ownedFocus ??= FocusNode(debugLabel: 'KitsButton'));

  bool get _enabled => !widget.disabled && !widget.loading;

  bool get _isIcon => KitsButton._isIconVariant(widget.variant);

  @override
  void dispose() {
    _ownedFocus?.dispose();
    super.dispose();
  }

  double _height(KitsTokens tokens) => switch (widget.size) {
        KitsButtonSize.sm => tokens.length('--ck-control-sm'),
        KitsButtonSize.md => tokens.length('--ck-control-md'),
        KitsButtonSize.lg => tokens.length('--ck-control-lg'),
      };

  ({Color background, Color foreground, bool capsule}) _paint(KitsTokens t) {
    switch (widget.variant) {
      case KitsButtonVariant.primary:
        return (
          background: t.color('--ck-accent-fill'),
          foreground: t.color('--ck-on-fill'),
          capsule: true,
        );
      case KitsButtonVariant.secondary:
        return (
          background: t.color('--ck-accent-soft'),
          foreground: t.color('--ck-accent-strong'),
          capsule: true,
        );
      case KitsButtonVariant.quiet:
        return (
          background: _hovered ? t.color('--ck-bg-hover') : const Color(0x00000000),
          foreground: t.color('--ck-text'),
          capsule: true,
        );
      case KitsButtonVariant.danger:
        return (
          background: t.color('--ck-danger-fill'),
          foreground: t.color('--ck-on-fill'),
          capsule: true,
        );
      case KitsButtonVariant.link:
        return (
          background: const Color(0x00000000),
          foreground: t.color('--ck-accent-strong'),
          capsule: false,
        );
      case KitsButtonVariant.iconDefault:
        return (
          background: _hovered ? t.color('--ck-bg-hover') : const Color(0x00000000),
          foreground: t.color('--ck-text'),
          capsule: true,
        );
      case KitsButtonVariant.iconAccent:
        return (
          background: t.color('--ck-accent-fill'),
          foreground: t.color('--ck-on-fill'),
          capsule: true,
        );
      case KitsButtonVariant.iconDanger:
        return (
          background: t.color('--ck-danger-fill'),
          foreground: t.color('--ck-on-fill'),
          capsule: true,
        );
      case KitsButtonVariant.iconOnDark:
        return (
          background: _hovered ? t.color('--ck-on-dark-hover') : const Color(0x00000000),
          foreground: t.color('--ck-on-dark'),
          capsule: true,
        );
    }
  }

  @override
  Widget build(BuildContext context) {
    final tokens = KitsTokens.of(context);
    final paint = _paint(tokens);
    final height = _height(tokens);
    final radius = paint.capsule
        ? BorderRadius.circular(tokens.length('--ck-radius-button'))
        : BorderRadius.zero;
    final isLink = widget.variant == KitsButtonVariant.link;

    Widget label = const SizedBox.shrink();
    final text = widget.label;
    if (text != null) {
      label = Text(
        text,
        maxLines: widget.wrap ? null : 1,
        softWrap: widget.wrap,
        overflow: TextOverflow.ellipsis,
        textAlign: TextAlign.center,
        style: KitsType.of(
          context,
          font: '--ck-font-control',
          color: paint.foreground,
          weight: FontWeight.w500,
        ).copyWith(
          decoration: isLink && _hovered ? TextDecoration.underline : null,
          decorationColor: paint.foreground,
        ),
      );
    }

    Widget? leading;
    if (widget.loading) {
      leading = SizedBox(
        width: 16,
        height: 16,
        child: CircularProgressIndicator(
          key: KitsButton.spinnerKey,
          strokeWidth: 2,
          color: paint.foreground,
        ),
      );
    } else if (_isIcon && widget.iconContent != null) {
      leading = widget.iconContent;
    } else if (widget.icon != null) {
      leading = widget.icon;
    }

    final Widget? gap = leading != null && text != null
        ? SizedBox(width: tokens.length('--ck-space-2'))
        : null;
    final Widget? labelWidget = text == null
        ? null
        : (widget.block
            ? Flexible(child: label)
            : Flexible(fit: FlexFit.loose, child: label));
    final content = Row(
      mainAxisSize: widget.block ? MainAxisSize.max : MainAxisSize.min,
      mainAxisAlignment: MainAxisAlignment.center,
      children: <Widget>[
        ?leading,
        ?gap,
        ?labelWidget,
      ],
    );

    Widget surface = Container(
      height: isLink ? null : height,
      width: _isIcon ? height : null,
      constraints: _isIcon ? null : const BoxConstraints(),
      padding: isLink
          ? EdgeInsets.zero
          : EdgeInsets.symmetric(
              horizontal: _isIcon ? 0 : tokens.length('--ck-space-4'),
            ),
      decoration: BoxDecoration(
        color: paint.background,
        borderRadius: radius,
      ),
      // Без alignment: контейнер не растягивается на всю ширину, иначе кнопка
      // в Wrap занимала бы строку целиком.
      child: _isIcon ? Center(child: content) : content,
    );

    if (widget.badge != null) {
      surface = Stack(
        clipBehavior: Clip.none,
        children: <Widget>[
          surface,
          Positioned(top: -tokens.length('--ck-space-1'), right: -tokens.length('--ck-space-1'), child: widget.badge!),
        ],
      );
    }

    if (!_enabled) {
      surface = Opacity(opacity: 0.5, child: surface);
    }
    surface = MouseRegion(
      cursor: !_enabled
          ? (widget.loading ? SystemMouseCursors.progress : SystemMouseCursors.forbidden)
          : SystemMouseCursors.click,
      onEnter: (_) => setState(() => _hovered = true),
      onExit: (_) => setState(() => _hovered = false),
      child: surface,
    );

    surface = FocusableActionDetector(
      focusNode: _focusNode,
      enabled: _enabled,
      mouseCursor: SystemMouseCursors.click,
      onShowFocusHighlight: (value) => setState(() => _showFocus = value),
      actions: <Type, Action<Intent>>{
        ActivateIntent: CallbackAction<ActivateIntent>(
          onInvoke: (_) {
            if (_enabled) widget.onClick?.call();
            return null;
          },
        ),
      },
      child: GestureDetector(
        behavior: HitTestBehavior.opaque,
        onTap: _enabled
            ? () {
                // Нажатие мышью тоже ставит фокус на кнопку: так окно,
                // открытое ею, вернёт фокус туда, откуда пришло.
                _focusNode.requestFocus();
                widget.onClick?.call();
              }
            : null,
        child: surface,
      ),
    );

    final tooltip = _isIcon ? (widget.semanticLabel ?? '') : '';
    if (tooltip.isNotEmpty) {
      surface = KitsTip(message: tooltip, child: surface);
    }

    surface = Semantics(
      container: true,
      button: true,
      enabled: _enabled,
      label: widget.semanticLabel ?? widget.label,
      value: widget.loading ? 'Загрузка' : null,
      onTap: _enabled ? widget.onClick : null,
      child: surface,
    );

    surface = KitsFocusRing(
      focused: _showFocus,
      borderRadius: radius,
      child: surface,
    );

    if (widget.block) {
      surface = SizedBox(width: double.infinity, child: surface);
    }
    return surface;
  }
}
