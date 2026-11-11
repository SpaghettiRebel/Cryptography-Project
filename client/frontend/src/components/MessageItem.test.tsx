import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactElement } from "react";
import { expect, test } from "vitest";
import type { MessageState } from "../backend";
import { msg } from "../test/fixtures";
import { getState } from "../store";
import * as W from "../test/wails";
import MessageItem, { sizeText, stateText } from "./MessageItem";

const list = (el: ReactElement) => render(<ul>{el}</ul>);

test("текст с разметкой показывается как текст", () => {
  list(<MessageItem message={msg({ text: "<b>x</b>\nвторая" })} />);
  expect(screen.getByTestId("message-text").textContent).toBe("<b>x</b>\nвторая");
  expect(document.querySelector("b")).toBeNull();
});

test("картинка: img с mediaUrl и «Сохранить как…»", async () => {
  list(<MessageItem message={msg({ kind: "image", fileName: "a.png", mediaUrl: "/media/7", seq: 7 })} />);
  expect((screen.getByTestId("message-img") as HTMLImageElement).getAttribute("src")).toBe("/media/7");
  fireEvent.click(screen.getByTestId("message-save"));
  await waitFor(() => expect(W.SaveFile).toHaveBeenCalledWith(7));
});

test("файл: имя, размер, сохранение блокируется на время вызова, закрытый диалог — без ошибки", async () => {
  let release: (v: unknown) => void = () => undefined;
  W.SaveFile.mockImplementationOnce(() => new Promise((r) => (release = r)));
  list(<MessageItem message={msg({ kind: "file", fileName: "отчёт.pdf", size: 1536, seq: 3 })} />);
  expect(screen.getByTestId("message-file").textContent).toContain("отчёт.pdf");
  expect(screen.getByTestId("message-size").textContent).toBe("1.5 КиБ");
  fireEvent.click(screen.getByTestId("message-save"));
  await waitFor(() => expect((screen.getByTestId("message-save") as HTMLButtonElement).disabled).toBe(true));
  release("");
  await waitFor(() => expect((screen.getByTestId("message-save") as HTMLButtonElement).disabled).toBe(false));
  expect(screen.queryByText("Не удалось сохранить файл")).toBeNull();
  W.SaveFile.mockRejectedValueOnce(W.goError("FILE_WRITE", "Не удалось сохранить файл"));
  fireEvent.click(screen.getByTestId("message-save"));
  await waitFor(() => expect(screen.getByText("Не удалось сохранить файл")).toBeTruthy());
});

test("состояния словами", () => {
  const words: Partial<Record<MessageState, string>> = {
    QUEUED: "в очереди",
    SENDING: "отправка",
    SENT: "отправлено",
    CANCELLED: "отменено",
    UNDELIVERED: "не доставлено",
    RECEIVING: "получение",
    DECRYPT_PENDING: "ожидает расшифрования",
    DECRYPTING: "расшифрование",
    DECRYPT_CANCELLED: "расшифрование отменено",
    CANCELLED_BY_PEER: "отменено отправителем",
    CORRUPTED: "повреждено",
    KEY_MISSING: "ключ удалён",
  };
  for (const [state, word] of Object.entries(words)) expect(stateText(msg({ state: state as MessageState }))).toBe(word);
  expect(stateText(msg({ state: "WAITING", error: { code: "QUEUE_FULL", message: "м" } }))).toBe("ожидание: очередь собеседника заполнена");
  expect(stateText(msg({ state: "WAITING", error: { code: "SERVER_UNAVAILABLE", message: "м" } }))).toBe("ожидание: нет связи");
  expect(stateText(msg({ state: "FAILED", error: { code: "SOURCE_CHANGED", message: "Файл изменён" } }))).toBe("ошибка: Файл изменён");
  expect(sizeText(10)).toBe("10 Б");
  expect(sizeText(3 * 1024 * 1024)).toBe("3.0 МиБ");
});

test("WAITING с QUEUE_FULL показывает текст причины из контракта", () => {
  const text = "Очередь собеседника на сервере заполнена: передача продолжится, когда он примет данные";
  list(<MessageItem message={msg({ direction: "out", state: "WAITING", error: { code: "QUEUE_FULL", message: text }, cancellable: true })} />);
  expect(screen.getByTestId("message-error").textContent).toBe(text);
});

test("«Отменить»: подтверждение для исходящего с отправленными частями, сразу — без них", async () => {
  W.CancelMessage.mockResolvedValue(msg({ seq: 4, direction: "out", state: "CANCELLED" }));
  const sending = msg({ seq: 4, direction: "out", state: "SENDING", cancellable: true, send: { done: 2, total: 9 }, encrypt: { done: 5, total: 9 } });
  const { unmount } = list(<MessageItem message={sending} />);
  fireEvent.click(screen.getByTestId("message-cancel"));
  expect(screen.getByTestId("confirm-text").textContent).toBe("Собеседник получит уведомление об отмене");
  expect(W.CancelMessage).not.toHaveBeenCalled();
  fireEvent.click(screen.getByTestId("confirm-ok"));
  await waitFor(() => expect(W.CancelMessage).toHaveBeenCalledWith(4));
  unmount();
  list(<MessageItem message={msg({ seq: 5, state: "RECEIVING", kind: "pending", cancellable: true, receive: { done: 1, total: 3 } })} />);
  expect(screen.getByTestId("message-pending").textContent).toContain("1 из 3");
  fireEvent.click(screen.getByTestId("message-cancel"));
  await waitFor(() => expect(W.CancelMessage).toHaveBeenCalledWith(5));
});

test("«Расшифровать снова» при retryable; только чтение — без кнопок и полос", async () => {
  const { unmount } = list(<MessageItem message={msg({ seq: 6, state: "DECRYPT_CANCELLED", kind: "pending", retryable: true })} />);
  fireEvent.click(screen.getByTestId("message-retry"));
  await waitFor(() => expect(W.RetryDecrypt).toHaveBeenCalledWith(6));
  unmount();
  list(<MessageItem readOnly message={msg({ state: "SENDING", direction: "out", cancellable: true, send: { done: 0, total: 1 } })} />);
  expect(screen.queryByTestId("message-cancel")).toBeNull();
  expect(screen.queryByTestId("progress-send")).toBeNull();
});

test("подпись получаемого: «получение…» только в RECEIVING, иначе состояние или «сообщение недоступно»", () => {
  const cases: [MessageState, string][] = [
    ["DECRYPT_PENDING", "ожидает расшифрования"],
    ["DECRYPTING", "расшифрование"],
    ["DECRYPT_CANCELLED", "расшифрование отменено"],
    ["CANCELLED", "отменено"],
    ["CANCELLED_BY_PEER", "отменено отправителем"],
    ["UNDELIVERED", "не доставлено"],
    ["CORRUPTED", "сообщение недоступно"],
    ["KEY_MISSING", "сообщение недоступно"],
  ];
  for (const [state, text] of cases) {
    const { unmount } = list(<MessageItem message={msg({ state, kind: "pending", receive: null })} />);
    const pending = screen.getByTestId("message-pending").textContent ?? "";
    expect(pending).toBe(text);
    expect(pending).not.toContain("получение");
    unmount();
  }
  list(<MessageItem message={msg({ state: "RECEIVING", kind: "pending", receive: { done: 2, total: 5 } })} />);
  expect(screen.getByTestId("message-pending").textContent).toBe("получение… 2 из 5 частей");
});

test("после сохранения — уведомление с путём; закрытый диалог — без уведомления", async () => {
  list(<MessageItem message={msg({ kind: "file", fileName: "a.pdf", seq: 8 })} />);
  fireEvent.click(screen.getByTestId("message-save"));
  await waitFor(() => expect(W.SaveFile).toHaveBeenCalledTimes(1));
  await waitFor(() => expect((screen.getByTestId("message-save") as HTMLButtonElement).disabled).toBe(false));
  expect(getState().notices).toHaveLength(0);
  W.SaveFile.mockResolvedValueOnce("C:\Загрузки\a.pdf");
  fireEvent.click(screen.getByTestId("message-save"));
  await waitFor(() => expect(getState().notices.map((n) => n.message)).toEqual(["Сохранено: C:\Загрузки\a.pdf"]));
  expect(getState().notices[0].kind).toBe("info");
});

test("ошибка диалога сохранения показывается кодом ошибки, а не как закрытый диалог", async () => {
  W.SaveFile.mockRejectedValueOnce(W.goError("FILE_WRITE", "Не удалось сохранить файл"));
  list(<MessageItem message={msg({ kind: "file", fileName: "a.pdf", seq: 9 })} />);
  fireEvent.click(screen.getByTestId("message-save"));
  await waitFor(() => expect(screen.getByText("Не удалось сохранить файл")).toBeTruthy());
  expect(getState().notices).toHaveLength(0);
});
