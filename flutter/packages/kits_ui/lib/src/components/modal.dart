import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../foundation/kits_layers.dart';
import '../foundation/kits_text.dart';
import '../tokens.dart';

/// Ширина окна.
enum KitsModalSize { sm, md, lg }

/// Тон окна: обычное или опасное действие.
enum KitsModalTone { normal, danger }

/// Окно кита поверх страницы.
///
/// Открывается и закрывается хозяином через [open]; кит запирает фокус внутри,
/// закрывает Escape (только верхний слой) и нажатием мимо, возвращает фокус
/// туда, откуда окно открыли. [busy] запрещает закрытие и повторную отправку.
class KitsModal extends StatefulWidget {
  const KitsModal({
    super.key,
    required this.open,
    required this.title,
    required this.child,
    this.description,
    this.footer,
    this.footerStart,
    this.onClose,
    this.onSubmit,
    this.size = KitsModalSize.md,
    this.tone = KitsModalTone.normal,
    this.dismissable = true,
    this.busy = false,
    this.form = false,
    this.initialFocus,
    this.overlay,
  });

  final bool open;
  final String title;
  final Widget child;
  final String? description;
  final Widget? footer;
  final Widget? footerStart;
  final VoidCallback? onClose;
  final VoidCallback? onSubmit;
  final KitsModalSize size;
  final KitsModalTone tone;
  final bool dismissable;
  final bool busy;
  final bool form;

  /// Куда поставить фокус при открытии; иначе — в первое поле окна.
  final FocusNode? initialFocus;

  /// Свой слой для окна; иначе — корневой слой приложения.
  final OverlayState? overlay;

  static const Key dialogKey = Key('kits-modal-dialog');
  static const Key closeKey = Key('kits-modal-close');
  static const Key bodyKey = Key('kits-modal-body');
  static const Key scrimKey = Key('kits-modal-scrim');

  /// Ширина окна в логических пикселях: 24, 28 и 40 rem.
  static double widthOf(KitsModalSize size) => switch (size) {
        KitsModalSize.sm => 24 * 16,
        KitsModalSize.md => 28 * 16,
        KitsModalSize.lg => 40 * 16,
      };

  @override
  State<KitsModal> createState() => _KitsModalState();
}

class _KitsModalState extends State<KitsModal> {
  OverlayEntry? _entry;
  final Object _owner = Object();
  final FocusScopeNode _scope = FocusScopeNode(debugLabel: 'KitsModalScope');
  final GlobalKey _surfaceKey = GlobalKey(debugLabel: 'KitsModalSurface');
  FocusNode? _previousFocus;

  @override
  void initState() {
    super.initState();
    if (widget.open) {
      WidgetsBinding.instance.addPostFrameCallback((_) => _open());
    }
  }

  @override
  void didUpdateWidget(covariant KitsModal oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (widget.open && _entry == null) {
      WidgetsBinding.instance.addPostFrameCallback((_) => _open());
    } else if (!widget.open && _entry != null) {
      _tearDown(restoreFocus: false, notify: false);
    } else if (_entry != null) {
      // Окно рисуется порталом: обновлять его во время сборки родителя нельзя,
      // иначе markNeedsBuild падает на уже идущем кадре.
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted) _entry?.markNeedsBuild();
      });
    }
  }

  @override
  void dispose() {
    _tearDown(restoreFocus: false, notify: false);
    _scope.dispose();
    super.dispose();
  }

  void _open() {
    if (!mounted || _entry != null) return;
    _previousFocus = FocusManager.instance.primaryFocus;
    KitsLayers.push(_owner, _closeFromEscape);
    _entry = OverlayEntry(builder: _buildOverlay);
    (widget.overlay ?? Overlay.of(context, rootOverlay: true)).insert(_entry!);
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted) return;
      (widget.initialFocus ?? _firstInputFocus() ?? _scope).requestFocus();
    });
  }

  /// Первое поле окна: курсор в него, а не в крестик.
  FocusNode? _firstInputFocus() {
    final context = _surfaceKey.currentContext;
    if (context is! Element) return null;
    FocusNode? found;
    void visit(Element element) {
      if (found != null) return;
      final widget = element.widget;
      if (widget is EditableText) {
        found = widget.focusNode;
        return;
      }
      element.visitChildElements(visit);
    }

    context.visitChildElements(visit);
    return found;
  }

  void _tearDown({required bool restoreFocus, required bool notify}) {
    final hadEntry = _entry != null;
    _entry?.remove();
    _entry = null;
    KitsLayers.remove(_owner);
    if (notify && hadEntry) widget.onClose?.call();
    if (restoreFocus) _previousFocus?.requestFocus();
    _previousFocus = null;
  }

  void _closeFromEscape() {
    if (widget.busy || !widget.dismissable) return;
    _tearDown(restoreFocus: true, notify: true);
  }

  void _closeFromButton() {
    if (widget.busy) return;
    _tearDown(restoreFocus: true, notify: true);
  }

  void _closeFromScrim() {
    if (widget.busy || !widget.dismissable) return;
    _tearDown(restoreFocus: true, notify: true);
  }

  @override
  Widget build(BuildContext context) {
    // Окно рисуется порталом; в дереве хозяина остаётся пустое место.
    return const SizedBox.shrink();
  }

  Widget _buildOverlay(BuildContext context) {
    final tokens = KitsTokens.of(context);
    final screen = MediaQuery.sizeOf(context);
    final isSheet = screen.width < KitsModal.widthOf(KitsModalSize.lg);
    final width = isSheet ? screen.width : KitsModal.widthOf(widget.size);

    return Actions(
      actions: <Type, Action<Intent>>{
        DismissIntent: CallbackAction<DismissIntent>(
          onInvoke: (_) {
            _closeFromEscape();
            return null;
          },
        ),
      },
      child: KitsShortcuts(
        onSubmit: widget.form ? widget.onSubmit : null,
        child: Stack(
        children: <Widget>[
          Positioned.fill(
            child: GestureDetector(
              key: KitsModal.scrimKey,
              behavior: HitTestBehavior.opaque,
              onTap: _closeFromScrim,
              child: ColoredBox(color: tokens.color('--ck-scrim')),
            ),
          ),
          Positioned(
            left: isSheet ? 0 : (screen.width - width) / 2,
            top: isSheet ? null : 0,
            bottom: isSheet ? 0 : null,
            width: width,
            child: isSheet
                ? _dialog(context, isSheet: true)
                : Center(child: _dialog(context, isSheet: false)),
          ),
          ],
        ),
      ),
    );
  }

  Widget _dialog(BuildContext context, {required bool isSheet}) {
    final tokens = KitsTokens.of(context);
    final screen = MediaQuery.sizeOf(context);
    final titleColor = widget.tone == KitsModalTone.danger
        ? tokens.color('--ck-danger-strong')
        : tokens.color('--ck-text');
    final radius = Radius.circular(tokens.length('--ck-radius-panel'));

    return FocusScope(
      key: _surfaceKey,
      node: _scope,
      child: FocusTraversalGroup(
        policy: ReadingOrderTraversalPolicy(),
        child: Semantics(
          scopesRoute: true,
          namesRoute: true,
          explicitChildNodes: true,
          label: widget.title,
          child: Material(
            key: KitsModal.dialogKey,
            color: tokens.color('--ck-bg'),
            elevation: 24,
            borderRadius: BorderRadius.only(
              topLeft: radius,
              topRight: radius,
              bottomLeft: isSheet ? Radius.zero : radius,
              bottomRight: isSheet ? Radius.zero : radius,
            ),
            child: ConstrainedBox(
              constraints: BoxConstraints(
                maxHeight: isSheet ? screen.height * 0.9 : screen.height - 96,
              ),
              child: SingleChildScrollView(
                child: Padding(
                  padding: EdgeInsets.all(tokens.length('--ck-space-5')),
                  child: Column(
                    mainAxisSize: MainAxisSize.min,
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: <Widget>[
                      Row(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: <Widget>[
                          Expanded(
                            child: Text(
                              widget.title,
                              style: KitsType.of(
                                context,
                                font: '--ck-font-title',
                                leading: '--ck-leading-tight',
                                color: titleColor,
                                weight: FontWeight.w600,
                              ),
                            ),
                          ),
                          IconButton(
                            key: KitsModal.closeKey,
                            tooltip: 'Закрыть',
                            onPressed:
                                widget.busy ? null : _closeFromButton,
                            icon: const Icon(Icons.close, size: 20),
                            color: tokens.color('--ck-text-secondary'),
                          ),
                        ],
                      ),
                      if (widget.description != null) ...<Widget>[
                        SizedBox(height: tokens.length('--ck-space-1')),
                        Text(
                          widget.description!,
                          style: KitsType.of(
                            context,
                            font: '--ck-font-hint',
                            color: tokens.color('--ck-text-secondary'),
                          ),
                        ),
                      ],
                      SizedBox(height: tokens.length('--ck-space-4')),
                      KeyedSubtree(key: KitsModal.bodyKey, child: widget.child),
                      if (widget.footer != null ||
                          widget.footerStart != null) ...<Widget>[
                        Divider(
                          height: tokens.length('--ck-space-5'),
                          color: tokens.color('--ck-border'),
                        ),
                        Row(
                          children: <Widget>[
                            if (widget.footerStart != null) widget.footerStart!,
                            const Spacer(),
                            if (widget.footer != null) widget.footer!,
                          ],
                        ),
                      ],
                    ],
                  ),
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }
}

/// Ctrl+Enter и Cmd+Enter отправляют форму окна.
class KitsShortcuts extends StatelessWidget {
  const KitsShortcuts({super.key, required this.child, this.onSubmit});

  final Widget child;
  final VoidCallback? onSubmit;

  @override
  Widget build(BuildContext context) {
    return Shortcuts(
      shortcuts: const <ShortcutActivator, Intent>{
        SingleActivator(LogicalKeyboardKey.enter, control: true):
            ActivateIntent(),
        SingleActivator(LogicalKeyboardKey.enter, meta: true): ActivateIntent(),
      },
      child: Actions(
        actions: <Type, Action<Intent>>{
          ActivateIntent: CallbackAction<ActivateIntent>(
            onInvoke: (_) {
              onSubmit?.call();
              return null;
            },
          ),
        },
        child: child,
      ),
    );
  }
}
