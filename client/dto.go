package main

import (
	"encoding/hex"

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
	}
	return nil
}
