import { useState } from "react";
import { api, type ChatView, type ClosedChatView, type KeyState } from "../backend";
import ConfirmDialog from "../components/ConfirmDialog";
import CreateChatDialog from "../components/CreateChatDialog";
import { useStore } from "../store";
import { useAction } from "../useAction";
import ClosedChats from "./ClosedChats";
import ConversationView, { receiveText } from "./ConversationView";

const OFFLINE_HINT = "нет связи с сервером";

export const keyStateText: Record<KeyState, string> = {
  NONE: "подключитесь, чтобы согласовать ключ",
  WAITING_SELF: "публикация значения…",
  WAITING_PEER: "ждём собеседника",
  READY: "ключ согласован",
  KEY_LOST: "ключ на этом устройстве получить нельзя: закройте чат и создайте новый",
  PEER_VALUE_INVALID: "собеседник прислал недопустимое значение: закройте чат",
};

const modeTitle: Record<string, string> = { RANDOM_DELTA: "Random Delta" };
const paddingTitle: Record<string, string> = { ZEROS: "Zeros", ANSI_X923: "ANSI X.923", ISO_10126: "ISO 10126" };

export function paramsText(c: Pick<ChatView, "algorithm" | "keyBits" | "cipherMode" | "padding">) {
  return [c.algorithm, String(c.keyBits), modeTitle[c.cipherMode] ?? c.cipherMode, paddingTitle[c.padding] ?? c.padding].join(" · ");
}

type ItemProps = { chat: ChatView; offline: boolean; onClose: (c: ChatView) => void; onOpen: (c: ChatView) => void };

function ChatItem({ chat, offline, onClose, onOpen }: ItemProps) {
  const connect = useAction(api.connectChat);
  const disconnect = useAction(api.disconnectChat);
  const here = useAction(api.receiveHere);
  const busy = connect.busy || disconnect.busy;
  const error = connect.error ?? disconnect.error ?? here.error;
  const broken = chat.keyState === "KEY_LOST" || chat.keyState === "PEER_VALUE_INVALID";
  return (
    <li data-testid="chat-item">
      <div className="grow">
        <div>
          <button type="button" className="link" onClick={() => onOpen(chat)} data-testid="chat-open">
            <strong>{chat.peer}</strong>
          </button>{" "}
          {chat.unread > 0 && (
            <span className="badge" data-testid="chat-unread" title="новые сообщения">
              {chat.unread}
            </span>
          )}{" "}
          <span className="chat-meta">создатель: {chat.creator}</span>
        </div>
        <div className="chat-meta">
          <span data-testid="chat-params">{paramsText(chat)}</span> ·{" "}
          <span data-testid="chat-date">{new Date(chat.createdAt).toLocaleString("ru-RU")}</span>
        </div>
        <div className="chat-meta" data-testid="chat-connections">
          вы: {chat.selfConnected ? "подключены" : "отключены"}, собеседник: {chat.peerConnected ? "подключён" : "отключён"}
        </div>
        <div className={`key-${chat.keyState}`} data-testid="chat-key">
          {keyStateText[chat.keyState] ?? chat.keyState}
        </div>
        <div className="chat-meta" data-testid="chat-receive">
          {receiveText(chat)}
          {chat.receiveState === "ELSEWHERE" && (
            <>
              {" "}
              <button type="button" disabled={here.busy || offline} onClick={() => void here.run(chat.id)} data-testid="chat-receive-here">
                {here.busy ? "Подключение…" : "Получать здесь"}
              </button>
            </>
          )}
        </div>
        {broken && (
          <div className="hint" data-testid="chat-key-hint">
            Закройте этот чат и создайте новый.
          </div>
        )}
        {chat.algorithmNote && (
          <div className="warning" data-testid="chat-note">
            ⚠ {chat.algorithmNote}
          </div>
        )}
        {error && <div className="error">{error.message}</div>}
      </div>
      {chat.selfConnected ? (
        <button type="button" disabled={busy || offline} title={offline ? OFFLINE_HINT : undefined} onClick={() => void disconnect.run(chat.id)}>
          {disconnect.busy ? "Отключение…" : "Отключиться"}
        </button>
      ) : (
        <button type="button" className="primary" disabled={busy || offline} title={offline ? OFFLINE_HINT : undefined} onClick={() => void connect.run(chat.id)}>
          {connect.busy ? "Подключение…" : "Подключиться"}
        </button>
      )}
      <button type="button" className="danger" disabled={busy || offline} title={offline ? OFFLINE_HINT : undefined} onClick={() => onClose(chat)}>
        Закрыть чат
      </button>
    </li>
  );
}

/** Вкладка «Чаты»: активные чаты, новые первыми. */
export default function ChatsTab({ onOpenContacts }: { onOpenContacts: () => void }) {
  const snapshot = useStore((s) => s.snapshot);
  const offline = snapshot.connection === "offline";
  const [creating, setCreating] = useState(false);
  const [closing, setClosing] = useState<ChatView | null>(null);
  const [open, setOpen] = useState<{ id: string; closed?: ClosedChatView } | null>(null);
  const close = useAction(api.closeChat);

  if (open) {
    return <ConversationView chatId={open.id} closed={open.closed} onBack={() => setOpen(null)} />;
  }

  return (
    <section className="chats">
      <div className="row">
        <button type="button" className="primary" disabled={offline} title={offline ? OFFLINE_HINT : undefined} onClick={() => setCreating(true)}>
          Новый чат
        </button>
      </div>
      <ul className="list">
        {snapshot.chats.length === 0 && (
          <li className="hint" data-testid="chats-empty">
            Создайте чат с контактом: кнопка «Новый чат»
          </li>
        )}
        {snapshot.chats.map((c) => (
          <ChatItem key={c.id} chat={c} offline={offline} onClose={setClosing} onOpen={(x) => setOpen({ id: x.id })} />
        ))}
      </ul>
      <ClosedChats onHistory={(c) => setOpen({ id: c.id, closed: c })} />
      {creating && <CreateChatDialog contacts={snapshot.contacts} onClose={() => setCreating(false)} onOpenContacts={onOpenContacts} />}
      {closing && (
        <ConfirmDialog
          title={`Закрыть чат с ${closing.peer}?`}
          text="Чат будет закрыт для обоих участников, ключ удалён с устройства"
          confirmLabel="Закрыть чат"
          busy={close.busy}
          error={close.error?.message}
          onCancel={() => {
            close.setError(null);
            setClosing(null);
          }}
          onConfirm={async () => {
            const r = await close.run(closing.id);
            if (r.ok) setClosing(null);
          }}
        />
      )}
    </section>
  );
}
