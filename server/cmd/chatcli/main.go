// Тестовый клиент для проверки сервера без приложения.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	apiv1 "cryptochat/server/api/v1"
	"cryptochat/server/internal/bootstrap"
	"cryptochat/server/internal/rpc"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		reason := rpc.ReasonOf(err)
		if field := rpc.FieldOf(err); field != "" {
			fmt.Fprintln(os.Stderr, "ошибка:", reason, "поле:", field)
		} else if reason != "" {
			fmt.Fprintln(os.Stderr, "ошибка:", reason)
		} else {
			fmt.Fprintln(os.Stderr, "ошибка:", err)
		}
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("chatcli", flag.ContinueOnError)
	addr := fs.String("addr", "localhost:8443", "адрес сервера")
	ca := fs.String("ca", "deploy/certs/server.crt", "файл сертификата сервера")
	sessionFile := fs.String("session", ".chatcli-session.tmp", "файл сессии")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("нужна команда: register, login, logout, contact-request, contact-accept, contact-reject, contact-remove, contacts, " + chatsUsage)
	}
	command, rest := rest[0], rest[1:]

	tlsConfig, err := bootstrap.ClientTLS(*ca, "localhost")
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	if err != nil {
		return err
	}
	defer conn.Close()
	accounts := apiv1.NewAccountsClient(conn)
	contacts := apiv1.NewContactsClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	needLogin := func() (string, error) {
		if len(rest) != 1 {
			return "", fmt.Errorf("команде %s нужен логин", command)
		}
		return rest[0], nil
	}
	authed := func() (context.Context, error) {
		raw, err := os.ReadFile(*sessionFile)
		if err != nil {
			return nil, errors.New("нет сессии: сначала выполните login")
		}
		return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+strings.TrimSpace(string(raw))), nil
	}

	switch command {
	case "register", "login":
		login, err := needLogin()
		if err != nil {
			return err
		}
		password, err := readPassword(stdin)
		if err != nil {
			return err
		}
		if command == "register" {
			r, err := accounts.Register(ctx, &apiv1.RegisterRequest{Login: login, Password: password})
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, "зарегистрирован:", r.GetLogin())
			return nil
		}
		r, err := accounts.Login(ctx, &apiv1.LoginRequest{Login: login, Password: password})
		if err != nil {
			return err
		}
		if err := os.WriteFile(*sessionFile, []byte(r.GetToken()), 0o600); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "вход выполнен:", r.GetLogin())
		return nil
	case "logout":
		c, err := authed()
		if err != nil {
			return err
		}
		if _, err := accounts.Logout(c, &apiv1.LogoutRequest{}); err != nil {
			return err
		}
		return os.Remove(*sessionFile)
	case "contact-request", "contact-accept", "contact-reject", "contact-remove":
		login, err := needLogin()
		if err != nil {
			return err
		}
		c, err := authed()
		if err != nil {
			return err
		}
		switch command {
		case "contact-request":
			r, err := contacts.SendRequest(c, &apiv1.SendContactRequest{Login: login})
			if err != nil {
				return err
			}
			if r.GetBecameContact() {
				fmt.Fprintln(stdout, "контакт создан встречным запросом:", login)
			} else {
				fmt.Fprintln(stdout, "запрос отправлен:", login)
			}
			return nil
		case "contact-accept":
			_, err = contacts.AcceptRequest(c, &apiv1.ContactLogin{Login: login})
		case "contact-reject":
			_, err = contacts.RejectRequest(c, &apiv1.ContactLogin{Login: login})
		case "contact-remove":
			_, err = contacts.RemoveContact(c, &apiv1.ContactLogin{Login: login})
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, "готово:", login)
		return nil
	case "contacts":
		c, err := authed()
		if err != nil {
			return err
		}
		r, err := contacts.ListContacts(c, &apiv1.ListContactsRequest{})
		if err != nil {
			return err
		}
		printList(stdout, "контакты", r.GetContacts())
		printList(stdout, "входящие", r.GetIncoming())
		printList(stdout, "исходящие", r.GetOutgoing())
		return nil
	}
	handled, err := runChat(conn, authed, command, rest, stdin, stdout)
	if handled {
		return err
	}
	handled, err = runDelivery(conn, authed, command, rest, stdin, stdout)
	if handled {
		return err
	}
	return fmt.Errorf("неизвестная команда %q", command)
}

func printList(w io.Writer, title string, list []*apiv1.UserRef) {
	names := make([]string, 0, len(list))
	for _, u := range list {
		names = append(names, u.GetLogin())
	}
	fmt.Fprintf(w, "%s: %s\n", title, strings.Join(names, ", "))
}

// readPassword читает строку и обрезает один завершающий перевод строки, в том числе \r\n.
func readPassword(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return line, nil
}
