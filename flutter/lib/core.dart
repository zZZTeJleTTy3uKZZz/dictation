// Разговор с ядром на Go.
//
// ПОЧЕМУ ТАК. Правила настроек — какие бывают режимы, что значит «всегда» для
// браузера, какую папку нельзя добавить дважды — живут в Go. Повтори их здесь, в
// Dart, и они разойдутся: в одном месте поправят, в другом забудут, и человек
// увидит в двух окнах разное.
//
// Поэтому окно не решает ничего. Оно спрашивает состояние («show») и присылает
// просьбы («set <действие> <значение>»). Канал — запуск процесса и его вывод: ни
// портов, ни служб, ни токенов, нечему сломаться и нечего настраивать.
import 'dart:convert';
import 'dart:io';

/// Ошибка ядра: человеку показываем её словами, а не «что-то пошло не так».
class CoreError implements Exception {
  CoreError(this.message);
  final String message;
  @override
  String toString() => message;
}

class Core {
  Core(this.executable, {this.environment});

  /// Путь к meetdigest-core.exe.
  final String executable;

  /// Окружение для ядра. В работе пустое (ядро берёт настройки из дома
  /// человека), в сквозных проверках сюда подставляют временный дом — иначе
  /// проверка правила бы живые настройки.
  final Map<String, String>? environment;

  /// Рядом с окном — так собирается и так ставится; искать в PATH было бы
  /// гаданием, а ошибку человек увидел бы в виде пустого окна.
  static Core beside() {
    final folder = File(Platform.resolvedExecutable).parent.path;
    return Core('$folder${Platform.pathSeparator}meetdigest-core.exe');
  }

  /// Состояние настроек целиком.
  Future<Map<String, dynamic>> show() => _ask(['show']);

  /// Применить просьбу и получить НОВОЕ состояние.
  ///
  /// Состояние приходит в том же ответе: окну не надо ни угадывать результат, ни
  /// спрашивать второй раз — показать он может только то, что записалось.
  Future<Map<String, dynamic>> set(String action, [String value = '']) =>
      _ask(['set', action, value]);

  /// Спросить папку системным диалогом.
  ///
  /// Диалог поднимает Go, а не Flutter: в Dart для этого понадобился бы ещё один
  /// пакет со своим кодом под Windows, а системный диалог у нас уже есть.
  Future<String> folder(String title) async {
    final done = await Process.run(
      executable,
      ['folder', title],
      stdoutEncoding: utf8,
      stderrEncoding: utf8,
      environment: environment,
    );
    if (done.exitCode != 0) {
      throw CoreError(_reason(done));
    }
    return (done.stdout as String).trim();
  }

  /// ask — позвать ядро одной командой. Внутренняя, но видна расширениям в
  /// этом же файле: заметки — та же связь, что настройки, и второй канал ради
  /// них заводить незачем.
  Future<Map<String, dynamic>> _ask(List<String> args) async {
    final ProcessResult done;
    try {
      done = await Process.run(
        executable,
        args,
        stdoutEncoding: utf8,
        stderrEncoding: utf8,
        environment: environment,
      );
    } on ProcessException catch (error) {
      // Ядра нет рядом — это сборка, а не настройки: так и говорим.
      throw CoreError('не найдено ядро: ${error.executable}');
    }
    if (done.exitCode != 0) {
      throw CoreError(_reason(done));
    }
    return jsonDecode(done.stdout as String) as Map<String, dynamic>;
  }

  String _reason(ProcessResult done) {
    final said = (done.stderr as String).trim();
    return said.isEmpty ? 'ядро ответило кодом ${done.exitCode}' : said;
  }
}

/// Пункт выбора, как его отдаёт ядро.
class Choice {
  Choice(this.key, this.title, this.note);
  factory Choice.from(Map<String, dynamic> raw) => Choice(
    raw['key'] as String,
    raw['title'] as String,
    (raw['note'] as String?) ?? '',
  );

  final String key;
  final String title;
  final String note;

  static List<Choice> all(dynamic raw) => ((raw as List?) ?? const [])
      .map((one) => Choice.from(one as Map<String, dynamic>))
      .toList();

  /// Пояснение к выбранному пункту — его показываем под названием настройки.
  static String noteOf(List<Choice> among, String key) =>
      among.where((one) => one.key == key).map((one) => one.note).firstOrNull ??
      '';
}

extension _First<T> on Iterable<T> {
  T? get firstOrNull => isEmpty ? null : first;
}

/// Папка под наблюдением.
class Folder {
  Folder(this.title, this.path, this.provider);
  factory Folder.from(Map<String, dynamic> raw) => Folder(
    raw['title'] as String,
    raw['path'] as String,
    raw['provider'] as bool,
  );

  final String title;
  final String path;

  /// Папку дал провайдер (Телемост), а не человек. Убрать её можно только
  /// выключив провайдера — иначе она вернётся сама, и кнопка «убрать» врала бы.
  final bool provider;

  static List<Folder> all(dynamic raw) => ((raw as List?) ?? const [])
      .map((one) => Folder.from(one as Map<String, dynamic>))
      .toList();
}

/// Решение, принятое по программе.
class App {
  App(this.program, this.title, this.decision);
  factory App.from(Map<String, dynamic> raw) => App(
    raw['program'] as String,
    raw['title'] as String,
    raw['decision'] as String,
  );

  final String program;
  final String title;
  final String decision;

  static List<App> all(dynamic raw) => ((raw as List?) ?? const [])
      .map((one) => App.from(one as Map<String, dynamic>))
      .toList();
}

/// Заметка, как её отдаёт ядро.
class Note {
  Note({
    required this.id,
    required this.at,
    required this.when,
    required this.length,
    required this.device,
    required this.text,
    required this.speech,
    required this.trouble,
    required this.hasAudio,
  });

  factory Note.from(Map<String, dynamic> raw) => Note(
    id: (raw['id'] as String?) ?? '',
    at: (raw['at'] as String?) ?? '',
    when: (raw['when'] as String?) ?? '',
    length: (raw['length'] as String?) ?? '',
    device: (raw['device'] as String?) ?? '',
    text: (raw['text'] as String?) ?? '',
    speech: (raw['speech'] as String?) ?? '',
    trouble: (raw['trouble'] as String?) ?? '',
    hasAudio: (raw['has_audio'] as bool?) ?? false,
  );

  final String id;
  final String at;
  final String when;
  final String length;
  final String device;
  final String text;
  final String speech;
  final String trouble;
  final bool hasAudio;
}

extension Notes on Core {
  /// История заметок; пустой поиск — вся.
  Future<List<Note>> notes([String search = '']) async {
    final answer = await _ask(['notes', search]);
    return ((answer['items'] as List?) ?? const [])
        .map((one) => Note.from(one as Map<String, dynamic>))
        .toList();
  }

  /// Послушать заметку тем, чем человек слушает музыку.
  Future<void> playNote(String id) => _ask(['note-play', id]);

  /// Убрать заметку вместе со звуком.
  Future<void> forgetNote(String id) => _ask(['note-forget', id]);
}
