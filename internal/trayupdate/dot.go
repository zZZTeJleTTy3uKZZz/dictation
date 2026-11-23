package trayupdate

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
)

// dotColor — синяя точка «есть обновление»: нейтральный цвет, не красный, как у
// кнопки «Выйти» (та означает опасность, а обновление — нет).
var dotColor = color.RGBA{R: 0x1E, G: 0x88, B: 0xE5, A: 0xFF}

// WithDot — копия значка с точкой в правом нижнем углу.
//
// ПОЧЕМУ НЕ ОТДЕЛЬНЫЙ ФАЙЛ. Вторая картинка значка — ещё один ассет, который
// надо держать в паре с первым. Точку рисуем из готового значка: каждый размер
// в ICO получает свою, пропорциональную, и облик состояний остаётся одним.
//
// ПОЧЕМУ БЕЛАЯ КАЙМА. Синяя точка на синем или сером фоне в лотке пропадает;
// белое кольцо вокруг неё держит её видимой на любом фоне панели задач.
func WithDot(ico []byte) ([]byte, error) {
	if len(ico) < 6 || ico[0] != 0 || ico[1] != 0 || ico[2] != 1 || ico[3] != 0 {
		return nil, errors.New("значок не .ico")
	}
	count := int(binary.LittleEndian.Uint16(ico[4:6]))
	if count == 0 || len(ico) < 6+16*count {
		return nil, errors.New("значок обрезан: нет записей каталога")
	}

	type picture struct {
		dir     []byte
		payload []byte
	}
	pictures := make([]picture, 0, count)
	for index := 0; index < count; index++ {
		dir := ico[6+16*index : 6+16*(index+1)]
		size := uint64(binary.LittleEndian.Uint32(dir[8:12]))
		offset := uint64(binary.LittleEndian.Uint32(dir[12:16]))
		if offset+size > uint64(len(ico)) {
			return nil, errors.New("запись значка за концом файла")
		}
		decoded, err := png.Decode(bytes.NewReader(ico[offset : offset+size]))
		if err != nil {
			return nil, fmt.Errorf("картинка значка не PNG: %w", err)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, withDot(decoded)); err != nil {
			return nil, fmt.Errorf("перекодировать значок: %w", err)
		}
		pictures = append(pictures, picture{dir: dir, payload: buf.Bytes()})
	}

	var out bytes.Buffer
	out.Write([]byte{0, 0, 1, 0})
	_ = binary.Write(&out, binary.LittleEndian, uint16(count))
	offset := uint32(6 + 16*count)
	for _, made := range pictures {
		entry := make([]byte, 16)
		copy(entry, made.dir[:4]) // ширина, высота, цвета, резерв
		binary.LittleEndian.PutUint16(entry[4:6], binary.LittleEndian.Uint16(made.dir[4:6]))
		binary.LittleEndian.PutUint16(entry[6:8], binary.LittleEndian.Uint16(made.dir[6:8]))
		binary.LittleEndian.PutUint32(entry[8:12], uint32(len(made.payload)))
		binary.LittleEndian.PutUint32(entry[12:16], offset)
		out.Write(entry)
		offset += uint32(len(made.payload))
	}
	for _, made := range pictures {
		out.Write(made.payload)
	}
	return out.Bytes(), nil
}

// withDot рисует точку на копии картинки; исходная не меняется.
func withDot(src image.Image) *image.RGBA {
	bounds := src.Bounds()
	dst := image.NewRGBA(bounds)
	draw.Draw(dst, bounds, src, bounds.Min, draw.Src)

	// Радиус — пятая часть стороны, но не меньше двух пикселей: на самых мелких
	// размерах точка меньше двух клеток уже не читается.
	radius := bounds.Dx() / 5
	if radius < 2 {
		radius = 2
	}
	centerX := bounds.Max.X - radius - 1
	centerY := bounds.Max.Y - radius - 1

	white := color.RGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	for y := centerY - radius - 1; y <= centerY+radius+1; y++ {
		for x := centerX - radius - 1; x <= centerX+radius+1; x++ {
			point := image.Point{X: x, Y: y}
			if !point.In(bounds) {
				continue
			}
			dx, dy := x-centerX, y-centerY
			distance := dx*dx + dy*dy
			switch {
			case distance <= radius*radius:
				dst.SetRGBA(x, y, dotColor)
			case distance <= (radius+1)*(radius+1):
				dst.SetRGBA(x, y, white)
			}
		}
	}
	return dst
}
