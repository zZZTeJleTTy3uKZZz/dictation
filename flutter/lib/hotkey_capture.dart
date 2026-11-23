import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'dictation_ui.dart';

// Physical keys keep shortcut names stable when Windows switches layouts.
String? shortcutKeyName(PhysicalKeyboardKey key) {
  final usage = key.usbHidUsage & 0xffff;
  if (usage >= 0x04 && usage <= 0x1d) {
    return String.fromCharCode(0x41 + usage - 0x04);
  }
  if (usage >= 0x1e && usage <= 0x26) return '${usage - 0x1d}';
  if (usage == 0x27) return '0';
  if (usage >= 0x3a && usage <= 0x45) return 'F${usage - 0x39}';
  if (usage >= 0x68 && usage <= 0x73) return 'F${usage - 0x68 + 13}';
  if (usage >= 0x59 && usage <= 0x61) return 'Numpad${usage - 0x58}';
  return const <int, String>{
    0x28: 'Enter',
    0x29: 'Escape',
    0x2a: 'Backspace',
    0x2b: 'Tab',
    0x2c: 'Space',
    0x2d: 'Minus',
    0x2e: 'Plus',
    0x2f: 'Bracketleft',
    0x30: 'Bracketright',
    0x31: 'Backslash',
    0x33: 'Semicolon',
    0x34: 'Quote',
    0x35: 'Backquote',
    0x36: 'Comma',
    0x37: 'Period',
    0x38: 'Slash',
    0x39: 'Capslock',
    0x46: 'Printscreen',
    0x47: 'Scrolllock',
    0x48: 'Pause',
    0x49: 'Insert',
    0x4a: 'Home',
    0x4b: 'Pageup',
    0x4c: 'Delete',
    0x4d: 'End',
    0x4e: 'Pagedown',
    0x4f: 'Right',
    0x50: 'Left',
    0x51: 'Down',
    0x52: 'Up',
    0x54: 'Numpaddivide',
    0x55: 'Numpadmultiply',
    0x56: 'Numpadsubtract',
    0x57: 'Numpadadd',
    0x58: 'Enter',
    0x62: 'Numpad0',
    0x63: 'Numpaddecimal',
    0x64: 'Intlbackslash',
  }[usage];
}

/// HotkeyStatus — свободно ли сочетание и кто его держит.
///
/// Ответ службы на /hotkey-check. Для занятого системным переключением ввода
/// (IME) Диктовка может освободить клавишу с согласия человека; для занятого
/// чужой программой — только предложить другое.
class HotkeyStatus {
  const HotkeyStatus({
    required this.state,
    this.owner = '',
    this.note = '',
    this.canRelease = false,
    this.active = false,
  });

  final String state;
  final String owner;
  final String note;
  final bool canRelease;
  final bool active;

  bool get free => state == 'free';
  bool get byIme => state == 'ime';
  bool get pending => state == 'ime-released';
  bool get byOther => state == 'other';
  bool get acceptable => free || byIme;

  static HotkeyStatus of(Map<String, dynamic>? data) => HotkeyStatus(
    state: data?['state'] as String? ?? 'free',
    owner: data?['owner'] as String? ?? '',
    note: data?['note'] as String? ?? '',
    canRelease: data?['can_release'] == true,
    active: data?['active'] == true,
  );

  String label(String combination) {
    if (byIme) {
      return '$combination занято системным переключением ввода '
          '(китайский/японский IME).';
    }
    if (pending) {
      return 'Освобождено. Заработает после выхода и входа в Windows.';
    }
    if (byOther) {
      return 'Сочетание занято: другая программа — выберите другое.';
    }
    return 'Свободно.';
  }
}

class HotkeyCaptureDialog extends StatefulWidget {
  const HotkeyCaptureDialog({super.key, this.check});

  /// Спросить у службы, свободно ли сочетание. nil — спросить нечем.
  final Future<HotkeyStatus?> Function(String combination)? check;
  @override
  State<HotkeyCaptureDialog> createState() => _HotkeyCaptureDialogState();
}

class _HotkeyCaptureDialogState extends State<HotkeyCaptureDialog>
    with WidgetsBindingObserver {
  final focus = FocusNode();
  String? combination;
  String preview = 'Нажмите сочетание';
  String? hint;
  HotkeyStatus? status;
  bool checking = false;
  int checkSeq = 0;
  bool closing = false;
  void close([String? result]) {
    if (closing || !mounted) return;
    closing = true;
    Navigator.pop(context, result);
  }

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state != AppLifecycleState.resumed) close();
  }

  bool modifier(PhysicalKeyboardKey key) {
    final usage = key.usbHidUsage & 0xffff;
    return usage >= 0xe0 && usage <= 0xe7;
  }

  KeyEventResult capture(FocusNode node, KeyEvent event) {
    if (event is! KeyDownEvent) return KeyEventResult.handled;
    final keyboard = HardwareKeyboard.instance;
    final modifiers = <String>[
      if (keyboard.isControlPressed) 'Ctrl',
      if (keyboard.isAltPressed) 'Alt',
      if (keyboard.isShiftPressed) 'Shift',
      if (keyboard.isMetaPressed) 'Win',
    ];
    if (event.physicalKey == PhysicalKeyboardKey.escape && modifiers.isEmpty) {
      close();
      return KeyEventResult.handled;
    }
    if (modifiers.isEmpty && event.physicalKey == PhysicalKeyboardKey.tab) {
      return KeyEventResult.ignored;
    }
    if (modifiers.isEmpty &&
        event.physicalKey == PhysicalKeyboardKey.enter &&
        combination != null) {
      close(combination);
      return KeyEventResult.handled;
    }
    if (modifier(event.physicalKey)) {
      setState(() {
        combination = null;
        preview = '${modifiers.join('+')}+…';
        hint = null;
      });
      return KeyEventResult.handled;
    }
    final key = shortcutKeyName(event.physicalKey);
    setState(() {
      combination = null;
      preview = 'Нажмите сочетание';
      if (modifiers.isEmpty) {
        hint = 'Добавьте Ctrl, Alt, Shift или Win.';
      } else if (key == null) {
        hint = 'Выберите букву, цифру, F-клавишу или другую обычную клавишу.';
      } else if (keyboard.physicalKeysPressed
              .where((key) => !modifier(key))
              .length >
          1) {
        hint = 'Используйте одну обычную клавишу вместе с модификаторами.';
      } else {
        combination = [...modifiers, key].join('+');
        preview = combination!;
        hint = null;
      }
    });
    if (combination != null) _check(combination!);
    return KeyEventResult.handled;
  }

  /// Спросить у службы, свободно ли сочетание. Ответ приходит не сразу, и
  /// человек за это время может набрать другое — номер проверки отсекает
  /// устаревший ответ.
  Future<void> _check(String combination) async {
    final ask = widget.check;
    if (ask == null) return;
    final seq = ++checkSeq;
    setState(() => checking = true);
    HotkeyStatus? answer;
    try {
      answer = await ask(combination);
    } catch (_) {
      answer = null;
    }
    if (!mounted || seq != checkSeq) return;
    setState(() {
      status = answer;
      checking = false;
    });
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    focus.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final tokens = LineTokens.of(context);
    return Focus(
      focusNode: focus,
      autofocus: true,
      onKeyEvent: capture,
      child: AlertDialog(
        title: const Text('Новое сочетание'),
        content: SizedBox(
          width: 340,
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: <Widget>[
              Text(
                'Нажмите нужные клавиши одновременно.',
                style: lineText(size: 14, height: 1.45, color: tokens.muted),
              ),
              const SizedBox(height: 20),
              Container(
                width: double.infinity,
                padding: const EdgeInsets.symmetric(
                  horizontal: 18,
                  vertical: 22,
                ),
                decoration: BoxDecoration(
                  color: tokens.ground,
                  borderRadius: BorderRadius.circular(LineTokens.fieldRadius),
                  border: Border.all(
                    color: combination == null
                        ? Colors.transparent
                        : tokens.accent,
                    width: 2,
                  ),
                ),
                child: Text(
                  preview.replaceAll('+', ' + '),
                  textAlign: TextAlign.center,
                  style: lineText(
                    size: combination == null ? 15 : 21,
                    weight: FontWeight.w600,
                    color: combination == null ? tokens.muted : tokens.ink,
                    tabular: true,
                  ),
                ),
              ),
              const SizedBox(height: 14),
              Text(
                hint ?? 'Ctrl, Alt, Shift или Win + клавиша. Esc — отмена.',
                style: lineText(
                  size: 13,
                  height: 1.4,
                  color: hint == null ? tokens.muted : tokens.danger,
                ),
              ),
              if (checking) ...<Widget>[
                const SizedBox(height: 6),
                Text(
                  'Проверяю сочетание…',
                  style: lineText(size: 13, height: 1.4, color: tokens.muted),
                ),
              ] else if (status != null && combination != null) ...<Widget>[
                const SizedBox(height: 6),
                Text(
                  status!.label(combination!),
                  style: lineText(
                    size: 13,
                    height: 1.4,
                    color: status!.acceptable ? tokens.muted : tokens.danger,
                  ),
                ),
              ],
            ],
          ),
        ),
        actions: <Widget>[
          TextButton(
            style: TextButton.styleFrom(
              foregroundColor: tokens.muted,
              minimumSize: const Size(0, 36),
            ),
            onPressed: close,
            child: const Text('Отмена'),
          ),
          FilledButton(
            style: FilledButton.styleFrom(minimumSize: const Size(0, 36)),
            onPressed:
                combination == null ||
                    checking ||
                    (status != null && !status!.acceptable)
                ? null
                : () => close(combination),
            child: const Text('Сохранить'),
          ),
        ],
      ),
    );
  }
}
