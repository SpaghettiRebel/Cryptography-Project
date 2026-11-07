// Пакет keyagree выполняет сторону клиента в протоколе Диффи-Хеллмана по шагам: секрет сохраняется до
// публикации открытого значения, после выработки ключа секрет удаляется в той же транзакции.
// Ключ, секрет и общий секрет не выходят из пакета, кроме записи в хранилище.
package keyagree

import (
	"context"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/localdb"
	"cryptochat/client/internal/rpcclient"
	"cryptochat/core/dh"
	"cryptochat/core/kdf"
)

// Store — хранилище секретов и ключей профиля.
type Store interface {
	DHSecret(id [16]byte) (localdb.Secret, bool, error)
	SaveDHSecret(id [16]byte, private, public []byte) error
	MarkPublished(id [16]byte) error
	SaveSessionKeyAndDropSecret(id [16]byte, key []byte) error
	SessionKeyExists(id [16]byte) (bool, error)
	DeleteChatSecrets(id [16]byte) error
}

// LocalState — состояние ключа по локальной базе, без обращения к серверу.
func LocalState(st Store, id [16]byte) State {
	if ok, err := st.SessionKeyExists(id); err == nil && ok {
		return Ready
	}
	sec, ok, err := st.DHSecret(id)
	switch {
	case err != nil || !ok:
		return None
	case sec.Published:
		return WaitingPeer
	default:
		return WaitingSelf
	}
}

// API — вызовы обмена открытыми значениями.
type API interface {
	PublishPublicValue(ctx context.Context, id [16]byte, value []byte) error
	GetPeerPublicValue(ctx context.Context, id [16]byte) ([]byte, bool, error)
}

// Advance делает следующий возможный шаг согласования ключа чата и возвращает состояние.
// Строка чата уже должна быть в хранилище: секрет ссылается на неё.
//
//   - ключ есть — READY без обращения к серверу;
//   - нет ни секрета, ни ключа, а сервер помнит своё значение — KEY_LOST, другое значение не публикуется;
//   - нет секрета и участник не подключён — NONE, ничего не создаётся;
//   - нет секрета и участник подключён — новая пара записывается в хранилище до публикации;
//   - секрет не опубликован — публикуется то же значение (повтор после обрыва);
//   - секрет опубликован — запрашивается значение собеседника; при его появлении ключ записывается,
//     а секрет удаляется в одной транзакции.
//
// Чат закрыт или сервер его не знает — секрет и ключ удаляются, возвращается ошибка CHAT_CLOSED или
// CHAT_NOT_FOUND.
func Advance(ctx context.Context, api API, st Store, chat rpcclient.Chat) (State, error) {
	id := chat.ID
	if chat.Closed() {
		return None, drop(st, id, apperr.New(apperr.ChatClosed))
	}
	if ok, err := st.SessionKeyExists(id); err != nil {
		return None, storageErr(err)
	} else if ok {
		return Ready, nil
	}
	sec, ok, err := st.DHSecret(id)
	if err != nil {
		return None, storageErr(err)
	}
	group := dh.Group14()
	if !ok {
		switch {
		case chat.Self.Published:
			return KeyLost, nil
		case !chat.Self.Connected:
			return None, nil
		}
		pair, err := group.GenerateKey()
		if err != nil {
			return None, apperr.New(apperr.Internal)
		}
		sec = localdb.Secret{Private: pair.PrivateBytes(), Public: pair.PublicBytes()}
		if err := st.SaveDHSecret(id, sec.Private, sec.Public); err != nil {
			return None, storageErr(err)
		}
	}
	if !sec.Published {
		if !chat.Self.Connected {
			return WaitingSelf, nil
		}
		err := api.PublishPublicValue(ctx, id, sec.Public)
		switch {
		case apperr.Is(err, apperr.KeyLost):
			// На сервере другое значение этого участника: ключ на устройстве получить нельзя.
			return KeyLost, nil
		case apperr.Is(err, apperr.ChatClosed, apperr.ChatNotFound):
			return None, drop(st, id, err)
		case err != nil:
			return WaitingSelf, err
		}
		if err := st.MarkPublished(id); err != nil {
			return WaitingSelf, storageErr(err)
		}
	}
	raw, published, err := api.GetPeerPublicValue(ctx, id)
	switch {
	case apperr.Is(err, apperr.ChatClosed, apperr.ChatNotFound):
		return None, drop(st, id, err)
	case err != nil:
		return WaitingPeer, err
	case !published:
		return WaitingPeer, nil
	}
	peer, err := group.ParsePublic(raw)
	if err != nil {
		return PeerValueInvalid, apperr.New(apperr.PeerValueInvalid)
	}
	pair, err := group.KeyPairFromPrivate(sec.Private)
	if err != nil {
		return None, apperr.New(apperr.Internal)
	}
	shared, err := pair.SharedSecret(peer)
	if err != nil {
		return PeerValueInvalid, apperr.New(apperr.PeerValueInvalid)
	}
	// В выводе ключа участвует алгоритм чата, а не тестовая подстановка: ключи сборок совпадают.
	key, err := kdf.SessionKey(shared, id, chat.Params.Algorithm, chat.Params.KeyBits)
	clear(shared)
	if err != nil {
		return None, apperr.New(apperr.Internal)
	}
	err = st.SaveSessionKeyAndDropSecret(id, key)
	clear(key)
	if err != nil {
		return WaitingPeer, storageErr(err)
	}
	return Ready, nil
}

func drop(st Store, id [16]byte, cause error) error {
	if err := st.DeleteChatSecrets(id); err != nil {
		return storageErr(err)
	}
	return cause
}

// storageErr скрывает подробности ошибки хранилища: в них не должно быть данных чата. Код
// PROFILE_STORAGE позволяет сервису сообщить пользователю о сбое записи.
func storageErr(error) error { return apperr.New(apperr.ProfileStorage) }
