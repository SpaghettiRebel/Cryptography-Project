import { useEffect, useState } from "react";
import { api, type ChatView, type ClosedChatView } from "../backend";
import ConfirmDialog from "../components/ConfirmDialog";
import { useAction } from "../useAction";
import { paramsText } from "./ChatsTab";

/** Раздел «Закрытые чаты»: история только для чтения и удаление истории с устройства. */
export default function ClosedChats({ onHistory }: { onHistory: (c: ClosedChatView) => void }) {
  const [open, setOpen] = useState(false);
  const [list, setList] = useState<ClosedChatView[]>([]);
  const [deleting, setDeleting] = useState<ClosedChatView | null>(null);
  const load = useAction(api.listClosedChats);
  const remove = useAction(api.deleteChatHistory);

  const reload = async () => {
    const r = await load.run();
    if (r.ok) setList(r.value ?? []);
  };
  useEffect(() => {
    if (open) void reload();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  return (
    <section className="closed-chats" data-testid="closed-chats">
      <button type="button" className="link" aria-expanded={open} onClick={() => setOpen(!open)} data-testid="closed-toggle">
        Закрытые чаты {open ? "▾" : "▸"}
      </button>
      {open && (
        <ul className="list">
          {list.length === 0 && !load.busy && <li className="hint">Закрытых чатов нет</li>}
          {list.map((c) => (
            <li key={c.id} data-testid="closed-item">
              <div className="grow">
                <strong>{c.peer}</strong> <span className="chat-meta">{paramsText(c as unknown as ChatView)}</span>
                <div className="chat-meta">
                  создан {new Date(c.createdAt).toLocaleString("ru-RU")}, закрыт {new Date(c.closedAt).toLocaleString("ru-RU")}, сообщений:{" "}
                  <span data-testid="closed-count">{c.messageCount}</span>
                </div>
              </div>
              <button type="button" onClick={() => onHistory(c)} data-testid="closed-history">
                История
              </button>
              <button type="button" className="danger" onClick={() => setDeleting(c)} data-testid="closed-delete">
                Удалить историю
              </button>
            </li>
          ))}
        </ul>
      )}
      {load.error && <div className="error">{load.error.message}</div>}
      {deleting && (
        <ConfirmDialog
          title={`Удалить историю чата с ${deleting.peer}?`}
          text="История и файлы этого чата будут удалены с устройства"
          confirmLabel="Удалить историю"
          busy={remove.busy}
          error={remove.error?.message}
          onCancel={() => {
            remove.setError(null);
            setDeleting(null);
          }}
          onConfirm={async () => {
            const r = await remove.run(deleting.id);
            if (r.ok) {
              setDeleting(null);
              await reload();
            }
          }}
        />
      )}
    </section>
  );
}
