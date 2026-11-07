type Props = {
  title: string;
  text: string;
  confirmLabel: string;
  busy?: boolean;
  error?: string | null;
  onConfirm: () => void;
  onCancel: () => void;
};

/** Подтверждение необратимого действия. */
export default function ConfirmDialog({ title, text, confirmLabel, busy, error, onConfirm, onCancel }: Props) {
  return (
    <div className="overlay">
      <div className="dialog" role="dialog" aria-modal="true" aria-label={title} data-testid="confirm-dialog">
        <h2>{title}</h2>
        <p data-testid="confirm-text">{text}</p>
        {error && <p className="error">{error}</p>}
        <div className="actions">
          <button type="button" onClick={onCancel} disabled={busy}>
            Отмена
          </button>
          <button type="button" className="danger" onClick={onConfirm} disabled={busy} data-testid="confirm-ok">
            {busy ? "Выполняется…" : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
