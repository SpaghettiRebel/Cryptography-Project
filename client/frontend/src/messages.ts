// Лента переписки по чатам: история из ListMessages, изменения из событий messages и progress.
// Здесь только готовые для показа данные: текст, имя и размер файла, состояния и прогресс.
import { useEffect, useSyncExternalStore } from "react";
import { api, type MessagesEvent, type MessageView, type ProgressEvent } from "./backend";

export const PAGE = 50;

export type Feed = { messages: MessageView[]; hasMore: boolean; loaded: boolean };

const empty: Feed = { messages: [], hasMore: false, loaded: false };
let feeds = new Map<string, Feed>();
const listeners = new Set<() => void>();
const changeHooks = new Set<(e: MessagesEvent) => void>();

function notify() {
  listeners.forEach((l) => l());
}

export function getFeed(chatId: string): Feed {
  return feeds.get(chatId) ?? empty;
}

function setFeed(chatId: string, f: Feed) {
  feeds = new Map(feeds).set(chatId, f);
  notify();
}

/** Сливает сообщения без дублей по seq; новые данные заменяют старые; порядок — по возрастанию seq. */
export function merge(list: MessageView[], incoming: MessageView[]): MessageView[] {
  const bySeq = new Map(list.map((m) => [m.seq, m]));
  for (const m of incoming) bySeq.set(m.seq, m);
  return [...bySeq.values()].sort((a, b) => a.seq - b.seq);
}

/** Состояния, в которых у стадии бывает прогресс. */
const stageStates: Record<ProgressEvent["stage"], MessageView["state"][]> = {
  encrypt: ["QUEUED", "SENDING", "WAITING"],
  send: ["QUEUED", "SENDING", "WAITING"],
  receive: ["RECEIVING"],
  decrypt: ["DECRYPTING"],
};

/** Прогресс подходит состоянию сообщения: запоздавшее событие (например, отправки у SENT) не показывается. */
export function progressFits(m: MessageView, stage: ProgressEvent["stage"]): boolean {
  return stageStates[stage]?.includes(m.state) ?? false;
}

/** Обновляет прогресс стадии одного сообщения; событие стадии, не подходящей состоянию, отбрасывается. */
export function withProgress(list: MessageView[], e: ProgressEvent): MessageView[] {
  let changed = false;
  const out = list.map((m) => {
    if (m.seq !== e.seq || !progressFits(m, e.stage)) return m;
    changed = true;
    return { ...m, [e.stage]: { done: e.done, total: e.total } };
  });
  return changed ? out : list;
}

/** Загружает последнюю страницу истории чата. */
export async function loadLatest(chatId: string) {
  const page = await api.listMessages(chatId, 0, PAGE);
  setFeed(chatId, { messages: page.messages, hasMore: page.hasMore, loaded: true });
}

/** Подгружает более ранние сообщения. */
export async function loadOlder(chatId: string) {
  const f = getFeed(chatId);
  if (!f.hasMore || f.messages.length === 0) return;
  const page = await api.listMessages(chatId, f.messages[0].seq, PAGE);
  const cur = getFeed(chatId);
  setFeed(chatId, { messages: merge(cur.messages, page.messages), hasMore: page.hasMore, loaded: true });
}

/** Событие messages: пустой список изменений — перечитать ленту. */
export function onMessages(e: MessagesEvent) {
  const f = feeds.get(e.chatId);
  if (f) {
    if (e.changed.length === 0) {
      void loadLatest(e.chatId).catch(() => undefined);
    } else {
      setFeed(e.chatId, { ...f, messages: merge(f.messages, e.changed) });
    }
  }
  changeHooks.forEach((h) => h(e));
}

export function onProgress(e: ProgressEvent) {
  const f = feeds.get(e.chatId);
  if (!f) return;
  const next = withProgress(f.messages, e);
  if (next !== f.messages) setFeed(e.chatId, { ...f, messages: next });
}

/** Добавляет сообщение, которое вернул вызов (SendText, SendFile, CancelMessage…). */
export function upsert(m: MessageView) {
  const f = getFeed(m.chatId);
  setFeed(m.chatId, { ...f, messages: merge(f.messages, [m]) });
}

/** Подписка на изменения сообщений (например, отметка просмотра открытого чата). */
export function onChange(h: (e: MessagesEvent) => void) {
  changeHooks.add(h);
  return () => {
    changeHooks.delete(h);
  };
}

export function resetFeeds() {
  feeds = new Map();
  changeHooks.clear();
  notify();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

/** Лента чата; при первом показе загружается история. */
export function useFeed(chatId: string): Feed {
  const f = useSyncExternalStore(subscribe, () => getFeed(chatId));
  useEffect(() => {
    void loadLatest(chatId).catch(() => undefined);
  }, [chatId]);
  return f;
}
