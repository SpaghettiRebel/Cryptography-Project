import { useEffect, useLayoutEffect, useRef } from "react";
import { api, type ChatView, type ClosedChatView } from "../backend";
import Composer from "../components/Composer";
import MessageItem, { reason } from "../components/MessageItem";
import { loadOlder, onChange, useFeed } from "../messages";
import { useStore } from "../store";
import { keyStateText, paramsText } from "./ChatsTab";

export const receiveText = (c: ChatView): string => {
  switch (c.receiveState) {
    case "RECEIVING":
      return "приём";
    case "OPENING":
      return "подключение…";
    case "RETRYING":
      return `повтор: ${reason(c.receiveReason, c.receiveReason)}`;
    case "ELSEWHERE":
      return "приём на другом устройстве";
    default:
      return "приём выключен";
  }
};

type Props = {
  chatId: string;
  /** Закрытый чат: лента только для чтения. */
  closed?: ClosedChatView;
  onBack: () => void;
};

/** Окно переписки: заголовок, лента сообщений и поле ввода. */
export default function ConversationView({ chatId, closed, onBack }: Props) {
  const chat = useStore((s) => s.snapshot.chats.find((c) => c.id === chatId));
  const feed = useFeed(chatId);
  const readOnly = !!closed || !chat;
  const listRef = useRef<HTMLUListElement>(null);
  // Прокрутка: при открытии и новом сообщении — к концу, если пользователь был внизу; при подгрузке
  // ранних сообщений видимое место ленты не сдвигается.
  const atBottom = useRef(true);
  const shown = useRef<{ chat: string; first: number; last: number; height: number } | null>(null);

  useLayoutEffect(() => {
    const el = listRef.current;
    if (!el || !feed.loaded) return;
    const msgs = feed.messages;
    const first = msgs.length > 0 ? msgs[0].seq : 0;
    const last = msgs.length > 0 ? msgs[msgs.length - 1].seq : 0;
    const prev = shown.current?.chat === chatId ? shown.current : null;
    if (prev === null) {
      el.scrollTop = el.scrollHeight;
      atBottom.current = true;
    } else if (first !== 0 && prev.first !== 0 && first < prev.first && last === prev.last) {
      el.scrollTop += el.scrollHeight - prev.height;
    } else if (last !== prev.last && atBottom.current) {
      el.scrollTop = el.scrollHeight;
    }
    shown.current = { chat: chatId, first, last, height: el.scrollHeight };
  }, [chatId, feed.messages, feed.loaded]);

  useEffect(() => {
    if (readOnly) return;
    void api.markRead(chatId).catch(() => undefined);
    // Пока окно открыто, новые входящие этого чата сразу считаются просмотренными.
    return onChange((e) => {
      if (e.chatId === chatId && e.changed.some((m) => m.direction === "in" && m.state === "DONE")) {
        void api.markRead(chatId).catch(() => undefined);
      }
    });
  }, [chatId, readOnly]);

  const header = chat ?? closed;
  return (
    <section className="conversation" data-testid="conversation">
      <div className="row">
        <button type="button" onClick={onBack} data-testid="conversation-back">
          Назад к чатам
        </button>
        <div className="grow">
          <strong>{header?.peer ?? ""}</strong>{" "}
          {header && <span className="chat-meta">{paramsText(header as ChatView)}</span>}
          {chat && (
            <div className="chat-meta" data-testid="conversation-state">
              {keyStateText[chat.keyState] ?? chat.keyState} · {receiveText(chat)}
            </div>
          )}
        </div>
      </div>
      {readOnly && (
        <div className="hint" data-testid="conversation-closed">
          Чат закрыт
        </div>
      )}
      <ul
        className="feed"
        ref={listRef}
        data-testid="feed"
        onScroll={(e) => {
          const el = e.currentTarget;
          atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
          if (el.scrollTop === 0) void loadOlder(chatId).catch(() => undefined);
        }}
      >
        {feed.hasMore && (
          <li>
            <button type="button" className="link" onClick={() => void loadOlder(chatId).catch(() => undefined)} data-testid="feed-older">
              Показать более ранние
            </button>
          </li>
        )}
        {feed.loaded && feed.messages.length === 0 && (
          <li className="hint" data-testid="feed-empty">
            Сообщений пока нет
          </li>
        )}
        {feed.messages.map((m) => (
          <MessageItem key={m.seq} message={m} readOnly={readOnly} />
        ))}
      </ul>
      {!readOnly && chat && <Composer chat={chat} />}
    </section>
  );
}
