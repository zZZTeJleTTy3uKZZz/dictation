// Стирание ключей распознавания: кнопка «Удалить» в настройках диктовки.
//
// ЗАЧЕМ СВЕРКА ДО И ПОСЛЕ. Ключ стирают, только если потом точно известно, что
// его нет. Хранилище, которое не отдало ключ назад, нельзя ни стереть вслепую
// (а вдруг это ключ, который человеку ещё нужен), ни считать пустым (тогда
// окно соврёт «ключа нет»). Поэтому: сначала читаем, и если хранилище не
// читается — ничего не трогаем; потом стираем и читаем обратно.
//
// ЗАЧЕМ ОТСУТСТВИЕ — НЕ ОШИБКА. Стереть то, чего нет, — тот же результат, что
// и стереть: окно может нажать «Удалить» дважды или после того, как ключ уже
// ушёл, и это не должно выглядеть сбоем.
package speech

import (
	"errors"
	"fmt"
	"strings"
)

// ForgetKeyChecked — стереть ключ по адресу и проверить, что его больше нет.
//
// Пустой адрес — отказ, а не стирание. У пустого адреса имя ключа — Groq (см.
// providerSecretName), и «стереть ключ своего сервиса, у которого адреса нет»
// стёрло бы ключ Groq. Вызывающий обязан сам не звать её без адреса.
//
// ErrKeyUnreadable — хранилище не прочиталось: ошибка возвращается ДО
// стирания, поэтому ключ остаётся как был.
func ForgetKeyChecked(path, baseURL string) error {
	if strings.TrimSpace(baseURL) == "" {
		return errNoAddress
	}
	if path == "" {
		path = ProvidersPath()
	}
	ensureMigrated(path)
	name := providerSecretName(baseURL)
	store := newStore(path)
	if _, err := probeKey(store, name, baseURL); err != nil {
		if errors.Is(err, errSecretNotFound) {
			// Ключа нет — стирать нечего, и это не ошибка.
			return nil
		}
		return fmt.Errorf("%w: %v — ключ не тронут", ErrKeyUnreadable, err)
	}
	if err := store.Delete(name); err != nil {
		return fmt.Errorf("ключ не стёрт: %w", err)
	}
	// Обратное чтение: «стёрто» — то, что мы видим в хранилище, а не то, что
	// Delete вернул без ошибки.
	if _, err := probeKey(store, name, baseURL); err == nil {
		return errors.New("ключ остался в хранилище после стирания — попробуйте ещё раз")
	} else if !errors.Is(err, errSecretNotFound) {
		return fmt.Errorf("%w: %v — стирание не проверено", ErrKeyUnreadable, err)
	}
	return nil
}

// ForgetGroqKey — стереть ключ Groq. Ключ Groq лежит под именем «Groq» при
// любом адресе Groq, поэтому адрес по умолчанию — тот же ключ.
func ForgetGroqKey(path string) error {
	return ForgetKeyChecked(path, GroqBaseURL)
}

// ForgetGroqProvider — убрать из providers.json запись провайдера Groq.
//
// ЗАЧЕМ. Облако «Groq» считается заведённым по записи провайдера (см.
// SaveGroqProvider). Ключ стёрт — а запись осталась бы, и окно и служба
// продолжали бы считать Groq заведённым без ключа. Убираем только эту запись:
// словарь терминов и прочие поля файла не трогаем.
//
// Файла нет или записи нет — ничего не делаем и ошибки нет.
func ForgetGroqProvider(path string) error {
	if path == "" {
		path = ProvidersPath()
	}
	whole, err := readWhole(path)
	if err != nil {
		return err
	}
	list, _ := whole["providers"].([]any)
	kept := make([]any, 0, len(list))
	removed := false
	for _, one := range list {
		if saved, ok := one.(map[string]any); ok && saved["id"] == GroqProviderID {
			removed = true
			continue
		}
		kept = append(kept, one)
	}
	if !removed {
		return nil
	}
	whole["providers"] = kept
	// Как и при записи провайдера: без хранилища, перенос ключей тут не нужен.
	return writeWholeSettings(path, whole)
}
