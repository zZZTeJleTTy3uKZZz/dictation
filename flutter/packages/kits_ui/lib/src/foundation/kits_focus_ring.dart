import 'package:flutter/gestures.dart';
import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';

import '../tokens.dart';

/// Видимость кольца фокуса: только при навигации с клавиатуры, как
/// `:focus-visible` в вебе. Один общий источник на все компоненты кита.
///
/// ПОЧЕМУ НЕ ХВАТАЕТ highlightMode. На Windows Flutter держит
/// `FocusManager.highlightMode` в traditional и после мыши, а клик ставит
/// фокус (кнопка, строка, переключатель). Поэтому кольцо оставалось на
/// элементе даже после того, как строку свернули. Здесь учитывается последний
/// ввод: нажатие мышью или касание гасит кольцо, клавиша снова его включает.
/// Режим traditional проверяется дополнительно, как и раньше.
class KitsFocusVisibility extends ChangeNotifier {
  KitsFocusVisibility._();

  static final KitsFocusVisibility instance = KitsFocusVisibility._();

  bool _attached = false;

  /// Последним был ввод мышью или касанием: кольцо не показываем.
  bool _pointerLast = false;

  /// Подключает слушателей ввода один раз. Вызывается из построения кольца,
  /// когда оно понадобилось: до первого фокуса подписываться незачем.
  void attach() {
    if (_attached) return;
    _attached = true;
    GestureBinding.instance.pointerRouter.addGlobalRoute(_onPointer);
    HardwareKeyboard.instance.addHandler(_onKey);
  }

  /// Нужно ли рисовать кольцо для элемента с фокусом [focused].
  bool shows(bool focused) =>
      focused &&
      !_pointerLast &&
      FocusManager.instance.highlightMode == FocusHighlightMode.traditional;

  void _onPointer(PointerEvent event) {
    // Нажатие мышью или касание ставит фокус без клавиатуры: кольцо гасим.
    if (event is! PointerDownEvent || _pointerLast) return;
    _pointerLast = true;
    notifyListeners();
  }

  bool _onKey(KeyEvent event) {
    // Клавиша возвращает навигацию с клавиатуры. Сама клавиша ничего не
    // перехватывает: решает обработчик фокуса, а здесь только помечаем ввод.
    if (event is KeyDownEvent && _pointerLast) {
      _pointerLast = false;
      notifyListeners();
    }
    return false;
  }
}

/// Кольцо фокуса кита: одно на все компоненты.
///
/// В CSS кольцо — `outline` с отступом `--ck-focus-offset`; здесь оно лежит
/// поверх ребёнка в [Stack], а не в отступе, поэтому не двигает раскладку.
/// Внутрь кольцо рисуется у строк списка и ленты вкладок: снаружи его срезал
/// бы край прокручиваемой колонки.
///
/// Кольцо показывается только при навигации с клавиатуры (см.
/// [KitsFocusVisibility]): клик мышью фокус ставит, но кольцо не рисует.
class KitsFocusRing extends StatelessWidget {
  const KitsFocusRing({
    super.key,
    required this.focused,
    required this.child,
    this.borderRadius,
    this.inset = false,
  });

  /// Есть ли фокус у элемента (до учёта способа ввода).
  final bool focused;

  final Widget child;

  /// Скругление кольца; по умолчанию — радиус контрола.
  final BorderRadius? borderRadius;

  /// Рисовать внутрь (`--ck-focus-inset`) или наружу (`--ck-focus-offset`).
  final bool inset;

  /// Ключ самого кольца — по нему проверки находят цвет и толщину.
  static const Key ringKey = Key('kits-focus-ring');

  @override
  Widget build(BuildContext context) {
    final visibility = KitsFocusVisibility.instance..attach();
    final tokens = KitsTokens.of(context);
    final margin =
        -tokens.length(inset ? '--ck-focus-inset' : '--ck-focus-offset');
    final radius = borderRadius ??
        BorderRadius.circular(tokens.length('--ck-radius-control'));
    // Стек всегда один и тот же: смена видимости кольца не должна переставлять
    // ребёнка в дереве. Иначе его State (с FocusableActionDetector) пересоздаётся,
    // подписка на фокус рвётся, и кольцо у потерявшего фокус элемента остаётся.
    return ListenableBuilder(
      listenable: visibility,
      builder: (context, _) => Stack(
        clipBehavior: Clip.none,
        children: <Widget>[
          child,
          if (visibility.shows(focused))
            Positioned(
              left: margin,
              top: margin,
              right: margin,
              bottom: margin,
              child: IgnorePointer(
                child: DecoratedBox(
                  key: ringKey,
                  decoration: BoxDecoration(
                    border: Border.all(
                      color: tokens.color('--ck-focus'),
                      width: tokens.focusWidth,
                    ),
                    borderRadius: radius,
                  ),
                ),
              ),
            ),
        ],
      ),
    );
  }
}
