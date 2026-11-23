import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';

/// Стек слоёв кита: кто из открытых слоёв верхний.
///
/// Один Escape обязан закрывать только верхний слой — так работают окно над
/// панелью выбора и меню «Ещё» над лентой. Слои договариваются здесь, а не
/// ищут друг друга в дереве виджетов: порталы и маршруты живут в разных
/// местах, а порядок открытия — общий.
abstract final class KitsLayers {
  static final List<_Layer> _stack = <_Layer>[];
  static bool _listening = false;

  /// Отмечает слой открытым и запоминает, как его закрыть.
  static void push(Object owner, VoidCallback close) {
    _stack.removeWhere((layer) => identical(layer.owner, owner));
    _stack.add(_Layer(owner, close));
    _listen(true);
  }

  /// Снимает слой: он закрылся сам.
  static void remove(Object owner) {
    _stack.removeWhere((layer) => identical(layer.owner, owner));
    if (_stack.isEmpty) _listen(false);
  }

  /// Верхний ли это слой.
  static bool isTop(Object owner) =>
      _stack.isNotEmpty && identical(_stack.last.owner, owner);

  /// Сколько слоёв открыто — для проверок.
  static int get depth => _stack.length;

  /// Забыть все слои: между независимыми проверками.
  @visibleForTesting
  static void reset() {
    _stack.clear();
    _listen(false);
  }

  static void _listen(bool enabled) {
    if (enabled == _listening) return;
    _listening = enabled;
    if (enabled) {
      HardwareKeyboard.instance.addHandler(_onKey);
    } else {
      HardwareKeyboard.instance.removeHandler(_onKey);
    }
  }

  static bool _onKey(KeyEvent event) {
    if (event is! KeyDownEvent ||
        event.logicalKey != LogicalKeyboardKey.escape ||
        _stack.isEmpty) {
      return false;
    }
    final top = _stack.removeLast();
    top.close();
    if (_stack.isEmpty) _listen(false);
    return true;
  }
}

class _Layer {
  _Layer(this.owner, this.close);

  final Object owner;
  final VoidCallback close;
}
