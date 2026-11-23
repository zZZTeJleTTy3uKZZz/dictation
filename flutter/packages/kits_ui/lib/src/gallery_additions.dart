import 'package:flutter/material.dart';
import 'package:kits_ui/kits_ui.dart';

/// Витрина новых компонентов: все состояния в одном экране.
///
/// Существующую витрину не трогаем: её снимки — контракт прежних компонентов.
/// Эта страница нужна golden-проверкам новых компонентов и ручному просмотру.
class KitsAdditionsGallery extends StatefulWidget {
  const KitsAdditionsGallery({super.key});

  @override
  State<KitsAdditionsGallery> createState() => _KitsAdditionsGalleryState();
}

class _KitsAdditionsGalleryState extends State<KitsAdditionsGallery> {
  final KitsToaster _toaster = KitsToaster();
  String _segment = 'mine';
  String _toolbarSegment = 'all';
  String _search = '';
  final TextEditingController _filledSearch = TextEditingController(
    text: 'Анна',
  );

  @override
  void dispose() {
    _toaster.dispose();
    _filledSearch.dispose();
    super.dispose();
  }

  static const List<KitsSegment> _segments = <KitsSegment>[
    KitsSegment(id: 'mine', label: 'Мои', count: 12),
    KitsSegment(id: 'all', label: 'Все', count: 48),
    KitsSegment(id: 'archive', label: 'Архив', disabled: true),
    KitsSegment(id: 'deleted', label: 'Удалённые'),
  ];

  @override
  Widget build(BuildContext context) {
    final tokens = KitsTokens.of(context);
    return Scaffold(
      backgroundColor: tokens.color('--ck-bg'),
      body: Stack(
        children: <Widget>[
          Align(
            alignment: Alignment.topCenter,
            child: ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 960),
              child: SingleChildScrollView(
                padding: EdgeInsets.all(tokens.length('--ck-space-6')),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: <Widget>[
                    _section('Тулбар списка — list-toolbar.yaml', <Widget>[
                      KitsListToolbar(
                        segments: _segments,
                        segmentValue: _toolbarSegment,
                        onSegmentChange: (value) =>
                            setState(() => _toolbarSegment = value),
                        searchValue: _search,
                        onSearchChange: (value) =>
                            setState(() => _search = value),
                        showReset: _search.isNotEmpty,
                        onReset: () => setState(() {
                          _search = '';
                          _toolbarSegment = 'all';
                        }),
                        actions: <Widget>[
                          KitsButton(
                            label: 'Подключить',
                            variant: KitsButtonVariant.primary,
                            size: KitsButtonSize.sm,
                            onClick: () {},
                          ),
                        ],
                      ),
                      SizedBox(height: tokens.length('--ck-space-3')),
                      SizedBox(
                        width: 360,
                        child: KitsListToolbar(
                          segments: _segments,
                          segmentValue: _segment,
                          onSegmentChange: (value) =>
                              setState(() => _segment = value),
                          searchValue: '',
                          onSearchChange: (_) {},
                          actions: <Widget>[
                            KitsButton(
                              label: 'Новая запись',
                              variant: KitsButtonVariant.primary,
                              size: KitsButtonSize.sm,
                              onClick: () {},
                            ),
                          ],
                        ),
                      ),
                    ]),
                    _section('Поле поиска — search-input.yaml', <Widget>[
                      const KitsSearchInput(
                        label: 'Поиск сотрудника',
                        placeholder: 'Найти сотрудника',
                      ),
                      SizedBox(height: tokens.length('--ck-space-2')),
                      KitsSearchInput(
                        controller: _filledSearch,
                        onClear: () {},
                        onChanged: (_) {},
                      ),
                      SizedBox(height: tokens.length('--ck-space-2')),
                      const KitsSearchInput(
                        label: 'Отключённый поиск',
                        disabled: true,
                      ),
                    ]),
                    _section('Сегменты — segmented-control.yaml', <Widget>[
                      KitsSegmentedControl(
                        segments: _segments,
                        value: _segment,
                        label: 'Мои или все записи',
                        onChanged: (value) => setState(() => _segment = value),
                      ),
                      SizedBox(height: tokens.length('--ck-space-2')),
                      KitsSegmentedControl(
                        segments: const <KitsSegment>[
                          KitsSegment(
                            id: 'day',
                            label: 'Дни',
                            icon: Icon(Icons.today),
                          ),
                          KitsSegment(
                            id: 'week',
                            label: 'Недели',
                            icon: Icon(Icons.date_range),
                          ),
                          KitsSegment(
                            id: 'month',
                            label: 'Месяцы',
                            icon: Icon(Icons.calendar_month),
                          ),
                        ],
                        value: 'week',
                        label: 'Единица периода',
                        size: KitsSegmentSize.sm,
                        onChanged: (_) {},
                      ),
                      SizedBox(height: tokens.length('--ck-space-2')),
                      const KitsSegmentedControl(
                        segments: <KitsSegment>[
                          KitsSegment(id: 'a', label: 'Активные'),
                          KitsSegment(id: 'b', label: 'Все'),
                        ],
                        value: 'a',
                        label: 'Недоступная группа',
                        disabled: true,
                        onChanged: _noop,
                      ),
                    ]),
                    _section('Пустое состояние — empty-state.yaml', <Widget>[
                      KitsEmptyState(
                        text: 'Записей пока нет',
                        action: KitsButton(
                          label: 'Новая запись',
                          variant: KitsButtonVariant.primary,
                          size: KitsButtonSize.sm,
                          onClick: () {},
                        ),
                      ),
                      const KitsEmptyState.nothingFound(),
                      KitsEmptyState(
                        text: 'Записей пока нет',
                        layout: KitsEmptyStateLayout.inline,
                        action: KitsButton(
                          label: 'Новая запись',
                          size: KitsButtonSize.sm,
                          onClick: () {},
                        ),
                      ),
                    ]),
                    _section('Метки и фишки — badge.yaml', <Widget>[
                      Wrap(
                        spacing: tokens.length('--ck-space-2'),
                        runSpacing: tokens.length('--ck-space-2'),
                        crossAxisAlignment: WrapCrossAlignment.center,
                        children: <Widget>[
                          KitsBadge(
                            text: 'neutral',
                            tone: KitsBadgeTone.neutral,
                          ),
                          KitsBadge(text: 'accent', tone: KitsBadgeTone.accent),
                          KitsBadge(
                            text: 'success',
                            tone: KitsBadgeTone.success,
                          ),
                          KitsBadge(
                            text: 'warning',
                            tone: KitsBadgeTone.warning,
                          ),
                          KitsBadge(text: 'danger', tone: KitsBadgeTone.danger),
                          KitsBadge(text: 'info', tone: KitsBadgeTone.info),
                          const KitsBadge(
                            text: 'solid',
                            tone: KitsBadgeTone.success,
                            appearance: KitsBadgeAppearance.solid,
                          ),
                          const KitsBadge(
                            text: 'outline',
                            tone: KitsBadgeTone.warning,
                            appearance: KitsBadgeAppearance.outline,
                          ),
                          const KitsBadge(
                            count: 120,
                            tone: KitsBadgeTone.danger,
                            appearance: KitsBadgeAppearance.solid,
                          ),
                          const KitsBadge(
                            dotOnly: true,
                            label: 'В сети',
                            tone: KitsBadgeTone.success,
                          ),
                          const KitsBadge(
                            text: 'Онлайн',
                            tone: KitsBadgeTone.success,
                            dot: true,
                          ),
                        ],
                      ),
                      SizedBox(height: tokens.length('--ck-space-3')),
                      Wrap(
                        spacing: tokens.length('--ck-space-2'),
                        runSpacing: tokens.length('--ck-space-2'),
                        children: <Widget>[
                          KitsChip(label: 'Telegram', onClick: () {}),
                          KitsChip(
                            label: 'Выбрано',
                            selected: true,
                            onClick: () {},
                          ),
                          KitsChip(
                            label: 'Почта',
                            selected: true,
                            onRemove: () {},
                            onClick: () {},
                          ),
                          KitsChip(label: 'Отключена', disabled: true),
                        ],
                      ),
                    ]),
                    _section('Окно подтверждения — confirm-dialog.yaml', <
                      Widget
                    >[
                      Wrap(
                        spacing: tokens.length('--ck-space-2'),
                        runSpacing: tokens.length('--ck-space-2'),
                        children: <Widget>[
                          KitsButton(
                            label: 'Подтвердить',
                            onClick: () => askConfirm(
                              context,
                              title: 'Опубликовать запись?',
                              text:
                                  'После публикации изменить текст будет нельзя.',
                              confirmLabel: 'Опубликовать',
                            ),
                          ),
                          KitsButton(
                            label: 'Удалить',
                            variant: KitsButtonVariant.danger,
                            onClick: () => askDanger(
                              context,
                              title: 'Удалить запись?',
                              text: 'Действие необратимо.',
                              consequences: const <String>[
                                'Текст записи исчезнет',
                                'Восстановить будет нельзя',
                              ],
                              confirmLabel: 'Удалить запись',
                              typeToConfirm: 'Запись',
                              onConfirm: () async {},
                            ),
                          ),
                        ],
                      ),
                    ]),
                    _section('Строка списка — list-row.yaml', <Widget>[
                      KitsListRow(
                        title: 'Запись с тремя действиями',
                        detail:
                            'Длинный текст, который раскрывается на весь экран',
                        meta: '12:40',
                        actions: <KitsListRowAction>[
                          KitsListRowAction(
                            label: 'Копировать',
                            icon: const Icon(Icons.copy, size: 18),
                            onClick: () {},
                          ),
                          KitsListRowAction(
                            label: 'Развернуть',
                            icon: const Icon(Icons.open_in_full, size: 18),
                            onClick: () {},
                          ),
                          KitsListRowAction(
                            label: 'Удалить',
                            icon: const Icon(Icons.delete_outline, size: 18),
                            danger: true,
                            onClick: () {},
                          ),
                        ],
                        menu: const <KitsRowMenuItem>[
                          KitsRowMenuItem(label: 'Поделиться', code: 'share'),
                          KitsRowMenuItem(
                            label: 'Удалить',
                            code: 'delete',
                            danger: true,
                          ),
                        ],
                        onTap: () {},
                      ),
                      KitsListRow(
                        title: 'Раскрытая запись',
                        detail:
                            'Полный текст записи виден целиком, без многоточия. '
                            'Время и статус остаются на месте.',
                        meta: '09:12',
                        expanded: true,
                        onToggle: (_) {},
                        onTap: () {},
                      ),
                      KitsListRow(
                        title: 'Свёрнутая запись',
                        detail: 'Первая строка текста, остальное скрыто',
                        meta: '08:03',
                        onToggle: (_) {},
                        onTap: () {},
                      ),
                    ]),
                    _section('Карточка обновления — update-card.yaml', <Widget>[
                      const KitsUpdateCard(status: KitsUpdateStatus.idle),
                      SizedBox(height: tokens.length('--ck-space-2')),
                      const KitsUpdateCard(status: KitsUpdateStatus.checking),
                      SizedBox(height: tokens.length('--ck-space-2')),
                      const KitsUpdateCard(
                        status: KitsUpdateStatus.upToDate,
                        version: '1.4.2',
                      ),
                      SizedBox(height: tokens.length('--ck-space-2')),
                      const KitsUpdateCard(
                        status: KitsUpdateStatus.available,
                        version: '1.5.0',
                        notes: 'Диктовка в фоне и новые горячие клавиши.',
                        total: 48213120,
                      ),
                      SizedBox(height: tokens.length('--ck-space-2')),
                      const KitsUpdateCard(
                        status: KitsUpdateStatus.available,
                        version: '1.5.0',
                        mandatory: true,
                        notes: 'Обязательное обновление протокола.',
                        total: 48213120,
                      ),
                      SizedBox(height: tokens.length('--ck-space-2')),
                      const KitsUpdateCard(
                        status: KitsUpdateStatus.downloading,
                        version: '1.5.0',
                        received: 24106560,
                        total: 48213120,
                      ),
                      SizedBox(height: tokens.length('--ck-space-2')),
                      const KitsUpdateCard(
                        status: KitsUpdateStatus.ready,
                        version: '1.5.0',
                      ),
                      SizedBox(height: tokens.length('--ck-space-2')),
                      const KitsUpdateCard(
                        status: KitsUpdateStatus.installing,
                        version: '1.5.0',
                      ),
                      SizedBox(height: tokens.length('--ck-space-2')),
                      const KitsUpdateCard(
                        status: KitsUpdateStatus.failed,
                        version: '1.5.0',
                        errorCode: KitsUpdateErrorCode.downloadFailed,
                        errorMessage: 'Соединение оборвалось.',
                      ),
                      SizedBox(height: tokens.length('--ck-space-2')),
                      const KitsUpdateCard(
                        status: KitsUpdateStatus.failed,
                        version: '1.5.0',
                        errorCode: KitsUpdateErrorCode.badManifest,
                        errorMessage: 'Выпуск собран с ошибкой.',
                      ),
                    ]),
                  ],
                ),
              ),
            ),
          ),
          KitsToasts(toaster: _toaster),
        ],
      ),
    );
  }

  Widget _section(String title, List<Widget> children) {
    final tokens = KitsTokens.of(context);
    return Padding(
      padding: EdgeInsets.only(bottom: tokens.length('--ck-space-6')),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: <Widget>[
          Text(
            title,
            style: KitsType.of(
              context,
              font: '--ck-font-title',
              color: tokens.color('--ck-text'),
              weight: FontWeight.w600,
            ),
          ),
          SizedBox(height: tokens.length('--ck-space-3')),
          ...children,
        ],
      ),
    );
  }
}

void _noop(String _) {}
