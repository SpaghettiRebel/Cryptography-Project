import { useEffect, useState } from "react";
import { api, type AppError, type Settings } from "../backend";
import Banner from "../components/Banner";
import { setState, useStore } from "../store";
import { useAction } from "../useAction";

const fieldOf: Record<string, string> = { INVALID_ADDRESS: "address", CERT_FILE: "cert", CERT_INVALID: "cert" };

/** Адрес сервера и файл его сертификата. */
export default function SettingsView() {
  const reason = useStore((s) => s.settingsReason);
  const [form, setForm] = useState<Settings | null>(null);
  const [checkResult, setCheckResult] = useState<string | null>(null);
  const [error, setError] = useState<AppError | null>(null);

  const save = useAction(api.saveSettings);
  const check = useAction(async (s: Settings) => {
    await api.saveSettings(s);
    await api.checkServer();
  });
  const choose = useAction(api.chooseCertificate);

  useEffect(() => {
    let alive = true;
    api.getSettings().then(
      (s) => alive && setForm(s),
      () => alive && setForm({ serverAddress: "localhost:8443", certificatePath: "", recentLogins: [] }),
    );
    return () => {
      alive = false;
    };
  }, []);

  if (!form) return <main className="screen">Загрузка…</main>;

  const busy = save.busy || check.busy || choose.busy;
  const invalid = (f: string) => (error ? fieldOf[error.code] === f : false);

  const onSave = async () => {
    setError(null);
    setCheckResult(null);
    const r = await save.run(form);
    if (r.ok) setState({ screen: "login", settingsReason: null });
    else if (r.error) setError(r.error);
  };

  const onCheck = async () => {
    setError(null);
    setCheckResult(null);
    const r = await check.run(form);
    if (r.ok) {
      setCheckResult("Связь с сервером есть");
      setState({ settingsReason: null });
    } else if (r.error) setError(r.error);
  };

  const onChoose = async () => {
    const r = await choose.run();
    if (r.ok && r.value) setForm({ ...form, certificatePath: r.value });
  };

  return (
    <main className="screen settings">
      <h1>Настройки сервера</h1>
      {reason && <Banner kind="error" text={reason} testId="settings-reason" />}
      <div className="card">
        <label>
          Адрес сервера
          <input
            data-testid="address-input"
            value={form.serverAddress}
            placeholder="localhost:8443"
            aria-invalid={invalid("address")}
            onChange={(e) => setForm({ ...form, serverAddress: e.target.value })}
          />
        </label>
        <small>хост:порт, например localhost:8443</small>
        <label>
          Сертификат сервера
          <div className="row">
            <input
              data-testid="cert-input"
              value={form.certificatePath}
              placeholder="путь к server.crt"
              aria-invalid={invalid("cert")}
              onChange={(e) => setForm({ ...form, certificatePath: e.target.value })}
            />
            <button type="button" onClick={onChoose} disabled={busy}>
              Выбрать файл…
            </button>
          </div>
        </label>
        <small>файл deploy/certs/server.crt, который создаёт развёртывание сервера</small>
        {error && (
          <p className="error" role="alert" data-testid="settings-error">
            {error.message}
          </p>
        )}
        {checkResult && (
          <p className="info" data-testid="check-result">
            {checkResult}
          </p>
        )}
        <div className="actions">
          <button type="button" onClick={() => setState({ screen: "login", settingsReason: null })} disabled={busy}>
            Назад
          </button>
          <button type="button" onClick={onCheck} disabled={busy}>
            {check.busy ? "Проверка…" : "Проверить связь"}
          </button>
          <button type="button" className="primary" onClick={onSave} disabled={busy}>
            {save.busy ? "Сохранение…" : "Сохранить"}
          </button>
        </div>
      </div>
    </main>
  );
}
