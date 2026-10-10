/**
 * A small QR code encoder (ISO/IEC 18004), only what the two-factor setup
 * needs: byte mode, error correction level M, versions 1–15 (up to 412
 * bytes). It runs in the browser, so the authenticator secret never goes to
 * a third party or into another request. Follows the structure of Project
 * Nayuki's reference implementation; checked by decoding with zbarimg in
 * qr.test.ts.
 */

// Error correction level M: codewords per block and block count per version.
const ECC_PER_BLOCK = [-1, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24];
const NUM_BLOCKS = [-1, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10];
const ALIGNMENT = [
  [], [], [6, 18], [6, 22], [6, 26], [6, 30], [6, 34], [6, 22, 38], [6, 24, 42], [6, 26, 46], [6, 28, 50],
  [6, 30, 54], [6, 32, 58], [6, 34, 62], [6, 26, 46, 66], [6, 26, 48, 70],
];
const MAX_VERSION = 15;
const FORMAT_BITS_M = 0; // L=1, M=0, Q=3, H=2

function rawDataModules(ver: number): number {
  let result = (16 * ver + 128) * ver + 64;
  if (ver >= 2) {
    const numAlign = Math.floor(ver / 7) + 2;
    result -= (25 * numAlign - 10) * numAlign - 55;
    if (ver >= 7) result -= 36;
  }
  return result;
}

function dataCodewords(ver: number): number {
  return Math.floor(rawDataModules(ver) / 8) - ECC_PER_BLOCK[ver]! * NUM_BLOCKS[ver]!;
}

function gfMul(x: number, y: number): number {
  let z = 0;
  for (let i = 7; i >= 0; i--) {
    z = (z << 1) ^ ((z >>> 7) * 0x11d);
    z ^= ((y >>> i) & 1) * x;
  }
  return z & 0xff;
}

function rsDivisor(degree: number): number[] {
  const result: number[] = new Array(degree - 1).fill(0);
  result.push(1);
  let root = 1;
  for (let i = 0; i < degree; i++) {
    for (let j = 0; j < result.length; j++) {
      result[j] = gfMul(result[j]!, root);
      if (j + 1 < result.length) result[j]! ^= result[j + 1]!;
    }
    root = gfMul(root, 0x02);
  }
  return result;
}

function rsRemainder(data: number[], divisor: number[]): number[] {
  const result: number[] = divisor.map(() => 0);
  for (const b of data) {
    const factor = b ^ result.shift()!;
    result.push(0);
    divisor.forEach((coef, i) => {
      result[i]! ^= gfMul(coef, factor);
    });
  }
  return result;
}

function encodeData(bytes: Uint8Array): { version: number; codewords: number[] } {
  let version = 0;
  for (let v = 1; v <= MAX_VERSION; v++) {
    const countBits = v < 10 ? 8 : 16;
    if (4 + countBits + bytes.length * 8 <= dataCodewords(v) * 8) {
      version = v;
      break;
    }
  }
  if (!version) throw new Error("too much data for a QR code");
  const bits: number[] = [];
  const put = (val: number, len: number) => {
    for (let i = len - 1; i >= 0; i--) bits.push((val >>> i) & 1);
  };
  put(0b0100, 4); // byte mode
  put(bytes.length, version < 10 ? 8 : 16);
  bytes.forEach((b) => put(b, 8));
  const capacity = dataCodewords(version) * 8;
  put(0, Math.min(4, capacity - bits.length));
  put(0, (8 - (bits.length % 8)) % 8);
  const data: number[] = [];
  for (let i = 0; i < bits.length; i += 8) {
    let b = 0;
    for (let j = 0; j < 8; j++) b = (b << 1) | bits[i + j]!;
    data.push(b);
  }
  for (let pad = 0xec; data.length < dataCodewords(version); pad ^= 0xec ^ 0x11) data.push(pad);
  return { version, codewords: interleave(data, version) };
}

function interleave(data: number[], ver: number): number[] {
  const numBlocks = NUM_BLOCKS[ver]!;
  const eccLen = ECC_PER_BLOCK[ver]!;
  const raw = Math.floor(rawDataModules(ver) / 8);
  const numShort = numBlocks - (raw % numBlocks);
  const shortLen = Math.floor(raw / numBlocks);
  const div = rsDivisor(eccLen);
  const blocks: number[][] = [];
  for (let i = 0, k = 0; i < numBlocks; i++) {
    const dat = data.slice(k, k + shortLen - eccLen + (i < numShort ? 0 : 1));
    k += dat.length;
    const ecc = rsRemainder(dat, div);
    if (i < numShort) dat.push(0);
    blocks.push(dat.concat(ecc));
  }
  const out: number[] = [];
  for (let i = 0; i < blocks[0]!.length; i++) {
    blocks.forEach((block, j) => {
      if (i !== shortLen - eccLen || j >= numShort) out.push(block[i]!);
    });
  }
  return out;
}

const MASKS: ((y: number, x: number) => boolean)[] = [
  (y, x) => (x + y) % 2 === 0,
  (y) => y % 2 === 0,
  (_, x) => x % 3 === 0,
  (y, x) => (x + y) % 3 === 0,
  (y, x) => (Math.floor(x / 3) + Math.floor(y / 2)) % 2 === 0,
  (y, x) => ((x * y) % 2) + ((x * y) % 3) === 0,
  (y, x) => (((x * y) % 2) + ((x * y) % 3)) % 2 === 0,
  (y, x) => (((x + y) % 2) + ((x * y) % 3)) % 2 === 0,
];

class Matrix {
  size: number;
  dark: boolean[][];
  fixed: boolean[][];
  constructor(readonly version: number) {
    this.size = version * 4 + 17;
    this.dark = Array.from({ length: this.size }, () => new Array(this.size).fill(false));
    this.fixed = Array.from({ length: this.size }, () => new Array(this.size).fill(false));
  }
  set(x: number, y: number, dark: boolean) {
    this.dark[y]![x] = dark;
    this.fixed[y]![x] = true;
  }
  functionPatterns() {
    const n = this.size;
    for (let i = 0; i < n; i++) {
      this.set(6, i, i % 2 === 0);
      this.set(i, 6, i % 2 === 0);
    }
    for (const [cx, cy] of [[3, 3], [n - 4, 3], [3, n - 4]] as const) {
      for (let dy = -4; dy <= 4; dy++) {
        for (let dx = -4; dx <= 4; dx++) {
          const x = cx + dx, y = cy + dy;
          const d = Math.max(Math.abs(dx), Math.abs(dy));
          if (x >= 0 && x < n && y >= 0 && y < n) this.set(x, y, d !== 2 && d !== 4);
        }
      }
    }
    const pos = ALIGNMENT[this.version]!;
    for (let i = 0; i < pos.length; i++) {
      for (let j = 0; j < pos.length; j++) {
        if ((i === 0 && j === 0) || (i === 0 && j === pos.length - 1) || (i === pos.length - 1 && j === 0)) continue;
        for (let dy = -2; dy <= 2; dy++) {
          for (let dx = -2; dx <= 2; dx++) this.set(pos[i]! + dx, pos[j]! + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1);
        }
      }
    }
    this.format(0); // reserve; the real bits are drawn after masking
    if (this.version >= 7) {
      let rem = this.version;
      for (let i = 0; i < 12; i++) rem = (rem << 1) ^ ((rem >>> 11) * 0x1f25);
      const bits = (this.version << 12) | rem;
      for (let i = 0; i < 18; i++) {
        const dark = ((bits >>> i) & 1) === 1;
        const a = n - 11 + (i % 3), b = Math.floor(i / 3);
        this.set(a, b, dark);
        this.set(b, a, dark);
      }
    }
  }
  format(mask: number) {
    const data = (FORMAT_BITS_M << 3) | mask;
    let rem = data;
    for (let i = 0; i < 10; i++) rem = (rem << 1) ^ ((rem >>> 9) * 0x537);
    const bits = ((data << 10) | rem) ^ 0x5412;
    const bit = (i: number) => ((bits >>> i) & 1) === 1;
    const n = this.size;
    for (let i = 0; i <= 5; i++) this.set(8, i, bit(i));
    this.set(8, 7, bit(6));
    this.set(8, 8, bit(7));
    this.set(7, 8, bit(8));
    for (let i = 9; i < 15; i++) this.set(14 - i, 8, bit(i));
    for (let i = 0; i < 8; i++) this.set(n - 1 - i, 8, bit(i));
    for (let i = 8; i < 15; i++) this.set(8, n - 15 + i, bit(i));
    this.set(8, n - 8, true);
  }
  place(codewords: number[]) {
    const n = this.size;
    let i = 0;
    for (let right = n - 1; right >= 1; right -= 2) {
      if (right === 6) right = 5;
      for (let vert = 0; vert < n; vert++) {
        for (let j = 0; j < 2; j++) {
          const x = right - j;
          const upward = ((right + 1) & 2) === 0;
          const y = upward ? n - 1 - vert : vert;
          if (!this.fixed[y]![x] && i < codewords.length * 8) {
            this.dark[y]![x] = ((codewords[i >>> 3]! >>> (7 - (i & 7))) & 1) === 1;
            i++;
          }
        }
      }
    }
  }
  applyMask(mask: number) {
    const f = MASKS[mask]!;
    for (let y = 0; y < this.size; y++) {
      for (let x = 0; x < this.size; x++) {
        if (!this.fixed[y]![x] && f(y, x)) this.dark[y]![x] = !this.dark[y]![x];
      }
    }
  }
  penalty(): number {
    const n = this.size, d = this.dark;
    let score = 0;
    const line = (get: (i: number) => boolean) => {
      let run = 1;
      for (let i = 1; i <= n; i++) {
        if (i < n && get(i) === get(i - 1)) run++;
        else {
          if (run >= 5) score += 3 + (run - 5);
          run = 1;
        }
      }
      for (let i = 0; i + 11 <= n; i++) {
        const w = Array.from({ length: 11 }, (_, k) => get(i + k));
        const core = w[0] && !w[1] && w[2] && w[3] && w[4] && !w[5] && w[6];
        if (core && !w[7] && !w[8] && !w[9] && !w[10]) score += 40;
        const core2 = w[4] && !w[5] && w[6] && w[7] && w[8] && !w[9] && w[10];
        if (core2 && !w[0] && !w[1] && !w[2] && !w[3]) score += 40;
      }
    };
    for (let y = 0; y < n; y++) line((x) => d[y]![x]!);
    for (let x = 0; x < n; x++) line((y) => d[y]![x]!);
    let darkCount = 0;
    for (let y = 0; y < n; y++) {
      for (let x = 0; x < n; x++) {
        if (d[y]![x]) darkCount++;
        if (y + 1 < n && x + 1 < n) {
          const c = d[y]![x];
          if (d[y]![x + 1] === c && d[y + 1]![x] === c && d[y + 1]![x + 1] === c) score += 3;
        }
      }
    }
    score += 10 * Math.floor(Math.abs((darkCount * 100) / (n * n) - 50) / 5);
    return score;
  }
}

/** The QR code for text as a square grid of dark (true) modules. */
export function qrMatrix(text: string): boolean[][] {
  const { version, codewords } = encodeData(new TextEncoder().encode(text));
  let best: { score: number; dark: boolean[][] } | undefined;
  for (let mask = 0; mask < 8; mask++) {
    const m = new Matrix(version);
    m.functionPatterns();
    m.place(codewords);
    m.applyMask(mask);
    m.format(mask);
    const score = m.penalty();
    if (!best || score < best.score) best = { score, dark: m.dark };
  }
  return best!.dark;
}

/** An SVG path ("M x y h1 v1 h-1 z" per dark module) and the image size,
 * with the 4-module quiet zone scanners need. */
export function qrPath(text: string): { path: string; size: number } {
  const m = qrMatrix(text);
  const q = 4;
  let path = "";
  m.forEach((row, y) => row.forEach((dark, x) => {
    if (dark) path += `M${x + q} ${y + q}h1v1h-1z`;
  }));
  return { path, size: m.length + 2 * q };
}
