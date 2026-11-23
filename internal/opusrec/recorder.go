// Package opusrec пишет диктовку сразу в Ogg/Opus: кодирование внутри процесса,
// страница и сброс на диск раз в секунду.
//
// ЧТО ЗДЕСЬ РЕШАЕТСЯ. Прежняя запись клала звук в WAV, а сжимала его потом,
// отдельным заходом: час диктовки лежал на диске стодвадцатимегабайтным файлом, а
// сжатие ждало конца разговора. Здесь сжатие идёт по ходу, и на диске сразу
// появляется то, что останется: сорок килобит в секунду, около восемнадцати
// мегабайт в час.
//
// ЧТО ЗДЕСЬ НЕ РЕШАЕТСЯ. Пакет не знает ни микрофона, ни истории, ни
// распознавания: он получает куски звука и отдаёт файл. Поэтому его проверки
// гоняются без Windows и без признания, а распознаватель к записи отношения не
// имеет.
package opusrec

import (
	"fmt"
	"os"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/oggopus"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/opusenc"
)

const (
	// Vendor — имя, которое уходит в OpusTags: по нему видно, чем сделан файл.
	Vendor = "meetdigest dictation"
	// frameSamples48k — сколько отсчётов 48 кГц в одном кадре 20 мс. В этих
	// отсчётах Ogg считает позиции страниц.
	frameSamples48k = 960
	// framesPerPage — кадров на страницу: пятьдесят по 20 мс — ровно секунда.
	// Столько звука теряется при падении питания, и это же — цена одного
	// сброса на диск, поэтому больше не надо.
	framesPerPage = 50
)

// Recorder — одна запись в один файл. Писать в него можно только из одной
// нити: так его и зовёт захват микрофона.
type Recorder struct {
	file *os.File
	path string
	enc  *opusenc.Encoder
	ogg  *oggopus.Writer

	// leftover — неполный кадр: звук приходит кусками по сколько дал драйвер, а
	// кодировщику нужен ровно кадр.
	leftover []byte
	pcm      []byte
	packet   []byte
	frames   int
}

// Start открывает запись: файл, кодировщик и заголовки Ogg пишутся сразу, чтобы
// даже пустой файл был узнаваем как Opus.
func Start(path string) (*Recorder, error) {
	encoder, err := opusenc.NewEncoder()
	if err != nil {
		return nil, err
	}
	file, err := os.Create(path)
	if err != nil {
		encoder.Close()
		return nil, err
	}
	writer, err := oggopus.NewWriter(file, uint16(encoder.Lookahead()), frameSamples48k, Vendor)
	if err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		encoder.Close()
		return nil, err
	}
	return &Recorder{
		file: file,
		path: path,
		enc:  encoder,
		ogg:  writer,
		pcm:  make([]byte, opusenc.FrameSamples*2),
	}, nil
}

// Path — файл, в который идёт запись.
func (r *Recorder) Path() string { return r.path }

// Write принимает звук с микрофона (16 кГц, моно, S16LE) и укладывает его
// кадрами в Ogg. Раз в секунду страница закрывается и файл сбрасывается на
// диск: с этого места запись переживает и вылет процесса, и падение питания, и
// читается обычным проигрывателем.
func (r *Recorder) Write(pcm []byte) error {
	if r.file == nil {
		return fmt.Errorf("opusrec: запись закрыта")
	}
	r.leftover = append(r.leftover, pcm...)
	frame := len(r.pcm)
	for len(r.leftover) >= frame {
		copy(r.pcm, r.leftover[:frame])
		r.leftover = r.leftover[frame:]
		if err := r.encodeFrame(); err != nil {
			return err
		}
	}
	return nil
}

// encodeFrame сжимает один кадр и, если кадров набралось на секунду, закрывает
// страницу и сбрасывает файл.
func (r *Recorder) encodeFrame() error {
	packet, err := r.enc.Encode(r.pcm)
	if err != nil {
		return err
	}
	if err = r.ogg.Write(packet); err != nil {
		return err
	}
	r.frames++
	if r.frames%framesPerPage == 0 {
		return r.sync()
	}
	return nil
}

// Flush закрывает текущую страницу и сбрасывает файл. Зовётся на остановке
// записи: последние кадры не должны ждать следующей страницы.
func (r *Recorder) Flush() error {
	if r.file == nil {
		return nil
	}
	return r.sync()
}

func (r *Recorder) sync() error {
	if err := r.ogg.Flush(); err != nil {
		return err
	}
	// Sync на Windows — это FlushFileBuffers: без него страница лежит в кеше
	// системы, и выключение питания её не сохранит.
	return r.file.Sync()
}

// Close дописывает хвост, закрывает поток пометкой о конце и отдаёт путь
// записи. Неполный кадр дополняется тишиной: потерять при остановке можно не
// больше двадцати миллисекунд, иначе последний слог пропал бы целиком.
func (r *Recorder) Close() string {
	if r.file == nil {
		return ""
	}
	if len(r.leftover) > 0 {
		clear(r.pcm)
		copy(r.pcm, r.leftover)
		r.leftover = nil
		_ = r.encodeFrame()
	}
	_ = r.ogg.Close()
	_ = r.file.Sync()
	path := r.path
	_ = r.file.Close()
	r.enc.Close()
	r.file = nil
	return path
}

// Abort закрывает файл, не дописывая пометку о конце: так выглядит запись,
// которую оборвали. Нужен проверкам и отмене, когда файл не нужен.
func (r *Recorder) Abort() {
	if r.file == nil {
		return
	}
	_ = r.file.Close()
	r.enc.Close()
	r.file = nil
}

// Seconds — сколько звука принято с начала записи. Считается по кадрам, а не по
// файлу: длительность не зависит от того, успела ли закрыться последняя
// страница.
func (r *Recorder) Seconds() float64 {
	return float64(r.frames) * float64(opusenc.FrameMillis) / 1000
}
