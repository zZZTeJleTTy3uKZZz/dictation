// Package speech — превращение записи в текст.
//
// ЗАЧЕМ РАЗНЫЕ СПОСОБЫ. Расшифровать можно по-разному, и выбор не вечен:
// сегодня проще отдать чужому сервису по сети, завтра на машине хватит сил
// держать модель локально (и тогда заметка не уезжает никуда вовсе), а на слабом
// ноутбуке придётся снова в сеть. Если бы способ был вписан в код записи, каждая
// такая перемена означала бы переписывание диктофона.
//
// Поэтому здесь один вопрос — «вот файл, дай текст» — и несколько ответов на
// него. Диктофон знает только вопрос.
//
// ПОРЯДОК ВЫБОРА. Человек называет способ в настройках. Назвал несуществующий
// или промолчал — берём первый доступный: молчащая расшифровка хуже неидеальной,
// потому что заметка без текста не ищется.
package speech

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Result — что вернул распознаватель.
type Result struct {
	Text string
	// By — чем распознано: остаётся в истории, чтобы потом было видно, чей это
	// текст (у разных способов разное качество, и сравнивать придётся).
	By string
}

// Channel — один способ расшифровки.
//
// Интерфейс объявлен здесь, у потребителя: диктофону нужна ровно одна операция,
// а не вся библиотека распознавания.
type Channel interface {
	// Name — как способ зовётся в настройках.
	Name() string
	// Title — как он называется для человека.
	Title() string
	// Ready — можно ли им пользоваться прямо сейчас (есть программа, есть ключ).
	// Вторым значением — почему нельзя, словами для человека.
	Ready() (bool, string)
	// Text — расшифровать файл.
	Text(ctx context.Context, path string) (string, error)
}

// Set — набор способов.
type Set struct {
	channels []Channel
	// off — способы, выведенные из работы: имя → почему.
	//
	// Выведенный способ остаётся в списке: человек видит его и причину, а уже
	// выбранный им способ не превращается в «такого нет». Но ни автоматический
	// выбор, ни выбор по имени его не берут — «выключено» значит выключено, а не «как
	// повезёт». Так местные модели уходят из диктовки, оставаясь функционалом:
	// сравнение движков и страница моделей работают ими по-прежнему.
	off map[string]string
}

// Off выводит способы из работы, называя причину словами для человека.
// Возвращает тот же набор, чтобы зваться цепочкой.
func (s *Set) Off(reason string, names ...string) *Set {
	if s.off == nil {
		s.off = make(map[string]string, len(names))
	}
	for _, name := range names {
		s.off[name] = reason
	}
	return s
}

// working — можно ли пользоваться способом сейчас: выключенные не годятся,
// остальные отвечают сами.
func (s *Set) working(one Channel) (bool, string) {
	if why, off := s.off[one.Name()]; off {
		return false, why
	}
	return one.Ready()
}

// Working — в работе ли способ с этим именем и почему нет. Тем же вопросом
// задаётся выбор, поэтому и ответ один и тот же: спросивший отдельно не получит
// другого мнения.
func (s *Set) Working(name string) (bool, string) {
	for _, one := range s.channels {
		if one.Name() == name {
			return s.working(one)
		}
	}
	return false, "способа «" + name + "» нет"
}

// All — все способы, какие знает программа.
//
// Порядок — порядок предпочтения: первым идёт то, что не отправляет запись
// наружу, потом сеть. Человек может выбрать иначе, но по умолчанию заметка
// должна оставаться на машине, если это возможно.
//
// Служба диктовки стоит сразу после локальной модели и до внешних программ:
// она поднимается вместе с машиной, модель у неё уже в памяти, и ответ приходит
// за доли секунды — а внешняя программа грузит свою модель заново на каждый
// вызов. Адрес у неё местный, и запись тоже никуда не уезжает.
//
// Groq — последним: имена он слышит вернее всех,
// но заметка при этом уходит на чужой сервер, и по умолчанию она должна
// оставаться на машине.
func All() *Set {
	return &Set{channels: []Channel{
		NewDictationService(),
		NewOpenAICompatible(Remote{}),
		NewGroq("", ""),
	}}
}

// NewSet — набор из заданных способов (для проверок).
func NewSet(channels ...Channel) *Set { return &Set{channels: channels} }

// Choose — выбрать способ по имени из настроек.
//
// Возвращает ещё и словесное объяснение выбора: его показывает окно, чтобы
// человек видел, чем именно расшифровывается, и почему не тем, что он выбрал.
func (s *Set) Choose(name string) (Channel, string) {
	name = strings.ToLower(strings.TrimSpace(name))

	if name != "" && name != "auto" {
		for _, one := range s.channels {
			if one.Name() != name {
				continue
			}
			if ready, why := s.working(one); !ready {
				// Выбранный способ не готов — берём любой рабочий, но говорим об
				// этом: иначе человек будет думать, что работает его выбор.
				if fallback := s.first(); fallback != nil {
					return fallback, fmt.Sprintf("%s не готов (%s) — расшифровываю через «%s»",
						one.Title(), why, fallback.Title())
				}
				return nil, fmt.Sprintf("%s не готов: %s", one.Title(), why)
			}
			return one, ""
		}
		if fallback := s.first(); fallback != nil {
			return fallback, fmt.Sprintf("способа «%s» нет — расшифровываю через «%s»",
				name, fallback.Title())
		}
		return nil, fmt.Sprintf("способа «%s» нет, и рабочих тоже", name)
	}

	if found := s.first(); found != nil {
		return found, ""
	}
	return nil, "расшифровывать нечем: ни локальной модели, ни ключа к сервису"
}

// first — первый готовый способ из тех, что в работе.
func (s *Set) first() Channel {
	for _, one := range s.channels {
		if ready, _ := s.working(one); ready {
			return one
		}
	}
	return nil
}

// Offer — чем описать способ в настройках.
type Offer struct {
	Name  string `json:"key"`
	Title string `json:"title"`
	Note  string `json:"note"`
}

// Offers — способы для окна настроек, с объяснением, что готово, а что нет.
func (s *Set) Offers() []Offer {
	found := []Offer{{
		Name: "auto", Title: "Выбрать самому",
		Note: "первый доступный из тех, что в работе",
	}}
	for _, one := range s.channels {
		ready, why := s.working(one)
		note := why
		if ready {
			note = "готов"
		}
		found = append(found, Offer{Name: one.Name(), Title: one.Title(), Note: note})
	}
	return found
}

// Names — имена способов по алфавиту (для проверок и подсказок).
func (s *Set) Names() []string {
	found := make([]string, 0, len(s.channels))
	for _, one := range s.channels {
		found = append(found, one.Name())
	}
	sort.Strings(found)
	return found
}

// Text — расшифровать выбранным способом.
func (s *Set) Text(ctx context.Context, name, path string) (Result, string, error) {
	channel, note := s.Choose(name)
	if channel == nil {
		return Result{}, note, fmt.Errorf("%s", note)
	}
	text, err := channel.Text(ctx, path)
	if err != nil {
		return Result{}, note, fmt.Errorf("%s: %w", channel.Title(), err)
	}
	if strings.TrimSpace(text) == "" {
		// Пустой текст — это не успех: в заметке должна остаться причина, иначе
		// человек увидит пустую строку и решит, что потерял запись.
		return Result{By: channel.Name()}, note, fmt.Errorf("%s не разобрал речь", channel.Title())
	}
	return Result{Text: strings.TrimSpace(text), By: channel.Name()}, note, nil
}
