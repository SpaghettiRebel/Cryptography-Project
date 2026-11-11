import type { MessageView, Progress } from "../backend";

const stages = [
  ["encrypt", "Шифрование"],
  ["send", "Передача"],
  ["receive", "Получение"],
  ["decrypt", "Расшифрование"],
] as const;

/** Процент выполнения; пустые данные (0 из 0) — 100%. */
export function percent(p: Progress): number {
  if (p.total <= 0) return 100;
  return Math.min(100, Math.floor((p.done * 100) / p.total));
}

/** Полосы прогресса стадий сообщения. */
export default function ProgressBars({ message }: { message: MessageView }) {
  const bars = stages.filter(([key]) => message[key] !== null);
  if (bars.length === 0) return null;
  return (
    <div className="progress-bars">
      {bars.map(([key, title]) => {
        const p = message[key] as Progress;
        const value = percent(p);
        return (
          <label key={key} className="progress-row" data-testid={`progress-${key}`}>
            <span>{title}</span>
            <progress max={100} value={value} />
            <span className="progress-value">{value}%</span>
          </label>
        );
      })}
    </div>
  );
}
