package proto

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"openmediad/internal/libs/wsclient"
)

// Двоичный протокол v2 (§5.1–5.2). Клиент отправляет кадр с заголовком 11 байт: [тип][флаги][кодек][время, мс: 8 байт LE].
const (
	msgAudioCoded = 4 // пакет аудиокодека: голосовая полоса участника
	codecOpus     = 4
	frameHeader   = 11
)

// FrameSamples — отсчётов в одном звуковом кадре: Opus, 48 кГц, моно, 20 мс (§5.5). Клиенты принимают только такие кадры.
const FrameSamples = 960

// ChatCipherPrefix — так начинается сообщение чата, зашифрованное сквозным шифрованием (§7.3).
const ChatCipherPrefix = "🔒e2e:"

// MaxChatText — предел длины сообщения чата в символах (§4.1).
const MaxChatText = 2000

const (
	joinTimeout  = 10 * time.Second
	writeTimeout = 5 * time.Second
	pingEvery    = 10 * time.Second
	readTimeout  = 35 * time.Second // три пропущенных ответа на ping — связь потеряна
	// maxMessage — предел текстового сообщения. join_ok несёт историю чата с картинками (до 24 штук, ≈0,5 МБ каждая);
	// сами картинки не разбираются, но сообщение приходит целиком.
	maxMessage = 32 << 20
)

// JoinError — сервер отказал во входе (или выгнал) с машинной причиной reason (§4.2).
type JoinError struct{ Reason string }

func (e *JoinError) Error() string { return "сервер MeetUp: " + ReasonText(e.Reason) }

// Fatal — причина окончательная: повторять вход бессмысленно, пока человек не поменяет ссылку или настройки.
func Fatal(reason string) bool {
	switch reason {
	case "room_not_found", "session_replaced", "room_closed", "alias_forbidden", "anonymous_forbidden", "wrong_password", "invalid_join":
		return true
	}
	return false
}

// ReasonText — причина отказа сервера словами.
func ReasonText(reason string) string {
	switch reason {
	case "room_not_found":
		return "комнаты нет (ссылка устарела или с ошибкой)"
	case "room_offline":
		return "личная комната не открыта: владелец ещё не вошёл"
	case "room_closed":
		return "владелец завершил конференцию"
	case "session_replaced":
		return "под этим аккаунтом вошли с другого устройства"
	case "wrong_password":
		return "пароль комнаты не подошёл или не задан: введите его в настройках колонки"
	case "alias_forbidden":
		return "ссылка-приглашение не пускает этого участника"
	case "anonymous_forbidden":
		return "сервер пускает только с аккаунтом: войдите в аккаунт в настройках колонки"
	case "invalid_join":
		return "сервер не принял имя или код комнаты"
	}
	return "отказ «" + reason + "»"
}

// Participant — участник комнаты.
type Participant struct {
	ID   uint32 `json:"id"`
	Name string `json:"name"`
	Mic  bool   `json:"mic"`
	Cam  bool   `json:"cam"`
	// UserID — аккаунт участника на сервере; 0 — вошёл без аккаунта (имя такого участника никем не подтверждено).
	UserID int64 `json:"user_id,omitempty"`
}

// Chat — сообщение чата, пришедшее во время конференции (история из join_ok сюда не попадает).
type Chat struct {
	SenderID uint32
	Sender   string
	// UserID — аккаунт отправителя на сервере; 0 — он вошёл без аккаунта, и его имя никем не подтверждено.
	UserID int64
	Text   string // текст; у сообщения, зашифрованного нашим ключом, — уже расшифрованный
	// Encrypted — сообщение зашифровано E2E, а прочесть его нечем (ключа нет или он не тот): Text недоступен.
	Encrypted bool
	Own       bool // это наше собственное сообщение (сервер возвращает его всем, включая отправителя)
}

// Handler — что сессия сообщает наружу. Функции зовутся из горутины чтения по очереди и должны возвращаться быстро.
type Handler struct {
	Chat   func(Chat)
	Roster func()              // список участников изменился
	Notice func(reason string) // сервер сообщил некритичную ошибку
}

// Options — параметры входа.
type Options struct {
	Name string      // имя участника (у входа без аккаунта)
	TLS  *tls.Config // для wss://
	// WSURL — адрес WebSocket вместо вычисленного из ссылки (сервер разработчика с нестандартными портами, тесты).
	WSURL string
	// Session — кука сессии аккаунта (Account.Token): соединение привязывается к аккаунту, имя берётся из профиля.
	// Слать её можно только серверу, который её выдал.
	Session string
	// Password — пароль личной комнаты или ссылки-приглашения ("" — без пароля).
	Password string
	// Cipher — сквозное шифрование: звук и чат уходят зашифрованными, шифрованный чат читается (nil — без шифрования).
	Cipher *Cipher
}

// Session — вход в одну комнату: от join_ok до разрыва соединения.
type Session struct {
	c      *wsclient.Conn
	id     uint32
	cipher *Cipher

	mu     sync.Mutex
	people []Participant
	title  string

	rttMS atomic.Int64 // последний замер «туда и обратно», мс; −1 — ещё не мерили
}

// serverMsg — поля сообщений сервера (§4.2), которые читает клиент. Картинки и история чата не разбираются.
type serverMsg struct {
	Type         string        `json:"type"`
	Reason       string        `json:"reason"`
	SenderID     uint32        `json:"sender_id"`
	SenderName   string        `json:"sender_name"`
	Text         string        `json:"text"`
	ID           uint32        `json:"id"`
	Name         string        `json:"name"`
	Mic          *bool         `json:"mic"`
	Cam          *bool         `json:"cam"`
	UserID       int64         `json:"user_id"`
	Participants []Participant `json:"participants"`
	RoomTitle    string        `json:"room_title"`
	T            int64         `json:"t"`
}

// Join открывает соединение и входит в комнату. Отказ сервера — *JoinError.
func Join(ctx context.Context, l Link, o Options) (*Session, error) {
	name := strings.TrimSpace(o.Name)
	if name == "" {
		return nil, errors.New("не задано имя участника")
	}
	if utf8.RuneCountInString(name) > 64 {
		name = string([]rune(name)[:64])
	}
	wsURL := o.WSURL
	if wsURL == "" {
		wsURL = l.WSURL()
	}
	dctx, cancel := context.WithTimeout(ctx, joinTimeout)
	defer cancel()
	wo := &wsclient.Options{TLS: o.TLS, WriteTimeout: writeTimeout, SkipBinary: true, MaxMessage: maxMessage}
	if o.Session != "" {
		wo.Header = http.Header{"Cookie": {sessionCookie + "=" + o.Session}}
	}
	c, err := wsclient.DialURL(dctx, wsURL, wo)
	if err != nil {
		return nil, fmt.Errorf("нет связи с сервером %s: %w", l.Host, err)
	}
	// Вход с выключенными микрофоном и камерой (§4.1): микрофон «включается», когда колонке есть что играть.
	msg := map[string]any{"type": "join", "room": l.Room, "name": name, "mic": false, "cam": false}
	if o.Password != "" {
		msg["password"] = o.Password
	}
	join, _ := json.Marshal(msg)
	if err := c.WriteText(string(join)); err != nil {
		c.Close()
		return nil, fmt.Errorf("вход в комнату: %w", err)
	}
	c.SetReadDeadline(time.Now().Add(joinTimeout))
	for {
		_, raw, err := c.Read()
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("сервер не ответил на вход: %w", err)
		}
		var m serverMsg
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		switch m.Type {
		case "join_ok":
			s := &Session{c: c, id: m.SenderID, cipher: o.Cipher, people: m.Participants, title: m.RoomTitle}
			s.rttMS.Store(-1)
			return s, nil
		case "error":
			c.Close()
			return nil, &JoinError{Reason: m.Reason}
		}
	}
}

// ID — наш идентификатор участника в комнате.
func (s *Session) ID() uint32 { return s.id }

// Account — сервер привязал соединение к аккаунту (кука сессии принята). false при заданной Options.Session значит, что
// сессия истекла: сервер пустил нас как участника без аккаунта.
func (s *Session) Account() bool { return s.id >= accountIDBase }

// Title — название личной комнаты ("" — разовая комната).
func (s *Session) Title() string { s.mu.Lock(); defer s.mu.Unlock(); return s.title }

// Participants — остальные участники комнаты (копия).
func (s *Session) Participants() []Participant {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Participant(nil), s.people...)
}

// RTT — время «туда и обратно» до сервера по последнему ping; ok=false — ещё не измерено.
func (s *Session) RTT() (time.Duration, bool) {
	ms := s.rttMS.Load()
	return time.Duration(ms) * time.Millisecond, ms >= 0
}

// Skipped — сколько двоичных кадров (звук и видео остальных участников) и байт прочитано и выброшено.
func (s *Session) Skipped() (frames, bytes uint64) { return s.c.Skipped() }

// Run читает сообщения сервера, пока соединение живо, и раз в 10 с проверяет связь. Возвращает причину конца: ошибку сети
// или *JoinError, если сервер выгнал (session_replaced, room_closed).
func (s *Session) Run(h Handler) error {
	stop := make(chan struct{})
	defer close(stop)
	go s.keepalive(stop)
	for {
		s.c.SetReadDeadline(time.Now().Add(readTimeout))
		_, raw, err := s.c.Read()
		if err != nil {
			return err
		}
		var m serverMsg
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		switch m.Type {
		case "chat":
			if h.Chat != nil {
				msg := Chat{SenderID: m.SenderID, Sender: m.SenderName, UserID: UserIDOf(m.SenderID), Text: m.Text, Own: m.SenderID == s.id}
				if strings.HasPrefix(m.Text, ChatCipherPrefix) {
					msg.Text, msg.Encrypted = "", true
					if s.cipher != nil {
						if plain, ok := s.cipher.OpenChat(m.Text); ok {
							msg.Text, msg.Encrypted = plain, false
						}
					}
				}
				h.Chat(msg)
			}
		case "participant_joined":
			s.mu.Lock()
			s.people = append(removeID(s.people, m.ID), Participant{ID: m.ID, Name: m.Name, Mic: m.Mic != nil && *m.Mic, Cam: m.Cam != nil && *m.Cam, UserID: m.UserID})
			s.mu.Unlock()
			s.roster(h)
		case "participant_left":
			s.mu.Lock()
			s.people = removeID(s.people, m.ID)
			s.mu.Unlock()
			s.roster(h)
		case "participant_state":
			s.mu.Lock()
			for i := range s.people {
				if s.people[i].ID != m.ID {
					continue
				}
				if m.Mic != nil {
					s.people[i].Mic = *m.Mic
				}
				if m.Cam != nil {
					s.people[i].Cam = *m.Cam
				}
			}
			s.mu.Unlock()
			s.roster(h)
		case "pong":
			if d := time.Now().UnixMilli() - m.T; d >= 0 && d < int64(readTimeout/time.Millisecond) {
				s.rttMS.Store(d)
			}
		case "error":
			if Fatal(m.Reason) {
				return &JoinError{Reason: m.Reason}
			}
			if h.Notice != nil {
				h.Notice(m.Reason)
			}
		}
	}
}

func (s *Session) roster(h Handler) {
	if h.Roster != nil {
		h.Roster()
	}
}

func removeID(list []Participant, id uint32) []Participant {
	out := list[:0]
	for _, p := range list {
		if p.ID != id {
			out = append(out, p)
		}
	}
	return out
}

// keepalive шлёт прикладной ping (§4.1): по ответу меряется задержка, а без ответов чтение обрывается по сроку.
func (s *Session) keepalive(stop <-chan struct{}) {
	t := time.NewTicker(pingEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if s.c.WriteText(fmt.Sprintf(`{"type":"ping","t":%d}`, time.Now().UnixMilli())) != nil {
				return
			}
		}
	}
}

// maxSealedChat — сколько байт текста помещается в зашифрованное сообщение чата: предел сервера считается по шифровке
// (префикс, IV и метка, base64), а она длиннее текста на треть.
const maxSealedChat = 1400

// SendChat пишет в чат комнаты (с шифром — зашифрованным). Слишком длинный текст обрезается до предела сервера.
func (s *Session) SendChat(text string) error {
	if utf8.RuneCountInString(text) > MaxChatText {
		text = string([]rune(text)[:MaxChatText-1]) + "…"
	}
	if s.cipher != nil {
		for len(text) > maxSealedChat { // по целым символам
			_, size := utf8.DecodeLastRuneInString(text)
			text = text[:len(text)-size]
		}
		text = s.cipher.SealChat(text)
	}
	b, _ := json.Marshal(map[string]string{"type": "chat", "text": text})
	return s.c.WriteText(string(b))
}

// SetMic сообщает комнате, включён ли наш «микрофон» (идёт ли от нас звук).
func (s *Session) SetMic(on bool) error {
	return s.c.WriteText(fmt.Sprintf(`{"type":"state","mic":%t}`, on))
}

// AudioFrame собирает двоичный кадр с одним пакетом Opus (48 кГц, моно, 20 мс) в buf и возвращает его. at — время кадра по
// стенным часам (§5.3: одна шкала Date.now() для всех кадров отправителя). С шифром данные кадра шифруются, заголовок
// остаётся открытым (§7.2).
func AudioFrame(buf, opus []byte, at time.Time, c *Cipher) []byte {
	flags := byte(0)
	if c != nil {
		flags, opus = flagEncrypted, c.Seal(msgAudioCoded, codecOpus, opus)
	}
	buf = append(buf[:0], msgAudioCoded, flags, codecOpus)
	buf = binary.LittleEndian.AppendUint64(buf, uint64(at.UnixMilli()))
	return append(buf, opus...)
}

// SendAudio отправляет один пакет Opus в голосовую полосу. Блокируется не дольше срока записи; ошибка — соединение потеряно.
func (s *Session) SendAudio(opus []byte, at time.Time) error {
	return s.c.WriteBinary(AudioFrame(make([]byte, 0, frameHeader+ivSize+len(opus)+tagSize), opus, at, s.cipher))
}

// Close закрывает соединение: Run вернётся с ошибкой.
func (s *Session) Close() { s.c.Close() }
