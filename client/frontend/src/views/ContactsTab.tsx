import { useState, type FormEvent } from "react";
import { api } from "../backend";
import ConfirmDialog from "../components/ConfirmDialog";
import { useStore } from "../store";
import { useAction } from "../useAction";

const OFFLINE_HINT = "нет связи с сервером";

function chatsWord(n: number) {
  const mod10 = n % 10;
  const mod100 = n % 100;
  if (mod10 === 1 && mod100 !== 11) return "активный чат";
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return "активных чата";
  return "активных чатов";
}

function Answer({ login, offline }: { login: string; offline: boolean }) {
  const accept = useAction(api.acceptContact);
  const reject = useAction(api.rejectContact);
  const busy = accept.busy || reject.busy;
  const error = accept.error ?? reject.error;
  return (
    <li>
      <span className="grow">{login}</span>
      <button type="button" className="primary" disabled={busy || offline} title={offline ? OFFLINE_HINT : undefined} onClick={() => void accept.run(login)}>
        {accept.busy ? "…" : "Принять"}
      </button>
      <button type="button" disabled={busy || offline} title={offline ? OFFLINE_HINT : undefined} onClick={() => void reject.run(login)}>
        {reject.busy ? "…" : "Отклонить"}
      </button>
      {error && <span className="error">{error.message}</span>}
    </li>
  );
}

/** Вкладка «Контакты»: запрос по логину, контакты, входящие и исходящие запросы. */
export default function ContactsTab() {
  const snapshot = useStore((s) => s.snapshot);
  const offline = snapshot.connection === "offline";
  const [login, setLogin] = useState("");
  const [removing, setRemoving] = useState<string | null>(null);
  const send = useAction(api.sendContactRequest);
  const remove = useAction(api.removeContact);

  const onSend = async (e: FormEvent) => {
    e.preventDefault();
    const r = await send.run(login.trim());
    if (r.ok) setLogin("");
  };

  const activeWith = (peer: string) => snapshot.chats.filter((c) => c.peer.toLowerCase() === peer.toLowerCase()).length;

  const confirmText = (peer: string) => {
    const n = activeWith(peer);
    const base = `Удалить ${peer} из контактов?`;
    return n === 0 ? base : `${base} С этим контактом ${n} ${chatsWord(n)}: они будут закрыты для обоих участников, ключи удалены с устройства.`;
  };

  return (
    <section className="contacts">
      <form className="row" onSubmit={onSend}>
        <input
          data-testid="contact-input"
          placeholder="логин пользователя"
          value={login}
          aria-invalid={send.error !== null}
          onChange={(e) => {
            setLogin(e.target.value);
            send.setError(null);
          }}
        />
        <button type="submit" className="primary" disabled={send.busy || offline || login.trim() === ""} title={offline ? OFFLINE_HINT : undefined}>
          {send.busy ? "Отправка…" : "Отправить запрос"}
        </button>
      </form>
      {send.error && (
        <p className="error" role="alert" data-testid="contact-error">
          {send.error.message}
        </p>
      )}
      <div className="columns">
        <div>
          <h2>Контакты</h2>
          <ul className="list" data-testid="contacts-list">
            {snapshot.contacts.length === 0 && <li className="hint">Пока нет контактов</li>}
            {snapshot.contacts.map((c) => (
              <li key={c}>
                <span className="grow">{c}</span>
                <button type="button" className="danger" disabled={offline} title={offline ? OFFLINE_HINT : undefined} onClick={() => setRemoving(c)}>
                  Удалить
                </button>
              </li>
            ))}
          </ul>
        </div>
        <div>
          <h2>Входящие запросы</h2>
          <ul className="list" data-testid="incoming-list">
            {snapshot.incoming.length === 0 && <li className="hint">Нет входящих запросов</li>}
            {snapshot.incoming.map((c) => (
              <Answer key={c} login={c} offline={offline} />
            ))}
          </ul>
        </div>
        <div>
          <h2>Исходящие запросы</h2>
          <ul className="list" data-testid="outgoing-list">
            {snapshot.outgoing.length === 0 && <li className="hint">Нет исходящих запросов</li>}
            {snapshot.outgoing.map((c) => (
              <li key={c}>
                <span className="grow">{c}</span>
                <span className="hint">ждёт ответа</span>
              </li>
            ))}
          </ul>
        </div>
      </div>
      {removing && (
        <ConfirmDialog
          title="Удаление контакта"
          text={confirmText(removing)}
          confirmLabel="Удалить"
          busy={remove.busy}
          error={remove.error?.message}
          onCancel={() => {
            remove.setError(null);
            setRemoving(null);
          }}
          onConfirm={async () => {
            const r = await remove.run(removing);
            if (r.ok) setRemoving(null);
          }}
        />
      )}
    </section>
  );
}
