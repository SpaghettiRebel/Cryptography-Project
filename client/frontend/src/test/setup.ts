import { beforeEach } from "vitest";
import { resetStore } from "../store";
import { resetWails } from "./wails";

beforeEach(() => {
  resetWails();
  resetStore();
});
