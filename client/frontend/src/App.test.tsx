import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import App from "./App";
import { getState } from "./store";
import * as W from "./test/wails";

const online = {
  ...W.emptySnapshot,
  version: 1,
  connection: "online",
  session: { login: "alice", userId: "u1" },
  contacts: ["bob"],
  chats: [
    {
      id: "a".repeat(32), peer: "bob", creator: "alice", algorithm: "RC6", keyBits: 256, cipherMode: "CBC", padding: "PKCS7",
      createdAt: 1, selfConnected: true, peerConnected: true, keyState: "READY", algorithmNote: "",
    },
  ],
};

async function loggedIn() {
  W.GetSnapshot.mockImplementation(async () => online);
  render(<App />);
  await waitFor(() => expect(screen.getByTestId("connection").textContent).toBe("в сети"));
}

test("после первой отрисовки вызывается FrontendReady", async () => {
  render(<App />);
  await waitFor(() => expect(W.FrontendReady).toHaveBeenCalled());
});

test("снимок с сессией открывает главное окно", async () => {
  await loggedIn();
  expect(getState().screen).toBe("main");
  expect(screen.getAllByTestId("chat-item")).toHaveLength(1);
});

test("offline: серверные действия неактивны с подсказкой, данные видны", async () => {
  await loggedIn();
  act(() => W.emit("snapshot", { ...online, version: 2, connection: "offline" }));
  expect(screen.getByTestId("connection").textContent).toBe("нет связи");
  expect(screen.getByTestId("offline-banner").textContent).toContain("Нет связи с сервером");
  const item = within(screen.getByTestId("chat-item"));
  const off = item.getByRole("button", { name: "Отключиться" }) as HTMLButtonElement;
  expect(off.disabled).toBe(true);
  expect(off.title).toBe("нет связи с сервером");
  expect((item.getByRole("button", { name: "Закрыть чат" }) as HTMLButtonElement).disabled).toBe(true);
  expect((screen.getByRole("button", { name: "Новый чат" }) as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(screen.getByRole("tab", { name: /Контакты/ }));
  expect((screen.getByRole("button", { name: "Удалить" }) as HTMLButtonElement).disabled).toBe(true);
  act(() => W.emit("snapshot", { ...online, version: 3, connection: "online" }));
  expect((screen.getByRole("button", { name: "Удалить" }) as HTMLButtonElement).disabled).toBe(false);
});

test("session-ended переводит на вход с баннером и логином", async () => {
  await loggedIn();
  act(() => W.emit("session-ended", { code: "SESSION_EXPIRED", login: "alice" }));
  expect(getState().screen).toBe("login");
  expect(screen.getByTestId("banner").textContent).toContain("Сессия истекла, войдите снова");
  expect((screen.getByTestId("login-input") as HTMLInputElement).value).toBe("alice");
  expect(screen.queryByTestId("chat-item")).toBeNull();
});

test("notice показывается и закрывается; не ошибки закрываются сами через 8 с", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  try {
    render(<App />);
    act(() => W.emit("notice", { kind: "info", code: "CHAT_CLOSED_BY_PEER", message: "Чат с bob закрыт собеседником" }));
    act(() => W.emit("notice", { kind: "error", code: "INTERNAL", message: "Внутренняя ошибка клиента" }));
    expect(screen.getAllByTestId("notice")).toHaveLength(2);
    act(() => {
      vi.advanceTimersByTime(8100);
    });
    expect(screen.getAllByTestId("notice")).toHaveLength(1);
    expect(screen.getByTestId("notice").textContent).toContain("Внутренняя ошибка клиента");
    fireEvent.click(screen.getByRole("button", { name: "Закрыть уведомление" }));
    expect(screen.queryByTestId("notice")).toBeNull();
  } finally {
    vi.useRealTimers();
  }
});

test("ошибка INTERNAL из метода показывается, окно остаётся рабочим", async () => {
  await loggedIn();
  W.DisconnectChat.mockImplementationOnce(async () => {
    throw W.goError("INTERNAL", "Внутренняя ошибка клиента");
  });
  fireEvent.click(screen.getByRole("button", { name: "Отключиться" }));
  await waitFor(() => expect(screen.getByText("Внутренняя ошибка клиента")).toBeTruthy());
  fireEvent.click(screen.getByRole("button", { name: "Отключиться" }));
  await waitFor(() => expect(W.DisconnectChat).toHaveBeenCalledTimes(2));
  fireEvent.click(screen.getByRole("tab", { name: /Контакты/ }));
  expect(screen.getByTestId("contacts-list")).toBeTruthy();
});

test("выход возвращает к окну входа", async () => {
  await loggedIn();
  fireEvent.click(screen.getByTestId("logout"));
  await waitFor(() => expect(getState().screen).toBe("login"));
  expect(W.Logout).toHaveBeenCalled();
  expect((screen.getByTestId("login-input") as HTMLInputElement).value).toBe("alice");
});
