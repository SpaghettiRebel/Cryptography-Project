import { useState } from "react";
import { api, type MessageState, type MessageView } from "../backend";
import { upsert } from "../messages";
import { pushNotice } from "../store";
import { useAction } from "../useAction";
import ConfirmDialog from "./ConfirmDialog";
import ProgressBars from "./ProgressBars";

/** Короткие причины ожидания и повтора. */
export const reasonText: Record<string, string> = {
  QUEUE_FULL: "очередь собеседника заполнена",
  SERVER_UNAVAILABLE: "нет связи",
  TIMEOUT: "нет связи",
  CERT_INVALID: "нет связи",
  SERVER_BUSY: "сервер занят",
  CHAT_NOT_READY: "ключ не согласован на сервере",
  NOT_CONNECTED: "нет подключения к чату",
  ACK_TIMEOUT: "сервер прервал приём",
  LOCAL_WRITE: "ошибка записи на устройство",
  INTERNAL: "внутренняя ошибка",
};

export function reason(code: string, fallback = ""): string {
  return reasonText[code] ?? (fallback || code);
}

const stateWords: Record<MessageState, string> = {
  QUEUED: "в очереди",
  SENDING: "отправка",
  WAITING: "ожидание",
  SENT: "отправлено",
  CANCELLED: "отменено",
  FAILED: "ошибка",
  UNDELIVERED: "не доставлено",
  RECEIVING: "получение",
  DECRYPT_PENDING: "ожидает расшифрования",
  DECRYPTING: "расшифрование",
  DECRYPT_CANCELLED: "расшифрование отменено",
  DONE: "",
  CANCELLED_BY_PEER: "отменено отправителем",
  CORRUPTED: "повреждено",
  KEY_MISSING: "ключ удалён",
};

/** Состояние сообщения словами. */
export function stateText(m: MessageView): string {
  const base = stateWords[m.state] ?? m.state;
  if ((m.state === "WAITING" || m.state === "FAILED") && m.error) {
    return `${base}: ${reason(m.error.code, m.error.message)}`;
  }
  return base;
}

/** Подпись сообщения, вид которого ещё не известен (до расшифрования). */
export function pendingText(m: MessageView): string {
  if (m.state === "RECEIVING") return `получение…${m.receive ? ` ${m.receive.done} из ${m.receive.total} частей` : ""}`;
  if (m.state === "CORRUPTED" || m.state === "KEY_MISSING") return "сообщение недоступно";
  return stateText(m);
}

/** Размер в читаемом виде. */
export function sizeText(n: number): string {
  if (n < 0) return "";
  if (n < 1024) return `${n} Б`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} КиБ`;
  return `${(n / (1024 * 1024)).toFixed(1)} МиБ`;
}

type Props = { message: MessageView; readOnly?: boolean };

/** Сообщение ленты: текст, картинка, файл или получаемое; состояние, прогресс и действия. */
export default function MessageItem({ message: m, readOnly }: Props) {
  const save = useAction(api.saveFile);
  const cancel = useAction(api.cancelMessage);
  const retry = useAction(api.retryDecrypt);
  const [confirming, setConfirming] = useState(false);
  const status = stateText(m);
  const error = save.error ?? cancel.error ?? retry.error;

  const doCancel = async () => {
    const r = await cancel.run(m.seq);
    if (r.ok) upsert(r.value);
    setConfirming(false);
  };
  const onCancel = () => {
    if (m.direction === "out" && (m.send?.done ?? 0) > 0) setConfirming(true);
    else void doCancel();
  };
  const onRetry = async () => {
    const r = await retry.run(m.seq);
    if (r.ok) upsert(r.value);
  };
  const onSave = async () => {
    const r = await save.run(m.seq);
    // Пустой путь — диалог закрыт без выбора.
    if (r.ok && r.value) pushNotice({ kind: "info", code: "SAVED", message: `Сохранено: ${r.value}` });
  };
  const saveButton = (
    <button type="button" disabled={save.busy} onClick={() => void onSave()} data-testid="message-save">
      {save.busy ? "Сохранение…" : "Сохранить как…"}
    </button>
  );
  const saveable = m.state === "DONE" || m.state === "SENT";

  return (
    <li className={`message message-${m.direction}`} data-testid="message" data-seq={m.seq}>
      {m.kind === "text" && (
        <div className="message-text" data-testid="message-text">
          {m.text}
        </div>
      )}
      {m.kind === "image" && (
        <div className="message-image">
          {m.mediaUrl && <img src={m.mediaUrl} alt={m.fileName} data-testid="message-img" />}
          <div className="chat-meta">{m.fileName}</div>
          {saveable && saveButton}
        </div>
      )}
      {m.kind === "file" && (
        <div className="message-file" data-testid="message-file">
          <span aria-hidden="true">📄</span> <strong>{m.fileName}</strong>{" "}
          <span className="chat-meta" data-testid="message-size">
            {sizeText(m.size)}
          </span>{" "}
          {saveable && saveButton}
        </div>
      )}
      {m.kind === "pending" && (
        <div className="message-pending" data-testid="message-pending">
          {pendingText(m)}
        </div>
      )}
      <div className="chat-meta">
        <span>{new Date(m.createdAt).toLocaleString("ru-RU")}</span>
        {status && (
          <span className={`message-state state-${m.state}`} data-testid="message-state">
            {" "}
            · {status}
          </span>
        )}
      </div>
      {m.error && (
        <div className="hint" data-testid="message-error">
          {m.error.message}
        </div>
      )}
      {!readOnly && <ProgressBars message={m} />}
      {!readOnly && (m.cancellable || m.retryable) && (
        <div className="row">
          {m.cancellable && (
            <button type="button" className="danger" disabled={cancel.busy} onClick={onCancel} data-testid="message-cancel">
              {cancel.busy ? "Отмена…" : "Отменить"}
            </button>
          )}
          {m.retryable && (
            <button type="button" disabled={retry.busy} onClick={() => void onRetry()} data-testid="message-retry">
              Расшифровать снова
            </button>
          )}
        </div>
      )}
      {error && <div className="error">{error.message}</div>}
      {confirming && (
        <ConfirmDialog
          title="Отменить отправку?"
          text="Собеседник получит уведомление об отмене"
          confirmLabel="Отменить отправку"
          busy={cancel.busy}
          onCancel={() => setConfirming(false)}
          onConfirm={() => void doCancel()}
        />
      )}
    </li>
  );
}
