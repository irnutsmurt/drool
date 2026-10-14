import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { CapsList, capsReported, isRootEquivalent } from "./CapsList";
import type { AgentCaps } from "./queries";

const managed: AgentCaps = {
  monitoring: true, shell: false, reboot: true, firewall: false, users: false,
  packages: { enabled: true, configured_repositories_only: true }, services: { enabled: true }, files: { enabled: false },
};

// A Shepherd that has enrolled but not connected yet has no capabilities on
// record: the API sends {} (dogfooding 2026-10-14: the host page failed with
// "Cannot read properties of undefined (reading 'enabled')").
describe("capabilities a Shepherd hasn't reported yet", () => {
  it("are told apart from reported ones", () => {
    expect(capsReported({})).toBe(false);
    expect(capsReported(null)).toBe(false);
    expect(capsReported(managed)).toBe(true);
  });

  it("render as not reported instead of failing", () => {
    expect(renderToStaticMarkup(createElement(CapsList, { caps: {} }))).toContain("Not reported yet");
  });

  it("don't change how reported ones render", () => {
    const html = renderToStaticMarkup(createElement(CapsList, { caps: managed }));
    expect(html).toContain("from configured repositories only");
    expect(html).not.toContain("Not reported yet");
    expect(isRootEquivalent(managed)).toBe(false);
  });
});
