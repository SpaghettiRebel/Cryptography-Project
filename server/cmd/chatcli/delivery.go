package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	apiv1 "cryptochat/server/api/v1"
)

const (
	// maxFragmentLine — наибольшая строка send: 61 440 байт фрагмента в шестнадцатеричном виде.
	maxFragmentLine = 122880
	defaultRecvWait = 15 * time.Second
	sendTimeout     = 15 * time.Second
	drainTimeout    = 5 * time.Second
)

// withoutTimeout переносит сессию на новый контекст: общий предел команд здесь не подходит.
func withoutTimeout(authed context.Context) context.Context {
	md, _ := metadata.FromOutgoingContext(authed)
	return metadata.NewOutgoingContext(context.Background(), md)
}

// parseFragmentLine разбирает строку send. Пустая строка даёт nil.
func parseFragmentLine(line string) ([]byte, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, nil
	}
	fragment, err := hex.DecodeString(line)
	if err != nil {
		return nil, errors.New("фрагмент должен быть шестнадцатеричным")
	}
	return fragment, nil
}

// sendFragments отправляет фрагменты по порядку и останавливается на первой ошибке: уже отправленные
// остаются в очереди получателя.
func sendFragments(in io.Reader, send func([]byte) error) (int, error) {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64<<10), maxFragmentLine+2)
	sent := 0
	for scanner.Scan() {
		fragment, err := parseFragmentLine(scanner.Text())
		if err != nil {
			return sent, err
		}
		if fragment == nil {
			continue
		}
		if err := send(fragment); err != nil {
			return sent, err
		}
		sent++
	}
	if err := scanner.Err(); err != nil {
		return sent, errors.New("строка фрагмента слишком длинная")
	}
	return sent, nil
}

// fragmentStream — клиентская сторона потока получения.
type fragmentStream interface {
	Send(*apiv1.ReceiveRequest) error
	Recv() (*apiv1.ReceiveEvent, error)
	CloseSend() error
}

// receiveFragments печатает до want фрагментов и подтверждает каждый после печати. Затем закрывает свою
// сторону потока и дочитывает его: сервер успевает принять подтверждения и снять подключение.
func receiveFragments(stream fragmentStream, chatID []byte, want int, wait time.Duration, out io.Writer) error {
	open := &apiv1.ReceiveRequest{Kind: &apiv1.ReceiveRequest_Open_{Open: &apiv1.ReceiveRequest_Open{ChatId: chatID}}}
	if err := stream.Send(open); err != nil {
		return err
	}
	events := make(chan *apiv1.ReceiveEvent)
	ended := make(chan error, 1)
	go func() {
		for {
			ev, err := stream.Recv()
			if err != nil {
				ended <- err
				return
			}
			events <- ev
		}
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	got := 0
	finish := func() {
		_ = stream.CloseSend()
		limit := time.After(drainTimeout)
		for {
			select {
			case <-events:
			case <-ended:
				return
			case <-limit:
				return
			}
		}
	}
	short := func() error { return fmt.Errorf("получено фрагментов: %d из %d", got, want) }
	for got < want {
		select {
		case ev := <-events:
			d := ev.GetFragment()
			if d == nil {
				continue
			}
			if _, err := fmt.Fprintln(out, hex.EncodeToString(d.GetFragment())); err != nil {
				finish()
				return err
			}
			if err := stream.Send(&apiv1.ReceiveRequest{Kind: &apiv1.ReceiveRequest_Ack_{Ack: &apiv1.ReceiveRequest_Ack{DeliveryId: d.GetDeliveryId()}}}); err != nil {
				return <-ended
			}
			got++
		case err := <-ended:
			if errors.Is(err, io.EOF) {
				return short()
			}
			return err
		case <-timer.C:
			finish()
			return short()
		}
	}
	finish()
	return nil
}

// runDelivery выполняет команды доставки и возвращает false, если команда не относится к ним. Клиент не
// шифрует данные: он передаёт готовые байты.
func runDelivery(conn *grpc.ClientConn, authed func() (context.Context, error), command string, args []string, stdin io.Reader, stdout io.Writer) (bool, error) {
	if command != "send" && command != "recv" {
		return false, nil
	}
	client := apiv1.NewDeliveryClient(conn)
	if command == "send" && len(args) != 1 || command == "recv" && len(args) != 2 && len(args) != 3 {
		return true, errors.New("send <id> или recv <id> <n> [секунд]")
	}
	id, err := hex.DecodeString(args[0])
	if err != nil {
		return true, errors.New("идентификатор чата должен быть шестнадцатеричным")
	}
	c, err := authed()
	if err != nil {
		return true, err
	}
	base := withoutTimeout(c)
	if command == "send" {
		sent, err := sendFragments(stdin, func(fragment []byte) error {
			ctx, cancel := context.WithTimeout(base, sendTimeout)
			defer cancel()
			_, err := client.SendFragment(ctx, &apiv1.SendFragmentRequest{ChatId: id, Fragment: fragment})
			return err
		})
		fmt.Fprintln(stdout, "отправлено:", sent)
		return true, err
	}
	want, err := strconv.Atoi(args[1])
	if err != nil || want < 1 {
		return true, errors.New("число фрагментов должно быть положительным")
	}
	wait := defaultRecvWait
	if len(args) == 3 {
		seconds, err := strconv.Atoi(args[2])
		if err != nil || seconds < 1 {
			return true, errors.New("срок ожидания должен быть положительным числом секунд")
		}
		wait = time.Duration(seconds) * time.Second
	}
	ctx, cancel := context.WithCancel(base)
	defer cancel()
	stream, err := client.ReceiveFragments(ctx)
	if err != nil {
		return true, err
	}
	return true, receiveFragments(stream, id, want, wait, stdout)
}
