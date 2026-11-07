type Props = {
  kind?: "info" | "warning" | "error";
  text: string;
  onClose?: () => void;
  testId?: string;
};

/** Сообщение над формой или в верхней части окна. */
export default function Banner({ kind = "error", text, onClose, testId = "banner" }: Props) {
  return (
    <div className={`banner banner-${kind}`} role={kind === "error" ? "alert" : "status"} data-testid={testId}>
      <span>{text}</span>
      {onClose && (
        <button type="button" className="link" aria-label="Закрыть" onClick={onClose}>
          ×
        </button>
      )}
    </div>
  );
}
