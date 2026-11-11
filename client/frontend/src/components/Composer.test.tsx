import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { expect, test } from "vitest";
import { chatView, msg, withChats } from "../test/fixtures";
import * as W from "../test/wails";
import Composer, { blockHint } from "./Composer";

test("Enter отправляет, Shift+Enter — перевод строки", async () => {
  W.SendText.mockResolvedValue(msg({ direction: "out", state: "QUEUED" }));
  render(<Composer chat={chatView()} />);
  const field = screen.getByTestId("composer-text") as HTMLTextAreaElement;
  fireEvent.change(field, { target: { value: "строка" } });
  fireEvent.keyDown(field, { key: "Enter", shiftKey: true });
  expect(W.SendText).not.toHaveBeenCalled();
  fireEvent.keyDown(field, { key: "Enter" });
  await waitFor(() => expect(W.SendText).toHaveBeenCalledWith(chatView().id, "строка"));
  await waitFor(() => expect(field.value).toBe(""));
});

test("повторное нажатие во время вызова не отправляет второй раз", async () => {
  let release: (v: unknown) => void = () => undefined;
  W.SendText.mockImplementation(() => new Promise((r) => (release = r)));
  render(<Composer chat={chatView()} />);
  fireEvent.change(screen.getByTestId("composer-text"), { target: { value: "x" } });
  fireEvent.click(screen.getByTestId("composer-send"));
  fireEvent.click(screen.getByTestId("composer-send"));
  fireEvent.keyDown(screen.getByTestId("composer-text"), { key: "Enter" });
  expect(W.SendText).toHaveBeenCalledTimes(1);
  release(msg({ direction: "out" }));
});

test("недоступная отправка: поле и кнопки неактивны, подсказка по причине", () => {
  for (const [block, hint] of Object.entries(blockHint)) {
    const { unmount } = render(<Composer chat={chatView({ canSend: false, sendBlock: block as never })} />);
    expect((screen.getByTestId("composer-text") as HTMLTextAreaElement).disabled).toBe(true);
    expect((screen.getByTestId("composer-send") as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByTestId("composer-file") as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByTestId("send-block-text").textContent).toBe(hint);
    unmount();
  }
  expect(blockHint.ALGORITHM_UNAVAILABLE).toBe("Алгоритм чата не реализован в этой сборке");
});

test("EMPTY_TEXT и TEXT_TOO_LONG показываются у поля", async () => {
  W.SendText.mockRejectedValueOnce(W.goError("EMPTY_TEXT", "Введите текст сообщения", "text"));
  render(<Composer chat={chatView()} />);
  fireEvent.change(screen.getByTestId("composer-text"), { target: { value: "  " } });
  fireEvent.click(screen.getByTestId("composer-send"));
  await waitFor(() => expect(screen.getByTestId("composer-error").textContent).toBe("Введите текст сообщения"));
  W.SendText.mockRejectedValueOnce(W.goError("TEXT_TOO_LONG", "Сообщение длиннее 64 КиБ: отправьте его файлом", "text"));
  fireEvent.change(screen.getByTestId("composer-text"), { target: { value: "я".repeat(33000) } });
  expect(screen.getByTestId("composer-size").textContent).toContain("66000");
  fireEvent.click(screen.getByTestId("composer-send"));
  await waitFor(() => expect(screen.getByTestId("composer-error").textContent).toContain("64 КиБ"));
});

test("«Файл…» вызывает SendFile, закрытый диалог ничего не добавляет", async () => {
  render(<Composer chat={chatView()} />);
  fireEvent.click(screen.getByTestId("composer-file"));
  await waitFor(() => expect(W.SendFile).toHaveBeenCalledWith(chatView().id));
  expect(document.querySelector(".error")).toBeNull();
});

test("подсказка о блокировке: «Подключиться» при NOT_CONNECTED и «Получать здесь» при RECEIVING_ELSEWHERE", async () => {
  const { unmount } = render(<Composer chat={chatView({ canSend: false, sendBlock: "NOT_CONNECTED", selfConnected: false })} />);
  expect(screen.queryByTestId("send-block-here")).toBeNull();
  fireEvent.click(screen.getByTestId("send-block-connect"));
  await waitFor(() => expect(W.ConnectChat).toHaveBeenCalledWith(chatView().id));
  unmount();
  render(<Composer chat={chatView({ canSend: false, sendBlock: "RECEIVING_ELSEWHERE", receiveState: "ELSEWHERE" })} />);
  expect(screen.queryByTestId("send-block-connect")).toBeNull();
  fireEvent.click(screen.getByTestId("send-block-here"));
  await waitFor(() => expect(W.ReceiveHere).toHaveBeenCalledWith(chatView().id));
});

test("ошибка подключения из подсказки показывается; при других причинах кнопок нет", async () => {
  W.ConnectChat.mockRejectedValueOnce(W.goError("CHAT_NOT_FOUND", "Чат не найден"));
  const { unmount } = render(<Composer chat={chatView({ canSend: false, sendBlock: "NOT_CONNECTED" })} />);
  fireEvent.click(screen.getByTestId("send-block-connect"));
  await waitFor(() => expect(screen.getByText("Чат не найден")).toBeTruthy());
  unmount();
  render(<Composer chat={chatView({ canSend: false, sendBlock: "KEY_NOT_READY" })} />);
  expect(screen.queryByTestId("send-block-connect")).toBeNull();
  expect(screen.queryByTestId("send-block-here")).toBeNull();
});

test("без связи кнопки подсказки неактивны", () => {
  withChats([chatView()], { connection: "offline" });
  render(<Composer chat={chatView({ canSend: false, sendBlock: "NOT_CONNECTED" })} />);
  expect((screen.getByTestId("send-block-connect") as HTMLButtonElement).disabled).toBe(true);
});

test("«Файл…»: ошибка диалога выбора показывается кодом ошибки", async () => {
  W.SendFile.mockRejectedValueOnce(W.goError("FILE_READ", "Не удалось открыть файл"));
  render(<Composer chat={chatView()} />);
  fireEvent.click(screen.getByTestId("composer-file"));
  await waitFor(() => expect(screen.getByText("Не удалось открыть файл")).toBeTruthy());
});
