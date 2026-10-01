var uiKey = "docker-exporter-ui";
var ui;
try {
  ui = JSON.parse(localStorage.getItem(uiKey)) || {};
} catch (e) {
  ui = {};
}
ui.filters = ui.filters || [];
ui.sorts = ui.sorts || [];

function persist() {
  localStorage.setItem(uiKey, JSON.stringify(ui));
}

function getGroups(table) {
  var tbody = table.tBodies[0];
  var groups = [],
    cur = null,
    hasGroups = false;
  Array.prototype.forEach.call(tbody.rows, function (r) {
    if (r.classList.contains("group-row")) {
      hasGroups = true;
      cur = { header: r, rows: [] };
      groups.push(cur);
    } else if (cur) {
      cur.rows.push(r);
    }
  });
  if (!hasGroups) {
    return [{ header: null, rows: Array.prototype.slice.call(tbody.rows) }];
  }
  return groups;
}

function applySort(table, col, asc) {
  var tbody = table.tBodies[0];
  var groups = getGroups(table);
  groups.forEach(function (g, gi) {
    var ref = gi < groups.length - 1 ? groups[gi + 1].header : null;
    g.rows.sort(function (a, b) {
      var x = a.cells[col].textContent,
        y = b.cells[col].textContent;
      var nx = sortVal(x),
        ny = sortVal(y);
      var c;
      if (nx != null && ny != null) {
        c = nx === ny ? 0 : nx < ny ? -1 : 1;
      } else {
        c = x.localeCompare(y, "ru", {
          numeric: true,
          sensitivity: "base",
        });
      }
      return c * (asc ? 1 : -1);
    });
    g.rows.forEach(function (r) {
      tbody.insertBefore(r, ref);
    });
  });
  table.querySelectorAll("th").forEach(function (h) {
    h.classList.remove("asc", "desc");
  });
  var th = table.querySelectorAll("th")[col];
  th.classList.add(asc ? "asc" : "desc");
  th.setAttribute("data-asc", asc ? "1" : "0");
}

function sortVal(text) {
  var s = parseSizeCell(text);
  if (s != null) {
    return s;
  }
  var d = parseDurCell(text);
  if (d != null) {
    return d;
  }
  var n = parseNumCell(text);
  if (n != null) {
    return n;
  }
  return null;
}

function parseSizeCell(text) {
  var m = /^([0-9.]+)\s*([KMGTPE]?i?B)(?:\/s)?$/i.exec(text.trim());
  if (!m) {
    return null;
  }
  var f = {
    B: 1,
    KB: 1024,
    KIB: 1024,
    MB: 1048576,
    MIB: 1048576,
    GB: 1073741824,
    GIB: 1073741824,
    TB: 1099511627776,
    TIB: 1099511627776,
    PB: 1125899906842624,
    PIB: 1125899906842624,
  };
  var u = m[2].toUpperCase();
  if (!(u in f)) {
    return null;
  }
  return parseFloat(m[1]) * f[u];
}

function parseDurCell(text) {
  var m = /^([0-9.]+)\s*(ms|s|min|m|h)$/i.exec(text.trim());
  if (!m) {
    return null;
  }
  var v = parseFloat(m[1]),
    u = m[2].toLowerCase();
  return u === "ms"
    ? v / 1000
    : u === "s"
      ? v
      : u === "min" || u === "m"
        ? v * 60
        : v * 3600;
}

function parseNumCell(text) {
  var t = text.trim();

  if (t.charAt(t.length - 1) === "%") {
    t = t.slice(0, -1).trim();
  }
  return /^[0-9.-]+$/.test(t) ? parseFloat(t) : null;
}

function parseQuery(q) {
  var m = /^\s*(>=|<=|>|<|=)\s*([0-9.]+)\s*([a-zA-Z]*)\s*(?:%|\/s)?\s*$/.exec(
    q,
  );
  if (!m) {
    return null;
  }
  var raw = m[3],
    u = raw.toLowerCase(),
    val = parseFloat(m[2]);
  var sec = { s: 1, ms: 0.001, min: 60, h: 3600 };
  if (u in sec) {
    return { op: m[1], kind: "time", val: val * sec[u] };
  }
  if (raw === "m") {
    return { op: m[1], kind: "time", val: val * 60 };
  }
  var size = {
    b: 1,
    k: 1024,
    kb: 1024,
    kib: 1024,
    m: 1048576,
    mb: 1048576,
    mib: 1048576,
    g: 1073741824,
    gb: 1073741824,
    gib: 1073741824,
    t: 1099511627776,
    tb: 1099511627776,
    tib: 1099511627776,
    p: 1125899906842624,
    pb: 1125899906842624,
    pib: 1125899906842624,
  };
  if (u in size) {
    return { op: m[1], kind: "size", val: val * size[u] };
  }
  return { op: m[1], kind: "num", val: val };
}

function cmpCell(text, q) {
  var v;
  if (q.kind === "size") {
    v = parseSizeCell(text);
  } else if (q.kind === "time") {
    v = parseDurCell(text);
  } else {
    v = parseNumCell(text);
  }
  if (v == null) {
    return false;
  }
  switch (q.op) {
    case ">":
      return v > q.val;
    case ">=":
      return v >= q.val;
    case "<":
      return v < q.val;
    case "<=":
      return v <= q.val;
    case "=":
      return Math.abs(v - q.val) < 1e-6;
  }
  return false;
}

function rowMatches(row, q, comp) {
  if (!comp) {
    return row.textContent.toLowerCase().indexOf(q) !== -1;
  }
  for (var i = 0; i < row.cells.length; i++) {
    if (cmpCell(row.cells[i].textContent, comp)) {
      return true;
    }
  }
  return false;
}

function applyFilter(table) {
  var search = table.parentElement.querySelector(".search");
  var raw = search ? search.value : "";
  var comp = parseQuery(raw);
  var q = raw.toLowerCase();
  getGroups(table).forEach(function (g) {
    var collapsed = g.header && g.header.classList.contains("collapsed");
    var visible = false;
    g.rows.forEach(function (r) {
      var show = rowMatches(r, q, comp);
      if (show) {
        visible = true;
      }
      if (collapsed) {
        r.style.display = "none";
      } else {
        r.style.display = show ? "" : "none";
      }
    });
    if (g.header) {
      g.header.style.display = visible || collapsed ? "" : "none";
    }
  });
}

function applyUI(ti) {
  var table = document.querySelectorAll("table")[ti];
  if (!table) {
    return;
  }
  if (ui.sorts[ti]) {
    applySort(table, ui.sorts[ti].i, ui.sorts[ti].asc === "1");
  }
  applyCollapsed(table, ti);
  applyFilter(table);
}

function collapsedProjects() {
  return ui.collapsed || [];
}

function applyCollapsed(table, ti) {
  if (ti !== 0) {
    return;
  }
  var list = collapsedProjects();
  getGroups(table).forEach(function (g) {
    if (!g.header) {
      return;
    }
    g.header.classList.toggle(
      "collapsed",
      list.indexOf(g.header.getAttribute("data-group")) !== -1,
    );
  });
}

function syncCollapseAll() {
  var table = document.querySelectorAll("table")[0];
  if (!table) {
    return;
  }
  var groups = getGroups(table);
  var allCollapsed =
    groups.length > 0 &&
    groups.every(function (g) {
      return !g.header || g.header.classList.contains("collapsed");
    });
  var btn = document.getElementById("collapseAllBtn");
  if (!btn) {
    return;
  }
  btn.textContent = allCollapsed ? "➕ Expand" : "➖ Collapse";
  btn.title = allCollapsed ? "Expand all stacks" : "Collapse all stacks";
}

document.querySelectorAll("table").forEach(function (table, ti) {
  var search = table.parentElement.querySelector(".search");
  if (search) {
    search.addEventListener("input", function () {
      ui.filters[ti] = search.value;
      persist();
      applyFilter(table);
    });
    if (ui.filters[ti]) {
      search.value = ui.filters[ti];
    }
  }
  table.querySelectorAll("th").forEach(function (th, i) {
    th.classList.add("sortable");
    th.addEventListener("click", function () {
      var asc = th.getAttribute("data-asc") !== "1";
      applySort(table, i, asc);
      ui.sorts[ti] = { i: i, asc: asc ? "1" : "0" };
      persist();
    });
  });
  if (ti === 0) {
    var collapseAllBtn = document.getElementById("collapseAllBtn");
    if (collapseAllBtn) {
      collapseAllBtn.addEventListener("click", function () {
        var groups = getGroups(table);
        var collapse = groups.some(function (g) {
          return g.header && !g.header.classList.contains("collapsed");
        });
        var list = [];
        groups.forEach(function (g) {
          if (!g.header) {
            return;
          }
          g.header.classList.toggle("collapsed", collapse);
          if (collapse) {
            list.push(g.header.getAttribute("data-group"));
          }
        });
        ui.collapsed = list;
        persist();
        applyFilter(table);
        syncCollapseAll();
      });
    }
    table.addEventListener("click", function (e) {
      var gr = e.target.closest("tr.group-row");
      if (!gr) {
        return;
      }
      var hide = gr.classList.toggle("collapsed");
      var r = gr.nextElementSibling;
      while (r && r.tagName === "TR" && !r.classList.contains("group-row")) {
        r.style.display = hide ? "none" : "";
        r = r.nextElementSibling;
      }
      var name = gr.getAttribute("data-group");
      var list = collapsedProjects();
      var idx = list.indexOf(name);
      if (hide && idx === -1) {
        list.push(name);
      }
      if (!hide && idx !== -1) {
        list.splice(idx, 1);
      }
      ui.collapsed = list;
      persist();
      syncCollapseAll();
    });
    syncCollapseAll();
  }
  applyUI(ti);
});

var MAX_MARKS = 2000;
var MAX_HIGHLIGHT_LEN = 8000;
function escRe(s) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
function highlightFrag(text, re) {
  var f = document.createDocumentFragment();
  if (!re || text.length > MAX_HIGHLIGHT_LEN) {
    f.appendChild(document.createTextNode(text));
    return f;
  }
  var last = 0,
    m,
    marks = 0;
  while ((m = re.exec(text)) !== null) {
    if (m[0].length === 0) {
      re.lastIndex++;
      continue;
    }
    if (marks >= MAX_MARKS) break;
    if (m.index > last)
      f.appendChild(document.createTextNode(text.slice(last, m.index)));
    var mark = document.createElement("mark");
    mark.textContent = m[0];
    f.appendChild(mark);
    last = m.index + m[0].length;
    marks++;
  }
  if (last < text.length)
    f.appendChild(document.createTextNode(text.slice(last)));
  re.lastIndex = 0;
  return f;
}

(function () {
  var key = "docker-exporter-refresh";
  var pausedKey = "docker-exporter-paused";
  var sel = document.getElementById("refreshSel");
  var btn = document.getElementById("pauseBtn");
  var refreshBtn = document.getElementById("refreshBtn");
  var timer = null;
  var saved = localStorage.getItem(key);
  if (saved) {
    sel.value = saved;
  }
  function paused() {
    return localStorage.getItem(pausedKey) === "1";
  }
  function update(data) {
    var cards = document.getElementById("cards");
    if (cards && data.cards) {
      cards.innerHTML = data.cards;
    }
    ["containers", "images", "volumes"].forEach(function (name, ti) {
      var tbody = document.getElementById("tbody-" + name);
      if (tbody && typeof data[name] === "string") {
        tbody.innerHTML = data[name];
        applyUI(ti);
      }
    });
    syncCollapseAll();
  }
  function poll() {
    fetch("/api/dashboard/refresh", { cache: "no-store" })
      .then(function (r) {
        return r.json();
      })
      .then(update)
      .catch(function () {});
  }
  function sync() {
    var p = paused();
    btn.classList.toggle("on", !p);
    btn.title = p ? "Resume auto-refresh" : "Pause auto-refresh";
    if (timer) {
      clearInterval(timer);
      timer = null;
    }
    if (!p) {
      timer = setInterval(poll, sel.value * 1000);
    }
  }
  sel.addEventListener("change", function () {
    localStorage.setItem(key, sel.value);
    sync();
  });
  btn.addEventListener("click", function () {
    localStorage.setItem(pausedKey, paused() ? "" : "1");
    sync();
  });
  refreshBtn.addEventListener("click", poll);
  sync();
})();

(function () {
  var key = "docker-exporter-theme";
  var theme =
    localStorage.getItem(key) ||
    (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
  document.documentElement.setAttribute("data-theme", theme);
  var btn = document.getElementById("themeBtn");
  function sync() {
    var t = document.documentElement.getAttribute("data-theme");
    btn.textContent = t === "dark" ? "🌙 Dark" : "☀️ Light";
    btn.title = t === "dark" ? "Switch to light theme" : "Switch to dark theme";
  }
  btn.addEventListener("click", function () {
    var t = document.documentElement.getAttribute("data-theme");
    document.documentElement.setAttribute(
      "data-theme",
      t === "dark" ? "light" : "dark",
    );
    localStorage.setItem(
      key,
      document.documentElement.getAttribute("data-theme"),
    );
    sync();
  });
  sync();
})();

(function () {
  var btn = document.getElementById("backTop");
  window.addEventListener("scroll", function () {
    btn.classList.toggle("show", window.scrollY > 300);
  });
  btn.addEventListener("click", function () {
    window.scrollTo({ top: 0, behavior: "smooth" });
  });
})();

(function () {
  var $ = function (id) {
    return document.getElementById(id);
  };
  var logBody = $("logBody"),
    mask = $("logsModal"),
    title = $("logsTitle"),
    truncEl = $("logsTrunc"),
    filter = $("logFilter"),
    insens = $("insensChk"),
    ctxChk = $("ctxChk"),
    wrapChk = $("wrapChk"),
    tsChk = $("tsChk"),
    streamChk = $("streamChk"),
    outChk = $("outChk"),
    errChk = $("errChk"),
    followChk = $("followChk"),
    autoScroll = $("autoScroll"),
    btnClearLogs = $("btnClearLogs"),
    btnDownload = $("btnDownload"),
    sinceSel = $("sinceSel"),
    linesSel = $("linesSel"),
    connState = $("connState"),
    lineCountEl = $("lineCount"),
    modeSeg = $("modeSeg"),
    jump = $("logsJump");
  var cur = null;
  var ES = null,
    pollTimer = null;
  var ssemode = typeof EventSource !== "undefined";
  var lastKey = null,
    lastTs = "";
  var stickBottom = false;

  var logs = [];
  var csum = [0];
  var totalLines = 0,
    shownLines = 0;
  var markIndex = -1,
    markTimer = null;

  var vpEls = new Map(),
    topSp = null,
    botSp = null;
  var vpProbe = null,
    vpChars = null,
    vpLead = null,
    vpWrapped = true,
    vpTick = false;

  var tlBar = $("logTimeline"),
    tlBins = $("tlBins"),
    tlStart = $("tlStart"),
    tlEnd = $("tlEnd"),
    tlTip = $("tlTip");
  var tlTimes = [],
    tlErrs = [],
    tlMin = null,
    tlMax = null,
    tlLines = 0;
  var tlCounts = null,
    tlErrPrev = null,
    tlBinEls = null,
    tlDirty = false,
    tlLastRender = 0;
  var NBINS = 160,
    TL_INTERVAL = 350;

  function raf(fn) {
    if (window.requestAnimationFrame) window.requestAnimationFrame(fn);
    else setTimeout(fn, 32);
  }

  function iso(ms) {
    return new Date(ms).toISOString();
  }
  function followOn() {
    return followChk.classList.contains("on");
  }
  function autoScrollOn() {
    return autoScroll.classList.contains("on");
  }
  function stream() {
    var s = outChk.checked,
      e = errChk.checked;
    return s && e ? "all" : s ? "stdout" : e ? "stderr" : "none";
  }
  function setConn(html, cls) {
    connState.textContent = html;
    connState.className = "conn " + (cls || "wait");
  }
  function ageSinceMs() {
    var v = sinceSel.value;
    if (!v) return 0;
    return Date.now() - parseFloat(v) * 3600000;
  }
  function tailValue() {
    var v = parseInt(linesSel.value, 10) || 0;
    if (v > 0) return v;

    return ageSinceMs() ? 500000 : 0;
  }

  function addLine(d) {
    var key =
      (d.ts || "") + "|" + (d.stream || "") + "|" + (d.line || d.error || "");
    if (key === lastKey) return;
    lastKey = key;
    var isErr = d.stream === "stderr";
    var tsMs = Date.parse(d.ts || "");
    if (!isNaN(tsMs)) {
      lastTs = d.ts;
      tlAdd(tsMs, isErr);
    }
    var msg = (d.line || d.error || "").replace(/\r?\n$/, "");
    var ok = lineMatches(msg, filter.value, logMode(), insens.checked);
    var it = {
      ts: d.ts || "",
      tsMs: isNaN(tsMs) ? -1 : tsMs,
      stream: isErr ? "stderr" : "stdout",
      msg: msg,
      isErr: isErr,
      match: ok ? 1 : 0,
      shown: ok,
      ctxN: false,
      h: 0,
      est: 0,
    };
    var pl = logs[logs.length - 1];
    if (!ok && ctxChk.checked && pl && pl.match) {
      it.shown = true;
      it.ctxN = true;
    }
    var flipped = false;
    if (ctxChk.checked && ok && pl && !pl.match && !pl.shown) {
      pl.shown = true;
      pl.ctxN = true;
      shownLines++;
      flipped = true;
    }
    if (it.shown) shownLines++;
    it.est = vpEstimate(it);
    logs.push(it);
    totalLines++;
    var m0 = logs.length;
    if (flipped) {
      var hp = vpSlotH(pl);
      csum[m0 - 1] = (m0 >= 2 ? csum[m0 - 2] : 0) + hp;
    }
    csum[m0] = (m0 >= 1 ? csum[m0 - 1] : 0) + (it.shown ? vpSlotH(it) : 0);
    if (logs.length > MAX_LOG_LINES) {
      dropOldestLines();
    }
    lineCountEl.textContent = shownLines + " / " + totalLines;
    followScrollBatch();
    vpNeed();
  }

  function dropOldestLines() {
    var cut = logs.length - MAX_LOG_LINES;
    if (cut <= 0) return;
    logs.splice(0, cut);
    tlTimes.splice(0, cut);
    tlErrs.splice(0, cut);
    csum.splice(0, cut);
    var delta = csum[0] || 0;
    for (var i = 0; i < csum.length; i++) {
      csum[i] -= delta;
    }
    vpClearRows();
    truncEl.textContent =
      "oldest lines dropped, " + MAX_LOG_LINES + " kept in memory";
  }

  function reset() {
    lastKey = null;
    lastTs = "";
    logs = [];
    csum = [0];
    markIndex = -1;
    if (markTimer) {
      clearTimeout(markTimer);
      markTimer = null;
    }
    totalLines = 0;
    shownLines = 0;
    vpClearRows();
    lineCountEl.textContent = "0";
    truncEl.textContent = "";
    tlReset();
    vpRefluid();
  }

  function clearLog() {
    closeStream();
    reset();
    setConn("-", "wait");
  }

  function loadHistory() {
    if (!cur || stream() === "none") {
      startStream();
      return;
    }
    var t = tailValue();
    if (t <= 0) {
      startStream();
      return;
    }
    var p = new URLSearchParams();
    p.set("tail", String(t));
    if (ageSinceMs()) p.set("since", iso(ageSinceMs()));
    p.set("stream", stream());
    fetch("/api/containers/" + encodeURIComponent(cur.id) + "/logs?" + p, {
      cache: "no-store",
    })
      .then(function (r) {
        if (r.status === 400 || r.status === 404) return null;
        if (!r.ok) {
          return r.text().then(function (body) {
            return { failed: true, text: body.trim() };
          });
        }
        return r.json();
      })
      .then(function (d) {
        if (!d) {
          close();
          return;
        }
        if (d.failed) {
          truncEl.textContent = d.text;
          startStream();
          return;
        }
        truncEl.textContent = d.truncated
          ? "output truncated by size limit"
          : "";
        var arr = d.logs || [];
        for (var i = 0; i < arr.length; i++) addLine(arr[i]);
        if (logBody.scrollTop) logBody.scrollTop = 0;
        startStream();
      })
      .catch(function (err) {
        var msg = err && err.message ? err.message : String(err);
        truncEl.textContent = "history failed: " + msg;
        setConn("History failed", "off");
      });
  }

  function streamURL(sinceVal) {
    return (
      "/api/containers/" +
      encodeURIComponent(cur.id) +
      "/logs?follow=1&stream=" +
      encodeURIComponent(stream()) +
      "&since=" +
      encodeURIComponent(sinceVal)
    );
  }

  function startStream() {
    if (!cur || mask.hidden) {
      closeStream();
      return;
    }
    closeStream();
    if (!followOn()) {
      setConn(totalLines ? "History loaded" : "Stopped", "done");
      return;
    }
    if (stream() === "none") {
      setConn("No stream selected", "off");
      return;
    }
    setConn("Connecting...", "wait");
    var sinceVal = lastTs || iso(Date.now());
    if (ssemode) {
      try {
        ES = new EventSource(streamURL(sinceVal));
        ES.onopen = function () {
          setConn("Live", "on");
        };
        ES.onmessage = function (e) {
          var d;
          try {
            d = JSON.parse(e.data);
          } catch (_) {
            return;
          }
          if (d) addLine(d);
        };
        ES.onerror = function () {
          if (ES && ES.readyState === EventSource.CLOSED)
            setConn("Closed", "off");
          else setConn("Network error, reconnecting...", "off");
        };
        return;
      } catch (_) {
        ssemode = false;
      }
    }
    setConn("Polling", "on");
    pollTick();
    pollTimer = setInterval(pollTick, 1000);
  }

  function pollTick() {
    if (!cur) return;
    var sinceVal = lastTs || iso(Date.now());
    var p =
      "tail=100000&stream=" +
      encodeURIComponent(stream()) +
      "&since=" +
      encodeURIComponent(sinceVal);
    fetch("/api/containers/" + encodeURIComponent(cur.id) + "/logs?" + p, {
      cache: "no-store",
    })
      .then(function (r) {
        if (r.status === 400 || r.status === 404) return null;
        if (!r.ok) return null;
        return r.json();
      })
      .then(function (d) {
        if (!d) return;
        if (d.truncated) truncEl.textContent = "output truncated by size limit";
        var arr = d.logs || [];
        for (var i = 0; i < arr.length; i++) addLine(arr[i]);
      })
      .catch(function () {
        setConn("Poll error", "off");
      });
  }

  function closeStream() {
    if (ES) {
      ES.close();
      ES = null;
    }
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  function reloadHistory() {
    if (!mask.hidden) {
      clearLog();
      loadHistory();
    }
  }

  function logMode() {
    var r = document.querySelector('input[name="logMode"]:checked');
    return r ? r.value : "text";
  }
  function parseTerms(q, mode) {
    if (mode === "regex" || mode === "text") {
      var s = q ? q.trim() : "";
      return s ? [s] : [];
    }
    return q.trim().split(/\s+/).filter(Boolean);
  }

  var MAX_LOG_LINES = 500000;

  var reKey = "",
    reVal = null;
  function compiledRe(q, mode, ins) {
    if (!q) {
      reKey = "\u0000none";
      reVal = null;
      return null;
    }
    var key = mode + "\u0000" + (ins ? "i" : "") + "\u0000" + q;
    if (key === reKey) return reVal;
    var terms = parseTerms(q, mode);
    var re = null;
    if (terms.length) {
      if (mode === "regex") {
        try {
          re = new RegExp(terms[0], ins ? "i" : "");
        } catch (_) {
          re = null;
        }
      } else {
        re = new RegExp(escRe(terms.join("|")), ins ? "i" : "");
      }
    }
    reKey = key;
    reVal = re;
    return re;
  }
  function isValidRegex(q) {
    try {
      new RegExp(q);
      return true;
    } catch (_) {
      return false;
    }
  }
  function lineMatches(raw, q, mode, ins) {
    if (!q) return true;
    var terms = parseTerms(q, mode);
    if (!terms.length) return true;
    var hay = ins ? raw.toLowerCase() : raw;
    if (mode === "regex") {
      var re = compiledRe(q, mode, ins);
      if (re) return re.test(raw);

      return hay.indexOf(terms[0].toLowerCase()) !== -1;
    }
    var test = function (t) {
      var w = ins ? t.toLowerCase() : t;
      return hay.indexOf(w) !== -1;
    };
    if (mode === "text") return test(terms[0]);
    if (mode === "or") return terms.some(test);
    return terms.every(test);
  }
  var hlKey = "",
    hlVal = null;
  function highlightRe() {
    var q = filter.value,
      ins = insens.checked;
    if (!q) {
      hlKey = "\u0000none";
      hlVal = null;
      return null;
    }
    var mode = logMode();
    var key = mode + "\u0000" + (ins ? "i" : "") + "\u0000" + q;
    if (key === hlKey) return hlVal;
    var re = compiledRe(q, mode, ins);
    var out = null;
    if (re) {
      var flags = re.flags.replace("g", "") + "g";
      try {
        out = new RegExp(re.source, flags);
      } catch (_) {
        out = null;
      }
    } else if (mode === "regex") {
      var terms = parseTerms(q, mode);
      if (terms.length)
        try {
          out = new RegExp(escRe(terms[0]), "g" + (ins ? "i" : ""));
        } catch (_) {
          out = null;
        }
    }
    hlKey = key;
    hlVal = out;
    return out;
  }
  function renderMsg(el, raw) {
    el.textContent = "";
    el.appendChild(highlightFrag(raw, highlightRe()));
  }

  function applyLogFilter() {
    var q = filter.value,
      mode = logMode(),
      ins = insens.checked,
      ctx = ctxChk.checked;
    var i,
      n = logs.length,
      match = new Array(n);
    for (i = 0; i < n; i++) {
      match[i] = lineMatches(logs[i].msg, q, mode, ins) ? 1 : 0;
    }
    shownLines = 0;
    for (i = 0; i < n; i++) {
      var ok =
        !!match[i] ||
        (ctx && ((i > 0 && match[i - 1]) || (i < n - 1 && match[i + 1])));
      logs[i].match = match[i] ? 1 : 0;
      logs[i].shown = ok;
      logs[i].ctxN = ok && !match[i];
      if (ok) shownLines++;
    }
    lineCountEl.textContent = shownLines + " / " + totalLines;
    filter.classList.toggle(
      "re",
      mode === "regex" && q !== "" && !isValidRegex(q),
    );
    rebuildCsum();
    vpClearRows();
    vpNeed();
  }

  function vpSpacers() {
    if (topSp || !logBody) return;
    topSp = document.createElement("div");
    topSp.className = "vspacer";
    botSp = document.createElement("div");
    botSp.className = "vspacer";
    logBody.appendChild(topSp);
    logBody.appendChild(botSp);
  }
  function vpDefaultH() {
    return vpLead || 21;
  }
  function vpEstimate(it) {
    if (!vpWrapped) return vpDefaultH();
    if (vpChars == null || !it.msg) return vpDefaultH();
    return Math.max(1, Math.ceil(it.msg.length / vpChars)) * vpDefaultH();
  }
  function vpSlotH(it) {
    return it.h > 0 ? it.h : it.est || vpDefaultH();
  }
  function rebuildCsum() {
    var n = logs.length,
      acc = 0,
      i,
      ns = new Array(n + 1);
    ns[0] = 0;
    for (i = 0; i < n; i++) {
      acc += logs[i].shown ? vpSlotH(logs[i]) : 0;
      ns[i + 1] = acc;
    }
    csum = ns;
  }
  function vpFirstAbove(y) {
    var lo = 0,
      hi = logs.length;
    while (lo < hi) {
      var mid = (lo + hi) >> 1;
      if (csum[mid + 1] > y) hi = mid;
      else lo = mid + 1;
    }
    return lo;
  }
  function vpClearRows() {
    vpEls.forEach(function (rec) {
      try {
        rec.el.remove();
      } catch (_) {}
    });
    vpEls.clear();
  }
  function fmtTs(ts) {
    var ms = Date.parse(ts);
    if (isNaN(ms)) return "";
    return new Date(ms).toISOString().replace("T", " ").replace("Z", "");
  }
  function vpRow(i) {
    var it = logs[i];
    var el = document.createElement("div");
    el.setAttribute("data-vix", String(i));
    var cls = "logline";
    if (it.ctxN) cls += " ctx";
    el.className = cls;
    var s = document.createElement("span");
    s.className = "lb-stream " + (it.isErr ? "stream-err" : "stream-out");
    s.textContent = it.stream;
    var t = document.createElement("span");
    t.className = "lb-time";
    t.textContent = fmtTs(it.ts);
    var m = document.createElement("span");
    m.className = "lb-msg";
    renderMsg(m, it.msg);
    el.appendChild(s);
    el.appendChild(t);
    el.appendChild(m);
    return el;
  }
  function vpRender() {
    vpTick = false;
    if (!logBody) return;
    if (logs.length === 0) {
      vpClearRows();
      if (topSp) {
        topSp.style.height = "0px";
        botSp.style.height = "0px";
      }
      return;
    }
    vpSpacers();
    if (!isFinite(csum[csum.length - 1])) rebuildCsum();
    var st = logBody.scrollTop || 0;
    var vh = logBody.clientHeight || 300;
    var over = 120;
    var lo = vpFirstAbove(st - over);
    var hi = vpFirstAbove(st + vh + over);
    var n = logs.length,
      i;
    vpEls.forEach(function (rec, ki) {
      if (ki < lo || ki >= hi || !logs[ki] || !logs[ki].shown) {
        try {
          rec.el.remove();
        } catch (_) {}
        vpEls.delete(ki);
      }
    });
    var remeasured = false;
    for (i = lo; i < hi && i < n; i++) {
      if (!logs[i].shown) continue;
      var rec = vpEls.get(i);
      if (!rec) {
        var el = vpRow(i);
        logBody.insertBefore(el, botSp);
        rec = { el: el, top: -1 };
        vpEls.set(i, rec);
      }
      if (rec.top !== csum[i]) {
        rec.top = csum[i];
        rec.el.style.top = csum[i] + "px";
      }
      var oh = rec.el.offsetHeight;
      if (logs[i].h !== oh) {
        logs[i].h = oh;
        remeasured = true;
      }
      rec.el.classList.toggle("tl-mark", i === markIndex);
    }
    topSp.style.height = csum[lo] + "px";
    botSp.style.height = csum[n] - csum[lo] + "px";
    if (remeasured) {
      rebuildCsum();
      for (i = lo; i < hi && i < n; i++) {
        if (!logs[i].shown) continue;
        var r2 = vpEls.get(i);
        if (r2) {
          r2.top = csum[i];
          r2.el.style.top = csum[i] + "px";
        }
      }
      topSp.style.height = csum[lo] + "px";
      botSp.style.height = csum[n] - csum[lo] + "px";
    }
  }
  function vpNeed() {
    if (vpTick || !logBody) return;
    vpTick = true;
    raf(vpRender);
  }
  function vpRefluid() {
    if (!logBody) return;
    vpSpacers();
    vpWrapped = wrapChk.checked;
    if (!vpProbe) {
      vpProbe = document.createElement("div");
      vpProbe.className = "logline";
      vpProbe.style.position = "absolute";
      vpProbe.style.top = "-1000px";
      vpProbe.style.left = "0";
      vpProbe.style.right = "0";
      vpProbe.style.visibility = "hidden";
      vpProbe.style.pointerEvents = "none";
      var ps = document.createElement("span");
      ps.className = "lb-stream stream-out";
      ps.textContent = "stdout";
      var pt = document.createElement("span");
      pt.className = "lb-time";
      pt.textContent = "ts";
      var pmsg = document.createElement("span");
      pmsg.className = "lb-msg";
      var pchar = document.createElement("i");
      pchar.className = "vp-char";
      pchar.textContent = "00000000000000000000000";
      pmsg.appendChild(pchar);
      vpProbe.appendChild(ps);
      vpProbe.appendChild(pt);
      vpProbe.appendChild(pmsg);
      logBody.appendChild(vpProbe);
    }
    var mspan = vpProbe.querySelector(".lb-msg");
    var cchr = vpProbe.querySelector(".vp-char");
    var mw = mspan && mspan.clientWidth ? mspan.clientWidth : 0;
    var cwpx =
      cchr && cchr.clientWidth && cchr.textContent
        ? cchr.clientWidth / cchr.textContent.length
        : 0;
    vpChars = mw > 0 && cwpx > 0 ? Math.max(8, Math.floor(mw / cwpx)) : 100;
    var oh = vpProbe.offsetHeight;
    var lh = mspan
      ? parseFloat(window.getComputedStyle(mspan).lineHeight || "0")
      : 0;
    vpLead = oh > 0 ? oh : lh > 0 ? Math.ceil(lh) + 3 : 21;
    var i;
    for (i = 0; i < logs.length; i++) {
      logs[i].est = vpEstimate(logs[i]);
      logs[i].h = 0;
    }
    rebuildCsum();
    vpClearRows();
    vpNeed();
  }

  function tlFmt(ms, span) {
    var d = new Date(ms);
    var p = function (x) {
      return String(x).padStart(2, "0");
    };
    var hm =
      p(d.getHours()) + ":" + p(d.getMinutes()) + ":" + p(d.getSeconds());
    if (span > 86400000)
      return d.getMonth() + 1 + "-" + p(d.getDate()) + " " + hm;
    return hm;
  }
  function tlReset() {
    tlTimes = [];
    tlErrs = [];
    tlMin = null;
    tlMax = null;
    tlLines = 0;
    tlCounts = null;
    tlErrPrev = null;
    if (tlBins) tlBins.innerHTML = "";
    tlBinEls = null;
    if (tlStart) tlStart.textContent = "";
    if (tlEnd) tlEnd.textContent = "";
    if (tlTip) {
      tlTip.classList.add("hidden");
      tlTip.textContent = "";
    }
    tlSchedule();
  }
  function tlAdd(ms, isErr) {
    if (isNaN(ms)) return;
    if (tlLines === 0) {
      tlMin = ms;
      tlMax = ms;
    } else {
      if (ms < tlMin) tlMin = ms;
      if (ms > tlMax) tlMax = ms;
    }
    tlLines++;
    tlTimes.push(ms);
    tlErrs.push(isErr ? 1 : 0);
    tlStart.textContent = tlFmt(tlMin, tlMax - tlMin);
    tlEnd.textContent = tlFmt(tlMax, tlMax - tlMin);
    tlSchedule();
  }
  function tlSchedule() {
    if (tlDirty) return;
    tlDirty = true;
    var wait = Math.max(0, TL_INTERVAL - (Date.now() - tlLastRender));
    setTimeout(function () {
      if (tlDirty) tlRender();
    }, wait);
  }
  function tlRender() {
    tlDirty = false;
    tlLastRender = Date.now();
    if (!tlBins || tlLines === 0) return;
    var span = tlMax - tlMin + 1;
    if (span <= 0) return;
    var counts = new Array(NBINS),
      errs = new Array(NBINS);
    var i;
    for (i = 0; i < NBINS; i++) {
      counts[i] = 0;
      errs[i] = 0;
    }
    for (i = 0; i < tlTimes.length; i++) {
      var idx = Math.floor(((tlTimes[i] - tlMin) / span) * NBINS);
      if (idx < 0) idx = 0;
      else if (idx >= NBINS) idx = NBINS - 1;
      counts[idx]++;
      if (tlErrs[i]) errs[idx] = 1;
    }
    if (tlCounts) {
      var same = true;
      for (i = 0; i < NBINS; i++) {
        if (tlCounts[i] !== counts[i] || tlErrPrev[i] !== errs[i]) {
          same = false;
          break;
        }
      }
      if (same) return;
    }
    var maxc = 1;
    for (i = 0; i < NBINS; i++) if (counts[i] > maxc) maxc = counts[i];
    if (!tlBinEls) {
      tlBinEls = [];
      for (i = 0; i < NBINS; i++) {
        var col = document.createElement("i");
        col.className = "bin";
        tlBins.appendChild(col);
        tlBinEls.push(col);
      }
    }
    for (i = 0; i < NBINS; i++) {
      var c = tlBinEls[i];
      c.classList.toggle("err", !!errs[i]);
      c.style.opacity = counts[i] ? "1" : "0";
      c.style.height = Math.round((counts[i] / maxc) * 94) + "%";
    }
    tlCounts = counts;
    tlErrPrev = errs;
  }
  function tlPos(ev) {
    var rect = tlBar.getBoundingClientRect();
    if (!rect.width || tlMin === null) return null;
    var x = ev.clientX - rect.left;
    var frac = Math.max(0, Math.min(1, x / rect.width));
    return { frac: frac, t: tlMin + frac * (tlMax - tlMin), x: x };
  }
  function tlJump(slot) {
    if (slot < 0 || slot >= logs.length) return;
    logBody.scrollTop = Math.max(0, csum[slot] - logBody.clientHeight / 3);
    markIndex = slot;
    if (markTimer) clearTimeout(markTimer);
    markTimer = setTimeout(function () {
      markIndex = -1;
    }, 1600);
    vpNeed();
  }
  tlBar.addEventListener("mousemove", function (ev) {
    var p = tlPos(ev);
    if (!p || !tlCounts) return;
    tlTip.classList.remove("hidden");
    var bidx = Math.floor(p.frac * NBINS);
    if (bidx < 0) bidx = 0;
    else if (bidx >= NBINS) bidx = NBINS - 1;
    var c = tlCounts[bidx] || 0;
    var span = tlMax - tlMin + 1;
    var t0b = tlMin + (bidx / NBINS) * span;
    var t1b = tlMin + ((bidx + 1) / NBINS) * span;
    tlTip.textContent =
      c + " logs \u00B7 " + tlFmt(t0b, span) + " \u2192 " + tlFmt(t1b, span);
    tlTip.style.left = p.x + "px";
  });
  tlBar.addEventListener("mouseleave", function () {
    tlTip.classList.add("hidden");
  });

  function vpLowerBound(t) {
    var lo = 0,
      hi = logs.length;
    while (lo < hi) {
      var mid = (lo + hi) >> 1;
      if (logs[mid].tsMs < t) lo = mid + 1;
      else hi = mid;
    }
    return lo;
  }
  tlBar.addEventListener("click", function (ev) {
    var p = tlPos(ev);
    if (!p) return;
    var span = tlMax - tlMin + 1;
    var b = Math.floor(p.frac * NBINS);
    if (b < 0) b = 0;
    else if (b >= NBINS) b = NBINS - 1;
    var tStart = tlMin + (b / NBINS) * span;
    var i = vpLowerBound(tStart);

    var n = logs.length,
      limit = Math.min(n, i + 64);
    for (; i < limit; i++) {
      if (logs[i].shown) break;
    }
    if (i >= limit) {
      i = vpLowerBound(tStart);
    }
    if (i >= n) i = n - 1;
    if (i < 0) return;
    tlJump(i);
  });

  var scrollTick = false;
  function followScrollBatch() {
    if (!autoScrollOn()) return;
    if (mask.hidden || scrollTick) return;
    scrollTick = true;
    raf(function () {
      scrollTick = false;
      if (!autoScrollOn()) return;
      if (mask.hidden) return;
      raf(function () {
        if (!autoScrollOn()) return;
        if (mask.hidden) return;
        if (
          !stickBottom &&
          logBody.scrollHeight - logBody.scrollTop - logBody.clientHeight > 80
        )
          return;
        stickBottom = true;
        logBody.scrollTop = logBody.scrollHeight;
      });
    });
  }

  var filterTimer = null;
  function applyLogFilterSoon() {
    if (filterTimer) clearTimeout(filterTimer);
    filterTimer = setTimeout(function () {
      filterTimer = null;
      applyLogFilter();
    }, 120);
  }
  filter.addEventListener("input", applyLogFilterSoon);
  insens.addEventListener("change", applyLogFilter);
  ctxChk.addEventListener("change", applyLogFilter);
  document.querySelectorAll('input[name="logMode"]').forEach(function (r) {
    r.addEventListener("change", applyLogFilter);
  });

  autoScroll.addEventListener("click", function () {
    autoScroll.classList.toggle("on");
    if (autoScrollOn()) {
      stickBottom = true;
      logBody.scrollTop = logBody.scrollHeight;
    } else {
      stickBottom = false;
    }
    vpNeed();
  });
  logBody.addEventListener("scroll", function () {
    var near =
      logBody.scrollHeight - logBody.scrollTop - logBody.clientHeight < 60;
    if (!near) {
      stickBottom = false;
      if (autoScrollOn()) autoScroll.classList.remove("on");
    }
    jump.classList.toggle("show", !near);
    vpNeed();
  });
  jump.addEventListener("click", function () {
    stickBottom = true;
    logBody.scrollTop = logBody.scrollHeight;
    if (!autoScrollOn()) autoScroll.classList.add("on");
    vpNeed();
  });

  sinceSel.addEventListener("change", reloadHistory);
  linesSel.addEventListener("change", reloadHistory);
  outChk.addEventListener("change", reloadHistory);
  errChk.addEventListener("change", reloadHistory);

  followChk.addEventListener("click", function () {
    followChk.classList.toggle("on");
    if (!followOn()) {
      closeStream();
      setConn("Stopped", "off");
      return;
    }
    if (lastTs && totalLines) {
      startStream();
      return;
    }
    reloadHistory();
  });

  wrapChk.addEventListener("change", function () {
    logBody.classList.toggle("no-wrap", !wrapChk.checked);
    vpRefluid();
  });
  tsChk.addEventListener("change", function () {
    logBody.classList.toggle("no-ts", !tsChk.checked);
    vpRefluid();
  });
  streamChk.addEventListener("change", function () {
    logBody.classList.toggle("no-stream", !streamChk.checked);
    vpRefluid();
  });

  btnClearLogs.addEventListener("click", function () {
    clearLog();
    loadHistory();
  });

  btnDownload.addEventListener("click", function () {
    var lines = [];
    for (var i = 0; i < logs.length; i++) {
      var it = logs[i];
      if (it.shown) lines.push(it.stream + " " + fmtTs(it.ts) + " " + it.msg);
    }
    if (!lines.length) return;
    var blob = new Blob([lines.join("\n")], {
      type: "text/plain;charset=utf-8",
    });
    var a = document.createElement("a");
    var f = new Date().toISOString().slice(0, 19).replace(/[:T]/g, "-");
    a.href = URL.createObjectURL(blob);
    a.download = (cur ? cur.name : "container") + "-" + f + ".log";
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(a.href);
  });

  var logKey = "docker-exporter-logs";
  function persistLogs() {
    try {
      localStorage.setItem(
        logKey,
        JSON.stringify({
          mode: logMode(),
          ins: insens.checked,
          ctx: ctxChk.checked,
          out: outChk.checked,
          err: errChk.checked,
          auto: autoScrollOn(),
          since: sinceSel.value,
          lines: linesSel.value,
        }),
      );
    } catch (_) {}
  }
  function restoreLogs() {
    var raw;
    try {
      raw = localStorage.getItem(logKey);
    } catch (_) {
      return;
    }
    if (!raw) return;
    var s;
    try {
      s = JSON.parse(raw);
    } catch (_) {
      return;
    }
    if (!s) return;
    if (s.mode) {
      var r = document.querySelector(
        'input[name="logMode"][value="' + s.mode + '"]',
      );
      if (r) r.checked = true;
    }
    if (s.since) sinceSel.value = s.since;
    if (s.lines) linesSel.value = s.lines;
    if (typeof s.ins === "boolean") insens.checked = s.ins;
    if (typeof s.ctx === "boolean") ctxChk.checked = s.ctx;
    if (typeof s.out === "boolean") outChk.checked = s.out;
    if (typeof s.err === "boolean") errChk.checked = s.err;
    autoScroll.classList.toggle("on", !!s.auto);
  }
  var FOCUSABLE =
    'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';
  var lastFocus = null;
  function focusables() {
    return Array.prototype.filter.call(
      mask.querySelectorAll(FOCUSABLE),
      function (el) {
        return el.offsetParent !== null;
      },
    );
  }
  function open(id, name, compose) {
    cur = { id: id, name: name };
    title.textContent = compose && name ? compose + "/" + name : name;
    lastFocus = document.activeElement;
    mask.hidden = false;
    document.body.style.overflow = "hidden";
    stickBottom = true;
    reset();

    var f = focusables();
    if (f.length) f[0].focus();
    loadHistory();
  }
  function close() {
    closeStream();
    persistLogs();
    cur = null;
    mask.hidden = true;
    document.body.style.overflow = "";
    reset();

    if (lastFocus && lastFocus.focus) lastFocus.focus();
    lastFocus = null;
  }
  $("logsClose").addEventListener("click", close);
  mask.addEventListener("click", function (e) {
    if (e.target === mask) close();
  });
  document.addEventListener("click", function (e) {
    var row = e.target.closest("tr[data-id]");
    if (row) open(row.dataset.id, row.dataset.name, row.dataset.compose);
  });
  document.addEventListener("keydown", function (e) {
    if (mask.hidden) return;
    if (e.key === "Escape") {
      close();
      return;
    }

    if (e.key !== "Tab") return;
    var f = focusables();
    if (!f.length) return;
    var first = f[0],
      last = f[f.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  });
  [filter, insens, ctxChk, outChk, errChk, sinceSel, linesSel].forEach(
    function (el) {
      el.addEventListener("change", persistLogs);
    },
  );
  modeSeg.addEventListener("change", persistLogs);
  autoScroll.addEventListener("click", persistLogs);
  restoreLogs();

  vpSpacers();
})();

(function () {
  var tip = document.createElement("div");
  tip.id = "ui-tooltip";
  document.body.appendChild(tip);
  document.addEventListener("mouseover", function (e) {
    var el = e.target.closest ? e.target.closest("[data-tip]") : null;
    if (!el) return;

    var text = el.getAttribute("data-tip");
    if (!text || !text.trim()) return;
    var r = el.getBoundingClientRect();
    tip.textContent = text;
    var x = r.left + r.width / 2;
    var half = tip.offsetWidth / 2;
    if (x - half < 8) x = half + 8;
    if (x + half > window.innerWidth - 8) x = window.innerWidth - half - 8;
    tip.style.left = x + "px";
    if (r.top < 110) {
      tip.style.transform = "translate(-50%, 0)";
      tip.style.top = r.bottom + 10 + "px";
    } else {
      tip.style.transform = "translate(-50%, -100%)";
      tip.style.top = r.top - 10 + "px";
    }
    tip.classList.add("show");
  });
  document.addEventListener("mouseout", function (e) {
    var to = e.relatedTarget;
    if (to && to.closest && to.closest("[data-tip]")) return;
    tip.classList.remove("show");
  });
})();
