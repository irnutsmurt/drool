import { execFileSync } from "node:child_process";
import { existsSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { qrMatrix } from "./qr";

// Decoding with zbarimg proves the codes are valid, whatever mask the
// encoder picked. Skipped where zbar isn't installed.
const zbar = ["/usr/bin/zbarimg", "/usr/local/bin/zbarimg"].find((p) => existsSync(p));

function decode(text: string): string {
  const m = qrMatrix(text);
  const scale = 6, quiet = 4, size = (m.length + 2 * quiet) * scale;
  const px = new Uint8Array(size * size).fill(255);
  m.forEach((row, y) => row.forEach((dark, x) => {
    if (!dark) return;
    for (let dy = 0; dy < scale; dy++) {
      for (let dx = 0; dx < scale; dx++) px[((y + quiet) * scale + dy) * size + (x + quiet) * scale + dx] = 0;
    }
  }));
  const dir = mkdtempSync(join(tmpdir(), "qr-"));
  const file = join(dir, "code.pgm");
  writeFileSync(file, Buffer.concat([Buffer.from(`P5\n${size} ${size}\n255\n`), Buffer.from(px)]));
  return execFileSync(zbar!, ["--quiet", "--raw", "-Sbinary", file]).toString("utf8").replace(/\n$/, "");
}

describe("qr", () => {
  it("has the size of the smallest version that fits", () => {
    expect(qrMatrix("hi").length).toBe(21); // version 1
    expect(qrMatrix("x".repeat(100)).length).toBe(4 * 6 + 17); // 100 bytes need version 6 at level M
  });

  it("refuses data that doesn't fit version 15", () => {
    expect(() => qrMatrix("x".repeat(500))).toThrow();
  });

  it.skipIf(!zbar)("decodes back to the input", () => {
    const uri = "otpauth://totp/DROOL%3Aalex?algorithm=SHA1&digits=6&issuer=DROOL&period=30&secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP";
    for (const text of [
      "hello",
      uri,
      uri.replace("alex", "a.very.long-user_name@example.org"),
      "ünïcödé ✓ " + "z".repeat(140), // byte mode carries UTF-8
      "y".repeat(260), // version 13 (version info blocks, 9 blocks of two sizes)
      "w".repeat(400), // version 15
    ]) {
      expect(decode(text)).toBe(text);
    }
  });
});
