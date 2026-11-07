import { fireEvent, render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import StatusBar from "./StatusBar";

test("индикатор связи по снимку", () => {
  const { rerender } = render(<StatusBar login="alice" connection="online" loggingOut={false} onLogout={() => undefined} />);
  expect(screen.getByTestId("connection").textContent).toBe("в сети");
  expect(screen.getByTestId("current-login").textContent).toBe("alice");
  rerender(<StatusBar login="alice" connection="offline" loggingOut={false} onLogout={() => undefined} />);
  expect(screen.getByTestId("connection").textContent).toBe("нет связи");
  rerender(<StatusBar login="alice" connection="connecting" loggingOut={false} onLogout={() => undefined} />);
  expect(screen.getByTestId("connection").textContent).toBe("подключение…");
});

test("кнопка «Выйти» и признак выполнения", () => {
  const out = vi.fn();
  const { rerender } = render(<StatusBar login="alice" connection="online" loggingOut={false} onLogout={out} />);
  fireEvent.click(screen.getByTestId("logout"));
  expect(out).toHaveBeenCalledTimes(1);
  rerender(<StatusBar login="alice" connection="online" loggingOut={true} onLogout={out} />);
  expect((screen.getByTestId("logout") as HTMLButtonElement).disabled).toBe(true);
  expect(screen.getByTestId("logout").textContent).toBe("Выход…");
});
