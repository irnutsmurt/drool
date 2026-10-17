import { describe, expect, it } from "vitest";
import { cardsNeeded } from "./tableFit";

// A table becomes cards on a phone and wherever its columns don't fit
// (final UI pass: no column or action behind a sideways scroll).
describe("cards or a table", () => {
  it("is always cards on a phone", () => {
    expect(cardsNeeded(300, 390, true)).toBe(true);
  });
  it("stays a table while its content fits its box", () => {
    expect(cardsNeeded(1100, 1160, false)).toBe(false);
    expect(cardsNeeded(1161, 1160, false)).toBe(false); // a pixel of rounding
  });
  it("becomes cards when the content is wider than the box", () => {
    expect(cardsNeeded(952, 752, false)).toBe(true);
  });
});
