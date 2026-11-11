import { useState, type KeyboardEvent } from "react";
import { api, type ChatView, type SendBlock } from "../backend";
import { upsert } from "../messages";
import { useStore } from "../store";
import { useAction } from "../useAction";

export const MAX_TEXT = 64 * 1024;

export const blockHint: Record<Exclude<SendBlock, "">, string> = {
  KEY_NOT_READY: "Ключ ещё не согласован",
  NOT_CONNECTED: "Подключитесь к чату",
  RECEIVING_ELSEWHERE: "Приём идёт на другом устройстве",
  ALGORITHM_UNAVAILABLE: "Алгоритм чата не реализован в этой сборке",
  CHAT_CLOSED: "Чат закрыт",
};

const bytes = (s: string) => new TextEncoder().encode(s).length;

/** Поле ввода и кнопки «Отправить» и «Файл…». Enter — отправить, Shift+Enter — перевод строки. */
export default function Composer({ chat }: { chat: ChatView }) {
  const [text, setText] = useState("");
  const send = useAction(api.sendText);
  const file = useAction(api.sendFile);
  const connect = useAction(api.connectChat);
  const here = useAction(api.receiveHere);
  const offline = useStore((s) => s.snapshot.connection === "offline");
  const blocked = !chat.canSend;
  const size = bytes(text);
  const fieldError = send.error && (send.error.code === "EMPTY_TEXT" || send.error.code === "TEXT_TOO_LONG") ? send.error : null;
  const otherError = (send.error && !fieldError ? send.error : null) ?? file.error ?? connect.error ?? here.error;

  const submit = async () => {
    if (blocked) return;
    const r = await send.run(chat.id, text);
    if (r.ok) {
      upsert(r.value);
      setText("");
    }
  };
  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      void submit();
    }
  };
  const pick = async () => {
    const r = await file.run(chat.id);
    if (r.ok && r.value) upsert(r.value);
  };

  return (
    <div className="composer" data-testid="composer">
      {blocked && chat.sendBlock && (
        <div className="hint" data-testid="send-block">
          <span data-testid="send-block-text">{blockHint[chat.sendBlock] ?? chat.sendBlock}</span>
          {chat.sendBlock === "NOT_CONNECTED" && (
            <>
              {" "}
              <button type="button" className="primary" disabled={connect.busy || offline} onClick={() => void connect.run(chat.id)} data-testid="send-block-connect">
                {connect.busy ? "Подключение…" : "Подключиться"}
              </button>
            </>
          )}
          {chat.sendBlock === "RECEIVING_ELSEWHERE" && (
            <>
              {" "}
              <button type="button" disabled={here.busy || offline} onClick={() => void here.run(chat.id)} data-testid="send-block-here">
                {here.busy ? "Подключение…" : "Получать здесь"}
              </button>
            </>
          )}
        </div>
      )}
      <textarea
        aria-label="Сообщение"
        data-testid="composer-text"
        rows={3}
        value={text}
        disabled={blocked}
        onChange={(e) => {
          setText(e.target.value);
          if (fieldError) send.setError(null);
        }}
        onKeyDown={onKey}
      />
      {size > MAX_TEXT * 0.9 && (
        <div className={size > MAX_TEXT ? "error" : "hint"} data-testid="composer-size">
          {size} из {MAX_TEXT} байт
        </div>
      )}
      {fieldError && (
        <div className="error" data-testid="composer-error">
          {fieldError.message}
        </div>
      )}
      {otherError && <div className="error">{otherError.message}</div>}
      <div className="row">
        <button type="button" className="primary" disabled={blocked || send.busy} onClick={() => void submit()} data-testid="composer-send">
          {send.busy ? "Отправка…" : "Отправить"}
        </button>
        <button type="button" disabled={blocked || file.busy} onClick={() => void pick()} data-testid="composer-file">
          Файл…
        </button>
      </div>
    </div>
  );
}
