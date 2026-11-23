import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../foundation/kits_text.dart';
import '../tokens.dart';

/// Размер поля поиска.
enum KitsSearchSize { sm, md }

/// Поле поиска кита: лупа слева, очистка справа и задержка запроса.
///
/// Escape очищает значение, не отдавая событие странице; после очистки фокус
/// остаётся в поле. [onChanged] зовётся после паузы [debounce] — один запрос на
/// серию нажатий, а не на каждый символ. Очистка и Enter отдают значение сразу.
class KitsSearchInput extends StatefulWidget {
  const KitsSearchInput({
    super.key,
    this.controller,
    this.focusNode,
    this.label = 'Поиск',
    this.placeholder = 'Поиск',
    this.onChanged,
    this.onClear,
    this.onSubmitted,
    this.debounce = const Duration(milliseconds: 300),
    this.size = KitsSearchSize.md,
    this.disabled = false,
    this.autofocus = false,
    this.suffix,
  });

  final TextEditingController? controller;
  final FocusNode? focusNode;

  /// Доступное имя поля; подсказка по умолчанию тоже «Поиск».
  final String label;
  final String? placeholder;

  /// Запрос после паузы ввода; пустая строка приходит сразу после очистки.
  final ValueChanged<String>? onChanged;

  /// Нажатие крестика: хозяин сбрасывает фильтр; поле очищается само.
  final VoidCallback? onClear;

  /// Enter в поле: отправить запрос немедленно.
  final ValueChanged<String>? onSubmitted;

  final Duration debounce;
  final KitsSearchSize size;
  final bool disabled;
  final bool autofocus;

  /// Содержимое справа внутри поля: сочетание клавиш или единица.
  final Widget? suffix;

  static const Key fieldKey = Key('kits-search-field');
  static const Key clearKey = Key('kits-search-clear');
  static const Key surfaceKey = Key('kits-search-surface');

  @override
  State<KitsSearchInput> createState() => _KitsSearchInputState();
}

class _KitsSearchInputState extends State<KitsSearchInput> {
  TextEditingController? _ownedController;
  FocusNode? _ownedFocus;
  Timer? _pending;
  bool _hovered = false;
  bool _focused = false;

  TextEditingController get _controller =>
      widget.controller ?? (_ownedController ??= TextEditingController());

  FocusNode get _focusNode =>
      widget.focusNode ?? (_ownedFocus ??= FocusNode(debugLabel: 'KitsSearch'));

  @override
  void initState() {
    super.initState();
    _controller.addListener(_onTextChanged);
    _focusNode.addListener(_onFocusChanged);
    if (widget.autofocus) {
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted) _focusNode.requestFocus();
      });
    }
  }

  @override
  void didUpdateWidget(covariant KitsSearchInput oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.controller != widget.controller) {
      (oldWidget.controller ?? _ownedController)?.removeListener(
        _onTextChanged,
      );
      _controller.addListener(_onTextChanged);
    }
    if (oldWidget.focusNode != widget.focusNode) {
      (oldWidget.focusNode ?? _ownedFocus)?.removeListener(_onFocusChanged);
      _focusNode.addListener(_onFocusChanged);
    }
  }

  @override
  void dispose() {
    _pending?.cancel();
    _controller.removeListener(_onTextChanged);
    _focusNode.removeListener(_onFocusChanged);
    _ownedController?.dispose();
    _ownedFocus?.dispose();
    super.dispose();
  }

  void _onTextChanged() {
    // Контроллер мог измениться у хозяина: перерисовываем крестик и пустоту.
    if (mounted) setState(() {});
  }

  void _onFocusChanged() {
    if (_focused != _focusNode.hasFocus) {
      setState(() => _focused = _focusNode.hasFocus);
    }
  }

  void _schedule(String value) {
    _pending?.cancel();
    _pending = Timer(widget.debounce, () {
      if (!mounted) return;
      widget.onChanged?.call(value);
    });
  }

  void _clear({required bool notify}) {
    if (_controller.text.isEmpty) {
      _focusNode.requestFocus();
      return;
    }
    _controller.clear();
    _pending?.cancel();
    widget.onClear?.call();
    if (notify) widget.onChanged?.call('');
    _focusNode.requestFocus();
  }

  KeyEventResult _onKey(FocusNode node, KeyEvent event) {
    if (event is! KeyDownEvent ||
        event.logicalKey != LogicalKeyboardKey.escape) {
      return KeyEventResult.ignored;
    }
    _clear(notify: true);
    return KeyEventResult.handled;
  }

  double _height(KitsTokens tokens) => switch (widget.size) {
    KitsSearchSize.sm => tokens.length('--ck-control-sm'),
    KitsSearchSize.md => tokens.length('--ck-control-md'),
  };

  @override
  Widget build(BuildContext context) {
    final tokens = KitsTokens.of(context);
    final showClear = _controller.text.isNotEmpty && !widget.disabled;
    final border = _focused
        ? tokens.color('--ck-focus')
        : tokens.color('--ck-field-border');

    final input = TextField(
      key: KitsSearchInput.fieldKey,
      controller: _controller,
      focusNode: _focusNode,
      enabled: !widget.disabled,
      autocorrect: false,
      enableSuggestions: false,
      textInputAction: TextInputAction.search,
      onChanged: _schedule,
      onSubmitted: (value) {
        _pending?.cancel();
        widget.onSubmitted?.call(value);
      },
      style: KitsType.of(
        context,
        font: '--ck-font-control',
        color: tokens.color('--ck-text'),
      ),
      cursorColor: tokens.color('--ck-focus'),
      decoration: InputDecoration(
        isDense: true,
        border: InputBorder.none,
        focusedBorder: InputBorder.none,
        enabledBorder: InputBorder.none,
        disabledBorder: InputBorder.none,
        contentPadding: EdgeInsets.zero,
        hintText: widget.placeholder,
        hintStyle: KitsType.of(
          context,
          font: '--ck-font-control',
          color: tokens.color('--ck-field-placeholder'),
        ),
      ),
    );

    Widget? clear;
    if (showClear) {
      clear = Semantics(
        button: true,
        label: 'Очистить',
        child: MouseRegion(
          cursor: SystemMouseCursors.click,
          child: GestureDetector(
            key: KitsSearchInput.clearKey,
            behavior: HitTestBehavior.opaque,
            // Крестик не забирает фокус: после очистки он остаётся в поле.
            onTapDown: (_) => _focusNode.requestFocus(),
            onTap: () => _clear(notify: true),
            child: SizedBox(
              width: 24,
              height: 24,
              child: Icon(
                Icons.close,
                size: 16,
                color: tokens.color('--ck-text-secondary'),
              ),
            ),
          ),
        ),
      );
    }

    Widget surface = MouseRegion(
      onEnter: (_) => setState(() => _hovered = true),
      onExit: (_) => setState(() => _hovered = false),
      cursor: widget.disabled
          ? SystemMouseCursors.forbidden
          : SystemMouseCursors.text,
      child: Container(
        key: KitsSearchInput.surfaceKey,
        height: _height(tokens),
        padding: EdgeInsets.symmetric(
          horizontal: tokens.length('--ck-space-3'),
        ),
        decoration: BoxDecoration(
          color: _focused
              ? tokens.color('--ck-field-fill')
              : (_hovered
                    ? tokens.color('--ck-field-fill-hover')
                    : tokens.color('--ck-field-fill')),
          border: Border.all(color: border),
          borderRadius: BorderRadius.circular(
            tokens.length('--ck-radius-control'),
          ),
        ),
        child: Row(
          children: <Widget>[
            Icon(
              Icons.search,
              size: 18,
              color: tokens.color('--ck-text-secondary'),
            ),
            SizedBox(width: tokens.length('--ck-space-2')),
            Expanded(child: input),
            if (widget.suffix != null) ...<Widget>[
              SizedBox(width: tokens.length('--ck-space-2')),
              widget.suffix!,
            ],
            if (clear != null) ...<Widget>[
              SizedBox(width: tokens.length('--ck-space-1')),
              clear,
            ],
          ],
        ),
      ),
    );

    if (widget.disabled) surface = Opacity(opacity: 0.55, child: surface);

    return Semantics(
      container: true,
      textField: true,
      label: widget.label,
      enabled: !widget.disabled,
      onTap: widget.disabled ? null : () => _focusNode.requestFocus(),
      child: Focus(onKeyEvent: _onKey, child: surface),
    );
  }
}
