import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { expect, test } from "vitest";
import { onMessages } from "../messages";
import { CHAT, chatView, msg, withChats } from "../test/fixtures";
import * as W from "../test/wails";
import ConversationView from "./ConversationView";

test("открытие вызывает MarkRead, лента по возрастанию seq, свои справа", async () => {
  withChats([chatView()]);
  W.ListMessages.mockResolvedValueOnce({
    messages: [msg({ seq: 1, text: "первое" }), msg({ seq: 2, direction: "out", state: "SENT", text: "второе" })],
    hasMore: false,
  });
  render(<ConversationView chatId={CHAT} onBack={() => undefined} />);
  await waitFor(() => expect(screen.getAllByTestId("message")).toHaveLength(2));
  expect(W.MarkRead).toHaveBeenCalledWith(CHAT);
  const items = screen.getAllByTestId("message");
  expect(items[0].className).toContain("message-in");
  expect(items[1].className).toContain("message-out");
  expect(within(items[1]).getByTestId("message-text").textContent).toBe("второе");
  expect(screen.getByTestId("conversation-state").textContent).toContain("ключ согласован");
  expect(screen.getByTestId("composer")).toBeTruthy();
});

test("новое входящее в открытом чате сразу отмечается просмотренным, исходящее — нет", async () => {
  withChats([chatView()]);
  render(<ConversationView chatId={CHAT} onBack={() => undefined} />);
  await waitFor(() => expect(W.MarkRead).toHaveBeenCalledTimes(1));
  onMessages({ chatId: CHAT, changed: [msg({ seq: 5, direction: "out", state: "SENT" })] });
  expect(W.MarkRead).toHaveBeenCalledTimes(1);
  onMessages({ chatId: CHAT, changed: [msg({ seq: 6, text: "новое" })] });
  await waitFor(() => expect(W.MarkRead).toHaveBeenCalledTimes(2));
  await waitFor(() => expect(screen.getAllByTestId("message").map((m) => m.getAttribute("data-seq"))).toEqual(["5", "6"]));
});

test("прокрутка вверх подгружает более ранние по beforeSeq", async () => {
  withChats([chatView()]);
  W.ListMessages.mockResolvedValueOnce({ messages: [msg({ seq: 51 }), msg({ seq: 52 })], hasMore: true });
  render(<ConversationView chatId={CHAT} onBack={() => undefined} />);
  await waitFor(() => expect(screen.getByTestId("feed-older")).toBeTruthy());
  W.ListMessages.mockResolvedValueOnce({ messages: [msg({ seq: 50 })], hasMore: false });
  fireEvent.scroll(screen.getByTestId("feed"), { target: { scrollTop: 0 } });
  await waitFor(() => expect(W.ListMessages).toHaveBeenLastCalledWith(CHAT, 51, 50));
  await waitFor(() => expect(screen.getAllByTestId("message")).toHaveLength(3));
});

test("при offline отправка не блокируется", () => {
  withChats([chatView()], { connection: "offline" });
  render(<ConversationView chatId={CHAT} onBack={() => undefined} />);
  expect((screen.getByTestId("composer-send") as HTMLButtonElement).disabled).toBe(false);
});

test("история закрытого чата — только чтение", async () => {
  withChats([]);
  W.ListMessages.mockResolvedValueOnce({ messages: [msg({ seq: 1, direction: "out", state: "SENDING", cancellable: true })], hasMore: false });
  render(
    <ConversationView
      chatId={CHAT}
      closed={{ id: CHAT, peer: "bob", algorithm: "RC6", keyBits: 256, cipherMode: "CBC", padding: "PKCS7", createdAt: 1, closedAt: 2, messageCount: 1 }}
      onBack={() => undefined}
    />,
  );
  await waitFor(() => expect(screen.getAllByTestId("message")).toHaveLength(1));
  expect(screen.getByTestId("conversation-closed").textContent).toBe("Чат закрыт");
  expect(screen.queryByTestId("composer")).toBeNull();
  expect(screen.queryByTestId("message-cancel")).toBeNull();
  expect(W.MarkRead).not.toHaveBeenCalled();
});

test("ожидание с QUEUE_FULL показывает причину", async () => {
  withChats([chatView()]);
  const text = "Очередь собеседника на сервере заполнена: передача продолжится, когда он примет данные";
  W.ListMessages.mockResolvedValueOnce({
    messages: [msg({ seq: 1, direction: "out", state: "WAITING", error: { code: "QUEUE_FULL", message: text }, cancellable: true })],
    hasMore: false,
  });
  render(<ConversationView chatId={CHAT} onBack={() => undefined} />);
  await waitFor(() => expect(screen.getByTestId("message-state").textContent).toContain("ожидание: очередь собеседника заполнена"));
  expect(screen.getByTestId("message-error").textContent).toBe(text);
});

/** Подменяет размеры ленты: jsdom их не считает. */
function geometry(el: HTMLElement, height: number) {
  const g = { height, top: 0 };
  Object.defineProperty(el, "scrollHeight", { configurable: true, get: () => g.height });
  Object.defineProperty(el, "clientHeight", { configurable: true, get: () => 200 });
  Object.defineProperty(el, "scrollTop", { configurable: true, get: () => g.top, set: (v: number) => (g.top = v) });
  return g;
}

function deferred<T>() {
  let resolve: (v: T) => void = () => undefined;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

test("при открытии лента прокручена к концу; новое сообщение прокручивает, только если пользователь был внизу", async () => {
  withChats([chatView()]);
  const page = deferred<unknown>();
  W.ListMessages.mockImplementationOnce(() => page.promise);
  render(<ConversationView chatId={CHAT} onBack={() => undefined} />);
  const g = geometry(screen.getByTestId("feed"), 1000);
  page.resolve({ messages: [msg({ seq: 1 }), msg({ seq: 2 })], hasMore: false });
  await waitFor(() => expect(g.top).toBe(1000));
  g.height = 1200;
  onMessages({ chatId: CHAT, changed: [msg({ seq: 3 })] });
  await waitFor(() => expect(g.top).toBe(1200));
  // Пользователь прокрутил вверх: новое сообщение не сдвигает ленту.
  g.top = 100;
  fireEvent.scroll(screen.getByTestId("feed"));
  g.height = 1400;
  onMessages({ chatId: CHAT, changed: [msg({ seq: 4 })] });
  await waitFor(() => expect(screen.getAllByTestId("message")).toHaveLength(4));
  expect(g.top).toBe(100);
  // Изменение уже показанного сообщения не прокручивает.
  g.top = 1200;
  fireEvent.scroll(screen.getByTestId("feed"));
  g.height = 1500;
  onMessages({ chatId: CHAT, changed: [msg({ seq: 2, text: "изменено" })] });
  await waitFor(() => expect(screen.getAllByTestId("message-text")[1].textContent).toBe("изменено"));
  expect(g.top).toBe(1200);
});

test("подгрузка ранних сообщений сохраняет видимое место ленты", async () => {
  withChats([chatView()]);
  const first = deferred<unknown>();
  W.ListMessages.mockImplementationOnce(() => first.promise);
  render(<ConversationView chatId={CHAT} onBack={() => undefined} />);
  const g = geometry(screen.getByTestId("feed"), 1000);
  first.resolve({ messages: [msg({ seq: 51 }), msg({ seq: 52 })], hasMore: true });
  await waitFor(() => expect(g.top).toBe(1000));
  const older = deferred<unknown>();
  W.ListMessages.mockImplementationOnce(() => older.promise);
  g.top = 0;
  fireEvent.scroll(screen.getByTestId("feed"));
  await waitFor(() => expect(W.ListMessages).toHaveBeenLastCalledWith(CHAT, 51, 50));
  g.height = 1500;
  older.resolve({ messages: [msg({ seq: 49 }), msg({ seq: 50 })], hasMore: false });
  await waitFor(() => expect(screen.getAllByTestId("message")).toHaveLength(4));
  expect(g.top).toBe(500);
});
