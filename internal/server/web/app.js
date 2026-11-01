"use strict";

const REFRESH_MS = 30000;
const $ = (id) => document.getElementById(id);
const esc = (s) => String(s).replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
const full = (n) => Math.round(n).toLocaleString("en-US");

function short(n) {
  const a = Math.abs(n);
  for (const [v, u] of [[1e12, "T"], [1e9, "B"], [1e6, "M"], [1e3, "K"]]) {
    if (a >= v) return (n / v).toFixed(a >= v * 100 ? 0 : a >= v * 10 ? 1 : 2) + u;
  }
  return String(Math.round(n));
}

function ago(iso) {
  if (new Date(iso).getUTCFullYear() < 2000) return "pending";
  const min = Math.round((Date.now() - new Date(iso)) / 60000);
  if (min < 1) return "just now";
  if (min < 60) return `${min} min ago`;
  return `${Math.floor(min / 60)} h ${min % 60} min ago`;
}

const dateTime = (t) => new Date(t).toLocaleString("en-US", { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
const dayLabel = (t) => new Date(t).toLocaleDateString("en-US", { month: "short", day: "numeric", timeZone: "UTC" });

function rankMove(n) {
  if (!n) return `<span class="muted">same</span>`;
  return n > 0 ? `<span class="up">▲ ${n}</span>` : `<span class="down">▼ ${-n}</span>`;
}

function tile(label, value, sub) {
  return `<div class="tile"><small>${esc(label)}</small><strong>${esc(value)}</strong><em>${esc(sub || "")}</em></div>`;
}

async function load(url, render) {
  async function tick() {
    try {
      const res = await fetch(url);
      if (!res.ok) throw new Error(res.status === 404 ? "No such participant." : "Waiting for the first stats batch.");
      render(await res.json());
      $("error").hidden = true;
    } catch (e) {
      $("error").textContent = e.message;
      $("error").hidden = false;
    }
  }
  await tick();
  setInterval(tick, REFRESH_MS);
}

function initOverview() {
  let data, query = "", key = "rank", desc = false;
  const columns = [
    ["rank", "#"], ["name", "Folder"], ["points", "Points"], ["points24h", "24 h"],
    ["wus", "WUs"], ["rankChange24h", "Move"], ["final", "Forecast"],
  ];
  const value = (s, k) => (k === "final" ? s.forecast.final : s[k]);

  function drawBoard() {
    const q = query.toLowerCase();
    const rows = data.standings.filter((s) => s.name.toLowerCase().includes(q));
    rows.sort((a, b) => {
      const x = value(a, key), y = value(b, key);
      const c = typeof x === "string" ? x.localeCompare(y) : x - y;
      return desc ? -c : c;
    });
    const head = columns.map(([k, label]) => `<th scope="col" aria-sort="${k === key ? (desc ? "descending" : "ascending") : "none"}"><button data-k="${k}">${label}</button></th>`).join("");
    const body = rows.map((s) => `<tr>
      <td>${s.rank}</td>
      <td><a href="/donor/${encodeURIComponent(s.name)}">${esc(s.name)}</a></td>
      <td title="${full(s.points)}">${s.pointsError ? "~" : ""}${short(s.points)}</td>
      <td>${short(s.points24h)}</td>
      <td>${full(s.wus)}</td>
      <td>${rankMove(s.rankChange24h)}</td>
      <td>${short(s.forecast.final)}</td>
    </tr>`).join("");
    $("board").innerHTML = `<thead><tr>${head}</tr></thead><tbody>${body}</tbody>`;
  }

  $("board").addEventListener("click", (e) => {
    const k = e.target.dataset && e.target.dataset.k;
    if (!k) return;
    desc = k === key ? !desc : k !== "rank" && k !== "name";
    key = k;
    drawBoard();
  });
  $("search").addEventListener("input", (e) => { query = e.target.value; drawBoard(); });

  load("/api/overview", (o) => {
    data = o;
    const left = Math.max(0, new Date(o.end) - Date.now());
    const days = Math.floor(left / 864e5), hours = Math.floor(left / 36e5) % 24;
    $("status").innerHTML = `Last stats batch <b>${ago(o.lastSample)}</b>`;
    const ltt = o.teams.find((t) => t.team === o.team);
    $("tiles").innerHTML =
      tile("Team LTT this event", ltt ? short(ltt.credit) : "n/a", ltt ? `rank ${ltt.rank} of tracked teams` : "") +
      tile("Team points, 24 h", ltt ? short(ltt.credit24h) : "n/a", o.partial24h ? "since tracking began" : "") +
      tile("Participants folding", `${o.active} of ${o.donors}`, "points seen since tracking began") +
      tile("Time left", `${days}d ${hours}h`, "ends Dec 6 23:59 UTC");
    drawBoard();
    const missing = o.unresolved || [];
    $("unresolved").hidden = !missing.length;
    $("unresolved").textContent = `${missing.length} sign-ups are not shown because their forum name does not match a Folding@home donor: ${missing.join(", ")}.`;

    const top = o.teams.slice(0, 10);
    if (ltt && !top.includes(ltt)) top.push(ltt);
    const scale = Math.max(...top.map((t) => t.credit), 1);
    $("teams").innerHTML = top.map((t) => `<div class="bar ${t.team === o.team ? "" : "other"}">
      <span>${t.rank}. ${esc(t.name)}</span><span>${short(t.credit)}</span>
      <div class="fill" style="width:${(100 * t.credit) / scale}%"></div>
    </div>`).join("");
  });
}

function axisTicks(lo, hi, count) {
  const span = hi - lo || 1;
  const raw = span / count, mag = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 5, 10].map((m) => m * mag).find((s) => s >= raw);
  const out = [];
  for (let v = Math.ceil(lo / step) * step; v <= hi; v += step) out.push(v);
  return out;
}

function frame(el, height) {
  return { w: Math.max(el.clientWidth, 280), h: height, l: 54, r: 12, t: 10, b: 24 };
}

function svgOpen(f, label) {
  return `<svg viewBox="0 0 ${f.w} ${f.h}" role="img" aria-label="${esc(label)}">`;
}

function yAxis(f, ticks, y, fmt) {
  return ticks.map((v) => `<line x1="${f.l}" x2="${f.w - f.r}" y1="${y(v)}" y2="${y(v)}" stroke="var(--grid)"/>
    <text x="${f.l - 8}" y="${y(v) + 4}" text-anchor="end">${fmt(v)}</text>`).join("");
}

function hover(el, f, points) {
  let tip = el.querySelector(".tip");
  if (!tip) { tip = document.createElement("div"); tip.className = "tip"; tip.hidden = true; el.append(tip); }
  const svg = el.querySelector("svg"), rule = svg.querySelector(".rule"), dot = svg.querySelector(".dot");
  svg.addEventListener("pointermove", (e) => {
    const box = svg.getBoundingClientRect(), scale = f.w / box.width;
    const x = (e.clientX - box.left) * scale;
    let best = points[0];
    for (const p of points) if (Math.abs(p.x - x) < Math.abs(best.x - x)) best = p;
    if (rule) { rule.setAttribute("x1", best.x); rule.setAttribute("x2", best.x); rule.style.display = ""; }
    if (dot) { dot.setAttribute("cx", best.x); dot.setAttribute("cy", best.y); dot.style.display = ""; }
    tip.innerHTML = best.tip;
    tip.hidden = false;
    const left = best.x / scale;
    tip.style.left = `${Math.max(0, Math.min(left + 10, box.width - tip.offsetWidth))}px`;
  });
  svg.addEventListener("pointerleave", () => {
    tip.hidden = true;
    if (rule) rule.style.display = "none";
    if (dot) dot.style.display = "none";
  });
}

const marks = (f) => `<line class="rule" y1="${f.t}" y2="${f.h - f.b}" stroke="var(--muted)" style="display:none"/>
  <circle class="dot" r="4" fill="var(--series)" stroke="var(--surface)" stroke-width="2" style="display:none"/>`;

function dateAxis(f, x, t0, t1) {
  const out = [];
  const stepDays = Math.max(1, Math.ceil((t1 - t0) / 864e5 / Math.floor(f.w / 80)));
  for (let t = Math.ceil(t0 / 864e5) * 864e5; t <= t1; t += stepDays * 864e5) {
    out.push(`<text x="${x(t)}" y="${f.h - 6}" text-anchor="middle">${dayLabel(t)}</text>`);
  }
  return out.join("");
}

function drawGrowth(d, start, end) {
  const f = frame($("growth"), 300), st = d.standing, fc = st.forecast;
  const t0 = +new Date(start), t1 = +new Date(end);
  const hist = d.history.map((p) => ({ t: +new Date(p.at), v: p.points }));
  const lastT = hist.length ? hist[hist.length - 1].t : Date.now();
  const hi = Math.max(fc.high, st.points, 1);
  const ticks = axisTicks(0, hi, 4);
  const top = Math.max(hi, ticks[ticks.length - 1]);
  const x = (t) => f.l + ((t - t0) / (t1 - t0)) * (f.w - f.l - f.r);
  const y = (v) => f.h - f.b - (v / top) * (f.h - f.t - f.b);
  const line = hist.map((p, i) => `${i ? "L" : "M"}${x(p.t)} ${y(p.v)}`).join("");
  const pts = hist.map((p) => ({ x: x(p.t), y: y(p.v), tip: `${dateTime(p.t)}<br><b>${full(p.v)}</b> points` }));
  pts.push({ x: x(t1), y: y(fc.final), tip: `Dec 6 forecast<br><b>${full(fc.final)}</b><br>${short(fc.low)} to ${short(fc.high)}` });
  $("growth").innerHTML = svgOpen(f, `Points so far and forecast, ending near ${short(fc.final)}`) +
    yAxis(f, ticks, y, short) + dateAxis(f, x, t0, t1) +
    `<path d="M${x(lastT)} ${y(st.points)}L${x(t1)} ${y(fc.high)}L${x(t1)} ${y(fc.low)}Z" fill="var(--band)"/>
     <path d="M${x(lastT)} ${y(st.points)}L${x(t1)} ${y(fc.final)}" fill="none" stroke="var(--series)" stroke-width="2" stroke-dasharray="5 5"/>
     <path d="${line}" fill="none" stroke="var(--series)" stroke-width="2" stroke-linejoin="round"/>
     <circle cx="${x(lastT)}" cy="${y(st.points)}" r="4" fill="var(--series)" stroke="var(--surface)" stroke-width="2"/>
     <text x="${x(t1)}" y="${Math.max(y(fc.final) - 8, 12)}" text-anchor="end">${short(fc.final)}</text>` +
    marks(f) + "</svg>";
  hover($("growth"), f, pts);
}

function drawRank(d) {
  const f = frame($("rank"), 220), hist = d.history;
  if (hist.length < 2) {
    $("rank").innerHTML = `<p class="note">Rank history starts after the second stats batch.</p>`;
    return;
  }
  const t0 = +new Date(hist[0].at), t1 = +new Date(hist[hist.length - 1].at);
  const best = Math.min(...hist.map((p) => p.rank)), worst = Math.max(...hist.map((p) => p.rank));
  const lo = Math.max(1, best - 2), hi = worst + 2;
  const x = (t) => f.l + ((t - t0) / (t1 - t0 || 1)) * (f.w - f.l - f.r);
  const y = (r) => f.t + ((r - lo) / (hi - lo)) * (f.h - f.t - f.b);
  const path = hist.map((p, i) => `${i ? "L" : "M"}${x(+new Date(p.at))} ${y(p.rank)}`).join("");
  $("rank").innerHTML = svgOpen(f, `Rank over time, now ${d.standing.rank}`) +
    yAxis(f, axisTicks(lo, hi, 4).filter((v) => Number.isInteger(v)), y, (v) => `#${v}`) + dateAxis(f, x, t0, t1) +
    `<path d="${path}" fill="none" stroke="var(--series)" stroke-width="2"/>` + marks(f) + "</svg>";
  hover($("rank"), f, hist.map((p) => ({ x: x(+new Date(p.at)), y: y(p.rank), tip: `${dateTime(p.at)}<br>Rank <b>#${p.rank}</b>` })));
}

function drawDaily(d) {
  const f = frame($("daily"), 220), days = d.daily;
  if (!days.length) { $("daily").innerHTML = `<p class="note">No daily output yet.</p>`; return; }
  const hi = Math.max(...days.map((v) => v.points), 1), ticks = axisTicks(0, hi, 4);
  const top = Math.max(hi, ticks[ticks.length - 1]);
  const width = (f.w - f.l - f.r) / Math.max(days.length, 7);
  const y = (v) => f.h - f.b - (v / top) * (f.h - f.t - f.b);
  const barW = Math.min(width - 2, 28);
  const pts = days.map((v, i) => ({ x: f.l + width * i + width / 2, y: y(v.points), tip: `${dayLabel(v.day)}<br><b>${full(v.points)}</b> points<br>${v.wus} WUs` }));
  const bars = days.map((v, i) => {
    const px = pts[i].x - barW / 2, py = y(v.points), base = f.h - f.b, r = Math.min(4, Math.max(base - py, 0));
    return `<path d="M${px} ${base}V${py + r}Q${px} ${py} ${px + r} ${py}H${px + barW - r}Q${px + barW} ${py} ${px + barW} ${py + r}V${base}Z" fill="var(--series)"/>`;
  }).join("");
  const step = Math.ceil(days.length / 7);
  const labels = days.map((v, i) => (i % step ? "" : `<text x="${pts[i].x}" y="${f.h - 6}" text-anchor="middle">${dayLabel(v.day)}</text>`)).join("");
  $("daily").innerHTML = svgOpen(f, "Points per day") + yAxis(f, ticks, y, short) + labels + bars + `<line class="rule" y1="${f.t}" y2="${f.h - f.b}" stroke="none"/></svg>`;
  hover($("daily"), f, pts);
}

function drawMinimums(s, o) {
  const e = s.eligibility;
  const row = (label, have, need, text) => `<li><div class="row"><span>${label}</span><span>${text}</span></div>
    <div class="meter"><i style="width:${Math.min(100, (100 * have) / need)}%"></i></div></li>`;
  const hidden = e.hiddenDays ? ` Up to ${e.hiddenDays} earlier active days may not be counted yet.` : "";
  const days = e.met ? "All minimums met" : e.reachable
    ? `${e.remainingDays} days left, ${Math.max(0, o.minDays - e.activeDays)} more active days needed.${hidden}`
    : `Not enough days left to reach ${o.minDays} active days`;
  $("minimums").innerHTML =
    row("Active days", e.activeDays, o.minDays, `${e.hiddenDays ? "at least " : ""}${e.activeDays} of ${o.minDays}`) +
    row("Points", s.points, o.minPoints, `${short(s.points)} of ${short(o.minPoints)}`) +
    row("Work units", s.wus, o.minWus, `${s.wus} of ${o.minWus}`) +
    `<li class="muted">${days}</li>`;
}

function drawRivals(d, o) {
  const s = d.standing;
  const daysLeft = Math.max(0, (new Date(o.end) - new Date(o.lastSample)) / 864e5);
  const eta = (days, text) => (days <= daysLeft ? `${text} in about ${Math.ceil(days)} days` : "no pass before the event ends at this pace");
  const gap = (o, word) => {
    if (!o) return "";
    const diff = o.forecast.pointsPerDay - s.forecast.pointsPerDay;
    const lead = Math.abs(o.points - s.points);
    let when = "pace is level or unknown";
    if (word === "ahead" && diff < 0) when = eta(lead / -diff, "you pass them");
    if (word === "ahead" && diff > 0) when = "pulling away";
    if (word === "behind" && diff > 0) when = eta(lead / diff, "they pass you");
    if (word === "behind" && diff < 0) when = "falling back";
    return `<div><span>#${o.rank} <a href="/donor/${encodeURIComponent(o.name)}">${esc(o.name)}</a></span>
      <span>${short(lead)} ${word}</span></div><div class="muted"><span>${when}</span></div>`;
  };
  $("rivals").innerHTML = gap(d.ahead, "ahead") + gap(d.behind, "behind") || `<p class="note">No rivals yet.</p>`;
}

function initDonor() {
  const name = decodeURIComponent(location.pathname.split("/").pop());
  document.title = `${name} - Folding Month IX`;
  $("name").textContent = name;
  load(`/api/donors/${encodeURIComponent(name)}`, async (d) => {
    const window_ = await (await fetch("/api/overview")).json();
    const s = d.standing, fc = s.forecast;
    $("status").textContent = `Last stats batch ${ago(window_.lastSample)}`;
    $("name").textContent = s.name;
    $("tiles").innerHTML =
      tile("Rank", `#${s.rank}`, `of ${d.field} participants`) +
      tile("Points this event", `${s.pointsError ? "~" : ""}${short(s.points)}`, s.pointsError ? `may be low by up to ${short(s.pointsError)}` : full(s.points)) +
      tile("Pace", `${short(fc.pointsPerDay)}/day`, `${short(s.points24h)} ${window_.partial24h ? "since tracking began" : "in the last 24 h"}`) +
      tile("Forecast finish", short(fc.final), `rank #${fc.finalRank}, range ${short(fc.low)} to ${short(fc.high)}`);
    $("forecastNote").textContent = fc.confident
      ? "The dashed line uses the last 24 hours. The band uses this folder's slow and fast days (20th to 80th percentile)."
      : "Early estimate: fewer than three full days of data, so the band is a flat 50% on either side of the current pace.";
    drawGrowth(d, window_.start, window_.end);
    drawRank(d);
    drawDaily(d);
    drawMinimums(s, window_);
    drawRivals(d, window_);
  });
}
