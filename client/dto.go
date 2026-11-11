package main

import (
	"encoding/hex"

	"cryptochat/client/internal/delivery"

	"cryptochat/client/internal/ciphers"
	"cryptochat/client/internal/profile"
	"cryptochat/client/internal/service"
)

// Типы, которые получает интерфейс. Время — Unix мс, идентификатор чата — 32 hex-символа.
// Ключей, секретов, открытых значений и токена здесь нет.

type Settings struct {
	ServerAddress   string   `json:"serverAddress"`
	CertificatePath string   `json:"certificatePath"`
	RecentLogins    []string `json:"recentLogins"`
}

type SessionView struct {
	Login  string `json:"login"`
	UserID string `json:"userId"`
}

type ChatView struct {
	ID            string `json:"id"`
	Peer          string `json:"peer"`
	Creator       string `json:"creator"`
	Algorithm     string `json:"algorithm"`
	KeyBits       int    `json:"keyBits"`
	CipherMode    string `json:"cipherMode"`
	Padding       string `json:"padding"`
	CreatedAt     int64  `json:"createdAt"`
	SelfConnected bool   `json:"selfConnected"`
	PeerConnected bool   `json:"peerConnected"`
	KeyState      string `json:"keyState"`
	AlgorithmNote string `json:"algorithmNote"`
	Unread        int    `json:"unread"`
	ReceiveState  string `json:"receiveState"`
	ReceiveReason string `json:"receiveReason"`
	CanSend       bool   `json:"canSend"`
	SendBlock     string `json:"sendBlock"`
}

type Snapshot struct {
	Version    uint64       `json:"version"`
	Connection string       `json:"connection"`
	Session    *SessionView `json:"session"`
	Contacts   []string     `json:"contacts"`
	Incoming   []string     `json:"incoming"`
	Outgoing   []string     `json:"outgoing"`
	Chats      []ChatView   `json:"chats"`
}

type AlgorithmOption struct {
	ID         string `json:"id"`
	Available  bool   `json:"available"`
	Substitute bool   `json:"substitute"`
	Note       string `json:"note"`
}

type Option struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Warning string `json:"warning"`
}

type ChatOptions struct {
	Algorithms  []AlgorithmOption `json:"algorithms"`
	KeyBits     []int             `json:"keyBits"`
	CipherModes []Option          `json:"cipherModes"`
	Paddings    []Option          `json:"paddings"`
}

type CreateChatRequest struct {
	Peer       string `json:"peer"`
	Algorithm  string `json:"algorithm"`
	KeyBits    int    `json:"keyBits"`
	CipherMode string `json:"cipherMode"`
	Padding    string `json:"padding"`
}

type ContactResult struct {
	BecameContact bool `json:"becameContact"`
}

type Notice struct {
	Kind    string `json:"kind"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type SessionEnded struct {
	Code  string `json:"code"`
	Login string `json:"login"`
}

func settingsView(s profile.Settings) Settings {
	out := Settings{ServerAddress: s.ServerAddress, CertificatePath: s.CertificatePath, RecentLogins: s.RecentLogins}
	if out.RecentLogins == nil {
		out.RecentLogins = []string{}
	}
	return out
}

func settingsModel(s Settings) profile.Settings {
	return profile.Settings{ServerAddress: s.ServerAddress, CertificatePath: s.CertificatePath, RecentLogins: s.RecentLogins}
}

func chatView(c service.ChatInfo) ChatView {
	return ChatView{
		ID:            hex.EncodeToString(c.ID[:]),
		Peer:          c.Peer,
		Creator:       c.Creator,
		Algorithm:     c.Algorithm.String(),
		KeyBits:       c.KeyBits,
		CipherMode:    ciphers.ModeID(c.Mode),
		Padding:       ciphers.PaddingID(c.Padding),
		CreatedAt:     c.CreatedAt.UnixMilli(),
		SelfConnected: c.SelfConnected,
		PeerConnected: c.PeerConnected,
		KeyState:      string(c.KeyState),
		AlgorithmNote: c.AlgorithmNote,
		Unread:        c.Unread,
		ReceiveState:  c.ReceiveState,
		ReceiveReason: c.ReceiveReason,
		CanSend:       c.CanSend,
		SendBlock:     c.SendBlock,
	}
}

func snapshotView(s service.Snapshot) Snapshot {
	out := Snapshot{
		Version:    s.Version,
		Connection: s.Connection,
		Contacts:   nonNil(s.Contacts),
		Incoming:   nonNil(s.Incoming),
		Outgoing:   nonNil(s.Outgoing),
		Chats:      []ChatView{},
	}
	if s.Session != nil {
		out.Session = &SessionView{Login: s.Session.Login, UserID: s.Session.UserID}
	}
	for _, c := range s.Chats {
		out.Chats = append(out.Chats, chatView(c))
	}
	return out
}

func optionsView(o ciphers.ChatOptions) ChatOptions {
	out := ChatOptions{KeyBits: o.KeyBits}
	for _, a := range o.Algorithms {
		out.Algorithms = append(out.Algorithms, AlgorithmOption{ID: a.ID, Available: a.Info.Available,
			Substitute: a.Info.Substitute, Note: a.Info.Note})
	}
	for _, m := range o.CipherModes {
		out.CipherModes = append(out.CipherModes, Option(m))
	}
	for _, p := range o.Paddings {
		out.Paddings = append(out.Paddings, Option(p))
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// eventView переводит событие сервиса в тип для интерфейса.
func eventView(data any) any {
	switch v := data.(type) {
	case service.Snapshot:
		return snapshotView(v)
	case service.Notice:
		return Notice{Kind: v.Kind, Code: v.Code, Message: v.Message}
	case service.SessionEnded:
		return SessionEnded{Code: v.Code, Login: v.Login}
	case service.MessagesEvent:
		out := MessagesEvent{ChatID: hex.EncodeToString(v.ChatID[:]), Changed: []MessageView{}}
		for _, m := range v.Changed {
			out.Changed = append(out.Changed, messageView(m))
		}
		return out
	case service.ProgressEvent:
		return ProgressEvent{Seq: v.Seq, ChatID: hex.EncodeToString(v.ChatID[:]), Stage: string(v.Stage), Done: v.Done, Total: v.Total}
	}
	return nil
}

// Progress — сделано из всего; 0 из 0 — пустые данные (100%).
type Progress struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
}

type MessageError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// MessageView — сообщение для интерфейса. Шифротекста, IV, ключей и message_id здесь нет.
type MessageView struct {
	Seq         int64         `json:"seq"`
	ChatID      string        `json:"chatId"`
	Direction   string        `json:"direction"`
	Kind        string        `json:"kind"`
	CreatedAt   int64         `json:"createdAt"`
	State       string        `json:"state"`
	Text        string        `json:"text"`
	FileName    string        `json:"fileName"`
	Size        int64         `json:"size"`
	MediaURL    string        `json:"mediaUrl"`
	Error       *MessageError `json:"error"`
	Encrypt     *Progress     `json:"encrypt"`
	Send        *Progress     `json:"send"`
	Receive     *Progress     `json:"receive"`
	Decrypt     *Progress     `json:"decrypt"`
	Cancellable bool          `json:"cancellable"`
	Retryable   bool          `json:"retryable"`
}

type MessagePage struct {
	Messages []MessageView `json:"messages"`
	HasMore  bool          `json:"hasMore"`
}

type ClosedChatView struct {
	ID           string `json:"id"`
	Peer         string `json:"peer"`
	Algorithm    string `json:"algorithm"`
	KeyBits      int    `json:"keyBits"`
	CipherMode   string `json:"cipherMode"`
	Padding      string `json:"padding"`
	CreatedAt    int64  `json:"createdAt"`
	ClosedAt     int64  `json:"closedAt"`
	MessageCount int    `json:"messageCount"`
}

type MessagesEvent struct {
	ChatID  string        `json:"chatId"`
	Changed []MessageView `json:"changed"`
}

type ProgressEvent struct {
	Seq    int64  `json:"seq"`
	ChatID string `json:"chatId"`
	Stage  string `json:"stage"`
	Done   int64  `json:"done"`
	Total  int64  `json:"total"`
}

func progressView(p *delivery.ProgressValue) *Progress {
	if p == nil {
		return nil
	}
	return &Progress{Done: p.Done, Total: p.Total}
}

func messageView(m service.MessageView) MessageView {
	out := MessageView{Seq: m.Seq, ChatID: hex.EncodeToString(m.ChatID[:]), Direction: m.Direction, Kind: m.Kind,
		CreatedAt: m.CreatedAt, State: m.State, Text: m.Text, FileName: m.FileName, Size: m.Size, MediaURL: m.MediaURL,
		Encrypt: progressView(m.Encrypt), Send: progressView(m.Send), Receive: progressView(m.Receive),
		Decrypt: progressView(m.Decrypt), Cancellable: m.Cancellable, Retryable: m.Retryable}
	if m.Error != nil {
		out.Error = &MessageError{Code: m.Error.Code, Message: m.Error.Message}
	}
	return out
}

func messagePageView(p service.MessagePage) MessagePage {
	out := MessagePage{Messages: []MessageView{}, HasMore: p.HasMore}
	for _, m := range p.Messages {
		out.Messages = append(out.Messages, messageView(m))
	}
	return out
}

func closedChatsView(list []service.ClosedChatInfo) []ClosedChatView {
	out := []ClosedChatView{}
	for _, c := range list {
		out = append(out, ClosedChatView{ID: hex.EncodeToString(c.ID[:]), Peer: c.Peer, Algorithm: c.Algorithm,
			KeyBits: c.KeyBits, CipherMode: ciphers.ModeID(c.Mode), Padding: ciphers.PaddingID(c.Padding),
			CreatedAt: c.CreatedAt.UnixMilli(), ClosedAt: c.ClosedAt.UnixMilli(), MessageCount: c.MessageCount})
	}
	return out
}
