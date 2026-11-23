import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../foundation/kits_focus_ring.dart';
import '../foundation/kits_layers.dart';
import '../foundation/kits_text.dart';
import '../tokens.dart';

/// Вкладка ленты: код, подпись, счётчик, значок и признаки ужимания.
class KitsTabItem {
  const KitsTabItem({
    required this.code,
    required this.name,
    this.count,
    this.icon,
    this.trail,
    this.compact = false,
    this.keep = false,
  });

  final String code;
  final String name;

  /// Счётчик бейджем после имени; ноль и `null` бейджа не рисуют.
  final int? count;

  /// Значок перед именем; только такая вкладка ужимается до значка.
  final Widget? icon;

  /// Узел после имени и счётчика, диктору не читается.
  final Widget? trail;

  /// Разрешить ужать подпись в тесной ленте.
  final bool compact;

  /// Не убирать в «Ещё» никогда.
  final bool keep;
}

/// Лента вкладок-«пилюль».
///
/// Выбранную держит снаружи [current]; кит сообщает о нажатии [onPick].
/// В тесной ленте ([fit]) значковые вкладки ужимаются, а не влезшие уходят в
/// меню «Ещё»; выбранная и `keep` остаются в ленте.
class KitsTabs extends StatefulWidget {
  const KitsTabs({
    super.key,
    required this.items,
    required this.current,
    required this.onPick,
    this.fit = false,
    this.tail,
    this.moreLabel = 'Ещё',
  });

  final List<KitsTabItem> items;
  final String current;
  final ValueChanged<String> onPick;
  final bool fit;
  final Widget? tail;
  final String moreLabel;

  /// Высота вкладки — правило `ck-tab`, не размер контрола.
  static const double tabHeight = 36;

  static const Key stripKey = Key('kits-tabs-strip');
  static const Key moreKey = Key('kits-tabs-more');
  static const Key moreMenuKey = Key('kits-tabs-more-menu');

  /// Ключ кнопки вкладки.
  static Key tabKey(String code) => ValueKey<String>('kits-tab-$code');

  /// Ключ видимой подписи вкладки (у ужатой её нет).
  static Key labelKey(String code) => ValueKey<String>('kits-tab-label-$code');

  /// Ключ бейджа счётчика.
  static Key countKey(String code) => ValueKey<String>('kits-tab-count-$code');

  @override
  State<KitsTabs> createState() => _KitsTabsState();
}

class _KitsTabsState extends State<KitsTabs> {
  final Map<String, FocusNode> _nodes = <String, FocusNode>{};
  final FocusNode _moreFocus = FocusNode(debugLabel: 'KitsTabsMore');
  OverlayEntry? _menu;
  Object? _menuOwner;

  @override
  void dispose() {
    for (final node in _nodes.values) {
      node.dispose();
    }
    _moreFocus.dispose();
    _closeMenu();
    super.dispose();
  }

  FocusNode _node(String code) =>
      _nodes.putIfAbsent(code, () => FocusNode(debugLabel: 'KitsTab-$code'));

  void _pick(String code) {
    widget.onPick(code);
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) _node(code).requestFocus();
    });
  }

  void _move(int delta) {
    final visible = widget.items;
    final index = visible.indexWhere((item) => item.code == widget.current);
    if (index == -1) return;
    final next = (index + delta + visible.length) % visible.length;
    _pick(visible[next].code);
  }

  void _jump(bool toEnd) {
    if (widget.items.isEmpty) return;
    _pick(toEnd ? widget.items.last.code : widget.items.first.code);
  }

  void _closeMenu() {
    final owner = _menuOwner;
    if (owner != null) KitsLayers.remove(owner);
    _menuOwner = null;
    _menu?.remove();
    _menu = null;
  }

  @override
  Widget build(BuildContext context) {
    final tokens = KitsTokens.of(context);
    return widget.fit
        ? _buildFit(context, tokens)
        : _buildStrip(context, tokens);
  }

  Widget _buildStrip(BuildContext context, KitsTokens tokens) {
    return SizedBox(
      height: KitsTabs.tabHeight,
      child: SingleChildScrollView(
        key: KitsTabs.stripKey,
        scrollDirection: Axis.horizontal,
        child: _row(context, tokens, widget.items, const <KitsTabItem>[]),
      ),
    );
  }

  Widget _buildFit(BuildContext context, KitsTokens tokens) {
    return LayoutBuilder(
      builder: (context, constraints) {
        final width = constraints.maxWidth;
        final plan = _plan(context, tokens, width);
        return SizedBox(
          height: KitsTabs.tabHeight,
          width: width,
          key: KitsTabs.stripKey,
          child: _row(context, tokens, plan.visible, plan.hidden, compacted: plan.compacted),
        );
      },
    );
  }

  ({List<KitsTabItem> visible, List<KitsTabItem> hidden, Set<String> compacted})
      _plan(BuildContext context, KitsTokens tokens, double width) {
    final compacted = <String>{};
    var visible = List<KitsTabItem>.of(widget.items);
    var hidden = <KitsTabItem>[];

    bool fits() {
      var total = 0.0;
      for (final item in visible) {
        total += _width(context, tokens, item, compacted.contains(item.code));
      }
      total += tokens.length('--ck-space-1') * (visible.length - 1);
      if (hidden.isNotEmpty) {
        total += _moreWidth(context, tokens) + tokens.length('--ck-space-1');
      }
      if (widget.tail != null) total += tokens.length('--ck-space-2');
      return total <= width;
    }

    if (fits()) return (visible: visible, hidden: hidden, compacted: compacted);

    // 1. Значковые вкладки ужимаются по одной с конца.
    for (var i = visible.length - 1; i >= 0 && !fits(); i--) {
      final item = visible[i];
      if (item.code == widget.current || item.keep || item.icon == null) {
        continue;
      }
      compacted.add(item.code);
    }
    if (fits()) return (visible: visible, hidden: hidden, compacted: compacted);

    // 2. Не влезшие уходят в «Ещё» по одной с конца; выбранная и keep — нет.
    visible = List<KitsTabItem>.of(widget.items);
    for (final item in widget.items.reversed) {
      if (fits()) break;
      if (item.code == widget.current || item.keep) continue;
      visible = visible.where((other) => other.code != item.code).toList();
      hidden = <KitsTabItem>[item, ...hidden];
    }
    return (visible: visible, hidden: hidden, compacted: compacted);
  }

  Widget _row(
    BuildContext context,
    KitsTokens tokens,
    List<KitsTabItem> items,
    List<KitsTabItem> hidden, {
    Set<String> compacted = const <String>{},
  }) {
    return Focus(
      canRequestFocus: false,
      skipTraversal: true,
      onKeyEvent: (node, event) {
        if (event is! KeyDownEvent) return KeyEventResult.ignored;
        final key = event.logicalKey;
        if (key == LogicalKeyboardKey.arrowRight) {
          _move(1);
          return KeyEventResult.handled;
        }
        if (key == LogicalKeyboardKey.arrowLeft) {
          _move(-1);
          return KeyEventResult.handled;
        }
        if (key == LogicalKeyboardKey.home) {
          _jump(false);
          return KeyEventResult.handled;
        }
        if (key == LogicalKeyboardKey.end) {
          _jump(true);
          return KeyEventResult.handled;
        }
        return KeyEventResult.ignored;
      },
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: <Widget>[
          for (var i = 0; i < items.length; i++) ...<Widget>[
            if (i > 0) SizedBox(width: tokens.length('--ck-space-1')),
            _tab(
              context,
              tokens,
              items[i],
              compact: compacted.contains(items[i].code),
            ),
          ],
          if (hidden.isNotEmpty) ...<Widget>[
            SizedBox(width: tokens.length('--ck-space-1')),
            _moreButton(context, tokens, hidden),
          ],
          if (widget.tail != null) ...<Widget>[
            SizedBox(width: tokens.length('--ck-space-2')),
            widget.tail!,
          ],
        ],
      ),
    );
  }

  Widget _tab(
    BuildContext context,
    KitsTokens tokens,
    KitsTabItem item, {
    required bool compact,
  }) {
    final current = item.code == widget.current;
    final semanticZero = item.count == null || item.count == 0;
    final node = _node(item.code);

    final content = Row(
      mainAxisSize: MainAxisSize.min,
      children: <Widget>[
        if (item.icon != null) ...<Widget>[
          IconTheme.merge(
            data: IconThemeData(
              size: 16,
              color: current
                  ? tokens.color('--ck-accent-text')
                  : tokens.color('--ck-text-secondary'),
            ),
            child: item.icon!,
          ),
          if (!compact) SizedBox(width: tokens.length('--ck-space-1')),
        ],
        if (!compact)
          Text(
            item.name,
            key: KitsTabs.labelKey(item.code),
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: KitsType.of(
              context,
              font: '--ck-font-row',
              color: current
                  ? tokens.color('--ck-accent-text')
                  : tokens.color('--ck-text-secondary'),
            ),
          ),
        if (!semanticZero) ...<Widget>[
          SizedBox(width: tokens.length('--ck-space-1')),
          Container(
            key: KitsTabs.countKey(item.code),
            padding: EdgeInsets.symmetric(
              horizontal: tokens.length('--ck-space-1'),
            ),
            decoration: BoxDecoration(
              color: current
                  ? tokens.color('--ck-accent-text')
                  : tokens.color('--ck-field-fill'),
              borderRadius: BorderRadius.circular(tokens.length('--ck-radius-full')),
            ),
            child: Text(
              item.count! > 99 ? '99+' : '${item.count}',
              style: KitsType.of(
                context,
                font: '--ck-font-micro',
                color: current
                    ? tokens.color('--ck-accent')
                    : tokens.color('--ck-text-secondary'),
              ),
            ),
          ),
        ],
        if (item.trail != null && !compact) ...<Widget>[
          SizedBox(width: tokens.length('--ck-space-1')),
          ExcludeSemantics(child: item.trail!),
        ],
      ],
    );

    return ListenableBuilder(
      listenable: node,
      builder: (context, _) {
        final button = Focus(
          focusNode: node,
          canRequestFocus: current,
          skipTraversal: !current,
          child: GestureDetector(
            key: KitsTabs.tabKey(item.code),
            behavior: HitTestBehavior.opaque,
            onTap: () => widget.onPick(item.code),
            child: MouseRegion(
              cursor: SystemMouseCursors.click,
              child: KitsFocusRing(
                focused: node.hasFocus,
                inset: true,
                borderRadius:
                    BorderRadius.circular(tokens.length('--ck-radius-full')),
                child: Container(
                  height: KitsTabs.tabHeight,
                  padding: EdgeInsets.symmetric(
                    horizontal: tokens.length('--ck-space-3'),
                  ),
                  decoration: BoxDecoration(
                    color: current
                        ? tokens.color('--ck-accent')
                        : tokens.color('--ck-field-fill'),
                    borderRadius: BorderRadius.circular(
                      tokens.length('--ck-radius-full'),
                    ),
                  ),
                  alignment: Alignment.center,
                  child: content,
                ),
              ),
            ),
          ),
        );
        return Semantics(
          button: true,
          selected: current,
          label: item.name,
          child: button,
        );
      },
    );
  }

  Widget _moreButton(
    BuildContext context,
    KitsTokens tokens,
    List<KitsTabItem> hidden,
  ) {
    return Focus(
      focusNode: _moreFocus,
      child: GestureDetector(
        key: KitsTabs.moreKey,
        onTap: () => _openMenu(context, hidden),
        child: MouseRegion(
          cursor: SystemMouseCursors.click,
          child: Container(
            height: KitsTabs.tabHeight,
            padding: EdgeInsets.symmetric(
              horizontal: tokens.length('--ck-space-3'),
            ),
            decoration: BoxDecoration(
              color: tokens.color('--ck-field-fill'),
              borderRadius: BorderRadius.circular(tokens.length('--ck-radius-full')),
            ),
            alignment: Alignment.center,
            child: Text(
              widget.moreLabel,
              style: KitsType.of(
                context,
                font: '--ck-font-row',
                color: tokens.color('--ck-text-secondary'),
              ),
            ),
          ),
        ),
      ),
    );
  }

  void _openMenu(BuildContext context, List<KitsTabItem> hidden) {
    _closeMenu();
    final box = context.findRenderObject()! as RenderBox;
    final overlay = Overlay.of(context);
    final origin = box.localToGlobal(Offset.zero);
    final owner = Object();
    _menuOwner = owner;

    void close({bool focusMore = false}) {
      _closeMenu();
      if (focusMore) _moreFocus.requestFocus();
    }

    KitsLayers.push(owner, () => close(focusMore: true));

    final tokens = KitsTokens.of(context);
    _menu = OverlayEntry(
      builder: (menuContext) {
        return Stack(
          children: <Widget>[
            Positioned.fill(
              child: GestureDetector(
                behavior: HitTestBehavior.opaque,
                onTap: () => close(),
              ),
            ),
            Positioned(
              left: origin.dx,
              top: origin.dy + KitsTabs.tabHeight + tokens.length('--ck-space-1'),
              child: Focus(
                autofocus: true,
                child: Material(
                    key: KitsTabs.moreMenuKey,
                    color: tokens.color('--ck-bg'),
                    elevation: 8,
                    borderRadius:
                        BorderRadius.circular(tokens.length('--ck-radius-row')),
                    child: ConstrainedBox(
                      constraints: BoxConstraints(
                        minWidth: 160,
                        maxWidth: 240,
                      ),
                      child: Column(
                        mainAxisSize: MainAxisSize.min,
                        children: <Widget>[
                          for (final item in hidden)
                            InkWell(
                              onTap: () {
                                close();
                                widget.onPick(item.code);
                              },
                              child: Padding(
                                padding: EdgeInsets.symmetric(
                                  horizontal: tokens.length('--ck-space-3'),
                                  vertical: tokens.length('--ck-space-2'),
                                ),
                                child: SizedBox(
                                  width: double.infinity,
                                  child: Text(
                                    item.name,
                                    style: KitsType.of(
                                      menuContext,
                                      font: '--ck-font-control',
                                      color: tokens.color('--ck-text'),
                                    ),
                                  ),
                                ),
                              ),
                            ),
                        ],
                      ),
                    ),
                  ),
                ),
            ),
          ],
        );
      },
    );
    overlay.insert(_menu!);
  }

  double _width(
    BuildContext context,
    KitsTokens tokens,
    KitsTabItem item,
    bool compact,
  ) {
    final padding = tokens.length('--ck-space-3') * 2;
    final icon = item.icon == null ? 0.0 : 16 + tokens.length('--ck-space-1');
    final count = (item.count == null || item.count == 0)
        ? 0.0
        : _textWidth(
                context,
                item.count! > 99 ? '99+' : '${item.count}',
                '--ck-font-micro',
              ) +
            tokens.length('--ck-space-1') * 2 +
            tokens.length('--ck-space-1');
    if (compact) return padding + icon + count;
    return padding +
        icon +
        _textWidth(context, item.name, '--ck-font-row') +
        count +
        (item.trail == null ? 0.0 : 24);
  }

  double _moreWidth(BuildContext context, KitsTokens tokens) {
    final padding = tokens.length('--ck-space-3') * 2;
    return padding + _textWidth(context, widget.moreLabel, '--ck-font-row');
  }

  double _textWidth(BuildContext context, String text, String font) {
    final painter = TextPainter(
      text: TextSpan(text: text, style: KitsType.of(context, font: font)),
      maxLines: 1,
      textDirection: TextDirection.ltr,
    )..layout();
    return painter.width;
  }
}
