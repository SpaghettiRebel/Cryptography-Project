import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { msg } from "../test/fixtures";
import ProgressBars, { percent } from "./ProgressBars";

test("полосы стадий с процентом, пустые данные — 100%", () => {
  render(
    <ProgressBars
      message={msg({ direction: "out", state: "SENDING", encrypt: { done: 0, total: 0 }, send: { done: 1, total: 4 } })}
    />,
  );
  expect(screen.getByTestId("progress-encrypt").textContent).toContain("Шифрование");
  expect(screen.getByTestId("progress-encrypt").textContent).toContain("100%");
  expect(screen.getByTestId("progress-send").textContent).toContain("Передача");
  expect(screen.getByTestId("progress-send").textContent).toContain("25%");
  expect(screen.queryByTestId("progress-receive")).toBeNull();
});

test("получение и расшифрование", () => {
  render(<ProgressBars message={msg({ state: "DECRYPTING", receive: { done: 3, total: 3 }, decrypt: { done: 1, total: 3 } })} />);
  expect(screen.getByTestId("progress-receive").textContent).toContain("Получение");
  expect(screen.getByTestId("progress-decrypt").textContent).toContain("Расшифрование");
  expect(screen.getByTestId("progress-decrypt").textContent).toContain("33%");
  expect(percent({ done: 0, total: 0 })).toBe(100);
});
