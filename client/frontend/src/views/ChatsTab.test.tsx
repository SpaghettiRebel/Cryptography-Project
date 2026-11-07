import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import type { ChatView, KeyState, Snapshot } from "../backend";
import { setState } from "../store";
import * as W from "../test/wails";
import ChatsTab, { keyStateText } from "./ChatsTab";

const chat = (patch: Partial<ChatView>): ChatView => ({
  id: "0123456789abcdef0123456789abcdef",
  peer: "bob",
  creator: "alice",
  algorithm: "RC6",
  keyBits: 256,
  cipherMode: "CBC",
  padding: "PKCS7",
  createdAt: Date.UTC(2026, 10, 7, 10, 30),
  selfConnected: true,
  peerConnected: false,
  keyState: "WAITING_PEER",
  algorithmNote: "",
  ...patch,
});

function withSnapshot(patch: Partial<Snapshot>) {
  setState({
    screen: "main",
    snapshot: { ...W.emptySnapshot, connection: "online", session: { login: "alice", userId: "u" }, ...patch } as Snapshot,
  });
}

test("список с параметрами, подключениями, состоянием ключа и пометкой алгоритма", () => {
  withSnapshot({
    chats: [
      chat({ id: "a".repeat(32), peer: "bob", creator: "bob", algorithmNote: "тестовый шифр вместо RC6, только для разработки" }),
      chat({ id: "b".repeat(32), peer: "carol", algorithm: "MAGENTA", keyBits: 128, cipherMode: "RANDOM_DELTA", padding: "ISO_10126", selfConnected: false, peerConnected: true, keyState: "NONE" }),
    ],
  });
  render(<ChatsTab onOpenContacts={() => undefined} />);
  const items = screen.getAllByTestId("chat-item");
  expect(items).toHaveLength(2);
  const first = within(items[0]);
  expect(first.getByText("bob")).toBeTruthy();
  expect(first.getByText("создатель: bob")).toBeTruthy();
  expect(first.getByTestId("chat-params").textContent).toBe("RC6 · 256 · CBC · PKCS7");
  expect(first.getByTestId("chat-connections").textContent).toContain("вы: подключены");
  expect(first.getByTestId("chat-connections").textContent).toContain("собеседник: отключён");
  expect(first.getByTestId("chat-key").textContent).toBe("ждём собеседника");
  expect(first.getByTestId("chat-note").textContent).toContain("тестовый шифр вместо RC6");
  expect(first.getByTestId("chat-date").textContent).not.toBe("");
  const second = within(items[1]);
  expect(second.getByTestId("chat-params").textContent).toBe("MAGENTA · 128 · Random Delta · ISO 10126");
  expect(second.getByTestId("chat-connections").textContent).toContain("вы: отключены");
  expect(second.getByTestId("chat-connections").textContent).toContain("собеседник: подключён");
  expect(second.getByTestId("chat-key").textContent).toBe("подключитесь, чтобы согласовать ключ");
});

test("пустой список — подсказка", () => {
  withSnapshot({});
  render(<ChatsTab onOpenContacts={() => undefined} />);
  expect(screen.getByTestId("chats-empty").textContent).toContain("Создайте чат с контактом");
});

test("закрытие с подтверждением", async () => {
  withSnapshot({ chats: [chat({})] });
  render(<ChatsTab onOpenContacts={() => undefined} />);
  fireEvent.click(screen.getByRole("button", { name: "Закрыть чат" }));
  expect(screen.getByTestId("confirm-text").textContent).toBe("Чат будет закрыт для обоих участников, ключ удалён с устройства");
  expect(W.CloseChat).not.toHaveBeenCalled();
  fireEvent.click(screen.getByTestId("confirm-ok"));
  await waitFor(() => expect(W.CloseChat).toHaveBeenCalledWith("0123456789abcdef0123456789abcdef"));
});

test("«Новый чат» открывает диалог", async () => {
  withSnapshot({ contacts: ["bob"] });
  render(<ChatsTab onOpenContacts={() => undefined} />);
  fireEvent.click(screen.getByRole("button", { name: "Новый чат" }));
  await waitFor(() => expect(screen.getByTestId("create-chat-dialog")).toBeTruthy());
});

test("без контактов диалог предлагает перейти в «Контакты»", async () => {
  withSnapshot({});
  const open = vi.fn();
  render(<ChatsTab onOpenContacts={open} />);
  fireEvent.click(screen.getByRole("button", { name: "Новый чат" }));
  await waitFor(() => expect(screen.getByTestId("no-contacts").textContent).toContain("Контакты"));
  fireEvent.click(screen.getByRole("button", { name: "Перейти к контактам" }));
  expect(open).toHaveBeenCalled();
});

test("тексты всех состояний ключа", () => {
  const states: KeyState[] = ["NONE", "WAITING_SELF", "WAITING_PEER", "READY", "KEY_LOST", "PEER_VALUE_INVALID"];
  for (const s of states) expect(keyStateText[s]).toBeTruthy();
  expect(keyStateText.READY).toBe("ключ согласован");
  expect(keyStateText.KEY_LOST).toBe("ключ на этом устройстве получить нельзя: закройте чат и создайте новый");
});

test("«Подключиться» и «Отключиться» по состоянию", async () => {
  withSnapshot({
    chats: [chat({ id: "c".repeat(32), selfConnected: false, keyState: "NONE" }), chat({ id: "d".repeat(32), selfConnected: true, keyState: "READY" })],
  });
  render(<ChatsTab onOpenContacts={() => undefined} />);
  const [first, second] = screen.getAllByTestId("chat-item").map((el) => within(el));
  expect(first.queryByRole("button", { name: "Отключиться" })).toBeNull();
  fireEvent.click(first.getByRole("button", { name: "Подключиться" }));
  await waitFor(() => expect(W.ConnectChat).toHaveBeenCalledWith("c".repeat(32)));
  expect(second.queryByRole("button", { name: "Подключиться" })).toBeNull();
  fireEvent.click(second.getByRole("button", { name: "Отключиться" }));
  await waitFor(() => expect(W.DisconnectChat).toHaveBeenCalledWith("d".repeat(32)));
  expect(second.getByTestId("chat-key").textContent).toBe("ключ согласован");
});

test("все состояния ключа показываются текстом, у KEY_LOST и PEER_VALUE_INVALID — подсказка закрыть чат", () => {
  const states: KeyState[] = ["NONE", "WAITING_SELF", "WAITING_PEER", "READY", "KEY_LOST", "PEER_VALUE_INVALID"];
  withSnapshot({ chats: states.map((s, i) => chat({ id: String(i).repeat(32), keyState: s })) });
  render(<ChatsTab onOpenContacts={() => undefined} />);
  const items = screen.getAllByTestId("chat-item").map((el) => within(el));
  states.forEach((s, i) => {
    expect(items[i].getByTestId("chat-key").textContent).toBe(keyStateText[s]);
    const hint = items[i].queryByTestId("chat-key-hint");
    if (s === "KEY_LOST" || s === "PEER_VALUE_INVALID") expect(hint?.textContent).toContain("Закройте этот чат");
    else expect(hint).toBeNull();
  });
  expect(keyStateText.WAITING_SELF).toBe("публикация значения…");
  expect(keyStateText.PEER_VALUE_INVALID).toBe("собеседник прислал недопустимое значение: закройте чат");
});
