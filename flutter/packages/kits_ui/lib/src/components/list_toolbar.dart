import 'package:flutter/material.dart';

import '../tokens.dart';
import 'button.dart';
import 'search_input.dart';
import 'segmented_control.dart';

/// Тулбар списка кита: поиск, сегменты и действия справа.
///
/// На узком контейнере не появляется горизонтальная прокрутка: поиск занимает
/// свою строку, сегменты и действия переносятся. «Сбросить» показывается, когда
/// есть непустой поиск или хозяин сказал, что фильтры включены. Поле поиска
/// следует за [searchValue]: сброс снаружи очищает и его.
class KitsListToolbar extends StatefulWidget {
  const KitsListToolbar({
    super.key,
    this.leading,
    this.searchValue = '',
    this.onSearchChange,
    this.searchPlaceholder = 'Поиск',
    this.segments = const <KitsSegment>[],
    this.segmentValue,
    this.onSegmentChange,
    this.segmentLabel = 'Разделы',
    this.extra,
    this.actions = const <Widget>[],
    this.onReset,
    this.showReset = false,
    this.sticky = false,
    this.padding,
  });

  /// Вкладки слева или заголовок полосы.
  final Widget? leading;

  final String searchValue;
  final ValueChanged<String>? onSearchChange;
  final String searchPlaceholder;

  final List<KitsSegment> segments;
  final String? segmentValue;
  final ValueChanged<String>? onSegmentChange;
  final String segmentLabel;

  /// Свои детали раздела: период, сортировка.
  final Widget? extra;

  /// Действия справа; последнее — главное.
  final List<Widget> actions;

  final VoidCallback? onReset;
  final bool showReset;

  /// Прилипает под заголовком страницы.
  final bool sticky;

  final EdgeInsetsGeometry? padding;

  static const Key toolbarKey = Key('kits-list-toolbar');
  static const Key searchKey = Key('kits-list-toolbar-search');
  static const Key resetKey = Key('kits-list-toolbar-reset');

  @override
  State<KitsListToolbar> createState() => _KitsListToolbarState();
}

class _KitsListToolbarState extends State<KitsListToolbar> {
  late final TextEditingController _search = TextEditingController(
    text: widget.searchValue,
  );

  @override
  void didUpdateWidget(covariant KitsListToolbar oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (widget.searchValue != _search.text) {
      _search.value = TextEditingValue(
        text: widget.searchValue,
        selection: TextSelection.collapsed(offset: widget.searchValue.length),
      );
    }
  }

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  bool get _resetVisible =>
      widget.onReset != null &&
      (widget.showReset || widget.searchValue.isNotEmpty);

  @override
  Widget build(BuildContext context) {
    assert(
      widget.segments.isEmpty ||
          (widget.segmentValue != null && widget.onSegmentChange != null),
      'сегментам нужны значение и обработчик',
    );
    final tokens = KitsTokens.of(context);
    final gap = SizedBox(width: tokens.length('--ck-space-2'));

    final search = KitsSearchInput(
      key: KitsListToolbar.searchKey,
      controller: _search,
      label: 'Поиск',
      placeholder: widget.searchPlaceholder,
      onChanged: widget.onSearchChange,
    );

    final segmentControl = widget.segments.isEmpty
        ? null
        : KitsSegmentedControl(
            segments: widget.segments,
            value: widget.segmentValue!,
            label: widget.segmentLabel,
            size: KitsSegmentSize.sm,
            onChanged: widget.onSegmentChange!,
          );

    final tail = <Widget>[
      if (widget.extra != null) widget.extra!,
      if (_resetVisible)
        KitsButton(
          key: KitsListToolbar.resetKey,
          label: 'Сбросить',
          variant: KitsButtonVariant.quiet,
          size: KitsButtonSize.sm,
          onClick: widget.onReset,
        ),
      ...widget.actions,
    ];

    Widget content = LayoutBuilder(
      builder: (context, constraints) {
        final narrow = constraints.maxWidth < 40 * 16;
        if (narrow) {
          return Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            mainAxisSize: MainAxisSize.min,
            children: <Widget>[
              if (widget.leading != null) ...<Widget>[
                widget.leading!,
                SizedBox(height: tokens.length('--ck-space-2')),
              ],
              search,
              if (segmentControl != null) ...<Widget>[
                SizedBox(height: tokens.length('--ck-space-2')),
                Align(alignment: Alignment.centerLeft, child: segmentControl),
              ],
              if (tail.isNotEmpty) ...<Widget>[
                SizedBox(height: tokens.length('--ck-space-2')),
                Align(
                  alignment: Alignment.centerRight,
                  child: Wrap(
                    spacing: tokens.length('--ck-space-2'),
                    runSpacing: tokens.length('--ck-space-2'),
                    crossAxisAlignment: WrapCrossAlignment.center,
                    children: tail,
                  ),
                ),
              ],
            ],
          );
        }
        return Row(
          children: <Widget>[
            if (widget.leading != null) ...<Widget>[widget.leading!, gap],
            Expanded(child: search),
            if (segmentControl != null) ...<Widget>[gap, segmentControl],
            if (tail.isNotEmpty) ...<Widget>[gap, ...tail],
          ],
        );
      },
    );

    content = Padding(
      padding:
          widget.padding ??
          EdgeInsets.symmetric(
            horizontal: tokens.length('--ck-space-3'),
            vertical: tokens.length('--ck-space-2'),
          ),
      child: content,
    );

    if (widget.sticky) {
      content = Material(color: tokens.color('--ck-bg'), child: content);
    }

    return Container(
      key: KitsListToolbar.toolbarKey,
      color: widget.sticky ? tokens.color('--ck-bg') : const Color(0x00000000),
      child: content,
    );
  }
}
