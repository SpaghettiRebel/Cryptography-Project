import { useEffect, useState, type FormEvent } from "react";
import { api, type Settings } from "../backend";
import Banner from "../components/Banner";
import { applySnapshot, setState, useStore } from "../store";
import { useAction } from "../useAction";

type Tab = "login" | "register";

/** Окно входа и регистрации. */
export default function LoginView() {
  const banner = useStore((s) => s.banner);
  const prefill = useStore((s) => s.loginPrefill);
  const [tab, setTab] = useState<Tab>("login");
  const [login, setLogin] = useState(prefill);
  const [password, setPassword] = useState("");
  const [info, setInfo] = useState<string | null>(null);
  const [settings, setSettings] = useState<Settings | null>(null);

  const submitLogin = useAction(api.login);
  const submitRegister = useAction(api.register);
  const action = tab === "login" ? submitLogin : submitRegister;
  const error = action.error;

  useEffect(() => {
    let alive = true;
    api.getSettings().then(
      (s) => alive && setSettings(s),
      () => undefined,
    );
    return () => {
      alive = false;
    };
  }, []);

  useEffect(() => setLogin(prefill), [prefill]);

  const switchTab = (t: Tab) => {
    setTab(t);
    setInfo(null);
    submitLogin.setError(null);
    submitRegister.setError(null);
  };

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setInfo(null);
    if (tab === "register") {
      const r = await submitRegister.run(login, password);
      if (r.ok) {
        setPassword("");
        setTab("login");
        setInfo("Учётная запись создана. Войдите с этим логином и паролем.");
      }
      return;
    }
    const r = await submitLogin.run(login, password);
    if (r.ok) {
      setPassword("");
      applySnapshot(r.value);
      setState({ screen: "main", banner: null });
    }
  };

  const invalid = (field: string) => error?.field === field;

  return (
    <main className="screen login">
      <h1>Крипточат</h1>
      {banner && <Banner kind="warning" text={banner} onClose={() => setState({ banner: null })} />}
      <div role="tablist" className="tabs">
        <button type="button" role="tab" aria-selected={tab === "login"} onClick={() => switchTab("login")}>
          Вход
        </button>
        <button type="button" role="tab" aria-selected={tab === "register"} onClick={() => switchTab("register")}>
          Регистрация
        </button>
      </div>
      <form onSubmit={onSubmit} className="card">
        <label>
          Логин
          <input
            data-testid="login-input"
            value={login}
            autoComplete="username"
            aria-invalid={invalid("login")}
            onChange={(e) => setLogin(e.target.value)}
          />
        </label>
        <small data-testid="login-hint">3–32 символа: латинские буквы, цифры, «_», «.», «-»</small>
        <label>
          Пароль
          <input
            data-testid="password-input"
            type="password"
            value={password}
            autoComplete={tab === "login" ? "current-password" : "new-password"}
            aria-invalid={invalid("password")}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        <small data-testid="password-hint">от 8 до 128 байт; пароль не сохраняется на устройстве</small>
        {error && (
          <p className="error" role="alert" data-testid="form-error">
            {error.message}
          </p>
        )}
        {info && (
          <p className="info" data-testid="form-info">
            {info}
          </p>
        )}
        <button type="submit" className="primary" disabled={action.busy} data-testid="submit">
          {action.busy ? "Подождите…" : tab === "login" ? "Войти" : "Зарегистрироваться"}
        </button>
      </form>
      {settings && settings.recentLogins.length > 0 && (
        <div className="recent">
          <span>Недавние:</span>
          {settings.recentLogins.map((l) => (
            <button type="button" className="link" key={l} onClick={() => setLogin(l)}>
              {l}
            </button>
          ))}
        </div>
      )}
      <footer className="server">
        <span data-testid="server-line">Сервер: {settings ? settings.serverAddress : "…"}</span>
        <button type="button" onClick={() => setState({ screen: "settings", settingsReason: null })}>
          Настройки сервера
        </button>
      </footer>
    </main>
  );
}
