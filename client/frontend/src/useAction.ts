// Действие, обращающееся к Go: признак выполнения, защита от повторного нажатия, ошибка.
import { useCallback, useEffect, useRef, useState } from "react";
import { parseError, type AppError } from "./backend";
import { setState } from "./store";

export type Result<R> = { ok: true; value: R } | { ok: false; error?: AppError };

/** Ошибки сертификата открывают окно настроек, где пользователь выбирает файл. */
export function routeError(err: AppError) {
  if (err.code === "CERT_INVALID" || err.code === "CERT_FILE") {
    setState({ screen: "settings", settingsReason: err.message });
  }
}

export function useAction<A extends unknown[], R>(fn: (...args: A) => Promise<R>) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<AppError | null>(null);
  const running = useRef(false);
  const mounted = useRef(true);
  const fnRef = useRef(fn);
  fnRef.current = fn;

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const run = useCallback(async (...args: A): Promise<Result<R>> => {
    if (running.current) return { ok: false };
    running.current = true;
    setBusy(true);
    setError(null);
    try {
      const value = await fnRef.current(...args);
      return { ok: true, value };
    } catch (e) {
      const err = parseError(e);
      if (mounted.current) setError(err);
      routeError(err);
      return { ok: false, error: err };
    } finally {
      running.current = false;
      if (mounted.current) setBusy(false);
    }
  }, []);

  return { run, busy, error, setError };
}
