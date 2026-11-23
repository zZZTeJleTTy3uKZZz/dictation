// Рисовалка значков: один облик для лотка, окон и установщика.
//
// ЗАЧЕМ ПРОГРАММОЙ, А НЕ ФАЙЛАМИ. Значок нужен в пяти размерах и трёх местах
// (лоток, окно, ярлык), и каждый раз в новом состоянии — покой, запись,
// диктовка. Дюжина .ico в репозитории превратилась бы в дюжину файлов, которые
// никто не сможет поправить, не открыв редактор. Здесь облик задан кодом:
// поменял форму — пересобрал все.
//
//	go run ./cmd/icons <куда>
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// Облик: микрофон. Не «капля», не «буква М» — предмет, который узнаётся с
// шестнадцати точек и говорит, чем программа занята.
type tone struct {
	ink   color.RGBA
	round color.RGBA
}

var looks = map[string]tone{
	// Покой: спокойный синий, тот же, что в окнах.
	"idle": {ink: color.RGBA{0xff, 0xff, 0xff, 0xff}, round: color.RGBA{0x2f, 0x6d, 0xf6, 0xff}},
	// Идёт запись встречи: красный, как у всех, кто пишет.
	"recording": {ink: color.RGBA{0xff, 0xff, 0xff, 0xff}, round: color.RGBA{0xd7, 0x26, 0x3d, 0xff}},
	// Диктовка: зелёный — человек говорит сам, и это другое состояние.
	"dictating": {ink: color.RGBA{0xff, 0xff, 0xff, 0xff}, round: color.RGBA{0x1f, 0xa4, 0x6b, 0xff}},
	// Связи нет: серый, чтобы не пугать красным попусту.
	"offline": {ink: color.RGBA{0xff, 0xff, 0xff, 0xff}, round: color.RGBA{0x8a, 0x8a, 0x96, 0xff}},
	// Метка опасного пункта меню: сплошной красный кружок без микрофона.
	"danger": {ink: color.RGBA{0xd7, 0x26, 0x3d, 0xff}, round: color.RGBA{0xd7, 0x26, 0x3d, 0xff}},
}

// plainNames — облики, которые рисуются кружком без микрофона.
var plainNames = map[string]bool{"danger": true}

// sizes — размеры внутри .ico. Windows берёт подходящий сам: 16 для лотка,
// 32 для заголовка окна, 256 для крупных плиток проводника.
var sizes = []int{16, 20, 24, 32, 48, 64, 128, 256}

func main() {
	folder := "assets"
	if len(os.Args) > 1 {
		folder = os.Args[1]
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	for name := range looks {
		if err := writeICO(filepath.Join(folder, name+".ico"), name); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		// PNG нужен Flutter: он рисует свои окна сам и .ico не понимает.
		if err := writePNG(filepath.Join(folder, name+".png"), name, 512); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("нарисован", name)
	}
}

// draw — микрофон в круге.
func draw(size int, look tone, lookName string) image.Image {
	sheet := image.NewRGBA(image.Rect(0, 0, size, size))
	side := float64(size)
	middle := (side - 1) / 2

	// Круг-подложка. Мягкий край считается дробно: без этого круг в 16 точек
	// выглядит шестиугольником.
	radius := side/2 - side*0.02
	for y := range size {
		for x := range size {
			away := math.Hypot(float64(x)-middle, float64(y)-middle)
			if cover := clamp(radius - away + 0.5); cover > 0 {
				put(sheet, x, y, look.round, cover)
			}
		}
	}

	if plainNames[lookName] {
		return sheet
	}

	// Капсула микрофона.
	capW, capH := side*0.22, side*0.36
	capX, capY := middle, side*0.38
	capR := capW / 2
	for y := range size {
		for x := range size {
			dx := math.Abs(float64(x) - capX)
			dy := float64(y) - capY
			var away float64
			switch {
			case dy < -capH/2+capR:
				away = math.Hypot(dx, dy+capH/2-capR) - capR
			case dy > capH/2-capR:
				away = math.Hypot(dx, dy-capH/2+capR) - capR
			default:
				away = dx - capR
			}
			if cover := clamp(-away + 0.5); cover > 0 {
				put(sheet, x, y, look.ink, cover)
			}
		}
	}

	// Дуга-держатель и ножка.
	arcR := side * 0.2
	thick := side * 0.055
	for y := range size {
		for x := range size {
			dx := float64(x) - middle
			dy := float64(y) - capY
			if dy > 0 {
				away := math.Abs(math.Hypot(dx, dy)-arcR) - thick/2
				if cover := clamp(-away + 0.5); cover > 0 {
					put(sheet, x, y, look.ink, cover)
				}
			}
			// Ножка.
			if float64(y) > capY+arcR-thick/2 && float64(y) < capY+arcR+side*0.1 &&
				math.Abs(dx) < thick/2 {
				put(sheet, x, y, look.ink, 1)
			}
		}
	}
	return sheet
}

func put(sheet *image.RGBA, x, y int, tint color.RGBA, cover float64) {
	under := sheet.RGBAAt(x, y)
	alpha := cover * float64(tint.A) / 255
	mix := func(over, was uint8) uint8 {
		return uint8(float64(over)*alpha + float64(was)*(1-alpha))
	}
	sheet.SetRGBA(x, y, color.RGBA{
		R: mix(tint.R, under.R),
		G: mix(tint.G, under.G),
		B: mix(tint.B, under.B),
		A: uint8(math.Min(255, float64(under.A)+alpha*255)),
	})
}

func clamp(value float64) float64 { return math.Max(0, math.Min(1, value)) }

func writePNG(path, name string, size int) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return png.Encode(file, draw(size, looks[name], name))
}

// writeICO — собрать .ico из нескольких размеров.
//
// Внутрь .ico с Vista кладут обычный PNG — так и делаем, иначе пришлось бы
// писать древний формат DIB с маской прозрачности.
func writeICO(path, name string) error {
	type entry struct {
		size int
		body []byte
	}
	parts := make([]entry, 0, len(sizes))
	for _, size := range sizes {
		body := &bytes.Buffer{}
		if err := png.Encode(body, draw(size, looks[name], name)); err != nil {
			return err
		}
		parts = append(parts, entry{size: size, body: body.Bytes()})
	}

	out := &bytes.Buffer{}
	_ = binary.Write(out, binary.LittleEndian, []uint16{0, 1, uint16(len(parts))})
	offset := 6 + 16*len(parts)
	for _, part := range parts {
		out.WriteByte(byte(part.size % 256)) // 0 значит 256
		out.WriteByte(byte(part.size % 256))
		out.WriteByte(0)                                       // палитра
		out.WriteByte(0)                                       // запас
		_ = binary.Write(out, binary.LittleEndian, uint16(1))  // плоскости
		_ = binary.Write(out, binary.LittleEndian, uint16(32)) // бит на точку
		_ = binary.Write(out, binary.LittleEndian, uint32(len(part.body)))
		_ = binary.Write(out, binary.LittleEndian, uint32(offset))
		offset += len(part.body)
	}
	for _, part := range parts {
		out.Write(part.body)
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}
