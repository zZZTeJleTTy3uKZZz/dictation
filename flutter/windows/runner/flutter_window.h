#ifndef RUNNER_FLUTTER_WINDOW_H_
#define RUNNER_FLUTTER_WINDOW_H_

#include <flutter/dart_project.h>
#include <flutter/encodable_value.h>
#include <flutter/flutter_view_controller.h>
#include <flutter/method_channel.h>

#include <memory>

#include "win32_window.h"

// Окно с Flutter внутри и собственным обрамлением.
//
// overlay — плашка диктовки: у неё нет ни заголовка, ни изменения размера, и
// хитрости обычного окна ей только мешают.
class FlutterWindow : public Win32Window {
 public:
  explicit FlutterWindow(const flutter::DartProject& project, bool overlay, bool dictation = false);
  virtual ~FlutterWindow();

 protected:
  bool OnCreate() override;
  void OnDestroy() override;
  LRESULT MessageHandler(HWND window, UINT const message, WPARAM const wparam,
                         LPARAM const lparam) noexcept override;

 private:
  // SetUpWindowChannel — канал для того, что умеет только система: свернуть,
  // развернуть, закрыть, тащить окно за нашу полосу заголовка.
  void SetUpWindowChannel();

  flutter::DartProject project_;
  bool overlay_ = false;
  bool dictation_ = false;
  // Последнее состояние, которое мы сообщили Dart: шлём только смену, а не
  // каждое изменение размера во время перетаскивания края.
  bool maximized_reported_ = false;
  std::unique_ptr<flutter::FlutterViewController> flutter_controller_;
  std::unique_ptr<flutter::MethodChannel<flutter::EncodableValue>> channel_;
};

#endif  // RUNNER_FLUTTER_WINDOW_H_
