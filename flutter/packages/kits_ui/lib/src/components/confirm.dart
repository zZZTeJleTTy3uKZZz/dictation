import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';

import '../foundation/kits_text.dart';
import '../tokens.dart';
import 'button.dart';
import 'field.dart';
import 'modal.dart';

/// Ключи окна подтверждения — по ним проверки находят кнопки и поля.
abstract final class KitsAsk {
  static const Key cancelKey = Key('kits-ask-cancel');
  static const Key confirmKey = Key('kits-ask-confirm');
  static const Key acknowledgeKey = Key('kits-ask-acknowledge');
  static const Key inputKey = Key('kits-ask-input');
  static const Key errorKey = Key('kits-ask-error');

  /// Забыть очередь вопросов: между независимыми проверками.
  @visibleForTesting
  static void reset() {
    _queue.clear();
    _showing = false;
  }
}

class _AskRequest {
  _AskRequest({
    required this.title,
    required this.text,
    required this.confirmLabel,
    required this.cancelLabel,
    required this.danger,
    required this.acknowledge,
    required this.consequences,
    required this.typeToConfirm,
    required this.onConfirm,
    required this.dismissable,
    required this.errorText,
  });

  final String title;
  final String? text;
  final String confirmLabel;
  final String cancelLabel;
  final bool danger;
  final bool acknowledge;
  final List<String> consequences;
  final String? typeToConfirm;
  final Future<void> Function()? onConfirm;
  final bool dismissable;
  final String? errorText;
  final Completer<bool?> completer = Completer<bool?>();
}

final List<_AskRequest> _queue = <_AskRequest>[];
bool _showing = false;

/// Задать вопрос человеку: подтверждение поверх окна кита.
///
/// Фокус при открытии — на «Отменить», чтобы Enter по привычке не подтверждал.
/// Escape и нажатие мимо отвечают отказом. Вопросы идут очередью по одному.
Future<bool?> askConfirm(
  BuildContext context, {
  required String title,
  String? text,
  String confirmLabel = 'Да',
  String cancelLabel = 'Отменить',
  bool danger = false,
  bool dismissable = true,
}) {
  return _enqueue(
    context,
    _AskRequest(
      title: title,
      text: text,
      confirmLabel: confirmLabel,
      cancelLabel: cancelLabel,
      danger: danger,
      acknowledge: false,
      consequences: const <String>[],
      typeToConfirm: null,
      onConfirm: null,
      dismissable: dismissable,
      errorText: null,
    ),
  );
}

/// Опасное подтверждение: последствия, чекбокс «Да, я уверен» и необязательный
/// ввод имени. Кнопка danger недоступна, пока не отмечен чекбокс и не совпало
/// имя; ошибка действия остаётся в окне, окно не закрывается.
Future<bool?> askDanger(
  BuildContext context, {
  required String title,
  required List<String> consequences,
  String? text,
  String confirmLabel = 'Удалить',
  String cancelLabel = 'Отменить',
  bool acknowledge = true,
  String? typeToConfirm,
  Future<void> Function()? onConfirm,
  String? errorText,
}) {
  assert(
    consequences.isNotEmpty,
    'у опасного окна обязан быть перечень потерь',
  );
  return _enqueue(
    context,
    _AskRequest(
      title: title,
      text: text,
      confirmLabel: confirmLabel,
      cancelLabel: cancelLabel,
      danger: true,
      acknowledge: acknowledge,
      consequences: consequences,
      typeToConfirm: typeToConfirm,
      onConfirm: onConfirm,
      dismissable: true,
      errorText: errorText,
    ),
  );
}

Future<bool?> _enqueue(BuildContext context, _AskRequest request) {
  _queue.add(request);
  _showNext(context);
  return request.completer.future;
}

void _showNext(BuildContext context) {
  if (_showing || _queue.isEmpty) return;
  if (!context.mounted) {
    final request = _queue.removeAt(0);
    request.completer.complete(null);
    _showNext(context);
    return;
  }
  _showing = true;
  final request = _queue.first;
  final overlay = Overlay.of(context, rootOverlay: true);
  final opener = FocusManager.instance.primaryFocus;
  // Окно кита само живёт в OverlayEntry: своей записи не заводим, иначе две
  // записи делят одно «место» аннотаций и платформа падает на третьей.
  late final OverlayEntry entry;
  entry = OverlayEntry(
    opaque: false,
    builder: (_) => _AskOverlay(
      overlay: overlay,
      request: request,
      onDone: (value) {
        if (entry.mounted) entry.remove();
        _showing = false;
        _queue.remove(request);
        if (!request.completer.isCompleted) request.completer.complete(value);
        opener?.requestFocus();
        WidgetsBinding.instance.addPostFrameCallback((_) {
          if (context.mounted) _showNext(context);
        });
      },
    ),
  );
  overlay.insert(entry);
}

/// Служебный слой вопроса: нужен только как место, где живёт окно [KitsModal].
class _AskOverlay extends StatelessWidget {
  const _AskOverlay({
    required this.overlay,
    required this.request,
    required this.onDone,
  });

  final OverlayState overlay;
  final _AskRequest request;
  final ValueChanged<bool?> onDone;

  @override
  Widget build(BuildContext context) {
    return FocusScope(
      canRequestFocus: false,
      // Свой Overlay на месте записи: окно кита вставляется туда.
      child: Overlay(
        initialEntries: <OverlayEntry>[
          OverlayEntry(
            builder: (_) =>
                _AskHost(overlay: overlay, request: request, onDone: onDone),
          ),
        ],
      ),
    );
  }
}

class _AskHost extends StatefulWidget {
  const _AskHost({
    required this.overlay,
    required this.request,
    required this.onDone,
  });

  final OverlayState overlay;
  final _AskRequest request;
  final ValueChanged<bool?> onDone;

  @override
  State<_AskHost> createState() => _AskHostState();
}

class _AskHostState extends State<_AskHost> {
  final FocusNode _cancelFocus = FocusNode(debugLabel: 'KitsAskCancel');
  final TextEditingController _typed = TextEditingController();
  bool _acknowledged = false;
  bool _busy = false;
  String? _error;
  bool _done = false;

  @override
  void dispose() {
    _cancelFocus.dispose();
    _typed.dispose();
    super.dispose();
  }

  bool get _matchesType {
    final expected = widget.request.typeToConfirm;
    if (expected == null) return true;
    return _typed.text.trim() == expected;
  }

  bool get _canConfirm =>
      !_busy && (!widget.request.acknowledge || _acknowledged) && _matchesType;

  void _finish(bool value) {
    if (_done) return;
    _done = true;
    widget.onDone(value);
  }

  void _cancel() {
    if (_busy) return;
    _finish(false);
  }

  Future<void> _confirm() async {
    if (!_canConfirm) return;
    final action = widget.request.onConfirm;
    if (action == null) {
      _finish(true);
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await action();
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _busy = false;
        _error = widget.request.errorText ?? _messageOf(error);
      });
      return;
    }
    if (!mounted) return;
    _finish(true);
  }

  /// Понятный текст ошибки: без «Exception:», «Bad state:» и прочих префиксов.
  static String _messageOf(Object error) {
    if (error is String) return error.trim();
    var text = error.toString().trim();
    for (final prefix in <String>[
      'Bad state: ',
      'Exception: ',
      'StateError: ',
      'FormatException: ',
      'ArgumentError: ',
      'Error: ',
    ]) {
      if (text.startsWith(prefix)) {
        text = text.substring(prefix.length).trim();
        break;
      }
    }
    return text.isEmpty ? 'Не удалось выполнить действие' : text;
  }

  @override
  Widget build(BuildContext context) {
    final tokens = KitsTokens.of(context);
    final request = widget.request;

    final body = <Widget>[
      if (request.consequences.isNotEmpty)
        Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisSize: MainAxisSize.min,
          children: <Widget>[
            for (final item in request.consequences)
              Padding(
                padding: EdgeInsets.only(bottom: tokens.length('--ck-space-1')),
                child: Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: <Widget>[
                    Text(
                      '•',
                      style: KitsType.of(
                        context,
                        font: '--ck-font-hint',
                        color: tokens.color('--ck-danger-strong'),
                      ),
                    ),
                    SizedBox(width: tokens.length('--ck-space-2')),
                    Expanded(
                      child: Text(
                        item,
                        style: KitsType.of(
                          context,
                          font: '--ck-font-hint',
                          leading: '--ck-leading-normal',
                          color: tokens.color('--ck-text-secondary'),
                        ),
                      ),
                    ),
                  ],
                ),
              ),
          ],
        ),
      if (request.acknowledge)
        InkWell(
          onTap: _busy
              ? null
              : () => setState(() => _acknowledged = !_acknowledged),
          child: Row(
            children: <Widget>[
              Checkbox(
                key: KitsAsk.acknowledgeKey,
                value: _acknowledged,
                onChanged: _busy
                    ? null
                    : (value) => setState(() => _acknowledged = value ?? false),
                activeColor: tokens.color('--ck-danger-fill'),
              ),
              Expanded(
                child: Text(
                  'Да, я уверен',
                  style: KitsType.of(
                    context,
                    font: '--ck-font-control',
                    color: tokens.color('--ck-text'),
                  ),
                ),
              ),
            ],
          ),
        ),
      if (request.typeToConfirm != null) ...<Widget>[
        SizedBox(height: tokens.length('--ck-space-2')),
        KitsTextField(
          key: KitsAsk.inputKey,
          label: request.typeToConfirm!,
          placeholder: request.typeToConfirm,
          controller: _typed,
          onChanged: (_) => setState(() {}),
        ),
      ],
      if (_error != null) ...<Widget>[
        SizedBox(height: tokens.length('--ck-space-2')),
        Semantics(
          liveRegion: true,
          label: _error,
          child: Text(
            _error!,
            key: KitsAsk.errorKey,
            style: KitsType.of(
              context,
              font: '--ck-font-hint',
              color: tokens.color('--ck-danger-strong'),
            ),
          ),
        ),
      ],
    ];

    return Stack(
      fit: StackFit.expand,
      children: <Widget>[
        ColoredBox(
          color: tokens.color('--ck-scrim'),
          child: const SizedBox.expand(),
        ),
        _modal(context, body),
      ],
    );
  }

  Widget _modal(BuildContext context, List<Widget> body) {
    final tokens = KitsTokens.of(context);
    final request = widget.request;
    return KitsModal(
      overlay: widget.overlay,
      open: true,
      title: request.title,
      description: request.text,
      size: KitsModalSize.sm,
      tone: request.danger ? KitsModalTone.danger : KitsModalTone.normal,
      busy: _busy,
      dismissable: request.dismissable && !_busy,
      initialFocus: _cancelFocus,
      onClose: _cancel,
      footer: Row(
        mainAxisSize: MainAxisSize.min,
        children: <Widget>[
          KitsButton(
            key: KitsAsk.cancelKey,
            label: request.cancelLabel,
            focusNode: _cancelFocus,
            onClick: _cancel,
          ),
          SizedBox(width: tokens.length('--ck-space-2')),
          KitsButton(
            key: KitsAsk.confirmKey,
            label: request.confirmLabel,
            variant: request.danger
                ? KitsButtonVariant.danger
                : KitsButtonVariant.primary,
            disabled: !_canConfirm,
            loading: _busy,
            onClick: _confirm,
          ),
        ],
      ),
      child: body.isEmpty
          ? const SizedBox.shrink()
          : Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              mainAxisSize: MainAxisSize.min,
              children: body,
            ),
    );
  }
}
