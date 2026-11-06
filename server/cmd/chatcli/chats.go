package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"google.golang.org/grpc"

	apiv1 "cryptochat/server/api/v1"
)

const chatsUsage = "chat-create, chats, chat-info, chat-connect, chat-disconnect, chat-close, key-publish, key-get, send, recv"

// runChat выполняет команды чатов и возвращает false, если команда не относится к ним.
func runChat(conn *grpc.ClientConn, authed func() (context.Context, error), command string, args []string, stdin io.Reader, stdout io.Writer) (bool, error) {
	chats := apiv1.NewChatsClient(conn)
	keys := apiv1.NewKeyExchangeClient(conn)

	withID := func(call func(context.Context, *apiv1.ChatRef) error) (bool, error) {
		if len(args) != 1 {
			return true, fmt.Errorf("команде %s нужен идентификатор чата", command)
		}
		id, err := hex.DecodeString(args[0])
		if err != nil {
			return true, errors.New("идентификатор чата должен быть шестнадцатеричным")
		}
		c, err := authed()
		if err != nil {
			return true, err
		}
		return true, call(c, &apiv1.ChatRef{ChatId: id})
	}
	show := func(r *apiv1.ChatResponse, err error) error {
		if err != nil {
			return err
		}
		printChat(stdout, r.GetChat())
		return nil
	}

	switch command {
	case "chat-create":
		if len(args) != 5 {
			return true, errors.New("chat-create <логин> <алгоритм> <биты> <режим> <набивка>")
		}
		bits, err := strconv.ParseUint(args[2], 10, 32)
		if err != nil {
			bits = 0 // неверное значение проверяет сервер
		}
		c, err := authed()
		if err != nil {
			return true, err
		}
		r, err := chats.CreateChat(c, &apiv1.CreateChatRequest{PeerLogin: args[0], Params: &apiv1.ChatParams{
			Algorithm:   apiv1.Algorithm(apiv1.Algorithm_value["ALGORITHM_"+args[1]]),
			KeyBits:     uint32(bits),
			CipherMode:  apiv1.CipherMode(apiv1.CipherMode_value["CIPHER_MODE_"+args[3]]),
			PaddingMode: apiv1.PaddingMode(apiv1.PaddingMode_value["PADDING_MODE_"+args[4]]),
		}})
		if err != nil {
			return true, err
		}
		// Только идентификатор: вывод подставляется в следующие команды.
		fmt.Fprintln(stdout, hex.EncodeToString(r.GetChat().GetChatId()))
		return true, nil
	case "chats":
		c, err := authed()
		if err != nil {
			return true, err
		}
		r, err := chats.ListChats(c, &apiv1.ListChatsRequest{})
		if err != nil {
			return true, err
		}
		for _, chat := range r.GetChats() {
			printChat(stdout, chat)
		}
		return true, nil
	case "chat-info":
		return withID(func(c context.Context, ref *apiv1.ChatRef) error {
			r, err := chats.GetChat(c, ref)
			return show(r, err)
		})
	case "chat-connect":
		return withID(func(c context.Context, ref *apiv1.ChatRef) error {
			r, err := chats.ConnectChat(c, ref)
			return show(r, err)
		})
	case "chat-disconnect":
		return withID(func(c context.Context, ref *apiv1.ChatRef) error {
			r, err := chats.DisconnectChat(c, ref)
			return show(r, err)
		})
	case "chat-close":
		return withID(func(c context.Context, ref *apiv1.ChatRef) error {
			r, err := chats.CloseChat(c, ref)
			return show(r, err)
		})
	case "key-publish":
		return withID(func(c context.Context, ref *apiv1.ChatRef) error {
			raw, err := io.ReadAll(io.LimitReader(stdin, 4096))
			if err != nil {
				return err
			}
			value, err := hex.DecodeString(strings.TrimSpace(string(raw)))
			if err != nil {
				return errors.New("значение должно быть шестнадцатеричным")
			}
			if _, err := keys.PublishPublicValue(c, &apiv1.PublishPublicValueRequest{ChatId: ref.GetChatId(), PublicValue: value}); err != nil {
				return err
			}
			fmt.Fprintln(stdout, "значение опубликовано")
			return nil
		})
	case "key-get":
		return withID(func(c context.Context, ref *apiv1.ChatRef) error {
			r, err := keys.GetPeerPublicValue(c, ref)
			if err != nil {
				return err
			}
			if !r.GetPublished() {
				fmt.Fprintln(stdout, "значения пока нет")
				return nil
			}
			fmt.Fprintln(stdout, hex.EncodeToString(r.GetPublicValue()))
			return nil
		})
	}
	return false, nil
}

func printChat(w io.Writer, c *apiv1.Chat) {
	p := c.GetParams()
	state := "активен"
	if c.GetClosedAt() != nil {
		state = "закрыт"
	}
	fmt.Fprintf(w, "%s %s собеседник=%s создатель=%s %s/%d/%s/%s я_подключён=%t собеседник_подключён=%t готов=%t\n",
		hex.EncodeToString(c.GetChatId()), state, c.GetPeer().GetLogin(), c.GetCreator(),
		strings.TrimPrefix(p.GetAlgorithm().String(), "ALGORITHM_"), p.GetKeyBits(),
		strings.TrimPrefix(p.GetCipherMode().String(), "CIPHER_MODE_"),
		strings.TrimPrefix(p.GetPaddingMode().String(), "PADDING_MODE_"),
		c.GetSelf().GetConnected(), c.GetPeer().GetConnected(), c.GetReady())
}
