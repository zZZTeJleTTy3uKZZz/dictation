import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../foundation/kits_text.dart';
import '../tokens.dart';

/// Тон тоста: обычная весть, успех, ошибка.
enum KitsToastTone { neutral, ok, bad }

/// Одна весть в очереди.
class KitsToastMessage {
  KitsToastMessage({
    required this.id,
    required this.text,
    this.tone = KitsToastTone.neutral,
    this.actionLabel,
    this.onAction,
    this.duration = const Duration(milliseconds: 4000),
  });

  final String id;
  final String text;
  final KitsToastTone tone;

  /// Подпись действия справа; без своей подписи действие называется «Вернуть».
  final String? actionLabel;
  final VoidCallback? onAction;
  final Duration duration;
}

/// Очередь тостов: один механизм для своей и общей очереди страницы.
///
/// Очередь не зависит от виджетов и живёт столько, сколько живёт её хозяин;
/// четвёртая весть вытесняет старшую — одновременно читаются не больше трёх.
class KitsToaster extends ChangeNotifier {
  KitsToaster({this.maxVisible = 3});

  /// Сколько вестей видно одновременно.
  final int maxVisible;

  final List<KitsToastMessage> _items = <KitsToastMessage>[];
  int _counter = 0;

  /// Текущая очередь, от старшей к младшей.
  List<KitsToastMessage> get items => List<KitsToastMessage>.unmodifiable(_items);

  /// Поставить весть в очередь; возвращает её id.
  String notify(
    String text, {
    KitsToastTone tone = KitsToastTone.neutral,
    String? actionLabel,
    VoidCallback? onAction,
    Duration? duration,
  }) {
    final id = 'toast-${_counter++}';
    _items.add(
      KitsToastMessage(
        id: id,
        text: text,
        tone: tone,
        actionLabel: actionLabel,
        onAction: onAction,
        duration: duration ??
            (onAction == null
                ? const Duration(milliseconds: 4000)
                : const Duration(milliseconds: 8000)),
      ),
    );
    _trim();
    notifyListeners();
    return id;
  }

  /// Весть об обратимом действии: «Вернуть», 8 секунд, слушает Ctrl+Z.
  String notifyUndo(String text, {required VoidCallback onUndo}) => notify(
        text,
        actionLabel: 'Вернуть',
        onAction: onUndo,
        duration: const Duration(milliseconds: 8000),
      );

  /// Убрать весть по id.
  void dismiss(String id) {
    final before = _items.length;
    _items.removeWhere((item) => item.id == id);
    if (_items.length != before) notifyListeners();
  }

  /// Убрать все вести.
  void clear() {
    if (_items.isEmpty) return;
    _items.clear();
    notifyListeners();
  }

  /// Последняя весть, которая умеет отменять действие.
  KitsToastMessage? get undoable {
    for (final item in _items.reversed) {
      if (item.onAction != null) return item;
    }
    return null;
  }

  void _trim() {
    while (_items.length > maxVisible) {
      _items.removeAt(0);
    }
  }
}

/// Область тостов: живые сообщения внизу страницы.
///
/// Область остаётся в DOM пустой: диктору не нужно ждать её появления.
/// Появление и уход вести не переносят фокус — тост ничего не забирает.
class KitsToasts extends StatefulWidget {
  const KitsToasts({
    super.key,
    required this.toaster,
    this.bottomOffset = 0,
  });

  final KitsToaster toaster;

  /// Поднимает область над закреплённой панелью действий.
  final double bottomOffset;

  static const Key regionKey = Key('kits-toast-region');

  /// Ключ вести.
  static Key toastKey(String id) => ValueKey<String>('kits-toast-$id');

  @override
  State<KitsToasts> createState() => _KitsToastsState();
}

class _KitsToastsState extends State<KitsToasts> {
  final Map<String, Timer> _timers = <String, Timer>{};
  bool _listening = false;

  @override
  void initState() {
    super.initState();
    HardwareKeyboard.instance.addHandler(_onKey);
    _listening = true;
    _syncTimers();
  }

  @override
  void didUpdateWidget(covariant KitsToasts oldWidget) {
    super.didUpdateWidget(oldWidget);
    _syncTimers();
  }

  @override
  void dispose() {
    if (_listening) HardwareKeyboard.instance.removeHandler(_onKey);
    for (final timer in _timers.values) {
      timer.cancel();
    }
    _timers.clear();
    super.dispose();
  }

  void _syncTimers() {
    final alive = widget.toaster.items.map((item) => item.id).toSet();
    for (final id in _timers.keys.toList()) {
      if (!alive.contains(id)) {
        _timers.remove(id)!.cancel();
      }
    }
    for (final item in widget.toaster.items) {
      if (_timers.containsKey(item.id)) continue;
      _timers[item.id] = Timer(item.duration, () {
        _timers.remove(item.id);
        widget.toaster.dismiss(item.id);
      });
    }
  }

  /// Ctrl+Z зовёт отмену последнего видимого тоста, если фокус не в поле.
  bool _onKey(KeyEvent event) {
    if (event is! KeyDownEvent || event.logicalKey != LogicalKeyboardKey.keyZ) {
      return false;
    }
    final keyboard = HardwareKeyboard.instance;
    if (!keyboard.isControlPressed && !keyboard.isMetaPressed) return false;
    if (_focusInTextInput()) return false;
    final toast = widget.toaster.undoable;
    if (toast?.onAction == null) return false;
    toast!.onAction!();
    widget.toaster.dismiss(toast.id);
    return true;
  }

  bool _focusInTextInput() {
    final context = FocusManager.instance.primaryFocus?.context;
    if (context is! Element) return false;
    var inside = false;
    context.visitAncestorElements((element) {
      if (element.widget is EditableText) {
        inside = true;
        return false;
      }
      return true;
    });
    return inside;
  }

  @override
  Widget build(BuildContext context) {
    final tokens = KitsTokens.of(context);
    return ListenableBuilder(
      listenable: widget.toaster,
      builder: (context, _) {
        final items = widget.toaster.items.reversed.toList(growable: false);
        return Align(
          alignment: Alignment.bottomCenter,
          child: Padding(
            padding: EdgeInsets.fromLTRB(
              tokens.length('--ck-space-4'),
              tokens.length('--ck-space-4'),
              tokens.length('--ck-space-4'),
              tokens.length('--ck-space-4') + widget.bottomOffset,
            ),
            child: Semantics(
              container: true,
              liveRegion: true,
              label: 'Уведомления',
              child: ConstrainedBox(
                key: KitsToasts.regionKey,
                constraints: const BoxConstraints(maxWidth: 420),
                child: Column(
                  mainAxisSize: MainAxisSize.min,
                  children: <Widget>[
                    for (final item in items)
                      Padding(
                        padding: EdgeInsets.only(top: tokens.length('--ck-space-2')),
                        child: _toast(context, tokens, item),
                      ),
                  ],
                ),
              ),
            ),
          ),
        );
      },
    );
  }

  Widget _toast(
    BuildContext context,
    KitsTokens tokens,
    KitsToastMessage message,
  ) {
    final bad = message.tone == KitsToastTone.bad;
    final ok = message.tone == KitsToastTone.ok;
    final label = message.actionLabel ?? 'Вернуть';
    return Semantics(
      container: true,
      liveRegion: true,
      label: message.text,
      hint: bad ? 'Ошибка' : null,
      child: Container(
        key: KitsToasts.toastKey(message.id),
        constraints: BoxConstraints(minHeight: tokens.length('--ck-control-md')),
        decoration: BoxDecoration(
          color: tokens.color('--ck-toast-bg'),
          borderRadius: BorderRadius.circular(tokens.length('--ck-radius-full')),
          boxShadow: tokens.shadows('--ck-shadow-pop-soft'),
        ),
        padding: EdgeInsets.symmetric(
          horizontal: tokens.length('--ck-space-5'),
          vertical: tokens.length('--ck-space-2'),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: <Widget>[
            if (ok || bad) ...<Widget>[
              Container(
                width: 3,
                height: 24,
                decoration: BoxDecoration(
                  color: ok
                      ? tokens.color('--ck-online')
                      : tokens.color('--ck-danger-fill'),
                  borderRadius:
                      BorderRadius.circular(tokens.length('--ck-radius-full')),
                ),
              ),
              SizedBox(width: tokens.length('--ck-space-3')),
            ],
            Flexible(
              child: Text(
                message.text,
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: KitsType.of(
                  context,
                  font: '--ck-font-control',
                  color: tokens.color('--ck-toast-text'),
                ),
              ),
            ),
            if (message.onAction != null) ...<Widget>[
              SizedBox(width: tokens.length('--ck-space-3')),
              GestureDetector(
                key: ValueKey<String>('kits-toast-action-${message.id}'),
                onTap: () {
                  message.onAction!.call();
                  widget.toaster.dismiss(message.id);
                },
                child: Text(
                  label,
                  style: KitsType.of(
                    context,
                    font: '--ck-font-control',
                    color: tokens.color('--ck-toast-action'),
                    weight: FontWeight.w600,
                  ),
                ),
              ),
            ],
            SizedBox(width: tokens.length('--ck-space-2')),
            GestureDetector(
              onTap: () => widget.toaster.dismiss(message.id),
              child: Semantics(
                button: true,
                label: 'Закрыть',
                child: Icon(
                  Icons.close,
                  size: 16,
                  color: tokens.color('--ck-toast-text'),
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}
