import 'package:flutter/material.dart';
import 'package:kits_ui/kits_ui.dart';

/// Витрина кита: все восемь компонентов на одной странице, обе темы.
///
/// Тема берётся из адреса (`?theme=dark`), чтобы снимок экрана снимался без
/// нажатий; кнопка в шапке переключает её руками.
class KitsGalleryApp extends StatefulWidget {
  const KitsGalleryApp({super.key});

  @override
  State<KitsGalleryApp> createState() => _KitsGalleryAppState();
}

class _KitsGalleryAppState extends State<KitsGalleryApp> {
  late bool _dark = Uri.base.queryParameters['theme'] == 'dark';

  @override
  Widget build(BuildContext context) {
    final theme = kitsTheme(brightness: _dark ? Brightness.dark : Brightness.light);
    return MaterialApp(
      title: 'kits_ui — компоненты',
      debugShowCheckedModeBanner: false,
      theme: theme,
      home: KitsGallery(
        dark: _dark,
        onToggleTheme: () => setState(() => _dark = !_dark),
      ),
    );
  }
}

/// Страница-витрина: по разделу на компонент.
class KitsGallery extends StatefulWidget {
  const KitsGallery({
    super.key,
    required this.dark,
    required this.onToggleTheme,
  });

  final bool dark;
  final VoidCallback onToggleTheme;

  @override
  State<KitsGallery> createState() => _KitsGalleryState();
}

class _KitsGalleryState extends State<KitsGallery> {
  final KitsToaster _toaster = KitsToaster();
  final TextEditingController _search = TextEditingController();
  final TextEditingController _clearable = TextEditingController(text: 'Черновик');
  final TextEditingController _notes = TextEditingController();

  String _tab = 'inbox';
  String? _person = 'anna';
  final Set<String> _people = <String>{'anna'};
  bool _notifications = true;
  bool _sound = false;
  bool _modalOpen = false;

  static const List<KitsSelectSection> _peopleOptions = <KitsSelectSection>[
    KitsSelectSection(
      title: 'Команда',
      options: <KitsSelectOption>[
        KitsSelectOption(id: 'anna', label: 'Анна Кузнецова'),
        KitsSelectOption(id: 'boris', label: 'Борис Лебедев'),
        KitsSelectOption(id: 'vera', label: 'Вера Соколова'),
        KitsSelectOption(id: 'disabled', label: 'Уволен', disabled: true),
      ],
    ),
  ];

  static const List<KitsTabItem> _tabs = <KitsTabItem>[
    KitsTabItem(code: 'inbox', name: 'Входящие', count: 12, icon: Icon(Icons.inbox)),
    KitsTabItem(code: 'recent', name: 'Недавние', icon: Icon(Icons.schedule)),
    KitsTabItem(code: 'groups', name: 'Группы', count: 3, icon: Icon(Icons.group)),
    KitsTabItem(code: 'channels', name: 'Каналы', icon: Icon(Icons.campaign)),
    KitsTabItem(code: 'bots', name: 'Боты', icon: Icon(Icons.smart_toy)),
  ];

  @override
  void dispose() {
    _toaster.dispose();
    _search.dispose();
    _clearable.dispose();
    _notes.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final tokens = KitsTokens.of(context);
    return Scaffold(
      appBar: AppBar(
        backgroundColor: tokens.color('--ck-bg-second'),
        title: const Text('kits_ui — компоненты'),
        actions: <Widget>[
          Padding(
            padding: EdgeInsets.only(right: tokens.length('--ck-space-4')),
            child: KitsButton(
              label: widget.dark ? 'Светлая тема' : 'Тёмная тема',
              onClick: widget.onToggleTheme,
            ),
          ),
        ],
      ),
      body: Stack(
        children: <Widget>[
          Center(
            child: ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 960),
              child: SingleChildScrollView(
                padding: EdgeInsets.all(tokens.length('--ck-space-6')),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: <Widget>[
                    _sectionButtons(context),
                    _sectionField(context),
                    _sectionSelect(context),
                    _sectionSwitch(context),
                    _sectionModal(context),
                    _sectionListRows(context),
                    _sectionToast(context),
                    _sectionTabs(context),
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
    return _Section(title: title, children: children);
  }

  /// Ряд кнопок: на широком экране строка, на узком — перенос.
  Widget _buttons(List<Widget> buttons) {
    return Wrap(
      spacing: 8,
      runSpacing: 8,
      crossAxisAlignment: WrapCrossAlignment.center,
      children: buttons,
    );
  }

  /// Пара полей или выборов в ряд, на узком экране — столбиком.
  Widget _pair(Widget left, Widget right) {
    return LayoutBuilder(
      builder: (context, constraints) {
        if (constraints.maxWidth < 560) {
          return Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: <Widget>[left, const SizedBox(height: 12), right],
          );
        }
        return Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: <Widget>[
            Expanded(child: left),
            const SizedBox(width: 12),
            Expanded(child: right),
          ],
        );
      },
    );
  }

  Widget _sectionButtons(BuildContext context) {
    return _section('Кнопка — button.yaml', <Widget>[
      _buttons(<Widget>[
          KitsButton(label: 'Сохранить', variant: KitsButtonVariant.primary, onClick: () {}),
          KitsButton(label: 'Отмена', onClick: () {}),
          KitsButton(label: 'Тихо', variant: KitsButtonVariant.quiet, onClick: () {}),
          KitsButton(label: 'Удалить', variant: KitsButtonVariant.danger, onClick: () {}),
          KitsButton(label: 'Подробнее', variant: KitsButtonVariant.link, onClick: () {}),
          KitsButton(label: 'Занято', loading: true, onClick: () {}),
          KitsButton(label: 'Недоступно', disabled: true),
      ]),
      const SizedBox(height: 12),
      _buttons(<Widget>[
          KitsButton(label: 'sm', size: KitsButtonSize.sm, onClick: () {}),
          KitsButton(label: 'md', size: KitsButtonSize.md, onClick: () {}),
          KitsButton(label: 'lg', size: KitsButtonSize.lg, onClick: () {}),
          KitsButton(
            variant: KitsButtonVariant.iconDefault,
            semanticLabel: 'Обновить',
            iconContent: const Icon(Icons.refresh, size: 18),
            onClick: () {},
          ),
          KitsButton(
            variant: KitsButtonVariant.iconAccent,
            semanticLabel: 'Добавить',
            iconContent: const Icon(Icons.add, size: 18),
            onClick: () {},
          ),
          KitsButton(
            variant: KitsButtonVariant.iconDanger,
            semanticLabel: 'Удалить',
            iconContent: const Icon(Icons.delete_outline, size: 18),
            onClick: () {},
          ),
      ]),
    ]);
  }

  Widget _sectionField(BuildContext context) {
    return _section('Поле — field.yaml', <Widget>[
      _pair(
        const KitsTextField(
          label: 'Почта',
          required: true,
          hint: 'Служебный адрес, не личный',
          placeholder: 'name@example.com',
        ),
        KitsTextField(
          label: 'Поиск',
          variant: KitsFieldVariant.search,
          controller: _search,
          onClear: _search.clear,
          placeholder: 'Найти сотрудника',
        ),
      ),
      const SizedBox(height: 12),
      const KitsTextField(
        label: 'Код приглашения',
        error: 'Код не подходит',
        placeholder: 'ABCD-1234',
      ),
      const SizedBox(height: 12),
      _pair(
        KitsTextField(
          label: 'Пометка',
          controller: _clearable,
          onClear: _clearable.clear,
        ),
        KitsTextField(
          label: 'Заметка',
          variant: KitsFieldVariant.textareaAutogrow,
          maxRows: 6,
          controller: _notes,
        ),
      ),
    ]);
  }

  Widget _sectionSelect(BuildContext context) {
    return _section('Выбор — select.yaml', <Widget>[
      _pair(
        KitsSelect(
          sections: _peopleOptions,
          value: _person,
          onChanged: (value) => setState(() => _person = value),
        ),
        KitsSelect(
          sections: _peopleOptions,
          value: _person,
          searchable: true,
          onChanged: (value) => setState(() => _person = value),
        ),
      ),
      const SizedBox(height: 12),
      KitsSelect(
        sections: _peopleOptions,
        values: _people,
        multiple: true,
        appearance: KitsSelectAppearance.row,
        actionLabel: '+ Создать сотрудника',
        onAction: (_) => _toaster.notify('Создать сотрудника'),
        onPick: (id) => setState(() {
          _people.contains(id) ? _people.remove(id) : _people.add(id);
        }),
      ),
    ]);
  }

  Widget _sectionSwitch(BuildContext context) {
    return _section('Выключатель — switch.yaml', <Widget>[
      KitsSwitch(
        label: 'Уведомления о новых сообщениях',
        value: _notifications,
        onChanged: (value) => setState(() => _notifications = value),
      ),
      KitsSwitch(
        label: 'Звук при упоминании',
        value: _sound,
        onChanged: (value) => setState(() => _sound = value),
      ),
      const KitsSwitch(label: 'Отключено', value: false),
    ]);
  }

  Widget _sectionModal(BuildContext context) {
    return _section('Окно — modal.yaml', <Widget>[
      _buttons(<Widget>[
          KitsButton(
            label: 'Открыть окно',
            variant: KitsButtonVariant.primary,
            onClick: () => setState(() => _modalOpen = true),
          ),
      ]),
      KitsModal(
            open: _modalOpen,
            title: 'Пригласить в команду',
            description: 'Окно заперто фокусом и закрывается Escape.',
            size: KitsModalSize.md,
            form: true,
            footer: Row(
              mainAxisSize: MainAxisSize.min,
              children: <Widget>[
                KitsButton(label: 'Отмена', onClick: () => setState(() => _modalOpen = false)),
                const SizedBox(width: 8),
                KitsButton(
                  label: 'Пригласить',
                  variant: KitsButtonVariant.primary,
                  onClick: () => setState(() => _modalOpen = false),
                ),
              ],
            ),
            onClose: () => setState(() => _modalOpen = false),
            child: const KitsTextField(
              label: 'Почта',
              placeholder: 'name@example.com',
            ),
          ),
    ]);
  }

  Widget _sectionListRows(BuildContext context) {
    return _section('Строка списка — list-row.yaml', <Widget>[
      KitsListRow(
        title: 'Анна Кузнецова',
        detail: 'Старший специалист',
        meta: '12:40',
        density: KitsListRowDensity.list,
        leading: CircleAvatar(
          backgroundColor: KitsTokens.of(context).color('--ck-avatar-2'),
          child: const Text('АК'),
        ),
        trailing: const Icon(Icons.chevron_right),
        onTap: () {},
      ),
      const KitsListRow(
        title: 'Плотная строка',
        density: KitsListRowDensity.compact,
        trailing: Icon(Icons.more_horiz),
      ),
      KitsListRow(
        title: 'Выбранная строка',
        selected: true,
        onTap: () {},
      ),
      KitsListRow(
        title: 'Опасное действие',
        danger: true,
        onTap: () {},
      ),
      const KitsListRow(title: 'Недоступная строка', disabled: true),
    ]);
  }

  Widget _sectionToast(BuildContext context) {
    return _section('Тост — toast.yaml', <Widget>[
      _buttons(<Widget>[
          KitsButton(
            label: 'Обычная весть',
            onClick: () => _toaster.notify('Черновик сохранён'),
          ),
          KitsButton(
            label: 'Успех',
            onClick: () => _toaster.notify('Приглашение отправлено', tone: KitsToastTone.ok),
          ),
          KitsButton(
            label: 'Ошибка',
            onClick: () => _toaster.notify('Не удалось отправить', tone: KitsToastTone.bad),
          ),
          KitsButton(
            label: 'С отменой',
            onClick: () => _toaster.notifyUndo(
              'Сообщение удалено',
              onUndo: () => _toaster.notify('Восстановлено', tone: KitsToastTone.ok),
            ),
          ),
      ]),
    ]);
  }

  Widget _sectionTabs(BuildContext context) {
    return _section('Вкладки — tabs.yaml', <Widget>[
      KitsTabs(
        items: _tabs,
        current: _tab,
        onPick: (code) => setState(() => _tab = code),
      ),
      const SizedBox(height: 12),
      ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 420),
        child: KitsTabs(
          items: _tabs,
          current: _tab,
          fit: true,
          onPick: (code) => setState(() => _tab = code),
        ),
      ),
    ]);
  }
}

class _Section extends StatelessWidget {
  const _Section({required this.title, required this.children});

  final String title;
  final List<Widget> children;

  @override
  Widget build(BuildContext context) {
    final tokens = KitsTokens.of(context);
    return Padding(
      padding: EdgeInsets.only(bottom: tokens.length('--ck-space-6')),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: <Widget>[
          Text(
            title,
            style: TextStyle(
              fontSize: tokens.length('--ck-font-page'),
              fontWeight: FontWeight.w600,
              color: tokens.color('--ck-text'),
            ),
          ),
          SizedBox(height: tokens.length('--ck-space-3')),
          ...children,
        ],
      ),
    );
  }
}
