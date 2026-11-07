import { useEffect, useState } from "react";
import { api, parseError, type AppError, type ChatOptions } from "../backend";
import { useAction } from "../useAction";

type Props = {
  contacts: string[];
  onClose: () => void;
  onOpenContacts: () => void;
};

const errorField: Record<string, string> = {
  NOT_CONTACT: "peer",
  SELF_CHAT: "peer",
  ALGORITHM_UNAVAILABLE: "algorithm",
  algorithm: "algorithm",
  key_bits: "key_bits",
  cipher_mode: "cipher_mode",
  padding_mode: "padding_mode",
};

/** Диалог «Новый чат»: собеседник и параметры из ChatOptions() — состава этой сборки. */
export default function CreateChatDialog({ contacts, onClose, onOpenContacts }: Props) {
  const [options, setOptions] = useState<ChatOptions | null>(null);
  const [loadError, setLoadError] = useState<AppError | null>(null);
  const [peer, setPeer] = useState(contacts[0] ?? "");
  const [algorithm, setAlgorithm] = useState("");
  const [keyBits, setKeyBits] = useState(256);
  const [mode, setMode] = useState("CBC");
  const [padding, setPadding] = useState("PKCS7");
  const create = useAction(api.createChat);

  useEffect(() => {
    let alive = true;
    api.chatOptions().then(
      (o) => {
        if (!alive) return;
        setOptions(o);
        setAlgorithm(o.algorithms.find((a) => a.available)?.id ?? "");
      },
      (e) => {
        if (alive) setLoadError(parseError(e));
      },
    );
    return () => {
      alive = false;
    };
  }, []);

  const error = create.error;
  const invalidField = error ? (errorField[error.field ?? ""] ?? errorField[error.code]) : undefined;
  const anyAvailable = options?.algorithms.some((a) => a.available) ?? false;
  const chosen = options?.algorithms.find((a) => a.id === algorithm);
  const modeWarning = options?.cipherModes.find((m) => m.id === mode)?.warning ?? "";
  const paddingWarning = options?.paddings.find((p) => p.id === padding)?.warning ?? "";

  const onCreate = async () => {
    const r = await create.run({ peer, algorithm, keyBits, cipherMode: mode, padding });
    if (r.ok) onClose();
  };

  return (
    <div className="overlay">
      <div className="dialog" role="dialog" aria-modal="true" aria-label="Новый чат" data-testid="create-chat-dialog">
        <h2>Новый чат</h2>
        {contacts.length === 0 ? (
          <>
            <p className="hint" data-testid="no-contacts">
              Чат создаётся только с контактом. Добавьте собеседника во вкладке «Контакты».
            </p>
            <div className="actions">
              <button type="button" onClick={onClose}>
                Отмена
              </button>
              <button
                type="button"
                className="primary"
                onClick={() => {
                  onClose();
                  onOpenContacts();
                }}
              >
                Перейти к контактам
              </button>
            </div>
          </>
        ) : !options ? (
          <>
            {/* Варианты не загрузились — показать причину; закрыть диалог можно и во время загрузки. */}
            {loadError ? (
              <p className="error" role="alert" data-testid="options-error">
                Не удалось получить параметры чата: {loadError.message}
              </p>
            ) : (
              <p>Загрузка…</p>
            )}
            <div className="actions">
              <button type="button" onClick={onClose}>
                {loadError ? "Закрыть" : "Отмена"}
              </button>
            </div>
          </>
        ) : (
          <>
            <label>
              Собеседник
              <select data-testid="peer-select" value={peer} aria-invalid={invalidField === "peer"} onChange={(e) => setPeer(e.target.value)}>
                {contacts.map((c) => (
                  <option key={c} value={c}>
                    {c}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Алгоритм
              <select
                data-testid="algorithm-select"
                value={algorithm}
                aria-invalid={invalidField === "algorithm"}
                onChange={(e) => setAlgorithm(e.target.value)}
              >
                {!anyAvailable && <option value="">— нет доступных —</option>}
                {options.algorithms.map((a) => (
                  <option key={a.id} value={a.id} disabled={!a.available} title={a.note}>
                    {a.id}
                    {!a.available ? " (недоступен)" : a.substitute ? " ⚠ тестовая замена" : ""}
                  </option>
                ))}
              </select>
            </label>
            <ul className="hint" data-testid="algorithm-notes">
              {options.algorithms
                .filter((a) => !a.available)
                .map((a) => (
                  <li key={a.id}>
                    {a.id}: {a.note}
                  </li>
                ))}
            </ul>
            {chosen?.substitute && (
              <p className="warning" data-testid="substitute-note">
                ⚠ {chosen.note}
              </p>
            )}
            <label>
              Длина ключа, бит
              <select
                data-testid="keybits-select"
                value={keyBits}
                aria-invalid={invalidField === "key_bits"}
                onChange={(e) => setKeyBits(Number(e.target.value))}
              >
                {options.keyBits.map((b) => (
                  <option key={b} value={b}>
                    {b}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Режим шифрования
              <select data-testid="mode-select" value={mode} aria-invalid={invalidField === "cipher_mode"} onChange={(e) => setMode(e.target.value)}>
                {options.cipherModes.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.title}
                  </option>
                ))}
              </select>
            </label>
            {modeWarning && (
              <p className="warning" data-testid="mode-warning">
                {modeWarning}
              </p>
            )}
            <label>
              Набивка
              <select
                data-testid="padding-select"
                value={padding}
                aria-invalid={invalidField === "padding_mode"}
                onChange={(e) => setPadding(e.target.value)}
              >
                {options.paddings.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.title}
                  </option>
                ))}
              </select>
            </label>
            {paddingWarning && (
              <p className="warning" data-testid="padding-warning">
                {paddingWarning}
              </p>
            )}
            {!anyAvailable && (
              <p className="hint" data-testid="no-algorithms">
                В этой сборке нет доступных алгоритмов: создать чат нельзя.
              </p>
            )}
            {error && (
              <p className="error" role="alert" data-testid="create-error">
                {error.message}
              </p>
            )}
            <div className="actions">
              <button type="button" onClick={onClose} disabled={create.busy}>
                Отмена
              </button>
              <button
                type="button"
                className="primary"
                onClick={onCreate}
                disabled={create.busy || !anyAvailable || !chosen?.available || peer === ""}
                title={!anyAvailable ? "нет доступных алгоритмов" : undefined}
              >
                {create.busy ? "Создание…" : "Создать"}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
