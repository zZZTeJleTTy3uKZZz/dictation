// Package state — общая тетрадь: воркер пишет, значок в лотке читает.
//
// Порт meetdigest_watcher/state.py, включая путь и формат файла: значок на Go
// и воркер на Python (или наоборот) должны понимать друг друга, пока идёт
// переход.
//
// ПОЧЕМУ ФАЙЛ, А НЕ КАНАЛ МЕЖДУ ПРОЦЕССАМИ. У воркера свой цикл, у окон свой;
// разделив их на процессы, мы получили независимость — значок можно закрыть и
// открыть заново, не трогая наблюдение. Файл — самый дешёвый способ передать
// между ними снимок состояния, и он же переживает перезапуск любого из двух.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/appdirs"
)

// FreshFor — сколько отметка воркера считается свежей.
//
// Втрое больше обычного круга обхода (30 с): один пропущенный круг из-за
// занятого диска не должен показывать человеку «наблюдение не запущено».
const FreshFor = 100 * time.Second

// State — снимок происходящего.
type State struct {
	// Working — воркер жив и обходит папки.
	Working bool `json:"working"`
	// Online — есть ли связь с сервисом.
	Online bool `json:"online"`
	// Recording — идёт ли запись и чего именно.
	Recording      bool    `json:"recording"`
	RecordingTitle string  `json:"recording_title"`
	RecordingSince float64 `json:"recording_since"`
	// SentCount — сколько записей ушло за всё время.
	SentCount int    `json:"sent_count"`
	LastError string `json:"last_error"`
	// Stamp — когда воркер отметился последний раз.
	Stamp float64 `json:"stamp"`
	// ReadyTitle и ReadyAt — последний готовый разбор: значок показывает это
	// облачком, иначе о готовности человек узнаёт, только когда сам полезет в
	// меню, а сервис к тому времени закончил полчаса назад.
	ReadyTitle string  `json:"ready_title"`
	ReadyAt    float64 `json:"ready_at"`
	// StopRequested — значок попросил остановить запись.
	StopRequested bool `json:"stop_requested"`
	// StartRequested — значок попросил начать запись того, что идёт сейчас, —
	// даже если про эту программу сказано «никогда»: запрет означает «не
	// предлагай сам», а не «не давай записать, когда я прошу».
	StartRequested bool `json:"start_requested"`
}

// Fresh — воркер отметился недавно.
func (s State) Fresh() bool {
	if s.Stamp == 0 {
		return false
	}
	return time.Since(time.Unix(int64(s.Stamp), 0)) < FreshFor
}

// LiveRecording — запись идёт и об этом известно точно.
func (s State) LiveRecording() bool { return s.Recording && s.Fresh() }

// Badge — каким показывать значок.
func (s State) Badge() string {
	switch {
	case s.LiveRecording():
		return "recording"
	case !s.Fresh():
		return "offline"
	case !s.Online:
		return "offline"
	default:
		return "idle"
	}
}

// Store — файл состояния.
type Store struct {
	path string
	mu   sync.Mutex
}

func DefaultPath() string {
	home := appdirs.HomeDir()
	if home == "" {
		return "state.json"
	}
	return filepath.Join(home, ".meetdigest", "state.json")
}

func NewStore(path string) *Store {
	if path == "" {
		path = DefaultPath()
	}
	return &Store{path: path}
}

// Read — прочитать состояние. Нет файла или он испорчен — пустое состояние,
// а не отказ: значок должен показать «наблюдение не запущено», а не упасть.
func (s *Store) Read() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

func (s *Store) read() State {
	var made State
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return made
	}
	if err := json.Unmarshal(raw, &made); err != nil {
		return State{}
	}
	return made
}

// Write — записать состояние целиком.
func (s *Store) Write(value State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(value)
}

func (s *Store) write(value State) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	// Пишем через временный файл и подменяем: значок читает этот файл каждую
	// секунду и не должен поймать его на середине записи.
	temp := s.path + ".tmp"
	if err := os.WriteFile(temp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(temp, s.path)
}

// Update — поправить часть состояния, не потеряв остальное.
//
// Отметку времени ставим сами: любая правка от воркера означает «я жив», и
// забыть об этом — значит однажды показать человеку мёртвый значок при
// работающем наблюдении.
func (s *Store) Update(change func(*State)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.read()
	change(&value)
	value.Stamp = float64(time.Now().Unix())
	return s.write(value)
}

// Ask — попросить воркер о чём-то из значка (остановить или начать запись).
func (s *Store) Ask(change func(*State)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.read()
	change(&value)
	// Здесь отметку НЕ обновляем: просьба приходит от значка, а свежесть
	// означает «воркер жив». Подделав её, значок показывал бы себя самого.
	return s.write(value)
}
