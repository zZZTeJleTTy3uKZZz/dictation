import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../foundation/kits_focus_ring.dart';
import '../foundation/kits_text.dart';
import '../tokens.dart';

/// Выключатель настройки: тумблер с подписью.
///
/// Подпись нажимается вместе с тумблером и связана с ним, состояние читается
/// диктором как переключатель. Enter, в отличие от Space, состояние не меняет —
/// как у нативного `input type=checkbox`.
class KitsSwitch extends StatefulWidget {
  const KitsSwitch({
    super.key,
    required this.label,
    required this.value,
    this.onChanged,
    this.disabled = false,
    this.after,
    this.focusNode,
  });

  /// Подпись внутри label: без неё у выключателя нет видимого имени.
  final String label;

  final bool value;

  /// Новое значение после нажатия или Space.
  final ValueChanged<bool>? onChanged;

  final bool disabled;

  /// Узел рядом с подписью, но вне нажатия: значок пояснения, ссылка, счётчик.
  final Widget? after;

  final FocusNode? focusNode;

  /// Ширина и высота тумблера — правило `ck-switch` кита, не размер контрола.
  static const double trackWidth = 36;
  static const double trackHeight = 20;

  static const Key trackKey = Key('kits-switch-track');
  static const Key thumbKey = Key('kits-switch-thumb');

  @override
  State<KitsSwitch> createState() => _KitsSwitchState();
}

class _KitsSwitchState extends State<KitsSwitch> {
  FocusNode? _ownedFocus;
  bool _showFocus = false;

  FocusNode get _focusNode =>
      widget.focusNode ?? (_ownedFocus ??= FocusNode(debugLabel: 'KitsSwitch'));

  bool get _enabled => !widget.disabled && widget.onChanged != null;

  @override
  void dispose() {
    _ownedFocus?.dispose();
    super.dispose();
  }

  void _toggle() {
    if (!_enabled) return;
    widget.onChanged!(!widget.value);
  }

  @override
  Widget build(BuildContext context) {
    final tokens = KitsTokens.of(context);
    final labelColor = widget.disabled
        ? tokens.color('--ck-text-secondary')
        : tokens.color('--ck-text');

    // Строка подписи высотой с тумблер, как `line-height: 1.25rem` у
    // `.ck-switch-label` в вебе: с межстрочным `--ck-leading-normal` буквы
    // съезжают с линии тумблера на полтора пикселя.
    final labelStyle = KitsType.of(
      context,
      color: labelColor,
      font: '--ck-font-control',
    ).copyWith(
      height: KitsSwitch.trackHeight / tokens.length('--ck-font-control'),
    );

    final track = AnimatedContainer(
      key: KitsSwitch.trackKey,
      duration: tokens.duration('--ck-menu-ease'),
      curve: tokens.curve('--ck-move-ease'),
      width: KitsSwitch.trackWidth,
      height: KitsSwitch.trackHeight,
      padding: const EdgeInsets.all(2),
      decoration: BoxDecoration(
        color: widget.value
            ? tokens.color('--ck-accent')
            : tokens.color('--ck-divider'),
        borderRadius: BorderRadius.circular(tokens.length('--ck-radius-full')),
      ),
      child: AnimatedAlign(
        duration: tokens.duration('--ck-menu-ease'),
        curve: tokens.curve('--ck-move-ease'),
        alignment: widget.value ? Alignment.centerRight : Alignment.centerLeft,
        child: Container(
          key: KitsSwitch.thumbKey,
          width: 16,
          height: 16,
          decoration: BoxDecoration(
            color: tokens.color('--ck-on-dark'),
            shape: BoxShape.circle,
          ),
        ),
      ),
    );

    Widget thumb = FocusableActionDetector(
      focusNode: _focusNode,
      enabled: _enabled,
      onShowFocusHighlight: (value) => setState(() => _showFocus = value),
      mouseCursor: _enabled
          ? SystemMouseCursors.click
          : SystemMouseCursors.forbidden,
      shortcuts: const <ShortcutActivator, Intent>{
        SingleActivator(LogicalKeyboardKey.space): _ToggleSwitchIntent(),
      },
      actions: <Type, Action<Intent>>{
        _ToggleSwitchIntent: CallbackAction<_ToggleSwitchIntent>(
          onInvoke: (_) {
            _toggle();
            return null;
          },
        ),
      },
      child: GestureDetector(
        onTap: _enabled ? _toggle : null,
        child: MouseRegion(
          cursor: _enabled
              ? SystemMouseCursors.click
              : SystemMouseCursors.forbidden,
          child: KitsFocusRing(
            focused: _showFocus,
            borderRadius: BorderRadius.circular(tokens.length('--ck-radius-full')),
            child: track,
          ),
        ),
      ),
    );

    if (widget.disabled) {
      thumb = Opacity(opacity: 0.5, child: thumb);
    }

    final content = ConstrainedBox(
      // Строка не ниже зоны нажатия `--ck-target-min`: у соседей в столбце
      // остаётся зазор, и тумблеры не налезают друг на друга.
      constraints: BoxConstraints(
        minHeight: tokens.length('--ck-target-min'),
      ),
      child: Row(
        children: <Widget>[
          Expanded(
            child: GestureDetector(
              behavior: HitTestBehavior.opaque,
              onTap: _enabled ? _toggle : null,
              child: MouseRegion(
                cursor: _enabled
                    ? SystemMouseCursors.click
                    : SystemMouseCursors.forbidden,
                child: Text(
                  widget.label,
                  style: labelStyle,
                ),
              ),
            ),
          ),
          if (widget.after != null) ...<Widget>[
            SizedBox(width: tokens.length('--ck-space-2')),
            widget.after!,
          ],
          SizedBox(width: tokens.length('--ck-space-3')),
          thumb,
        ],
      ),
    );

    return Semantics(
      container: true,
      label: widget.label,
      toggled: widget.value,
      enabled: _enabled,
      onTap: _enabled ? _toggle : null,
      child: content,
    );
  }
}


/// Намерение «переключить выключатель»: только Space, не Enter.
class _ToggleSwitchIntent extends Intent {
  const _ToggleSwitchIntent();
}
