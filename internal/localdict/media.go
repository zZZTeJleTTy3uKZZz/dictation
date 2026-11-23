package localdict

import "github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/mediapause"

// newMediaPauser — пауза чужого медиа на время диктовки (Windows: SMTC). Нет
// системного управляющего — nil: у nil-Pauser все вызовы пустые, и диктовка
// работает как раньше. Только события и имена приложений идут в журнал
// диктовки, ни заголовков, ни текста.
func newMediaPauser(log func(event, meta string)) *mediapause.Pauser {
	backend, err := mediapause.NewDefault()
	if err != nil {
		log("media", "unavailable "+err.Error())
		return nil
	}
	return mediapause.New(backend, mediapause.Options{Log: func(event, detail string) {
		log("media", event+" "+detail)
	}})
}
