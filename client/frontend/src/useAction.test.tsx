import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { getState } from "./store";
import * as W from "./test/wails";
import { useAction } from "./useAction";

function Button({ fn }: { fn: () => Promise<unknown> }) {
  const a = useAction(fn);
  return (
    <div>
      <button disabled={a.busy} onClick={() => void a.run()} data-testid="btn">
        {a.busy ? "идёт" : "нажать"}
      </button>
      <button onClick={() => void a.run()} data-testid="again">
        ещё раз
      </button>
      {a.error && <p data-testid="err">{a.error.message}</p>}
    </div>
  );
}

test("признак выполнения и защита от повторного нажатия", async () => {
  let release!: () => void;
  const fn = vi.fn(() => new Promise<void>((r) => (release = r)));
  render(<Button fn={fn} />);
  fireEvent.click(screen.getByTestId("btn"));
  expect(screen.getByTestId("btn").textContent).toBe("идёт");
  expect((screen.getByTestId("btn") as HTMLButtonElement).disabled).toBe(true);
  // Обходим неактивную кнопку: второй вызов во время выполнения не доходит до Go.
  fireEvent.click(screen.getByTestId("again"));
  fireEvent.click(screen.getByTestId("again"));
  expect(fn).toHaveBeenCalledTimes(1);
  await act(async () => release());
  await waitFor(() => expect(screen.getByTestId("btn").textContent).toBe("нажать"));
  fireEvent.click(screen.getByTestId("btn"));
  expect(fn).toHaveBeenCalledTimes(2);
});

test("ошибка показывается, окно остаётся рабочим", async () => {
  const fn = vi.fn(async () => {
    throw W.goError("INTERNAL", "Внутренняя ошибка клиента");
  });
  render(<Button fn={fn} />);
  fireEvent.click(screen.getByTestId("btn"));
  await waitFor(() => expect(screen.getByTestId("err").textContent).toBe("Внутренняя ошибка клиента"));
  expect((screen.getByTestId("btn") as HTMLButtonElement).disabled).toBe(false);
});

test("ошибка сертификата открывает настройки", async () => {
  const fn = vi.fn(async () => {
    throw W.goError("CERT_INVALID", "Сертификат не подходит");
  });
  render(<Button fn={fn} />);
  fireEvent.click(screen.getByTestId("btn"));
  await waitFor(() => expect(getState().screen).toBe("settings"));
  expect(getState().settingsReason).toBe("Сертификат не подходит");
});
