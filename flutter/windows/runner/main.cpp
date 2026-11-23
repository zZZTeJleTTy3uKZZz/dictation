// Точка входа приложения.
//
// ДВА ОБЛИКА ОДНОЙ ПРОГРАММЫ. Обычный запуск — окно настроек и заметок. Запуск с
// «--overlay» — плашка диктовки: узкая полоска без рамки поверх всех окон, у
// нижнего края экрана. Это одна и та же программа, потому что вторая половина
// весила бы столько же, сколько первая, а показывает три строки.
//
// ПОЧЕМУ ОКНО НАСТРАИВАЕТСЯ ЗДЕСЬ, А НЕ ПАКЕТОМ. Пакеты Flutter для окон
// (window_manager и прочие) требуют включить «Режим разработчика» Windows — то
// есть трогать системные настройки ради рамки окна. Здесь же всё это — три
// вызова самой Windows, и они уже написаны.
#include <flutter/dart_project.h>
#include <flutter/flutter_view_controller.h>
#include <windows.h>

#include <string>
#include <vector>

#include "flutter_window.h"
#include "utils.h"

namespace {

// Размер плашки диктовки.
//
// Полоска, а не окно: она висит поверх работы, и всё, что ей надо показать, —
// что её слышно и что уже распозналось.
constexpr int kOverlayWidth = 470;
constexpr int kOverlayHeight = 56;
// Отступ от нижнего края: над панелью задач, но рядом с ней.
constexpr int kOverlayBottomGap = 80;

bool HasFlag(const std::vector<std::string>& args, const std::string& flag) {
  for (const auto& one : args) {
    if (one == flag) {
      return true;
    }
  }
  return false;
}

// MakeOverlay — превратить обычное окно в плашку.
//
// Без рамки и заголовка, поверх всех окон, не в панели задач и — главное — не
// забирающая фокус: человек диктует в СВОЁ окно, и увести у него фокус значит
// напечатать текст не туда.
//
// РАЗМЕР ЗДЕСЬ НЕ МЕНЯЕМ. Окно уже создано нужного размера — с поправкой на
// масштаб экрана, которую сделал сам Flutter. Первая версия пересчитывала размер
// заново и ошибалась: на экране со 125% Flutter рисовал картинку на четверть
// шире окна, и правый край с кнопками просто срезало. Теперь мы только
// передвигаем окно и скругляем ему углы.
void MakeOverlay(HWND window) {
  ::SetWindowLongPtr(window, GWL_STYLE, WS_POPUP | WS_VISIBLE);
  ::SetWindowLongPtr(window, GWL_EXSTYLE,
                     WS_EX_TOPMOST | WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE);

  RECT bounds;
  ::GetWindowRect(window, &bounds);
  const int width = bounds.right - bounds.left;
  const int height = bounds.bottom - bounds.top;

  // Рабочая область, а не весь экран: под панелью задач плашку не видно.
  HMONITOR monitor = ::MonitorFromWindow(window, MONITOR_DEFAULTTOPRIMARY);
  MONITORINFO screen = {sizeof(MONITORINFO)};
  ::GetMonitorInfo(monitor, &screen);
  const int gap = ::MulDiv(kOverlayBottomGap, ::GetDpiForWindow(window), 96);

  ::SetWindowPos(window, HWND_TOPMOST,
                 screen.rcWork.left + (screen.rcWork.right - screen.rcWork.left - width) / 2,
                 screen.rcWork.bottom - height - gap,
                 width, height, SWP_NOACTIVATE | SWP_SHOWWINDOW);

  // Скругление делает само окно, а не рисунок внутри него. Иначе углы окна
  // остаются прямыми и проступают вокруг нарисованной карточки — ровно так
  // первая версия плашки и выглядела обрубком.
  const int radius = ::MulDiv(16, ::GetDpiForWindow(window), 96);
  ::SetWindowRgn(window, ::CreateRoundRectRgn(0, 0, width + 1, height + 1, radius, radius), TRUE);
}

}  // namespace

int APIENTRY wWinMain(_In_ HINSTANCE instance, _In_opt_ HINSTANCE prev,
                      _In_ wchar_t *command_line, _In_ int show_command) {
  // Консоль подключаем только при отладке: у фоновой программы она выскакивала
  // бы поверх всего, а посреди диктовки это последнее, что нужно человеку.
  if (!::AttachConsole(ATTACH_PARENT_PROCESS) && ::IsDebuggerPresent()) {
    CreateAndAttachConsole();
  }

  ::CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED);

  flutter::DartProject project(L"data");
  std::vector<std::string> command_line_arguments = GetCommandLineArguments();
  const bool overlay = HasFlag(command_line_arguments, "--overlay");
  project.set_dart_entrypoint_arguments(command_line_arguments);

  const bool dictation = HasFlag(command_line_arguments, "--dictation");
  FlutterWindow window(project, overlay, dictation);
  Win32Window::Point origin(10, 10);
  Win32Window::Size size(overlay ? kOverlayWidth : (dictation ? 680 : 980),
                         overlay ? kOverlayHeight : (dictation ? 660 : 760));
  const wchar_t* title = dictation ? L"Диктовка" : (overlay ? L"MeetDigest — диктовка" : L"MeetDigest");
  // Окно приложения тоже без системного заголовка: его рисует Flutter, и он
  // часть интерфейса, а не чужая полоса над ним.
  if (!window.Create(title, origin, size)) {
    return EXIT_FAILURE;
  }
  if (dictation) {
    HWND handle = window.GetHandle();
    LONG_PTR style = ::GetWindowLongPtr(handle, GWL_STYLE);
    ::SetWindowLongPtr(handle, GWL_STYLE, style & ~WS_CAPTION);
    ::SetWindowPos(handle, nullptr, 0, 0, 0, 0,
                   SWP_NOMOVE | SWP_NOSIZE | SWP_NOZORDER | SWP_FRAMECHANGED);
  }
  window.SetQuitOnClose(true);

  if (overlay) {
    MakeOverlay(window.GetHandle());
  }

  ::MSG msg;
  while (::GetMessage(&msg, nullptr, 0, 0)) {
    ::TranslateMessage(&msg);
    ::DispatchMessage(&msg);
  }

  ::CoUninitialize();
  return EXIT_SUCCESS;
}

