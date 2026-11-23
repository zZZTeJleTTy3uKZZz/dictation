// Значок лотка: микрофон в круге, цвет по состоянию.
//
// ПОЧЕМУ ФАЙЛЫ, А НЕ РИСОВАНИЕ НА ХОДУ. Раньше значок рисовался здесь кружком —
// и выглядел кружком: в лотке двадцать значков, и наш не читался вовсе. Теперь
// облик рисует cmd/icons (микрофон в круге, восемь размеров в одном .ico), а
// сюда он попадает готовым и вшивается в программу — чтобы значок не зависел от
// того, лежит ли рядом папка с картинками.
package main

import (
	_ "embed"
)

//go:embed icons/idle.ico
var iconIdle []byte

//go:embed icons/recording.ico
var iconRecording []byte

//go:embed icons/dictating.ico
var iconDictating []byte

//go:embed icons/offline.ico
var iconOffline []byte

//go:embed icons/danger.ico
var iconDanger []byte

// iconFor — значок для состояния.
func iconFor(badge string) []byte {
	switch badge {
	case "recording":
		return iconRecording
	case "dictating":
		return iconDictating
	case "offline":
		return iconOffline
	default:
		return iconIdle
	}
}

// redDot — красная метка для опасного пункта меню.
//
// Красного текста в системном меню Windows не бывает: его рисует сама Windows, и
// цвет строки ей не задать. Метка — единственный способ отличить «Выйти» от
// остальных пунктов, не рисуя своё меню (а своё — это ровно та самодельная
// отрисовка, из-за которой у Python-версии меню открывалось три секунды).
func redDot() []byte { return iconDanger }
