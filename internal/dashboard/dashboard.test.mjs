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
