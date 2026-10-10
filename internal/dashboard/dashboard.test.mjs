import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { fileURLToPath } from "node:url";
import vm from "node:vm";

const scriptPath = fileURLToPath(new URL("./dashboard.js", import.meta.url));
const source = readFileSync(scriptPath, "utf8");

function classList() {
  const set = new Set();
  return {
    add: (...c) => c.forEach((x) => set.add(x)),
    remove: (...c) => c.forEach((x) => set.delete(x)),
    contains: (c) => set.has(c),
    toggle: (c, force) => {
      const on = force === undefined ? !set.has(c) : !!force;
      if (on) set.add(c);
      else set.delete(c);
      return on;
    },
    replace: (a, b) => {
      if (set.has(a)) {
        set.delete(a);
        set.add(b);
      }
    },
  };
}

function el(tag = "div") {
  const node = {
    tagName: String(tag).toUpperCase(),
    style: { setProperty() {}, removeProperty() {} },
    dataset: {},
    attrs: {},
    children: [],
    classList: classList(),
    textContent: "",
    innerHTML: "",
    value: "",
    checked: false,
    hidden: false,
    offsetWidth: 0,
    offsetHeight: 0,
    offsetParent: {},
    clientWidth: 0,
    clientHeight: 0,
    scrollTop: 0,
    tBodies: [{ rows: [] }],
    cells: [],
    parentElement: null,
    addEventListener() {},
    removeEventListener() {},
    appendChild(c) {
      node.children.push(c);
      return c;
    },
    insertBefore(c) {
      node.children.push(c);
      return c;
    },
    removeChild() {},
    remove() {},
    setAttribute(k, v) {
      node.attrs[k] = v;
    },
    getAttribute(k) {
      return k in node.attrs ? node.attrs[k] : null;
    },
    removeAttribute(k) {
      delete node.attrs[k];
    },
    hasAttribute(k) {
      return k in node.attrs;
    },
    getBoundingClientRect: () => ({ left: 0, top: 0, width: 100, height: 10 }),
    focus() {},
    closest: () => null,
    matches: () => false,
    querySelector: () => el(),
    querySelectorAll: () => [],
    getElementsByTagName: () => [],
  };
  return node;
}

const byId = new Map();
const getById = (id) => {
  if (!byId.has(id)) byId.set(id, el());
  return byId.get(id);
};

const store = new Map();
const document = {
  documentElement: el("html"),
  body: el("body"),
  activeElement: null,
  createElement: (tag) => el(tag),
  createDocumentFragment: () => el("fragment"),
  createTextNode: (t) => ({ nodeType: 3, textContent: t }),
  getElementById: getById,
  querySelector: () => el(),
  querySelectorAll: () => [],
  addEventListener() {},
  removeEventListener() {},
};

const sandbox = {
  document,
  localStorage: {
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => store.set(k, String(v)),
    removeItem: (k) => store.delete(k),
  },
  navigator: { userAgent: "node" },
  location: { href: "http://localhost/", search: "" },
  console,

  setTimeout: () => 0,
  clearTimeout: () => {},
  setInterval: () => 0,
  clearInterval: () => {},
  queueMicrotask,
  fetch: () => new Promise(() => {}),
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  requestAnimationFrame: () => 0,
  cancelAnimationFrame: () => {},
  addEventListener() {},
  removeEventListener() {},
  innerWidth: 1280,
  innerHeight: 800,
  scrollTo() {},
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
};
sandbox.window = sandbox;
sandbox.globalThis = sandbox;
sandbox.self = sandbox;

vm.createContext(sandbox);
vm.runInContext(source, sandbox, { filename: scriptPath });

const {
  parseSizeCell,
  parseDurCell,
  parseNumCell,
  parseQuery,
  sortVal,
  cmpCell,
  escRe,
  highlightFrag,
  colorSpans,
  colorFrag,
  HL,
  MAX_MARKS,
  MAX_HIGHLIGHT_LEN,
} = sandbox;

test("parseSizeCell accepts plain and rate values", () => {
  assert.equal(parseSizeCell("512 B"), 512);
  assert.equal(parseSizeCell("1 KiB"), 1024);
  assert.equal(parseSizeCell("1 KB"), 1024);
  assert.equal(parseSizeCell("2.5 MiB"), 2.5 * 1048576);

  assert.equal(parseSizeCell("1 KiB/s"), 1024);
  assert.equal(parseSizeCell("2.5 MiB/s"), 2.5 * 1048576);
  assert.equal(parseSizeCell("100 B/s"), 100);
});

test("parseSizeCell rejects non sizes", () => {
  assert.equal(parseSizeCell("n/a"), null);
  assert.equal(parseSizeCell("12"), null);
  assert.equal(parseSizeCell("1 XB"), null);
});

test("parseSizeCell keeps ordering for rates", () => {
  assert.ok(parseSizeCell("1 KiB/s") > parseSizeCell("512 B/s"));
  assert.ok(parseSizeCell("1 MiB/s") > parseSizeCell("900 KiB/s"));
});

test("parseNumCell strips a percent suffix", () => {
  assert.equal(parseNumCell("42"), 42);
  assert.equal(parseNumCell("0"), 0);
  assert.equal(parseNumCell("3.5%"), 3.5);
  assert.equal(parseNumCell("100%"), 100);
  assert.equal(parseNumCell("-2.5%"), -2.5);
  assert.equal(parseNumCell("n/a"), null);
});

test("parseDurCell converts to seconds", () => {
  assert.equal(parseDurCell("500ms"), 0.5);
  assert.equal(parseDurCell("2s"), 2);
  assert.equal(parseDurCell("2m"), 120);
  assert.equal(parseDurCell("1h"), 3600);
  assert.equal(parseDurCell("x"), null);
});

test("parseQuery understands units, percents and rates", () => {
  const same = (q, op, kind, val) => {
    const r = parseQuery(q);
    assert.ok(r, `${q} must parse`);
    assert.equal(r.op, op);
    assert.equal(r.kind, kind);
    assert.equal(r.val, val);
  };
  same(">1mb", ">", "size", 1048576);
  same(">=2 KiB/s", ">=", "size", 2048);
  same(">10%", ">", "num", 10);
  same("=5", "=", "num", 5);
  same(">1h", ">", "time", 3600);
  assert.equal(parseQuery("hello"), null);
});

test("sortVal prefers size, then duration, then number", () => {
  assert.equal(sortVal("1 KiB"), 1024);
  assert.equal(sortVal("2m"), 120);
  assert.equal(sortVal("12%"), 12);
  assert.equal(sortVal("plain"), null);
});

test("cmpCell compares percent and rate cells", () => {
  assert.equal(cmpCell("50%", parseQuery(">10")), true);
  assert.equal(cmpCell("5%", parseQuery(">10")), false);
  assert.equal(cmpCell("1 KiB/s", parseQuery(">512 B/s")), true);
  assert.equal(cmpCell("plain text", parseQuery(">1")), false);
});

test("escRe escapes metacharacters", () => {
  assert.ok(new RegExp(escRe("a.b")).test("a.b"));
  assert.equal(new RegExp(escRe("a.b")).test("axb"), false);
});

function render(text, re) {
  const frag = highlightFrag(text, re);
  let marks = 0;
  let out = "";
  for (const child of frag.children) {
    if (child.tagName === "MARK") marks++;
    out += child.textContent;
  }
  return { marks, text: out };
}

test("highlightFrag marks every occurrence", () => {
  const { marks, text } = render("foo bar foo", /foo/g);
  assert.equal(marks, 2);
  assert.equal(text, "foo bar foo");
});

test("highlightFrag returns plain text without a regex", () => {
  const { marks, text } = render("nothing here", null);
  assert.equal(marks, 0);
  assert.equal(text, "nothing here");
});

test("highlightFrag terminates on a zero-length pattern", () => {
  for (const re of [/^/g, /(?=a)/g, /\b/g, /(?<=a)/g]) {
    const { marks, text } = render("abcabc", re);
    assert.equal(marks, 0, "empty matches must not create marks");
    assert.equal(text, "abcabc", "text must be preserved");
  }
});

test("highlightFrag handles a pattern mixing empty and real matches", () => {
  const { marks, text } = render("abcabc", /a*/g);
  assert.equal(marks, 2, "only the two real matches are marked");
  assert.equal(text, "abcabc");
});

test("highlightFrag caps the number of marks", () => {
  const { marks, text } = render("a".repeat(5000), /a/g);
  assert.equal(marks, MAX_MARKS);

  assert.equal(text, "a".repeat(5000));
});

test("highlightFrag skips regex on very long lines", () => {
  const long = "x".repeat(MAX_HIGHLIGHT_LEN + 1);
  const { marks, text } = render(long, /x/g);
  assert.equal(marks, 0);
  assert.equal(text, long);
});

test("highlightFrag keeps working on a long line with a short match", () => {
  const text = "hit " + "y".repeat(MAX_HIGHLIGHT_LEN + 100);
  const { marks } = render(text, /hit/g);
  assert.equal(marks, 0, "long lines are skipped entirely");
  const short = "hit " + "y".repeat(10);
  assert.equal(render(short, /hit/g).marks, 1);
});

test("the log viewer offers Follow but no Pause", () => {
  const tmpl = readFileSync(
    fileURLToPath(new URL("./dashboard.tmpl", import.meta.url)),
    "utf8",
  );
  assert.match(tmpl, /id="followChk"/, "Follow toggle is present");
  assert.doesNotMatch(tmpl, /id="btnPause"/, "the Pause button is gone");
  assert.doesNotMatch(source, /btnPause/, "no Pause wiring is left");
  assert.doesNotMatch(source, /setConn\("Paused"/, "the Paused status is gone");
  assert.match(
    source,
    /function reloadHistory\(\)/,
    "history reload is unconditional",
  );
  assert.doesNotMatch(source, /restartIfStreaming/);
  assert.doesNotMatch(
    source,
    /followOn\(\) &&/,
    "history is never gated on the follow state",
  );
});

function csumAssignment() {
  const m = source.match(/^[ \t]*csum\[m0\]\s*=[^\n]*$/m);
  assert.ok(m, "csum[m0] assignment not found in dashboard.js");
  return m[0].trim();
}

test("the first log line does not poison csum with NaN", () => {
  const ctx = vm.createContext({
    csum: [0],
    m0: 0,
    it: { shown: true },
    vpSlotH: () => 20,
  });
  vm.runInContext(csumAssignment(), ctx);
  assert.equal(ctx.csum.length, 1);
  assert.equal(
    ctx.csum[0],
    20,
    "csum[0] must be the first line height, not NaN",
  );
  assert.ok(Number.isFinite(ctx.csum[0]), "csum[0] must be a finite number");
});

test("csum keeps accumulating across lines", () => {
  const stmt = csumAssignment();
  const ctx = vm.createContext({
    csum: [0],
    m0: 0,
    it: { shown: true },
    vpSlotH: () => 20,
  });
  vm.runInContext(stmt, ctx);
  ctx.m0 = 1;
  vm.runInContext(stmt, ctx);
  ctx.m0 = 2;
  vm.runInContext(stmt, ctx);
  assert.deepEqual(Array.from(ctx.csum), [20, 40, 60]);
});

test("csum skips hidden lines instead of adding NaN", () => {
  const stmt = csumAssignment();
  const ctx = vm.createContext({
    csum: [0],
    m0: 0,
    it: { shown: false },
    vpSlotH: () => 20,
  });
  vm.runInContext(stmt, ctx);
  assert.equal(ctx.csum[0], 0);
  assert.ok(Number.isFinite(ctx.csum[0]));
});

test("vpRender repairs a non-finite csum before measuring", () => {
  assert.match(
    source,
    /if \(!isFinite\(csum\[csum\.length - 1\]\)\) rebuildCsum\(\);/,
    "vpRender must self-heal a corrupted csum",
  );
});

test("loadHistory shows the 504 body verbatim and still starts the live stream", () => {
  const start = source.indexOf("function loadHistory");
  const end = source.indexOf("function startStream");
  assert.ok(start > -1 && end > start, "loadHistory body not found");
  const body = source.slice(start, end);
  assert.match(
    body,
    /if \(!r\.ok\) \{\s*return r\.text\(\)/,
    "a non-2xx response is read as text",
  );
  assert.match(
    body,
    /truncEl\.textContent = d\.text;/,
    "the server message is shown as is",
  );
  assert.match(
    body,
    /if \(d\.failed\) \{\s*truncEl\.textContent = d\.text;\s*startStream\(\);/,
    "a gateway timeout must not block the live stream",
  );
});

test("pollTick ignores a 504 instead of reporting a poll error", () => {
  const start = source.indexOf("function pollTick");
  assert.ok(start > -1, "pollTick body not found");
  const body = source.slice(
    start,
    source.indexOf("function closeStream", start),
  );
  assert.match(
    body,
    /if \(!r\.ok\) return null;/,
    "a gateway timeout must not break the polling loop",
  );
});

function picks(text) {
  const s = colorSpans(text);
  if (!s) return [];
  const out = [];
  for (let i = 0; i < s.length; i += 3)
    out.push([text.slice(s[i], s[i + 1]), s[i + 2]]);
  return out;
}

test("colorSpans returns null for an empty line", () => {
  assert.equal(colorSpans(""), null);
});

test("colorSpans returns null past the highlight limit", () => {
  const at = "error " + "x".repeat(MAX_HIGHLIGHT_LEN - 6);
  assert.deepEqual(picks(at), [["error", "hl-error"]]);
  assert.equal(colorSpans(at + "x"), null);
});

test("colorSpans marks error warn and ok words", () => {
  assert.deepEqual(picks("fatal error connection failed warn retry done"), [
    ["fatal", "hl-error"],
    ["error", "hl-error"],
    ["connection", "hl-ok"],
    ["failed", "hl-error"],
    ["warn", "hl-warn"],
    ["retry", "hl-warn"],
    ["done", "hl-ok"],
  ]);
});

test("colorSpans respects word boundaries", () => {
  assert.deepEqual(picks("unblocked unavailable available password"), [
    ["unblocked", "hl-ok"],
    ["unavailable", "hl-error"],
    ["available", "hl-ok"],
  ]);
});

test("colorSpans matches keywords and units in any case", () => {
  assert.deepEqual(picks("ERROR WARN OK Failed 512MIB V8.14.6"), [
    ["ERROR", "hl-error"],
    ["WARN", "hl-warn"],
    ["OK", "hl-ok"],
    ["Failed", "hl-error"],
    ["512MIB", "hl-num"],
    ["V8.14.6", "hl-num"],
  ]);
});

test("colorSpans marks a full ISO timestamp", () => {
  const ts = "2025-02-26T21:38:35.956968+03:00";
  assert.deepEqual(picks(ts), [[ts, "hl-time"]]);
});

test("colorSpans marks date formats", () => {
  for (const d of [
    "2025-02-26",
    "2025/02/26",
    "2025.02.26",
    "26-02-2025",
    "26.02.2025",
    "26/02/2025",
  ]) {
    assert.deepEqual(picks(d), [[d, "hl-time"]], d);
  }
});

test("colorSpans marks clock times", () => {
  for (const t of ["21:38", "21:38:35", "21:38:35.956+03:00"]) {
    assert.deepEqual(picks(t), [[t, "hl-time"]], t);
  }
});

test("colorSpans marks mac addresses", () => {
  for (const m of ["11:11:11:11:11:11", "aa-bb-cc-dd-ee-ff"]) {
    assert.deepEqual(picks(m), [[m, "hl-time"]], m);
  }
});

test("colorSpans marks ip addresses", () => {
  assert.deepEqual(picks("255.255.255.255/24"), [
    ["255.255.255.255/24", "hl-time"],
  ]);
  assert.deepEqual(picks("127.0.0.1:8443"), [
    ["127.0.0.1", "hl-time"],
    ["8443", "hl-num"],
  ]);
});

test("colorSpans marks numbers decimals and versions", () => {
  assert.deepEqual(picks("1 1.1 1.1.1 2.10.14 v8.14.6"), [
    ["1", "hl-num"],
    ["1.1", "hl-num"],
    ["1.1.1", "hl-num"],
    ["2.10.14", "hl-num"],
    ["v8.14.6", "hl-num"],
  ]);
});

test("colorSpans marks number units", () => {
  assert.deepEqual(picks("300ms 512MiB 50% 5s"), [
    ["300ms", "hl-num"],
    ["512MiB", "hl-num"],
    ["50%", "hl-num"],
    ["5s", "hl-num"],
  ]);
});

test("colorSpans ignores numbers inside hyphenated identifiers", () => {
  assert.equal(colorSpans("k3d-rpi-105-serverlb"), null);
});

test("colorSpans marks a url as a single link span", () => {
  assert.deepEqual(picks("see https://example.com/api/v1?x=1 now"), [
    ["https://example.com/api/v1?x=1", "hl-link"],
  ]);
});

test("colorSpans keeps a url whole instead of splitting it", () => {
  assert.deepEqual(picks("http://10.0.0.1:8443/api/v2 www.x.io"), [
    ["http://10.0.0.1:8443/api/v2", "hl-link"],
    ["www.x.io", "hl-link"],
  ]);
});

test("colorSpans trims trailing punctuation from a url", () => {
  assert.deepEqual(picks("go http://example.com, stop"), [
    ["http://example.com", "hl-link"],
    ["stop", "hl-error"],
  ]);
});

test("colorSpans matches links in any case", () => {
  assert.deepEqual(picks("HTTPS://EXAMPLE.COM/A"), [
    ["HTTPS://EXAMPLE.COM/A", "hl-link"],
  ]);
});

test("colorSpans keeps spans sorted and non overlapping", () => {
  const text = "fatal2025-02-26 1.5ms warn 10.0.0.1 done";
  const s = colorSpans(text);
  assert.ok(s && s.length);
  let prev = 0;
  for (let i = 0; i < s.length; i += 3) {
    assert.ok(s[i] >= prev, "spans must not overlap");
    assert.ok(s[i] < s[i + 1], "a span must be non empty");
    prev = s[i + 1];
  }
});

test("every color class has a stylesheet rule", () => {
  const tmpl = readFileSync(
    fileURLToPath(new URL("./dashboard.tmpl", import.meta.url)),
    "utf8",
  );
  for (const g of HL) assert.ok(tmpl.includes("." + g.cls), g.cls);
});

function flat(node, out = []) {
  for (const c of node.children) {
    if (c.tagName === "FRAGMENT") flat(c, out);
    else out.push(c);
  }
  return out;
}

function plain(node) {
  if (!node.children || !node.children.length) return node.textContent;
  return flat(node).map(plain).join("");
}

function nodes(line, on, re) {
  return flat(colorFrag(line, re || null, on)).map((c) => [
    plain(c),
    c.nodeType === 3 ? null : c.className,
  ]);
}

test("colorFrag wraps every color span and keeps the plain text", () => {
  assert.deepEqual(nodes("fatal 1.5ms warn", true), [
    ["fatal", "hl-error"],
    [" ", null],
    ["1.5ms", "hl-num"],
    [" ", null],
    ["warn", "hl-warn"],
  ]);
});

test("colorFrag leaves the line untouched when coloring is off", () => {
  assert.deepEqual(nodes("fatal done", false), [["fatal done", null]]);
});

test("colorFrag nests search marks inside color spans", () => {
  const f = flat(colorFrag("fatal", /fat/g, true));
  assert.equal(f.length, 1);
  assert.equal(f[0].className, "hl-error");
  const inner = flat(f[0]);
  assert.equal(inner[0].tagName, "MARK");
  assert.equal(inner[0].textContent, "fat");
  assert.equal(inner[1].textContent, "al");
});

test("colorFrag renders links as clickable anchors", () => {
  const [link] = flat(colorFrag("http://example.com", null, true));
  assert.equal(link.tagName, "A");
  assert.equal(link.className, "hl-link");
  assert.equal(link.href, "http://example.com");
  assert.equal(link.target, "_blank");
  assert.equal(link.rel, "noopener");
  const [www] = flat(colorFrag("www.example.com", null, true));
  assert.equal(www.tagName, "A");
  assert.equal(www.href, "https://www.example.com");
});

test("colorFrag renders links alongside other color spans", () => {
  assert.deepEqual(nodes("fatal https://x.io/v1 300ms", true), [
    ["fatal", "hl-error"],
    [" ", null],
    ["https://x.io/v1", "hl-link"],
    [" ", null],
    ["300ms", "hl-num"],
  ]);
});

test("renderMsg renders through colorFrag", () => {
  const start = source.indexOf("function renderMsg");
  assert.ok(start > -1, "renderMsg not found");
  const body = source.slice(
    start,
    source.indexOf("function applyLogFilter", start),
  );
  assert.match(body, /colorFrag\(raw, highlightRe\(\), colorChk\.checked\)/);
});