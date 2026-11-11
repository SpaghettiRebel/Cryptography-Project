import { expect, test } from "vitest";
import { getFeed, loadLatest, merge, onMessages, onProgress, progressFits, upsert, withProgress } from "./messages";
import { CHAT, msg } from "./test/fixtures";
import * as W from "./test/wails";

test("слияние без дублей по seq, по возрастанию, новое заменяет старое", () => {
  const out = merge([msg({ seq: 3 }), msg({ seq: 1 })], [msg({ seq: 2 }), msg({ seq: 3, text: "новое" })]);
  expect(out.map((m) => m.seq)).toEqual([1, 2, 3]);
  expect(out[2].text).toBe("новое");
});

test("прогресс обновляет только своё сообщение", () => {
  const list = [msg({ seq: 1 }), msg({ seq: 2, direction: "out", state: "SENDING" })];
  const out = withProgress(list, { seq: 2, chatId: CHAT, stage: "send", done: 3, total: 10 });
  expect(out[0]).toBe(list[0]);
  expect(out[1].send).toEqual({ done: 3, total: 10 });
  expect(withProgress(list, { seq: 9, chatId: CHAT, stage: "send", done: 1, total: 1 })).toBe(list);
});

test("события messages и progress меняют загруженную ленту, пустой changed — перечитывание", async () => {
  W.ListMessages.mockResolvedValueOnce({ messages: [msg({ seq: 1 })], hasMore: false });
  await loadLatest(CHAT);
  onMessages({ chatId: CHAT, changed: [msg({ seq: 2, state: "RECEIVING", kind: "pending" })] });
  onMessages({ chatId: CHAT, changed: [msg({ seq: 2, state: "RECEIVING", kind: "pending" })] });
  expect(getFeed(CHAT).messages.map((m) => m.seq)).toEqual([1, 2]);
  onProgress({ seq: 2, chatId: CHAT, stage: "receive", done: 1, total: 4 });
  expect(getFeed(CHAT).messages[1].receive).toEqual({ done: 1, total: 4 });
  W.ListMessages.mockResolvedValueOnce({ messages: [], hasMore: false });
  onMessages({ chatId: CHAT, changed: [] });
  await new Promise((r) => setTimeout(r, 0));
  expect(W.ListMessages).toHaveBeenCalledTimes(2);
  expect(getFeed(CHAT).messages).toEqual([]);
  upsert(msg({ seq: 5 }));
  expect(getFeed(CHAT).messages.map((m) => m.seq)).toEqual([5]);
});

test("прогресс стадии, не подходящей состоянию, отбрасывается", () => {
  const list = [
    msg({ seq: 1, direction: "out", state: "SENT", send: null, encrypt: null }),
    msg({ seq: 2, direction: "out", state: "SENDING" }),
    msg({ seq: 3, kind: "pending", state: "RECEIVING" }),
    msg({ seq: 4, kind: "pending", state: "DECRYPT_CANCELLED" }),
  ];
  // Запоздавшие события после смены состояния.
  expect(withProgress(list, { seq: 1, chatId: CHAT, stage: "send", done: 9, total: 10 })).toBe(list);
  expect(withProgress(list, { seq: 1, chatId: CHAT, stage: "encrypt", done: 9, total: 10 })).toBe(list);
  expect(withProgress(list, { seq: 3, chatId: CHAT, stage: "decrypt", done: 1, total: 10 })).toBe(list);
  expect(withProgress(list, { seq: 4, chatId: CHAT, stage: "decrypt", done: 5, total: 10 })).toBe(list);
  expect(withProgress(list, { seq: 2, chatId: CHAT, stage: "receive", done: 1, total: 2 })).toBe(list);
  // Подходящие применяются.
  expect(withProgress(list, { seq: 2, chatId: CHAT, stage: "encrypt", done: 1, total: 2 })[1].encrypt).toEqual({ done: 1, total: 2 });
  expect(withProgress(list, { seq: 3, chatId: CHAT, stage: "receive", done: 1, total: 2 })[2].receive).toEqual({ done: 1, total: 2 });
  expect(progressFits(msg({ state: "DECRYPTING" }), "decrypt")).toBe(true);
  expect(progressFits(msg({ state: "DONE" }), "decrypt")).toBe(false);
});

test("событие progress после перехода в SENT не возвращает полосу", async () => {
  W.ListMessages.mockResolvedValueOnce({ messages: [msg({ seq: 1, direction: "out", state: "SENDING", send: { done: 1, total: 2 } })], hasMore: false });
  await loadLatest(CHAT);
  onMessages({ chatId: CHAT, changed: [msg({ seq: 1, direction: "out", state: "SENT", send: null, encrypt: null })] });
  onProgress({ seq: 1, chatId: CHAT, stage: "send", done: 2, total: 2 });
  expect(getFeed(CHAT).messages[0].send).toBeNull();
});
