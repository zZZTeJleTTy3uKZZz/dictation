import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../foundation/kits_text.dart';
import '../tokens.dart';
import 'badge.dart';
import 'button.dart';

/// Состояние карточки обновления — ровно восемь состояний §6 протокола
/// `update/1`; булевых флагов вместо него нет.
enum KitsUpdateStatus {
  idle,
  checking,
  upToDate,
  available,
  downloading,
  ready,
  installing,
  failed,
}

/// Код ошибки обновления из §6; одинаков в Go и Dart.
enum KitsUpdateErrorCode {
  badManifest,
  badVersion,
  insecureUrl,
  noAsset,
  noChecksum,
  checksumMismatch,
  sizeMismatch,
  tooLarge,
  downloadFailed,
  installFailed,
  cancelled;

  /// Повторять можно только эти ошибки; остальные требуют правки выпуска.
  bool get retryable => switch (this) {
    KitsUpdateErrorCode.downloadFailed ||
    KitsUpdateErrorCode.checksumMismatch ||
    KitsUpdateErrorCode.sizeMismatch ||
    KitsUpdateErrorCode.installFailed => true,
    _ => false,
  };

  /// Код протокола для текста ошибки.
  String get code => switch (this) {
    KitsUpdateErrorCode.badManifest => 'bad_manifest',
    KitsUpdateErrorCode.badVersion => 'bad_version',
    KitsUpdateErrorCode.insecureUrl => 'insecure_url',
    KitsUpdateErrorCode.noAsset => 'no_asset',
    KitsUpdateErrorCode.noChecksum => 'no_checksum',
    KitsUpdateErrorCode.checksumMismatch => 'checksum_mismatch',
    KitsUpdateErrorCode.sizeMismatch => 'size_mismatch',
    KitsUpdateErrorCode.tooLarge => 'too_large',
    KitsUpdateErrorCode.downloadFailed => 'download_failed',
    KitsUpdateErrorCode.installFailed => 'install_failed',
    KitsUpdateErrorCode.cancelled => 'cancelled',
  };
}

/// Карточка обновления приложения.
///
/// Принимает простые данные состояния §6 и колбэки; пакет `kits_update` не
/// нужен — адаптер «UpdateState → данные карточки» живёт в приложении. Карточка
/// показывает ровно одно состояние и его действия. Обязательное обновление не
/// предлагает «Позже» и не закрывается; при `installing` действий нет.
///
/// При смене состояния фокус переходит на главное действие нового состояния:
/// после «Скачать» — на «Отменить», после отмены — на «Скачать», при ошибке —
/// на «Повторить». Размеры показываются человекочитаемо: «23 МБ из 46 МБ».
class KitsUpdateCard extends StatefulWidget {
  const KitsUpdateCard({
    super.key,
    required this.status,
    this.version,
    this.notes,
    this.received,
    this.total,
    this.mandatory = false,
    this.errorCode,
    this.errorMessage,
    this.onCheck,
    this.onDownload,
    this.onCancel,
    this.onInstall,
    this.onRetry,
    this.onLater,
    this.onClose,
  });

  final KitsUpdateStatus status;

  /// Версия из манифеста: «Доступна версия X», «Последняя версия X».
  final String? version;

  /// «Что нового»: текст без разметки, до скачивания и при ошибке.
  final String? notes;

  /// Сколько байт уже получено; прогресс считается из [received] и [total].
  final int? received;

  /// Полный размер файла; больше него прогресс не бывает.
  final int? total;

  /// Обязательное обновление: нет «Позже» и крестика ни в одном состоянии.
  final bool mandatory;

  final KitsUpdateErrorCode? errorCode;
  final String? errorMessage;

  final VoidCallback? onCheck;
  final VoidCallback? onDownload;
  final VoidCallback? onCancel;
  final VoidCallback? onInstall;
  final VoidCallback? onRetry;
  final VoidCallback? onLater;
  final VoidCallback? onClose;

  static const Key cardKey = Key('kits-update-card');
  static const Key progressKey = Key('kits-update-progress');
  static const Key closeKey = Key('kits-update-close');
  static const Key errorKey = Key('kits-update-error');
  static const Key laterKey = Key('kits-update-later');
  static const Key primaryKey = Key('kits-update-primary');

  /// Размер человеческим языком: КБ, МБ, ГБ; одна цифра после запятой, если
  /// она нужна. Байты и килобайты без дробной части, мегабайты и выше — с ней.
  static String formatSize(int bytes) {
    if (bytes <= 0) return '0 Б';
    const units = <String>['Б', 'КБ', 'МБ', 'ГБ', 'ТБ'];
    var value = bytes.toDouble();
    var unit = 0;
    while (value >= 1024 && unit < units.length - 1) {
      value /= 1024;
      unit++;
    }
    final digits = unit == 0 ? 0 : 1;
    var text = value.toStringAsFixed(digits);
    if (text.endsWith('.0')) text = text.substring(0, text.length - 2);
    text = text.replaceAll('.', ',');
    return '$text ${units[unit]}';
  }

  /// Целые проценты 0…100 из [received] и [total].
  int get percent => KitsUpdateCard.percentOf(received, total);

  /// Целые проценты 0…100 из [received] и [total].
  static int percentOf(int? received, int? total) {
    if (total == null || total <= 0) return 0;
    return (((received ?? 0) / total) * 100).floor().clamp(0, 100);
  }

  @override
  State<KitsUpdateCard> createState() => _KitsUpdateCardState();
}

class _KitsUpdateCardState extends State<KitsUpdateCard> {
  final FocusNode _primaryFocus = FocusNode(debugLabel: 'KitsUpdatePrimary');

  @override
  void didUpdateWidget(covariant KitsUpdateCard oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.status == widget.status) return;
    // Действие прежнего состояния исчезло: фокус не должен уйти на body.
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted) return;
      if (_primaryFocus.context == null) return;
      _primaryFocus.requestFocus();
    });
  }

  @override
  void dispose() {
    _primaryFocus.dispose();
    super.dispose();
  }

  int get percent => KitsUpdateCard.percentOf(widget.received, widget.total);

  bool get _closable =>
      !widget.mandatory &&
      (widget.status == KitsUpdateStatus.available ||
          widget.status == KitsUpdateStatus.failed ||
          widget.status == KitsUpdateStatus.upToDate);

  String get _title => switch (widget.status) {
    KitsUpdateStatus.idle => 'Обновление приложения',
    KitsUpdateStatus.checking => 'Проверяю…',
    KitsUpdateStatus.upToDate =>
      widget.version == null
          ? 'Последняя версия'
          : 'Последняя версия ${widget.version}',
    KitsUpdateStatus.available =>
      widget.version == null
          ? 'Доступна новая версия'
          : 'Доступна версия ${widget.version}',
    KitsUpdateStatus.downloading => 'Скачиваю $percent%',
    KitsUpdateStatus.ready => 'Готово к установке',
    KitsUpdateStatus.installing =>
      widget.version == null
          ? 'Устанавливаю…'
          : 'Устанавливаю ${widget.version}',
    KitsUpdateStatus.failed => 'Не удалось обновить',
  };

  String? get _sizeLabel =>
      widget.total == null ? null : KitsUpdateCard.formatSize(widget.total!);

  String get _receivedLabel {
    final received = KitsUpdateCard.formatSize(widget.received ?? 0);
    final total = widget.total;
    if (total == null) return '$received получено';
    return '$received из ${KitsUpdateCard.formatSize(total)}';
  }

  bool get _showNotes =>
      widget.notes != null &&
      widget.notes!.isNotEmpty &&
      (widget.status == KitsUpdateStatus.available ||
          widget.status == KitsUpdateStatus.downloading ||
          widget.status == KitsUpdateStatus.failed);

  @override
  Widget build(BuildContext context) {
    final tokens = KitsTokens.of(context);
    final body = <Widget>[
      if (_showNotes) ...<Widget>[
        SizedBox(height: tokens.length('--ck-space-3')),
        Text(
          widget.notes!,
          style: KitsType.of(
            context,
            font: '--ck-font-hint',
            leading: '--ck-leading-relaxed',
            color: tokens.color('--ck-text-secondary'),
          ),
        ),
      ],
      if (widget.status == KitsUpdateStatus.available &&
          _sizeLabel != null) ...<Widget>[
        SizedBox(height: tokens.length('--ck-space-2')),
        Text(
          'Размер файла: $_sizeLabel',
          style: KitsType.of(
            context,
            font: '--ck-font-micro',
            color: tokens.color('--ck-text-meta'),
          ),
        ),
      ],
      if (widget.status == KitsUpdateStatus.downloading) ...<Widget>[
        SizedBox(height: tokens.length('--ck-space-3')),
        LinearProgressIndicator(
          key: KitsUpdateCard.progressKey,
          value: percent / 100,
          minHeight: 6,
          color: tokens.color('--ck-accent-fill'),
          backgroundColor: tokens.color('--ck-bg-second'),
        ),
        SizedBox(height: tokens.length('--ck-space-1')),
        Text(
          _receivedLabel,
          style: KitsType.of(
            context,
            font: '--ck-font-micro',
            color: tokens.color('--ck-text-meta'),
          ),
        ),
      ],
      if (widget.status == KitsUpdateStatus.ready &&
          widget.version != null) ...<Widget>[
        SizedBox(height: tokens.length('--ck-space-2')),
        Text(
          'Версия ${widget.version}',
          style: KitsType.of(
            context,
            font: '--ck-font-hint',
            color: tokens.color('--ck-text-secondary'),
          ),
        ),
      ],
      if (widget.status == KitsUpdateStatus.failed) ...<Widget>[
        SizedBox(height: tokens.length('--ck-space-2')),
        Semantics(
          liveRegion: true,
          label: _errorText,
          child: Text(
            _errorText,
            key: KitsUpdateCard.errorKey,
            style: KitsType.of(
              context,
              font: '--ck-font-hint',
              leading: '--ck-leading-normal',
              color: tokens.color('--ck-danger-strong'),
            ),
          ),
        ),
      ],
    ];

    final actions = _actions(context, tokens);

    Widget card = Container(
      key: KitsUpdateCard.cardKey,
      width: double.infinity,
      padding: EdgeInsets.all(tokens.length('--ck-space-4')),
      decoration: BoxDecoration(
        color: tokens.color('--ck-bg'),
        border: Border.all(color: tokens.color('--ck-border')),
        borderRadius: BorderRadius.circular(tokens.length('--ck-radius-card')),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        mainAxisSize: MainAxisSize.min,
        children: <Widget>[
          Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: <Widget>[
              Expanded(
                child: Wrap(
                  spacing: tokens.length('--ck-space-2'),
                  runSpacing: tokens.length('--ck-space-1'),
                  crossAxisAlignment: WrapCrossAlignment.center,
                  children: <Widget>[
                    Text(
                      _title,
                      style: KitsType.of(
                        context,
                        font: '--ck-font-title',
                        leading: '--ck-leading-tight',
                        color: tokens.color('--ck-text'),
                        weight: FontWeight.w600,
                      ),
                    ),
                    if (widget.mandatory)
                      const KitsBadge(
                        text: 'Обязательное обновление',
                        tone: KitsBadgeTone.danger,
                        size: KitsBadgeSize.xs,
                      ),
                  ],
                ),
              ),
              if (_closable && widget.onClose != null)
                KitsButton(
                  key: KitsUpdateCard.closeKey,
                  variant: KitsButtonVariant.iconDefault,
                  size: KitsButtonSize.sm,
                  semanticLabel: 'Закрыть',
                  iconContent: const Icon(Icons.close, size: 18),
                  onClick: widget.onClose,
                ),
            ],
          ),
          ...body,
          if (actions.isNotEmpty) ...<Widget>[
            SizedBox(height: tokens.length('--ck-space-4')),
            Align(
              alignment: Alignment.centerRight,
              child: Wrap(
                spacing: tokens.length('--ck-space-2'),
                runSpacing: tokens.length('--ck-space-2'),
                alignment: WrapAlignment.end,
                children: actions,
              ),
            ),
          ],
        ],
      ),
    );

    if (!widget.mandatory &&
        widget.status != KitsUpdateStatus.installing &&
        widget.onClose != null) {
      card = CallbackShortcuts(
        bindings: <ShortcutActivator, VoidCallback>{
          const SingleActivator(LogicalKeyboardKey.escape): widget.onClose!,
        },
        child: card,
      );
    }
    return card;
  }

  String get _errorText {
    // Код протокола нужен журналу и решению «Повторить», но человеку его
    // показывать нельзя: одна спокойная строка вместо Method Not Allowed.
    final message = widget.errorMessage;
    if (message != null && message.isNotEmpty) return message;
    return 'Не удалось проверить обновления — нет связи с GitHub';
  }

  KitsButton _primary({
    required String label,
    required VoidCallback? onClick,
    bool loading = false,
    bool disabled = false,
  }) {
    return KitsButton(
      key: KitsUpdateCard.primaryKey,
      label: label,
      variant: KitsButtonVariant.primary,
      loading: loading,
      disabled: disabled,
      onClick: onClick,
      focusNode: disabled || loading ? null : _primaryFocus,
    );
  }

  List<Widget> _actions(BuildContext context, KitsTokens tokens) {
    final later = !widget.mandatory && widget.onLater != null
        ? KitsButton(
            key: KitsUpdateCard.laterKey,
            label: 'Позже',
            onClick: widget.onLater,
          )
        : null;

    switch (widget.status) {
      case KitsUpdateStatus.idle:
        return <Widget>[
          _primary(label: 'Проверить обновление', onClick: widget.onCheck),
        ];
      case KitsUpdateStatus.checking:
        return <Widget>[
          _primary(
            label: 'Проверяю…',
            onClick: null,
            loading: true,
            disabled: true,
          ),
        ];
      case KitsUpdateStatus.upToDate:
        return <Widget>[
          if (later != null) later,
          _primary(label: 'Проверить обновление', onClick: widget.onCheck),
        ];
      case KitsUpdateStatus.available:
        return <Widget>[
          if (later != null) later,
          _primary(label: 'Скачать', onClick: widget.onDownload),
        ];
      case KitsUpdateStatus.downloading:
        return <Widget>[_primary(label: 'Отменить', onClick: widget.onCancel)];
      case KitsUpdateStatus.ready:
        return <Widget>[
          if (later != null) later,
          _primary(label: 'Установить', onClick: widget.onInstall),
        ];
      case KitsUpdateStatus.installing:
        return <Widget>[
          _primary(
            label: 'Устанавливаю…',
            onClick: null,
            loading: true,
            disabled: true,
          ),
        ];
      case KitsUpdateStatus.failed:
        final retryable = widget.errorCode?.retryable ?? false;
        return <Widget>[
          if (later != null) later,
          if (retryable) _primary(label: 'Повторить', onClick: widget.onRetry),
        ];
    }
  }
}
