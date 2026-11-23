import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../foundation/kits_focus_ring.dart';
import '../foundation/kits_text.dart';
import '../tokens.dart';

/// Размер переключателя-сегментов.
enum KitsSegmentSize { sm, md }

/// Один пункт переключателя-сегментов.
///
/// Счётчик — часть подписи: диктор читает его вместе с ней, отдельного поля
/// счётчика у сегментов нет.
@immutable
class KitsSegment {
  const KitsSegment({
    required this.id,
    required this.label,
    this.icon,
    this.count,
    this.disabled = false,
  });

  final String id;
  final String label;

  /// Значок перед подписью; декоративный, имя даёт подпись.
  final Widget? icon;

  /// Счётчик в подписи; `null` не рисуется.
  final int? count;

  final bool disabled;
}

/// Переключатель-сегменты кита: радиогруппа коротких пунктов в капсуле.
///
/// Выбран ровно один пункт; в порядке табуляции группа стоит одним шагом.
/// Стрелки вправо/вниз и влево/вверх переходят по кругу, пропуская недоступные,
/// Home и End выбирают первый и последний доступный. Смена значения и фокус
/// остаются на выбранном пункте.
class KitsSegmentedControl extends StatefulWidget {
  const KitsSegmentedControl({
    super.key,
    required this.segments,
    required this.value,
    required this.onChanged,
    this.label = 'Разделы',
    this.size = KitsSegmentSize.md,
    this.disabled = false,
    this.focusNode,
  });

  final List<KitsSegment> segments;
  final String value;
  final ValueChanged<String> onChanged;

  /// Обязательное имя группы для диктора.
  final String label;

  final KitsSegmentSize size;
  final bool disabled;
  final FocusNode? focusNode;

  static const Key groupKey = Key('kits-segmented-group');

  static Key segmentKey(String id) => ValueKey<String>('kits-segment-$id');

  @override
  State<KitsSegmentedControl> createState() => _KitsSegmentedControlState();
}

class _KitsSegmentedControlState extends State<KitsSegmentedControl> {
  FocusNode? _ownedFocus;
  bool _hovered = false;
  bool _showFocus = false;

  FocusNode get _focusNode =>
      widget.focusNode ??
      (_ownedFocus ??= FocusNode(debugLabel: 'KitsSegments'));

  @override
  void initState() {
    super.initState();
    _focusNode.addListener(_onFocusChange);
  }

  @override
  void dispose() {
    _focusNode.removeListener(_onFocusChange);
    _ownedFocus?.dispose();
    super.dispose();
  }

  void _onFocusChange() {
    if (_showFocus != _focusNode.hasFocus) {
      setState(() => _showFocus = _focusNode.hasFocus);
    }
  }

  int get _selectedIndex {
    final index = widget.segments.indexWhere((item) => item.id == widget.value);
    if (index >= 0) return index;
    return widget.segments.indexWhere((item) => !item.disabled);
  }

  bool _enabled(KitsSegment segment) => !widget.disabled && !segment.disabled;

  int? _nextIndex(int from, int step) {
    if (widget.segments.isEmpty) return null;
    var index = from;
    for (var guard = 0; guard < widget.segments.length; guard++) {
      index = (index + step) % widget.segments.length;
      if (_enabled(widget.segments[index])) return index;
    }
    return null;
  }

  int? _edgeIndex({required bool first}) {
    final indexes = <int>[
      for (var i = 0; i < widget.segments.length; i++)
        if (_enabled(widget.segments[i])) i,
    ];
    if (indexes.isEmpty) return null;
    return first ? indexes.first : indexes.last;
  }

  void _pick(int index, {bool focus = true}) {
    final segment = widget.segments[index];
    if (!_enabled(segment)) return;
    if (segment.id != widget.value) widget.onChanged(segment.id);
    if (focus) _focusNode.requestFocus();
  }

  KeyEventResult _onKey(FocusNode node, KeyEvent event) {
    if (event is! KeyDownEvent) return KeyEventResult.ignored;
    final selected = _selectedIndex;
    if (selected < 0) return KeyEventResult.ignored;
    final key = event.logicalKey;
    int? next;
    if (key == LogicalKeyboardKey.arrowRight ||
        key == LogicalKeyboardKey.arrowDown) {
      next = _nextIndex(selected, 1);
    } else if (key == LogicalKeyboardKey.arrowLeft ||
        key == LogicalKeyboardKey.arrowUp) {
      next = _nextIndex(selected, -1);
    } else if (key == LogicalKeyboardKey.home) {
      next = _edgeIndex(first: true);
    } else if (key == LogicalKeyboardKey.end) {
      next = _edgeIndex(first: false);
    } else {
      return KeyEventResult.ignored;
    }
    if (next == null) return KeyEventResult.handled;
    _pick(next);
    return KeyEventResult.handled;
  }

  double _height(KitsTokens tokens) => switch (widget.size) {
    KitsSegmentSize.sm => tokens.length('--ck-control-sm'),
    KitsSegmentSize.md => tokens.length('--ck-control-md'),
  };

  @override
  Widget build(BuildContext context) {
    assert(widget.segments.isNotEmpty, 'группе нужен хотя бы один пункт');
    final tokens = KitsTokens.of(context);
    final selected = _selectedIndex;

    Widget group = LayoutBuilder(
      builder: (context, constraints) {
        final bounded = constraints.hasBoundedWidth;
        final items = <Widget>[
          for (var i = 0; i < widget.segments.length; i++)
            _segment(
              context,
              tokens,
              index: i,
              selected: i == selected,
              height: _height(tokens),
              bounded: bounded,
            ),
        ];
        final row = Row(
          mainAxisSize: MainAxisSize.min,
          children: bounded
              ? <Widget>[
                  for (final item in items)
                    Flexible(fit: FlexFit.loose, child: item),
                ]
              : items,
        );
        final capsule = Container(
          key: KitsSegmentedControl.groupKey,
          padding: EdgeInsets.all(tokens.length('--ck-space-1')),
          decoration: BoxDecoration(
            color: tokens.color('--ck-bg-second'),
            border: Border.all(color: tokens.color('--ck-border')),
            borderRadius: BorderRadius.circular(
              tokens.length('--ck-radius-full'),
            ),
          ),
          child: ClipRect(child: row),
        );
        // Узкий контейнер: капсула уступает место по ширине, пункты сокращаются
        // многоточием — переноса и горизонтальной прокрутки нет.
        return bounded
            ? Center(
                child: ConstrainedBox(
                  constraints: BoxConstraints(maxWidth: constraints.maxWidth),
                  child: capsule,
                ),
              )
            : capsule;
      },
    );
    group = Focus(focusNode: _focusNode, onKeyEvent: _onKey, child: group);

    return Semantics(
      container: true,
      explicitChildNodes: true,
      label: widget.label,
      child: group,
    );
  }

  Widget _label(
    BuildContext context,
    String label,
    Color foreground,
    bool selected,
  ) {
    return Text(
      label,
      maxLines: 1,
      softWrap: false,
      overflow: TextOverflow.ellipsis,
      style: KitsType.of(
        context,
        font: '--ck-font-control',
        color: foreground,
        weight: selected ? FontWeight.w600 : FontWeight.w500,
      ),
    );
  }

  Widget _segment(
    BuildContext context,
    KitsTokens tokens, {
    required int index,
    required bool selected,
    required double height,
    required bool bounded,
  }) {
    final segment = widget.segments[index];
    final enabled = _enabled(segment);
    final label = segment.count == null
        ? segment.label
        : '${segment.label} ${segment.count}';
    final foreground = selected
        ? tokens.color('--ck-accent-strong')
        : tokens.color('--ck-text-secondary');

    Widget content = Row(
      mainAxisSize: MainAxisSize.min,
      children: <Widget>[
        if (segment.icon != null) ...<Widget>[
          IconTheme.merge(
            data: IconThemeData(size: 16, color: foreground),
            child: segment.icon!,
          ),
          SizedBox(width: tokens.length('--ck-space-1')),
        ],
        if (bounded)
          Flexible(child: _label(context, label, foreground, selected))
        else
          _label(context, label, foreground, selected),
      ],
    );

    Widget item = AnimatedContainer(
      duration: tokens.duration('--ck-menu-ease'),
      height: height,
      padding: EdgeInsets.symmetric(horizontal: tokens.length('--ck-space-3')),
      decoration: BoxDecoration(
        color: selected
            ? tokens.color('--ck-bg')
            : (_hovered && enabled
                  ? tokens.color('--ck-bg-hover')
                  : const Color(0x00000000)),
        border: selected
            ? Border.all(color: tokens.color('--ck-accent-border'))
            : Border.all(color: const Color(0x00000000)),
        borderRadius: BorderRadius.circular(tokens.length('--ck-radius-full')),
      ),
      child: Align(
        alignment: Alignment.center,
        widthFactor: 1,
        heightFactor: 1,
        child: content,
      ),
    );

    if (!enabled) item = Opacity(opacity: 0.5, child: item);

    item = MouseRegion(
      cursor: enabled ? SystemMouseCursors.click : SystemMouseCursors.basic,
      onEnter: (_) => setState(() => _hovered = true),
      onExit: (_) => setState(() => _hovered = false),
      child: GestureDetector(
        key: KitsSegmentedControl.segmentKey(segment.id),
        behavior: HitTestBehavior.opaque,
        onTap: enabled ? () => _pick(index) : null,
        child: item,
      ),
    );

    if (selected) {
      item = KitsFocusRing(focused: _showFocus, inset: true, child: item);
    }

    return Semantics(
      container: true,
      inMutuallyExclusiveGroup: true,
      checked: selected,
      enabled: enabled,
      button: true,
      label: segment.label,
      value: segment.count?.toString(),
      onTap: enabled ? () => _pick(index) : null,
      excludeSemantics: true,
      child: item,
    );
  }
}
