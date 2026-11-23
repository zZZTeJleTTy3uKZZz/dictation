import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../foundation/kits_focus_ring.dart';
import '../foundation/kits_layers.dart';
import '../foundation/kits_text.dart';
import '../tokens.dart';

/// Вид строки выбора: поле формы, строка списка или текст в таблице.
enum KitsSelectAppearance { field, row, text }

/// Вариант выбора.
class KitsSelectOption {
  const KitsSelectOption({
    required this.id,
    required this.label,
    this.disabled = false,
  });

  final String id;
  final String label;
  final bool disabled;
}

/// Раздел панели: заголовок и варианты.
class KitsSelectSection {
  const KitsSelectSection({this.title, required this.options});

  final String? title;
  final List<KitsSelectOption> options;
}

/// Выбор из списка кита: `Select` без поиска и `Combobox` с поиском.
///
/// Строка-кнопка показывает выбранное, панель строится разделами, поиск ведёт
/// хозяин. Панель живёт порталом: сама переворачивается у нижнего края и не
/// выходит за окно. Escape закрывает только верхний слой, фокус возвращается
/// строке.
class KitsSelect extends StatefulWidget {
  const KitsSelect({
    super.key,
    this.sections = const <KitsSelectSection>[],
    this.value,
    this.values = const <String>{},
    this.multiple = false,
    this.placeholder = 'Выберите',
    this.searchable = false,
    this.status,
    this.actionLabel,
    this.onAction,
    this.disabled = false,
    this.appearance = KitsSelectAppearance.field,
    this.searchPlaceholder = 'Поиск',
    this.onSearchChanged,
    this.onChanged,
    this.onPick,
    this.onOpenChange,
    this.onClear,
    this.focusNode,
  });

  final List<KitsSelectSection> sections;
  final String? value;
  final Set<String> values;
  final bool multiple;
  final String placeholder;
  final bool searchable;

  /// Строка состояния под списком: «ищем…», «никого не нашли».
  final String? status;

  /// Строка-действие в конце списка.
  final String? actionLabel;
  final ValueChanged<String>? onAction;
  final bool disabled;
  final KitsSelectAppearance appearance;
  final String searchPlaceholder;
  final ValueChanged<String>? onSearchChanged;

  /// Выбор одного значения (`Select`).
  final ValueChanged<String>? onChanged;

  /// Выбор варианта (`Combobox`); при `multiple` зовётся на каждый.
  final ValueChanged<String>? onPick;

  final ValueChanged<bool>? onOpenChange;
  final VoidCallback? onClear;
  final FocusNode? focusNode;

  static const Key triggerKey = Key('kits-select-trigger');
  static const Key panelKey = Key('kits-select-panel');
  static const Key searchKey = Key('kits-select-search');
  static const Key listKey = Key('kits-select-list');
  static const Key statusKey = Key('kits-select-status');

  /// Ключ варианта.
  static Key optionKey(String id) => ValueKey<String>('kits-select-option-$id');

  @override
  State<KitsSelect> createState() => _KitsSelectState();
}

class _KitsSelectState extends State<KitsSelect> {
  final Object _owner = Object();
  final FocusNode _listFocus = FocusNode(debugLabel: 'KitsSelectList');
  final FocusNode _searchFocus = FocusNode(debugLabel: 'KitsSelectSearch');
  final TextEditingController _search = TextEditingController();
  OverlayEntry? _entry;
  FocusNode? _ownedTrigger;
  bool _open = false;
  bool _hovered = false;
  bool _showFocus = false;
  int _active = 0;
  Rect _triggerRect = Rect.zero;
  Size _screen = Size.zero;
  bool _disposed = false;

  bool get _enabled => !widget.disabled;

  FocusNode get _trigger =>
      widget.focusNode ?? (_ownedTrigger ??= FocusNode(debugLabel: 'KitsSelectTrigger'));

  @override
  void dispose() {
    _disposed = true;
    _close(restoreFocus: false);
    _ownedTrigger?.dispose();
    _listFocus.dispose();
    _searchFocus.dispose();
    _search.dispose();
    super.dispose();
  }

  List<KitsSelectOption> get _flat => <KitsSelectOption>[
        for (final section in widget.sections) ...section.options,
      ];

  bool _isSelected(KitsSelectOption option) => widget.multiple
      ? widget.values.contains(option.id)
      : widget.value == option.id;

  int _firstEnabled(List<KitsSelectOption> options, {int from = 0}) {
    for (var i = from; i < options.length; i++) {
      if (!options[i].disabled) return i;
    }
    for (var i = 0; i < from && i < options.length; i++) {
      if (!options[i].disabled) return i;
    }
    return -1;
  }

  void _openPanel() {
    if (!_enabled || _open) return;
    final box = context.findRenderObject()! as RenderBox;
    _triggerRect = box.localToGlobal(Offset.zero) & box.size;
    _screen = MediaQuery.sizeOf(context);

    final options = _flat;
    final selected = options.indexWhere(_isSelected);
    _active = selected != -1 && !options[selected].disabled
        ? selected
        : _firstEnabled(options);

    _open = true;
    KitsLayers.push(_owner, _closeFromEscape);
    widget.onOpenChange?.call(true);
    _entry = OverlayEntry(builder: (context) => _buildPanel(context));
    Overlay.of(context, rootOverlay: true).insert(_entry!);
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted || !_open) return;
      if (widget.searchable) {
        _searchFocus.requestFocus();
      } else {
        _listFocus.requestFocus();
      }
    });
    if (!_disposed) setState(() {});
  }

  void _close({bool restoreFocus = true, bool notify = true}) {
    if (!_open) return;
    _open = false;
    _entry?.remove();
    _entry = null;
    KitsLayers.remove(_owner);
    if (notify) widget.onOpenChange?.call(false);
    if (restoreFocus) _trigger.requestFocus();
    if (mounted && !_disposed) setState(() {});
  }

  void _closeFromEscape() => _close();

  void _setActive(int index) {
    if (index == _active) return;
    if (_disposed) return;
    setState(() => _active = index);
    _entry?.markNeedsBuild();
  }

  void _move(int delta) {
    final options = _flat;
    if (options.isEmpty) return;
    var index = _active;
    for (var step = 0; step < options.length; step++) {
      index = (index + delta + options.length) % options.length;
      if (!options[index].disabled) {
        _setActive(index);
        return;
      }
    }
  }

  void _jump(bool toEnd) {
    final options = _flat;
    if (options.isEmpty) return;
    if (toEnd) {
      for (var i = options.length - 1; i >= 0; i--) {
        if (!options[i].disabled) {
          _setActive(i);
          return;
        }
      }
    } else {
      final first = _firstEnabled(options);
      if (first != -1) _setActive(first);
    }
  }

  void _pick(KitsSelectOption option, {bool fromAction = false}) {
    if (option.disabled) return;
    if (fromAction) {
      widget.onAction?.call(_search.text);
      _close();
      return;
    }
    widget.onPick?.call(option.id);
    widget.onChanged?.call(option.id);
    if (widget.multiple) {
      _entry?.markNeedsBuild();
    } else {
      _close();
    }
  }

  KeyEventResult _onTriggerKey(FocusNode node, KeyEvent event) {
    if (event is! KeyDownEvent) return KeyEventResult.ignored;
    if (event.logicalKey == LogicalKeyboardKey.arrowDown ||
        event.logicalKey == LogicalKeyboardKey.arrowUp) {
      _openPanel();
      return KeyEventResult.handled;
    }
    return KeyEventResult.ignored;
  }

  KeyEventResult _onPanelKey(FocusNode node, KeyEvent event) {
    if (event is! KeyDownEvent) return KeyEventResult.ignored;
    switch (event.logicalKey) {
      case LogicalKeyboardKey.arrowDown:
        _move(1);
        return KeyEventResult.handled;
      case LogicalKeyboardKey.arrowUp:
        _move(-1);
        return KeyEventResult.handled;
      case LogicalKeyboardKey.home:
        _jump(false);
        return KeyEventResult.handled;
      case LogicalKeyboardKey.end:
        _jump(true);
        return KeyEventResult.handled;
      case LogicalKeyboardKey.enter:
        final options = _flat;
        if (_active >= 0 && _active < options.length) {
          _pick(options[_active]);
        }
        return KeyEventResult.handled;
    }
    return KeyEventResult.ignored;
  }

  @override
  Widget build(BuildContext context) {
    final tokens = KitsTokens.of(context);
    final String label;
    if (widget.multiple) {
      final picked = <String>[
        for (final option in _flat)
          if (widget.values.contains(option.id)) option.label,
      ];
      label = picked.isEmpty ? widget.placeholder : picked.join(', ');
    } else {
      final selected = _flat.where((option) => option.id == widget.value);
      label = selected.isEmpty ? widget.placeholder : selected.first.label;
    }

    final height = tokens.length('--ck-control-md');
    final appearance = widget.appearance;
    final background = switch (appearance) {
      KitsSelectAppearance.field => _open
          ? tokens.color('--ck-field-fill')
          : tokens.color('--ck-field-fill'),
      KitsSelectAppearance.row => _hovered
          ? tokens.color('--ck-bg-hover')
          : const Color(0x00000000),
      KitsSelectAppearance.text => _hovered || _open
          ? tokens.color('--ck-bg-hover')
          : const Color(0x00000000),
    };

    final trigger = Focus(
      focusNode: _trigger,
      canRequestFocus: _enabled,
      skipTraversal: !_enabled,
      onFocusChange: (value) => setState(() => _showFocus = value),
      onKeyEvent: _onTriggerKey,
      child: GestureDetector(
        key: KitsSelect.triggerKey,
        behavior: HitTestBehavior.opaque,
        onTap: _enabled ? _openPanel : null,
        child: MouseRegion(
          cursor: _enabled
              ? SystemMouseCursors.click
              : SystemMouseCursors.forbidden,
          onEnter: (_) => setState(() => _hovered = true),
          onExit: (_) => setState(() => _hovered = false),
          child: KitsFocusRing(
            focused: _showFocus,
            inset: true,
            borderRadius:
                BorderRadius.circular(tokens.length('--ck-radius-control')),
            child: Container(
              height: height,
              padding: EdgeInsets.symmetric(
                horizontal: tokens.length('--ck-space-3'),
              ),
              decoration: BoxDecoration(
                color: background,
                borderRadius:
                    BorderRadius.circular(tokens.length('--ck-radius-control')),
              ),
              child: Row(
                children: <Widget>[
                  Expanded(
                    child: Text(
                      label,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: KitsType.of(
                        context,
                        font: '--ck-font-control',
                        color: widget.multiple
                            ? (widget.values.isEmpty
                                ? tokens.color('--ck-text-secondary')
                                : tokens.color('--ck-text'))
                            : (widget.value == null
                                ? tokens.color('--ck-text-secondary')
                                : tokens.color('--ck-text')),
                      ),
                    ),
                  ),
                  if (widget.onClear != null && widget.value != null)
                    GestureDetector(
                      onTap: () {
                        widget.onClear?.call();
                        _trigger.requestFocus();
                      },
                      child: Semantics(
                        button: true,
                        label: 'Очистить',
                        child: Icon(
                          Icons.close,
                          size: 16,
                          color: tokens.color('--ck-text-secondary'),
                        ),
                      ),
                    ),
                  if (!widget.disabled)
                    Icon(
                      _open ? Icons.keyboard_arrow_up : Icons.keyboard_arrow_down,
                      size: 18,
                      color: tokens.color('--ck-text-secondary'),
                    ),
                ],
              ),
            ),
          ),
        ),
      ),
    );

    return Semantics(
      container: true,
      button: true,
      enabled: _enabled,
      label: label,
      child: trigger,
    );
  }

  Widget _buildPanel(BuildContext context) {
    final tokens = KitsTokens.of(context);
    final gap = tokens.length('--ck-space-1');
    final width = math.min(
      math.max(_triggerRect.width, 14.0 * 16),
      _screen.width - gap * 2,
    );
    final left = _triggerRect.left
        .clamp(gap, math.max(gap, _screen.width - width - gap))
        .toDouble();
    final options = _flat;
    final estimated = math.min(360.0, 16 + options.length * 40.0);
    final below = _triggerRect.bottom + gap;
    final above = _triggerRect.top - gap - estimated;
    final flip = below + estimated > _screen.height - gap && above >= gap;
    final top = flip ? above : below;

    final rows = <Widget>[];
    var index = -1;
    for (final section in widget.sections) {
      if (section.title != null) {
        rows.add(
          Padding(
            padding: EdgeInsets.fromLTRB(
              tokens.length('--ck-space-3'),
              tokens.length('--ck-space-2'),
              tokens.length('--ck-space-3'),
              tokens.length('--ck-space-1'),
            ),
            child: Text(
              section.title!,
              style: KitsType.of(
                context,
                font: '--ck-font-micro',
                color: tokens.color('--ck-text-secondary'),
              ),
            ),
          ),
        );
      }
      for (final option in section.options) {
        index++;
        final active = index == _active;
        final selected = _isSelected(option);
        rows.add(
          GestureDetector(
            key: KitsSelect.optionKey(option.id),
            behavior: HitTestBehavior.opaque,
            onTap: option.disabled ? null : () => _pick(option),
            child: Opacity(
              opacity: option.disabled ? 0.5 : 1,
              child: Container(
                height: 40,
                padding: EdgeInsets.symmetric(
                  horizontal: tokens.length('--ck-space-3'),
                ),
                color: active
                    ? tokens.color('--ck-bg-hover')
                    : const Color(0x00000000),
                child: Row(
                  children: <Widget>[
                    Expanded(
                      child: Text(
                        option.label,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: KitsType.of(
                          context,
                          font: '--ck-font-control',
                          color: tokens.color('--ck-text'),
                        ),
                      ),
                    ),
                    if (selected)
                      Icon(
                        Icons.check,
                        size: 16,
                        color: tokens.color('--ck-accent-strong'),
                      ),
                  ],
                ),
              ),
            ),
          ),
        );
      }
    }

    final searchField = widget.searchable
        ? Padding(
            padding: EdgeInsets.all(tokens.length('--ck-space-2')),
            child: TextField(
              key: KitsSelect.searchKey,
              controller: _search,
              focusNode: _searchFocus,
              onChanged: (value) {
                widget.onSearchChanged?.call(value);
                _entry?.markNeedsBuild();
              },
              style: KitsType.of(
                context,
                font: '--ck-font-control',
                color: tokens.color('--ck-text'),
              ),
              decoration: InputDecoration(
                isDense: true,
                hintText: widget.searchPlaceholder,
                prefixIcon: const Icon(Icons.search, size: 18),
                filled: true,
                fillColor: tokens.color('--ck-field-fill'),
                border: OutlineInputBorder(
                  borderRadius: BorderRadius.circular(
                    tokens.length('--ck-radius-control'),
                  ),
                  borderSide: BorderSide.none,
                ),
              ),
            ),
          )
        : null;

    final panel = Material(
      key: KitsSelect.panelKey,
      color: tokens.color('--ck-bg'),
      elevation: 12,
      borderRadius: BorderRadius.circular(tokens.length('--ck-radius-card')),
      child: Focus(
        canRequestFocus: false,
        skipTraversal: true,
        onKeyEvent: _onPanelKey,
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxHeight: 360),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: <Widget>[
              ?searchField,
              Flexible(
                child: Focus(
                  focusNode: _listFocus,
                  child: ListView(
                    key: KitsSelect.listKey,
                    shrinkWrap: true,
                    padding: EdgeInsets.zero,
                    children: rows,
                  ),
                ),
              ),
              if (widget.actionLabel != null)
                GestureDetector(
                  key: const Key('kits-select-action'),
                  behavior: HitTestBehavior.opaque,
                  onTap: () => widget.onAction?.call(_search.text),
                  child: Container(
                    height: 40,
                    alignment: Alignment.centerLeft,
                    padding: EdgeInsets.symmetric(
                      horizontal: tokens.length('--ck-space-3'),
                    ),
                    child: Text(
                      widget.actionLabel!,
                      style: KitsType.of(
                        context,
                        font: '--ck-font-control',
                        color: tokens.color('--ck-accent-strong'),
                      ),
                    ),
                  ),
                ),
              if (widget.status != null)
                Semantics(
                  liveRegion: true,
                  label: widget.status,
                  child: Padding(
                    key: KitsSelect.statusKey,
                    padding: EdgeInsets.all(tokens.length('--ck-space-3')),
                    child: Text(
                      widget.status!,
                      style: KitsType.of(
                        context,
                        font: '--ck-font-hint',
                        color: tokens.color('--ck-text-secondary'),
                      ),
                    ),
                  ),
                ),
            ],
          ),
        ),
      ),
    );

    return Actions(
      actions: <Type, Action<Intent>>{
        DismissIntent: CallbackAction<DismissIntent>(
          onInvoke: (_) {
            if (KitsLayers.isTop(_owner)) _close();
            return null;
          },
        ),
      },
      child: Stack(
        children: <Widget>[
          Positioned.fill(
            child: GestureDetector(
              behavior: HitTestBehavior.opaque,
              onTap: () => _close(restoreFocus: false),
            ),
          ),
          Positioned(left: left, top: top, width: width, child: panel),
        ],
      ),
    );
  }
}
