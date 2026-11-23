import 'package:flutter/material.dart';

import '../foundation/kits_text.dart';
import '../foundation/kits_tip.dart';
import '../tokens.dart';

/// Вид поля: поле формы, ячейка таблицы, поиск, многострочное поле.
enum KitsFieldVariant { field, cell, search, textarea, textareaAutogrow }

/// Размер поля.
enum KitsFieldSize { sm, md }

/// Поле ввода кита: подпись, подсказка за «?», ошибка и само поле.
///
/// В покое — заливка без рамки, место под рамку занято; в фокусе рамка цвета
/// `--ck-focus`; ошибка делает поле недействительным и объясняет причину под
/// полем. Подпись связана с полем: нажатие на неё ставит фокус.
class KitsTextField extends StatefulWidget {
  const KitsTextField({
    super.key,
    required this.label,
    this.controller,
    this.focusNode,
    this.hint,
    this.error,
    this.required = false,
    this.hideLabel = false,
    this.disabled = false,
    this.variant = KitsFieldVariant.field,
    this.size = KitsFieldSize.md,
    this.placeholder,
    this.prefix,
    this.suffix,
    this.onChanged,
    this.onClear,
    this.autoFocusEnd = false,
    this.autofocus = false,
    this.maxRows = 12,
  });

  /// Подпись поля; при [hideLabel] остаётся только диктору.
  final String label;

  final TextEditingController? controller;
  final FocusNode? focusNode;

  /// Пояснение за «?»: одна-две строки, показывается подсказкой кита.
  final String? hint;

  /// Текст ошибки под полем: его наличие делает поле недействительным.
  final String? error;

  final bool required;
  final bool hideLabel;
  final bool disabled;

  final KitsFieldVariant variant;
  final KitsFieldSize size;
  final String? placeholder;
  final Widget? prefix;
  final Widget? suffix;

  final ValueChanged<String>? onChanged;

  /// Кнопка «Очистить» внутри поля, пока значение непустое и поле доступно.
  final VoidCallback? onClear;

  /// Фокус и курсор в конце текста при открытии.
  final bool autoFocusEnd;

  final bool autofocus;

  /// Предел роста многострочного поля, дальше прокрутка.
  final int maxRows;

  /// Ключ обёртки поля — по нему проверки читают рамку.
  static const Key surfaceKey = Key('kits-field-surface');

  /// Ключ крестика очистки.
  static const Key clearKey = Key('kits-field-clear');

  /// Ключ значка «?» с подсказкой.
  static const Key hintKey = Key('kits-field-hint');

  @override
  State<KitsTextField> createState() => _KitsTextFieldState();
}

class _KitsTextFieldState extends State<KitsTextField> {
  TextEditingController? _ownedController;
  FocusNode? _ownedFocus;
  bool _hovered = false;
  bool _focused = false;

  TextEditingController get _controller =>
      widget.controller ?? (_ownedController ??= TextEditingController());

  FocusNode get _focusNode =>
      widget.focusNode ?? (_ownedFocus ??= FocusNode(debugLabel: 'KitsField'));

  @override
  void initState() {
    super.initState();
    _focusNode.addListener(_onFocusChange);
    if (widget.autoFocusEnd) {
      WidgetsBinding.instance.addPostFrameCallback((_) {
        _focusNode.requestFocus();
        final text = _controller.text;
        _controller.selection =
            TextSelection.collapsed(offset: text.length);
      });
    } else if (widget.autofocus) {
      WidgetsBinding.instance.addPostFrameCallback((_) {
        _focusNode.requestFocus();
      });
    }
  }

  @override
  void didUpdateWidget(covariant KitsTextField oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.focusNode != widget.focusNode) {
      (oldWidget.focusNode ?? _ownedFocus)?.removeListener(_onFocusChange);
      _focusNode.addListener(_onFocusChange);
    }
  }

  void _onFocusChange() {
    if (_focused != _focusNode.hasFocus) {
      setState(() => _focused = _focusNode.hasFocus);
    }
  }

  @override
  void dispose() {
    _focusNode.removeListener(_onFocusChange);
    _ownedController?.dispose();
    _ownedFocus?.dispose();
    super.dispose();
  }

  Color _fill(KitsTokens tokens) {
    final base = widget.variant == KitsFieldVariant.cell
        ? const Color(0x00000000)
        : tokens.color('--ck-field-fill');
    if (_focused) return base;
    if (_hovered) return tokens.color('--ck-field-fill-hover');
    return base;
  }

  Color _border(KitsTokens tokens) {
    if (widget.error != null) return tokens.color('--ck-danger-strong');
    if (_focused) return tokens.color('--ck-focus');
    return tokens.color('--ck-field-border');
  }

  @override
  Widget build(BuildContext context) {
    final tokens = KitsTokens.of(context);
    final isTextarea = widget.variant == KitsFieldVariant.textarea ||
        widget.variant == KitsFieldVariant.textareaAutogrow;
    final height = widget.size == KitsFieldSize.sm
        ? tokens.length('--ck-control-sm')
        : tokens.length('--ck-control-md');
    final showClear = widget.onClear != null &&
        !widget.disabled &&
        _controller.text.isNotEmpty;

    Widget? prefixIcon = widget.prefix;
    if (widget.variant == KitsFieldVariant.search && prefixIcon == null) {
      prefixIcon = Icon(
        Icons.search,
        size: 18,
        color: tokens.color('--ck-text-secondary'),
      );
    }

    final input = TextField(
      controller: _controller,
      focusNode: _focusNode,
      enabled: !widget.disabled,
      onChanged: widget.onChanged,
      autofocus: false,
      maxLines: isTextarea
          ? (widget.variant == KitsFieldVariant.textareaAutogrow
              ? widget.maxRows
              : 3)
          : 1,
      minLines: widget.variant == KitsFieldVariant.textareaAutogrow ? 3 : null,
      keyboardType:
          isTextarea ? TextInputType.multiline : TextInputType.text,
      textInputAction:
          isTextarea ? TextInputAction.newline : TextInputAction.done,
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

    final field = MouseRegion(
      onEnter: (_) => setState(() => _hovered = true),
      onExit: (_) => setState(() => _hovered = false),
      cursor: widget.disabled
          ? SystemMouseCursors.forbidden
          : SystemMouseCursors.text,
      child: Container(
        key: KitsTextField.surfaceKey,
        height: isTextarea ? null : height,
        constraints: isTextarea
            ? BoxConstraints(minHeight: height)
            : null,
        padding: EdgeInsets.symmetric(
          horizontal: tokens.length('--ck-space-3'),
          vertical: isTextarea ? tokens.length('--ck-space-2') : 0,
        ),
        decoration: BoxDecoration(
          color: _fill(tokens),
          border: Border.all(color: _border(tokens)),
          borderRadius: BorderRadius.circular(tokens.length('--ck-radius-control')),
        ),
        child: Row(
          crossAxisAlignment:
              isTextarea ? CrossAxisAlignment.start : CrossAxisAlignment.center,
          children: <Widget>[
            if (prefixIcon != null) ...<Widget>[
              prefixIcon,
              SizedBox(width: tokens.length('--ck-space-2')),
            ],
            Expanded(child: input),
            if (widget.suffix != null) ...<Widget>[
              SizedBox(width: tokens.length('--ck-space-2')),
              widget.suffix!,
            ],
            if (showClear)
              GestureDetector(
                key: KitsTextField.clearKey,
                onTap: () {
                  widget.onClear?.call();
                  _controller.clear();
                  _focusNode.requestFocus();
                  setState(() {});
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
          ],
        ),
      ),
    );

    Widget? labelRow;
    if (!widget.hideLabel) {
      labelRow = GestureDetector(
        behavior: HitTestBehavior.opaque,
        onTap: () => _focusNode.requestFocus(),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: <Widget>[
            Text(
              widget.label,
              style: KitsType.of(
                context,
                font: '--ck-font-hint',
                color: tokens.color('--ck-text-secondary'),
              ),
            ),
            if (widget.required)
              ExcludeSemantics(
                child: Text(
                  ' *',
                  style: KitsType.of(
                    context,
                    font: '--ck-font-hint',
                    color: tokens.color('--ck-danger-strong'),
                  ),
                ),
              ),
            if (widget.hint != null)
              KitsTip(
                message: widget.hint!,
                child: Padding(
                  key: KitsTextField.hintKey,
                  padding: EdgeInsets.only(left: tokens.length('--ck-space-1')),
                  child: Icon(
                    Icons.help_outline,
                    size: 14,
                    color: tokens.color('--ck-text-secondary'),
                  ),
                ),
              ),
          ],
        ),
      );
    }

    final children = <Widget>[
      if (labelRow != null) ...<Widget>[
        labelRow,
        SizedBox(height: tokens.length('--ck-space-1')),
      ],
      widget.disabled ? Opacity(opacity: 0.55, child: field) : field,
      if (widget.error != null) ...<Widget>[
        SizedBox(height: tokens.length('--ck-space-1')),
        Semantics(
          liveRegion: true,
          label: widget.error,
          child: Text(
            widget.error!,
            style: KitsType.of(
              context,
              font: '--ck-font-hint',
              color: tokens.color('--ck-danger-strong'),
            ),
          ),
        ),
      ],
    ];

    return Semantics(
      container: true,
      textField: true,
      label: widget.label,
      hint: widget.error,
      enabled: !widget.disabled,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        mainAxisSize: MainAxisSize.min,
        children: children,
      ),
    );
  }
}
