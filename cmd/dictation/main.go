package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"fyne.io/systray"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/dictconfig"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/dictpaster"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/dictservice"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/localdict"
)

//go:embed icon.ico
var icon []byte

func main() {
	bin, _ := os.Executable()
	bin = filepath.Dir(bin)
	dir := localdict.SettingsDir()
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version":
			fmt.Println(localdict.Version)
			return
		case "--status":
			// Установщик и скрипт выкатки спрашивают, идёт ли запись. Ответ —
			// JSON в stdout для разборщика и строка в stderr человеку; код
			// возврата 3 значит «идёт запись», и трогать файлы нельзя.
			localdict.AttachConsole()
			// Предел ответа: спросивший ждёт эту команду целиком, и зависший
			// вопрос остановил бы установку молча.
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			status, err := localdict.StatusRunning(ctx, localdict.DataDir())
			cancel()
			if err != nil {
				fmt.Fprintln(os.Stderr, "диктовка: "+err.Error())
				os.Exit(localdict.ExitFailed)
			}
			_ = json.NewEncoder(os.Stdout).Encode(status)
			fmt.Fprintln(os.Stderr, localdict.FormatStatus(status))
			if status.Recording {
				os.Exit(localdict.ExitRecording)
			}
			return
		case "--quit":
			// Установщик зовёт это перед заменой файлов: работающую программу
			// Windows перезаписать не даёт, а обрывать разговор силой нельзя.
			// Поэтому --quit ждёт конца идущей записи, а --wait задаёт предел;
			// не дождался — отказывает кодом 3, и программу не убивают.
			localdict.AttachConsole()
			options, err := localdict.ParseStopOptions(os.Args[2:])
			if err != nil {
				fmt.Fprintln(os.Stderr, "диктовка: "+err.Error())
				os.Exit(localdict.ExitFailed)
			}
			limit := options.Wait
			if limit <= 0 {
				limit = localdict.MaxStopWait
			}
			ctx, cancel := context.WithTimeout(context.Background(), limit+2*time.Minute)
			defer cancel()
			if err = localdict.QuitRunning(ctx, localdict.DataDir(), options); err != nil {
				fmt.Fprintln(os.Stderr, "диктовка: "+err.Error())
				var refused *localdict.StopRefused
				if errors.As(err, &refused) {
					os.Exit(localdict.ExitRecording)
				}
				os.Exit(localdict.ExitFailed)
			}
			return
		case "doctor", "--doctor":
			// Лекарь: проверить, что диктовка собрана и жива, и починить то, что
			// можно починить без человека (см. localdict.Doctor). Отчёт кладём
			// ещё и в файл: эта сборка без консоли, и запущенный из проводника
			// лекарь иначе не сказал бы ничего.
			localdict.AttachConsole()
			checks := localdict.Doctor(context.Background())
			var report strings.Builder
			code := localdict.PrintDoctor(checks, &report)
			fmt.Print(report.String())
			localdict.WriteDoctorReport(report.String())
			os.Exit(code)
		case "--paster":
			// Узкий помощник вставки: слушает именованный канал своего сеанса
			// и нажимает Ctrl+V. Ни HTTP, ни настроек, ни ключей, ни чужих
			// процессов — повышенные права нужны только для SendInput.
			if err := dictpaster.Serve(); err != nil {
				os.Exit(1)
			}
			return
		case "--service":
			// Службой диктовку зовёт сама Windows, а не человек.
			exe, err := dictservice.Executable()
			if err != nil {
				os.Exit(1)
			}
			if err = dictservice.Run(exe); err != nil {
				os.Exit(1)
			}
			return
		case "--release-ime-hotkey":
			// Установщик с ключом «освободить системный Ctrl+Space»: убираем
			// системное переключение ввода из реестра, чтобы Диктовка могла
			// занять сочетание. Применяется после выхода и входа в Windows.
			localdict.AttachConsole()
			note, err := localdict.ReleaseDefaultIMEHotkey(localdict.SettingsDir())
			if err != nil {
				fmt.Fprintln(os.Stderr, "диктовка: "+err.Error())
				os.Exit(1)
			}
			fmt.Println("диктовка: " + note)
			return
		case "--restore-ime-hotkey":
			// Удаление программы: возвращаем системное сочетание как было.
			localdict.AttachConsole()
			note, err := localdict.RestoreSavedIMEHotkeys(localdict.SettingsDir())
			if err != nil {
				fmt.Fprintln(os.Stderr, "диктовка: "+err.Error())
				os.Exit(1)
			}
			fmt.Println("диктовка: " + note)
			return
		case "--apply-config":
			// Тихая установка/обновление из скрипта: установщик передаёт
			// путь к файлу со ссылкой-конфигурацией провайдера. Ключ приходит
			// ТОЛЬКО из файла, и в ответе его нет.
			localdict.AttachConsole()
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "диктовка: --apply-config требует путь к файлу со ссылкой-конфигурацией")
				os.Exit(1)
			}
			applied, err := dictconfig.ApplyLinkFile(os.Args[2])
			if err != nil {
				fmt.Fprintln(os.Stderr, "диктовка: "+err.Error())
				os.Exit(1)
			}
			key := "ключ не задан"
			if applied.KeySet {
				key = "ключ задан"
			}
			fmt.Printf("диктовка: провайдер настроен — %s (%s), модель %s, %s\n",
				applied.Name, applied.URL, applied.Model, key)
			return
		case "--install-service", "--uninstall-service":
			// Ставит и снимает службу человек с правами администратора — обычно
			// из установщика. Эта сборка без консоли, поэтому сначала
			// подключаемся к консоли того, кто позвал, иначе он не увидит ни
			// удачи, ни причины отказа.
			localdict.AttachConsole()
			install := os.Args[1] == "--install-service"
			var err error
			if install {
				exe, exeErr := dictservice.Executable()
				if exeErr != nil {
					fmt.Fprintln(os.Stderr, "диктовка: "+exeErr.Error())
					os.Exit(1)
				}
				err = dictservice.Install(exe)
			} else {
				err = dictservice.Uninstall()
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, "диктовка: "+err.Error())
				os.Exit(1)
			}
			if install {
				fmt.Println("диктовка: служба «" + dictservice.DisplayName + "» поставлена и запущена")
			} else {
				fmt.Println("диктовка: служба «" + dictservice.DisplayName + "» снята")
			}
			return
		}
	}
	daemon := slices.Contains(os.Args, "--daemon")
	settings := slices.Contains(os.Args, "--settings")
	mutexName := "Local\\Dictation-" + os.Getenv("USERNAME")
	if !daemon {
		mutexName += "-Supervisor"
	}
	name, _ := syscall.UTF16PtrFromString(mutexName)
	mutex, _, err := syscall.NewLazyDLL("kernel32.dll").NewProc("CreateMutexW").Call(0, 1, uintptr(unsafe.Pointer(name)))
	if mutex == 0 {
		os.Exit(1)
	}
	defer func() { _, _, _ = syscall.NewLazyDLL("kernel32.dll").NewProc("CloseHandle").Call(mutex) }()
	if err == syscall.Errno(183) {
		if settings {
			localdict.NewApp(dir, bin).OpenUI()
		}
		return
	}
	if !daemon {
		executable, _ := os.Executable()
		if localdict.RunSupervisor(context.Background(), executable, dir, settings) != nil {
			os.Exit(1)
		}
		return
	}
	_, _, _ = syscall.NewLazyDLL("user32.dll").NewProc("SetProcessDpiAwarenessContext").Call(^uintptr(3))
	app := localdict.NewApp(dir, bin)
	if err = app.Start(); err != nil {
		os.Exit(1)
	}
	server, err := app.Serve()
	if err != nil {
		app.Close()
		os.Exit(1)
	}
	systray.Run(func() {
		systray.SetIcon(icon)
		systray.SetTitle("Диктовка")
		systray.SetTooltip("Диктовка · " + app.Snapshot().Config.Hotkey)
		// Окно выключения: без него машину гасят вместе с записью, и звук
		// последнего разговора остаётся недописанным. Окно
		// создаётся в этой же нити — в ней живёт цикл сообщений лотка.
		if err := localdict.WatchShutdown(app); err != nil {
			fmt.Fprintln(os.Stderr, "диктовка: "+err.Error())
		}
		open := systray.AddMenuItem("Открыть диктовку", "Настройки и история")
		systray.AddSeparator()
		quit := systray.AddMenuItem("Выйти", "Выключить диктовку")
		if settings {
			app.OpenUI()
		}
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					app.OpenUI()
				case <-quit.ClickedCh:
					systray.Quit()
					return
				case <-app.Done():
					systray.Quit()
					return
				}
			}
		}()
	}, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		app.Close()
		_ = os.Remove(filepath.Join(localdict.DataDir(), "endpoint.json"))
	})
}
