// Окно, в котором живёт Flutter, и его собственное обрамление.
//
// ПОЧЕМУ СВОЁ ОБРАМЛЕНИЕ. Системный заголовок Windows нельзя ни покрасить, ни
// уместить в него что-нибудь своё: он всегда чужой полосой висит над
// приложением. Поэтому убираем его и рисуем сами — заголовок, кнопки, тень
// становятся частью интерфейса, а не рамкой вокруг него.
//
// КАК УБИРАЕТСЯ ЗАГОЛОВОК. Не «окно без рамки» (тогда пропадут и тень, и
// изменение размера, и привязка к краям экрана), а хитрость, которой пользуются
// все: рамка остаётся, но окну говорят, что клиентская область занимает её
// целиком (WM_NCCALCSIZE). Windows продолжает считать окно обычным — со всеми
// привычками, — а рисуем в нём мы.
//
// ЧТО УМЕЕТ КАНАЛ. Кнопки заголовка и перетаскивание нарисованы во Flutter, а
// делать их работу может только система, — поэтому между ними канал:
// «свернуть», «развернуть», «закрыть», «тащить», «я развёрнут?».
#include "flutter_window.h"

#include <dwmapi.h>
#include <flutter/method_channel.h>
#include <flutter/standard_method_codec.h>
#include <windows.h>
#include <windowsx.h>

#include <memory>
#include <optional>

#include "flutter/generated_plugin_registrant.h"

namespace {

// Толщина невидимой полосы у краёв, за которую тянут размер окна.
constexpr int kResizeEdge = 6;
// Высота нашей полосы заголовка — ровно столько же в Dart.
constexpr int kTitleBar = 44;

// Развёрнутое окно занимает рабочую область монитора, то есть экран без панели
// задач. Без явных границ окно без системного заголовка уезжало под панель
// задач. Координаты в MINMAXINFO — относительно монитора, поэтому от
// rcWork отнимаем левый верхний угол rcMonitor.
void FillMaximizedBounds(HWND hwnd, MINMAXINFO* info) {
  HMONITOR monitor = ::MonitorFromWindow(hwnd, MONITOR_DEFAULTTONEAREST);
  MONITORINFO bounds{};
  bounds.cbSize = sizeof(bounds);
  if (!::GetMonitorInfo(monitor, &bounds)) {
    return;
  }
  const RECT& work = bounds.rcWork;
  const RECT& screen = bounds.rcMonitor;
  info->ptMaxPosition.x = work.left - screen.left;
  info->ptMaxPosition.y = work.top - screen.top;
  info->ptMaxSize.x = work.right - work.left;
  info->ptMaxSize.y = work.bottom - work.top;
}

}  // namespace

FlutterWindow::FlutterWindow(const flutter::DartProject& project, bool overlay, bool dictation)
    : project_(project), overlay_(overlay), dictation_(dictation) {}

FlutterWindow::~FlutterWindow() {}

bool FlutterWindow::OnCreate() {
  if (!Win32Window::OnCreate()) {
    return false;
  }

  RECT frame = GetClientArea();
  flutter_controller_ = std::make_unique<flutter::FlutterViewController>(
      frame.right - frame.left, frame.bottom - frame.top, project_);
  if (!flutter_controller_->engine() || !flutter_controller_->view()) {
    return false;
  }
  RegisterPlugins(flutter_controller_->engine());
  SetChildContent(flutter_controller_->view()->GetNativeWindow());

  SetUpWindowChannel();

  flutter_controller_->engine()->SetNextFrameCallback([&]() {
    if (overlay_) {
      // Плашка живёт всё время, но прячется: поднять окно заново стоит пару
      // секунд, и человек ждал бы их после каждого нажатия клавиши.
      ::ShowWindow(GetHandle(), SW_HIDE);
    } else {
      this->Show();
    }
  });
  flutter_controller_->ForceRedraw();
  return true;
}

// SetUpWindowChannel — то, что Flutter не может сделать сам.
void FlutterWindow::SetUpWindowChannel() {
  channel_ = std::make_unique<flutter::MethodChannel<flutter::EncodableValue>>(
      flutter_controller_->engine()->messenger(), "meetdigest/window",
      &flutter::StandardMethodCodec::GetInstance());

  channel_->SetMethodCallHandler(
      [this](const auto& call, auto result) {
        const std::string& name = call.method_name();
        HWND window = GetHandle();
        if (name == "drag") {
          // Перетаскивание за нашу полосу заголовка: отпускаем мышь и говорим
          // системе, что схватили за заголовок. Дальше она тащит окно сама — со
          // всеми привычками вроде прилипания к краям экрана.
          ::ReleaseCapture();
          ::SendMessage(window, WM_NCLBUTTONDOWN, HTCAPTION, 0);
          result->Success();
        } else if (name == "minimize") {
          ::ShowWindow(window, SW_MINIMIZE);
          result->Success();
        } else if (name == "maximize") {
          const bool full = ::IsZoomed(window);
          ::ShowWindow(window, full ? SW_RESTORE : SW_MAXIMIZE);
          result->Success(flutter::EncodableValue(!full));
        } else if (name == "close") {
          ::PostMessage(window, WM_CLOSE, 0, 0);
          result->Success();
        } else if (name == "showOverlay") {
          // Показываем без активации: фокус остаётся у того окна, куда человек
          // диктует.
          ::ShowWindow(window, SW_SHOWNOACTIVATE);
          ::SetWindowPos(window, HWND_TOPMOST, 0, 0, 0, 0,
                         SWP_NOMOVE | SWP_NOSIZE | SWP_NOACTIVATE | SWP_SHOWWINDOW);
          result->Success();
        } else if (name == "hideOverlay") {
          ::ShowWindow(window, SW_HIDE);
          result->Success();
        } else if (name == "isMaximized") {
          result->Success(flutter::EncodableValue(::IsZoomed(window) != 0));
        } else {
          result->NotImplemented();
        }
      });
}

void FlutterWindow::OnDestroy() {
  if (flutter_controller_) {
    flutter_controller_ = nullptr;
  }
  Win32Window::OnDestroy();
}

LRESULT FlutterWindow::MessageHandler(HWND hwnd, UINT const message,
                                      WPARAM const wparam,
                                      LPARAM const lparam) noexcept {
  switch (message) {
    case WM_CLOSE:
      if (dictation_) {
        ::ShowWindow(hwnd, SW_HIDE);
        ::SetTimer(hwnd, 17, 60000, nullptr);
        return 0;
      }
      break;
    case WM_SHOWWINDOW:
      if (dictation_ && wparam) ::KillTimer(hwnd, 17);
      break;
    case WM_TIMER:
      if (dictation_ && wparam == 17) {
        ::KillTimer(hwnd, 17);
        ::DestroyWindow(hwnd);
        return 0;
      }
      break;
    case WM_WINDOWPOSCHANGING:
      if (overlay_) {
        // Держимся поверх всех при каждой перестановке окон. Одного
        // SetWindowPos при запуске мало: развёрнутое окно другой программы
        // (у нас — видеоредактор) встаёт выше, и плашка пропадает из виду
        // ровно тогда, когда человек её ждёт.
        reinterpret_cast<WINDOWPOS*>(lparam)->hwndInsertAfter = HWND_TOPMOST;
      }
      break;
    case WM_GETMINMAXINFO:
      if (!overlay_) {
        // Сначала системные значения (минимальный размер окна), потом наши
        // границы развёрнутого окна: иначе минимум пропал бы вместе с ними.
        ::DefWindowProc(hwnd, message, wparam, lparam);
        FillMaximizedBounds(hwnd, reinterpret_cast<MINMAXINFO*>(lparam));
        return 0;
      }
      break;

    case WM_NCCALCSIZE:
      if (wparam == TRUE && !overlay_) {
        // Клиентская область занимает всё окно: системный заголовок исчезает, а
        // рамка (тень, изменение размера, привязка к краям) остаётся.
        // Отступа рамки для развёрнутого окна нет: его размер уже задан
        // рабочей областью (WM_GETMINMAXINFO), и отступ оставлял бы светлую
        // полосу по краям.
        return 0;
      }
      break;

    case WM_SIZE:
      // Окно развернули или вернули — кнопкой, двойным кликом, Win+стрелкой или
      // прилипанием к краю. Dart узнаёт об этом сам, и подпись кнопки не
      // отстаёт от окна. Шлём только смену состояния.
      if (channel_ && !overlay_) {
        const bool maximized = ::IsZoomed(hwnd) != 0;
        if (maximized != maximized_reported_) {
          maximized_reported_ = maximized;
          channel_->InvokeMethod(
              "maximizedChanged",
              std::make_unique<flutter::EncodableValue>(maximized));
        }
      }
      break;

    case WM_NCHITTEST: {
      if (overlay_) {
        break;
      }
      // Края окна остаются «ручками» для изменения размера: полосы в шесть
      // точек хватает, чтобы попасть мышью, и она не мешает содержимому.
      const POINT cursor = {GET_X_LPARAM(lparam), GET_Y_LPARAM(lparam)};
      RECT bounds;
      ::GetWindowRect(hwnd, &bounds);
      const bool left = cursor.x < bounds.left + kResizeEdge;
      const bool right = cursor.x >= bounds.right - kResizeEdge;
      const bool top = cursor.y < bounds.top + kResizeEdge;
      const bool bottom = cursor.y >= bounds.bottom - kResizeEdge;

      if (top && left) return HTTOPLEFT;
      if (top && right) return HTTOPRIGHT;
      if (bottom && left) return HTBOTTOMLEFT;
      if (bottom && right) return HTBOTTOMRIGHT;
      if (left) return HTLEFT;
      if (right) return HTRIGHT;
      if (top) return HTTOP;
      if (bottom) return HTBOTTOM;
      break;
    }
  }

  // Flutter разбирает сообщения после нас: иначе он съел бы нажатия мыши на
  // краях окна и размер перестал бы меняться.
  if (flutter_controller_) {
    std::optional<LRESULT> result =
        flutter_controller_->HandleTopLevelWindowProc(hwnd, message, wparam,
                                                      lparam);
    if (result) {
      return *result;
    }
  }

  if (message == WM_FONTCHANGE) {
    flutter_controller_->engine()->ReloadSystemFonts();
  }
  return Win32Window::MessageHandler(hwnd, message, wparam, lparam);
}
