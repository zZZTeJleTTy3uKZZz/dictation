// Заметки: всё, что было наговорено, с поиском и устройством.
//
// ЗАЧЕМ УСТРОЙСТВО НА ВИДУ. Человек говорит с ноутбука, со стола, из машины — и
// «откуда это» половина ответа на «что это было». Поэтому оно не в подробностях,
// а прямо в строке.
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'core.dart';

class NotesPage extends StatefulWidget {
  const NotesPage({super.key, required this.core});
  final Core core;

  @override
  State<NotesPage> createState() => _NotesPageState();
}

class _NotesPageState extends State<NotesPage> {
  final search = TextEditingController();
  List<Note> notes = const [];
  String? trouble;
  bool first = true;
  Timer? later;
  Timer? again;

  @override
  void initState() {
    super.initState();
    _load();
    // Расшифровка приезжает через несколько секунд после заметки: обновляемся
    // сами, чтобы человек не искал кнопку «обновить».
    again = Timer.periodic(
      const Duration(seconds: 5),
      (_) => _load(quiet: true),
    );
  }

  @override
  void dispose() {
    later?.cancel();
    again?.cancel();
    search.dispose();
    super.dispose();
  }

  Future<void> _load({bool quiet = false}) async {
    try {
      final found = await widget.core.notes(search.text);
      if (!mounted) return;
      setState(() {
        notes = found;
        trouble = null;
        first = false;
      });
    } on CoreError catch (error) {
      if (!mounted || quiet) return;
      setState(() {
        trouble = error.message;
        first = false;
      });
    }
  }

  void _searchLater() {
    // Не дёргаем историю на каждую букву: она читается с диска.
    later?.cancel();
    later = Timer(const Duration(milliseconds: 220), _load);
  }

  @override
  Widget build(BuildContext context) {
    final colors = Theme.of(context).colorScheme;
    return Column(
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(20, 16, 20, 8),
          child: TextField(
            controller: search,
            onChanged: (_) => _searchLater(),
            decoration: InputDecoration(
              hintText: 'Искать по словам — «созвон», «идея», «купить»',
              prefixIcon: const Icon(Icons.search, size: 20),
              isDense: true,
              filled: true,
              fillColor: colors.surfaceContainerHighest,
              border: OutlineInputBorder(
                borderRadius: BorderRadius.circular(10),
                borderSide: BorderSide.none,
              ),
            ),
          ),
        ),
        Expanded(child: _body(context)),
      ],
    );
  }

  Widget _body(BuildContext context) {
    if (first) return const Center(child: CircularProgressIndicator());
    if (trouble != null) {
      return _Empty(
        icon: Icons.error_outline,
        title: 'История не прочиталась',
        note: trouble!,
        action: FilledButton.tonal(
          onPressed: _load,
          child: const Text('Ещё раз'),
        ),
      );
    }
    if (notes.isEmpty) {
      return _Empty(
        icon: search.text.isEmpty ? Icons.mic_none_rounded : Icons.search_off,
        title: search.text.isEmpty ? 'Заметок пока нет' : 'Ничего не нашлось',
        note: search.text.isEmpty
            ? 'Нажмите сочетание клавиш и наговорите мысль — она появится здесь'
            : 'Попробуйте другое слово',
      );
    }
    return ListView.builder(
      padding: const EdgeInsets.fromLTRB(20, 4, 20, 20),
      itemCount: notes.length,
      itemBuilder: (context, index) => _NoteCard(
        note: notes[index],
        play: () => _do(() => widget.core.playNote(notes[index].id)),
        forget: () => _forget(notes[index]),
      ),
    );
  }

  Future<void> _do(Future<void> Function() what) async {
    try {
      await what();
    } on CoreError catch (error) {
      if (mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text(error.message)));
      }
    }
  }

  Future<void> _forget(Note note) async {
    // Спрашиваем: удаление уносит и звук, а восстановить его неоткуда.
    final sure = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Удалить заметку?'),
        content: const Text(
          'Вместе с текстом пропадёт и запись голоса — вернуть будет нечем.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('Оставить'),
          ),
          FilledButton(
            style: FilledButton.styleFrom(
              backgroundColor: Theme.of(context).colorScheme.error,
            ),
            onPressed: () => Navigator.pop(context, true),
            child: const Text('Удалить'),
          ),
        ],
      ),
    );
    if (sure != true) return;
    await _do(() => widget.core.forgetNote(note.id));
    await _load();
  }
}

class _NoteCard extends StatelessWidget {
  const _NoteCard({
    required this.note,
    required this.play,
    required this.forget,
  });
  final Note note;
  final VoidCallback play;
  final VoidCallback forget;

  @override
  Widget build(BuildContext context) {
    final colors = Theme.of(context).colorScheme;
    return Card(
      margin: const EdgeInsets.only(bottom: 10),
      elevation: 0,
      color: colors.surfaceContainerLow,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(12),
        side: BorderSide(color: colors.outlineVariant.withValues(alpha: .4)),
      ),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 12, 12, 10),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Text(
                  note.at,
                  style: TextStyle(
                    fontWeight: FontWeight.w600,
                    fontSize: 13,
                    color: colors.onSurface,
                  ),
                ),
                const SizedBox(width: 8),
                Text(
                  note.when,
                  style: TextStyle(
                    fontSize: 12.5,
                    color: colors.onSurfaceVariant,
                  ),
                ),
                const SizedBox(width: 8),
                Text(
                  '· ${note.length}',
                  style: TextStyle(
                    fontSize: 12.5,
                    color: colors.onSurfaceVariant,
                  ),
                ),
                if (note.speech.isNotEmpty) ...[
                  const SizedBox(width: 8),
                  Text(
                    '· ${note.speech}',
                    style: TextStyle(
                      fontSize: 12.5,
                      color: colors.onSurfaceVariant,
                    ),
                  ),
                ],
                const Spacer(),
                _Chip(text: note.device),
              ],
            ),
            const SizedBox(height: 8),
            if (note.text.isNotEmpty)
              SelectableText(
                note.text,
                style: const TextStyle(fontSize: 14, height: 1.4),
              )
            else if (note.trouble.isNotEmpty)
              Text(
                'без расшифровки: ${note.trouble}',
                style: TextStyle(fontSize: 13.5, color: colors.error),
              )
            else
              Row(
                children: [
                  SizedBox(
                    width: 13,
                    height: 13,
                    child: CircularProgressIndicator(
                      strokeWidth: 2,
                      color: colors.onSurfaceVariant,
                    ),
                  ),
                  const SizedBox(width: 8),
                  Text(
                    'расшифровывается…',
                    style: TextStyle(
                      fontSize: 13.5,
                      color: colors.onSurfaceVariant,
                    ),
                  ),
                ],
              ),
            const SizedBox(height: 6),
            Row(
              children: [
                if (note.hasAudio)
                  TextButton.icon(
                    onPressed: play,
                    icon: const Icon(Icons.play_arrow_rounded, size: 18),
                    label: const Text('послушать'),
                  ),
                if (note.text.isNotEmpty)
                  TextButton.icon(
                    onPressed: () async {
                      await Clipboard.setData(ClipboardData(text: note.text));
                      if (context.mounted) {
                        ScaffoldMessenger.of(context).showSnackBar(
                          const SnackBar(
                            content: Text('Скопировано'),
                            duration: Duration(seconds: 1),
                          ),
                        );
                      }
                    },
                    icon: const Icon(Icons.copy_rounded, size: 16),
                    label: const Text('скопировать'),
                  ),
                const Spacer(),
                IconButton(
                  onPressed: forget,
                  icon: const Icon(Icons.delete_outline, size: 19),
                  tooltip: 'Удалить',
                  color: colors.onSurfaceVariant,
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}

class _Chip extends StatelessWidget {
  const _Chip({required this.text});
  final String text;

  @override
  Widget build(BuildContext context) {
    final colors = Theme.of(context).colorScheme;
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 9, vertical: 2),
      decoration: BoxDecoration(
        borderRadius: BorderRadius.circular(999),
        border: Border.all(color: colors.outlineVariant),
      ),
      child: Text(
        text,
        style: TextStyle(fontSize: 11.5, color: colors.onSurfaceVariant),
      ),
    );
  }
}

class _Empty extends StatelessWidget {
  const _Empty({
    required this.icon,
    required this.title,
    required this.note,
    this.action,
  });
  final IconData icon;
  final String title;
  final String note;
  final Widget? action;

  @override
  Widget build(BuildContext context) {
    final colors = Theme.of(context).colorScheme;
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(32),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(
              icon,
              size: 44,
              color: colors.onSurfaceVariant.withValues(alpha: .6),
            ),
            const SizedBox(height: 14),
            Text(
              title,
              style: TextStyle(
                fontSize: 16,
                fontWeight: FontWeight.w600,
                color: colors.onSurface,
              ),
            ),
            const SizedBox(height: 6),
            Text(
              note,
              textAlign: TextAlign.center,
              style: TextStyle(color: colors.onSurfaceVariant),
            ),
            if (action != null) ...[const SizedBox(height: 16), action!],
          ],
        ),
      ),
    );
  }
}
