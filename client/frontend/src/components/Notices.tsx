import { useEffect } from "react";
import { dismissNotice, useStore } from "../store";

/** Через сколько закрываются уведомления, кроме ошибок. */
export const NOTICE_TIMEOUT_MS = 8000;

/** Всплывающие уведомления: закрываются кнопкой или через 8 с; ошибки остаются до закрытия. */
export default function Notices() {
  const notices = useStore((s) => s.notices);

  useEffect(() => {
    const timers = notices
      .filter((n) => n.kind !== "error")
      .map((n) => setTimeout(() => dismissNotice(n.id), NOTICE_TIMEOUT_MS));
    return () => timers.forEach(clearTimeout);
  }, [notices]);

  if (notices.length === 0) return null;
  return (
    <div className="notices" aria-live="polite">
      {notices.map((n) => (
        <div key={n.id} className={`notice notice-${n.kind}`} data-testid="notice" role={n.kind === "error" ? "alert" : "status"}>
          <span>{n.message}</span>
          <button type="button" className="link" aria-label="Закрыть уведомление" onClick={() => dismissNotice(n.id)}>
            ×
          </button>
        </div>
      ))}
    </div>
  );
}
