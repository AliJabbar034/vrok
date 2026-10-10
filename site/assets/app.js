/* The site's behaviour: copy a command, preselect the visitor's OS, point the
   archive links at whatever the newest release actually is, replay the CLI
   in the "Watch it run" section, show the star count once it means something,
   ask for a star once a visitor has shown they care, count the few actions
   that say whether the page works, ask whether each guide section helped,
   prefill problem reports, and list the releases. Everything
   degrades to a working page with JavaScript off — the commands are in the
   markup and the archive links fall back to the release page. */

const REPO = "AliJabbar034/vrok";
const RELEASES = `https://github.com/${REPO}/releases`;

/* ---------- counts ---------- */

/* Umami, cookieless, loaded from each page's <head>. Its script is deferred
   and often blocked, so every call goes through here and is a no-op when it
   is missing. Events carry no identifiers, only what was done and where.
   guide.html#privacy lists them; keep that list in step with this file. */

function track(name, data) {
  try {
    window.umami?.track(name, data);
  } catch {
    /* counting must never break the page */
  }
}

// Which OS a copied command is for: the tab that owns its panel, else the
// visitor's own OS (the closing command has no tabs).
function osOf(node) {
  const panel = node.closest("[role='tabpanel']");
  const tab = panel && document.querySelector(`[aria-controls="${panel.id}"]`);
  return tab?.dataset.os || detectOS() || "other";
}

function wireCounts() {
  document.addEventListener("click", (event) => {
    const link = event.target.closest("a[href]");
    if (!link) return;
    if (link.matches("a[data-asset]")) {
      track("download-clicked", { file: link.dataset.asset });
    } else if (/^https:\/\/github\.com\/AliJabbar034\/vrok/.test(link.href)) {
      track("github-clicked", { to: new URL(link.href).pathname });
    }
  });

  // A few errors a page view is enough to see a pattern; more is a loop.
  let errors = 0;
  const report = (message, where) => {
    if (++errors > 5) return;
    track("js-error", {
      message: String(message).slice(0, 200),
      where: String(where || "").slice(0, 200)
    });
  };
  addEventListener("error", (event) =>
    report(
      event.message,
      event.filename && `${event.filename.split("/").pop()}:${event.lineno}`
    )
  );
  addEventListener("unhandledrejection", (event) =>
    report(event.reason?.message || event.reason, "promise")
  );
}

/* ---------- copy ---------- */

function wireCopy() {
  for (const button of document.querySelectorAll("[data-copy]")) {
    const field = button.closest(".cmd")?.querySelector(".cmd__text");
    const label = button.querySelector(".copy__label");
    if (!field || !label) continue;

    let reset;
    const settle = (text, done) => {
      label.textContent = text;
      if (done) button.dataset.done = "true";
      else delete button.dataset.done;
      clearTimeout(reset);
      reset = setTimeout(() => {
        delete button.dataset.done;
        label.textContent = "Copy";
      }, 2200);
    };

    button.addEventListener("click", async () => {
      const text = field.textContent.replace(/\s+/g, " ").trim();
      try {
        await navigator.clipboard.writeText(text);
        settle("Copied", true);
        track("install-copied", { os: osOf(button) });
      } catch {
        // Clipboard writes are refused on insecure origins and by some
        // permission policies. Selecting the command is a worse but honest
        // fallback, and the shortcut named has to match the visitor's OS.
        const range = document.createRange();
        range.selectNodeContents(field);
        const selection = window.getSelection();
        selection.removeAllRanges();
        selection.addRange(range);
        settle(detectOS() === "mac" ? "Press ⌘C" : "Press Ctrl+C", false);
        track("install-copied", { os: osOf(button), clipboard: "refused" });
      }
    });
  }
}

/* ---------- os ---------- */

function detectOS() {
  const ua = navigator.userAgent;
  const plat = navigator.platform || "";
  if (/Win/i.test(plat) || /Windows/i.test(ua)) return "win";
  if (/Mac/i.test(plat) || /Mac OS X/i.test(ua)) return "mac";
  if (/Linux|X11|CrOS/i.test(plat + ua) && !/Android/i.test(ua)) return "linux";
  return null;
}

function detectArch() {
  const ua = navigator.userAgent;
  // Browsers do not expose Apple silicon, so a Mac is assumed to be arm64
  // unless the UA says otherwise; that is the common case now and the
  // universal installer gets it right regardless.
  if (/arm64|aarch64/i.test(ua)) return "arm64";
  if (/Mac/i.test(navigator.platform || "")) return "arm64";
  return "amd64";
}

function selectOS(os, groups) {
  for (const group of groups) {
    const tabs = [...group.querySelectorAll("[data-os]")];
    if (!tabs.some((t) => t.dataset.os === os)) continue;
    for (const tab of tabs) {
      const on = tab.dataset.os === os;
      tab.setAttribute("aria-selected", String(on));
      const panel = document.getElementById(tab.getAttribute("aria-controls"));
      if (panel) panel.hidden = !on;
    }
  }
}

function wireOS() {
  const groups = [...document.querySelectorAll(".os")];
  if (!groups.length) return;

  for (const group of groups) {
    const tabs = [...group.querySelectorAll("[data-os]")];
    for (const [i, tab] of tabs.entries()) {
      tab.addEventListener("click", () => selectOS(tab.dataset.os, groups));
      tab.addEventListener("keydown", (event) => {
        const step = { ArrowRight: 1, ArrowLeft: -1 }[event.key];
        if (!step) return;
        event.preventDefault();
        const next = tabs[(i + step + tabs.length) % tabs.length];
        next.focus();
        selectOS(next.dataset.os, groups);
      });
    }
  }

  const os = detectOS();
  if (!os) return;
  selectOS(os, groups);

  const note = document.querySelector("[data-detected]");
  if (note) {
    const name = { mac: "macOS", linux: "Linux", win: "Windows" }[os];
    note.textContent = `Detected ${name} · ${detectArch()}`;
    note.hidden = false;
  }

  const mine = `${{ mac: "darwin", linux: "linux", win: "windows" }[os]}-${detectArch()}`;
  const slot = document.querySelector(`.slot[data-plat="${mine}"]`);
  if (slot) {
    slot.dataset.yours = "true";
    const line = slot.querySelector(".slot__id p");
    if (line)
      line.insertAdjacentHTML(
        "beforeend",
        ' <span class="yours">· yours</span>'
      );
  }
}

/* ---------- release ---------- */

const FRESH_MS = 21 * 24 * 60 * 60 * 1000;

async function wireRelease() {
  const needsTag = document.querySelectorAll("[data-latest-tag]");
  const proofTag = document.querySelector("[data-proof-tag]");
  const assets = document.querySelectorAll("[data-asset]");
  const banner = document.querySelector("[data-release]");
  if (!needsTag.length && !assets.length && !banner && !proofTag) return;

  // Without a tag the asset links still have to go somewhere useful.
  for (const link of assets) link.href = `${RELEASES}/latest`;

  let release;
  try {
    const response = await fetch(
      `https://api.github.com/repos/${REPO}/releases/latest`,
      {
        headers: { Accept: "application/vnd.github+json" }
      }
    );
    if (!response.ok) throw new Error(String(response.status));
    release = await response.json();
  } catch {
    for (const el of needsTag) el.textContent = "see releases";
    return;
  }

  const tag = release.tag_name;
  if (!tag) return;

  for (const el of needsTag) el.textContent = tag;
  // The proof row's release tile is left hidden unless the tag is known: a
  // fallback like "see releases" reads as broken inside a tile.
  for (const el of document.querySelectorAll("[data-proof-tag]")) {
    el.textContent = tag;
    el.closest("[data-proof-release]")?.removeAttribute("hidden");
  }

  const sums = document.getElementById("sumsLink");
  if (sums) sums.href = `${RELEASES}/download/${tag}/checksums.txt`;

  for (const link of assets) {
    // The archive names carry the tag verbatim, `v` and all
    // (vrok_v0.1.0_darwin_arm64.tar.gz), so this must not strip the prefix.
    const file = link.dataset.asset.replace("{tag}", tag);
    link.href = `${RELEASES}/download/${tag}/${file}`;
    link.setAttribute("download", "");
    const label = link.querySelector(".slot__file");
    if (label) label.title = file;
  }

  if (!banner) return;
  const seenKey = `vrok-release-seen:${tag}`;
  try {
    if (localStorage.getItem(seenKey) === "1") return;
  } catch {
    // Private mode can throw; still show the banner.
  }

  const published = Date.parse(release.published_at || "");
  const fresh = Number.isFinite(published) && Date.now() - published < FRESH_MS;
  const mark = banner.querySelector("[data-release-fresh]");
  if (mark) mark.hidden = !fresh;
  banner.hidden = false;

  const hide = banner.querySelector("[data-release-dismiss]");
  if (hide) {
    hide.addEventListener("click", () => {
      banner.hidden = true;
      try {
        localStorage.setItem(seenKey, "1");
      } catch {
        /* ignore */
      }
    });
  }
}

/* ---------- proof ---------- */

// A star count only helps once it is big enough to read as interest; "3
// GitHub stars" argues against the project, so below this the homepage proof
// row stays hidden.
const STARS_WORTH_SHOWING = 25;
// Every page's header asks for the count, and unauthenticated GitHub API
// calls are capped at 60 an hour per visitor, so one answer serves a session.
const STARS_CACHE = "vrok-stars";
const STARS_CACHE_MS = 60 * 60 * 1000;

async function fetchStars() {
  try {
    const cached = JSON.parse(storeGet(sessionStorage, STARS_CACHE) || "null");
    if (cached && Date.now() - cached.at < STARS_CACHE_MS) return cached.stars;
  } catch {
    /* fall through to the network */
  }
  try {
    const response = await fetch(`https://api.github.com/repos/${REPO}`, {
      headers: { Accept: "application/vnd.github+json" }
    });
    if (!response.ok) return null;
    const stars = (await response.json()).stargazers_count;
    if (!Number.isFinite(stars)) return null;
    storeSet(
      sessionStorage,
      STARS_CACHE,
      JSON.stringify({ stars, at: Date.now() })
    );
    return stars;
  } catch {
    // Offline or rate-limited: every star count simply stays hidden.
    return null;
  }
}

async function wireStars() {
  const row = document.querySelector("[data-stars-row]");
  const count = document.querySelector("[data-stars]");
  const badges = document.querySelectorAll("[data-star-count]");
  if (!(row && count) && !badges.length) return;
  const stars = await fetchStars();
  if (stars === null) return;
  const label =
    stars >= 1000
      ? `${(stars / 1000).toFixed(1).replace(/\.0$/, "")}k`
      : String(stars);
  // The header badge is small, conventional and always shown; the proof
  // tile is a headline number, so it waits for the threshold.
  if (row && count && stars >= STARS_WORTH_SHOWING) {
    count.textContent = label;
    row.hidden = false;
  }
  for (const badge of badges) {
    badge.textContent = label;
    badge.hidden = false;
    badge.closest("a")?.setAttribute("aria-label", `GitHub, ${stars} stars`);
  }
}

/* ---------- star ask ---------- */

/* A plate in the corner asking for a GitHub star. It waits for a sign the
   visitor is interested: copying an install command, taking an archive, or
   reading most of a page for a while. An ask on arrival is noise. A star
   ends the ask for good; "Not now" holds it off for a month; and it shows
   at most once a session. Add ?star to the URL to force it while working on it. */

const STAR_DONE = "vrok-star:done";
const STAR_SNOOZE = "vrok-star:snooze";
const STAR_SESSION = "vrok-star:session";
const STAR_SNOOZE_MS = 30 * 24 * 60 * 60 * 1000;
const STAR_READ_MS = 40 * 1000;
const STAR_READ_DEPTH = 0.5;

function storeGet(store, key) {
  try {
    return store.getItem(key);
  } catch {
    return null;
  }
}
function storeSet(store, key, value) {
  try {
    store.setItem(key, value);
  } catch {
    /* private mode: the ask just may come back next session */
  }
}

function starAllowed() {
  if (new URLSearchParams(location.search).has("star")) return true;
  if (document.title.startsWith("Not found")) return false;
  if (storeGet(localStorage, STAR_DONE)) return false;
  if (storeGet(sessionStorage, STAR_SESSION)) return false;
  const snoozed = Number(storeGet(localStorage, STAR_SNOOZE));
  return !(snoozed && Date.now() - snoozed < STAR_SNOOZE_MS);
}

function buildStarAsk() {
  const ask = el("aside", "star-ask");
  ask.setAttribute("aria-labelledby", "star-ask-title");
  ask.innerHTML = `
    <span class="rivet rivet--tl" aria-hidden="true"></span>
    <span class="rivet rivet--tr" aria-hidden="true"></span>
    <button type="button" class="star-ask__close" data-star-close aria-label="Dismiss">×</button>
    <div class="star-ask__body" data-star-body>
      <p class="star-ask__eyebrow">Made by one person</p>
      <h2 class="star-ask__title" id="star-ask-title">Useful? Star it on GitHub.</h2>
      <p class="star-ask__text">No ads, no account, no telemetry. A star is the only way I find out vrok helped someone.</p>
      <div class="star-ask__actions">
        <a class="star-ask__star" data-star-go href="https://github.com/${REPO}" target="_blank" rel="noopener">
          <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path fill="currentColor" d="M8 .25a.75.75 0 0 1 .67.42l1.88 3.8 4.2.61a.75.75 0 0 1 .41 1.28l-3.04 2.96.72 4.18a.75.75 0 0 1-1.09.79L8 12.33l-3.75 1.97a.75.75 0 0 1-1.09-.8l.72-4.17L.84 6.37a.75.75 0 0 1 .41-1.28l4.2-.61L7.33.67A.75.75 0 0 1 8 .25Z"/></svg>
          Star on GitHub
        </a>
        <button type="button" class="star-ask__later" data-star-close>Not now</button>
      </div>
    </div>
    <p class="star-ask__thanks" data-star-thanks role="status" hidden>Thank you. That genuinely helps.</p>`;
  return ask;
}

function wireStarAsk() {
  if (!starAllowed()) return;
  const forced = new URLSearchParams(location.search).has("star");
  const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
  let ask = null;
  let shown = false;
  const cleanups = [];

  const close = (snooze, how) => {
    if (!ask) return;
    if (snooze) storeSet(localStorage, STAR_SNOOZE, String(Date.now()));
    if (how) track("star-ask-dismissed", { how });
    ask.dataset.state = "leaving";
    const node = ask;
    setTimeout(() => node.remove(), reduced ? 0 : 260);
    document.removeEventListener("keydown", onKey);
  };
  const onKey = (event) => {
    if (event.key === "Escape") close(true, "escape");
  };

  const show = (delay, trigger) => {
    if (shown) return;
    shown = true;
    for (const undo of cleanups) undo();
    setTimeout(() => {
      if (!forced && !starAllowed()) return;
      storeSet(sessionStorage, STAR_SESSION, "1");
      track("star-ask-shown", { trigger });
      ask = buildStarAsk();
      document.body.append(ask);
      // A frame between insert and state change so the entry transitions.
      requestAnimationFrame(() =>
        requestAnimationFrame(() => (ask.dataset.state = "open"))
      );
      for (const button of ask.querySelectorAll("[data-star-close]"))
        button.addEventListener("click", () =>
          close(
            true,
            button.classList.contains("star-ask__later") ? "not-now" : "close"
          )
        );
      ask.querySelector("[data-star-go]").addEventListener("click", () => {
        // GitHub has no link that stars a repo, so a click on the way there
        // is the closest honest signal. Take it as done.
        storeSet(localStorage, STAR_DONE, "1");
        track("star-ask-clicked");
        ask.querySelector("[data-star-body]").hidden = true;
        ask.querySelector("[data-star-close]").hidden = true;
        ask.querySelector("[data-star-thanks]").hidden = false;
        setTimeout(() => close(false), 2600);
      });
      document.addEventListener("keydown", onKey);
    }, delay);
  };

  if (forced) return show(600, "forced");

  // Interest signal 1: an install command copied. Wait for "Copied" to land.
  const onCopy = (event) => {
    if (event.target.closest("[data-copy]")) show(2600, "copy");
  };
  // Interest signal 2: an archive or release page taken.
  const onTake = (event) => {
    const link = event.target.closest("a[data-asset], a[href*='/releases']");
    if (link) show(1800, "download");
  };
  document.addEventListener("click", onCopy);
  document.addEventListener("click", onTake);
  cleanups.push(() => document.removeEventListener("click", onCopy));
  cleanups.push(() => document.removeEventListener("click", onTake));

  // Interest signal 3: real reading — time with the tab visible, plus depth.
  let readMs = 0;
  let deepest = 0;
  const depth = () => {
    const max = document.documentElement.scrollHeight - innerHeight;
    deepest = Math.max(deepest, max > 0 ? scrollY / max : 1);
  };
  const tick = setInterval(() => {
    if (document.visibilityState === "visible") readMs += 1000;
    depth();
    if (readMs >= STAR_READ_MS && deepest >= STAR_READ_DEPTH)
      show(0, "reading");
  }, 1000);
  addEventListener("scroll", depth, { passive: true });
  cleanups.push(() => clearInterval(tick));
  cleanups.push(() => removeEventListener("scroll", depth));
}

/* ---------- report a problem ---------- */

/* "Report a problem" opens GitHub's website-problem form with the page and
   browser already filled in. Nothing is sent anywhere: the visitor sees the
   prefilled form and decides whether to submit it. */

function reportHref(whatHappened) {
  const url = new URL(`https://github.com/${REPO}/issues/new`);
  url.searchParams.set("template", "website_problem.yml");
  url.searchParams.set(
    "page",
    location.origin + location.pathname + location.hash
  );
  url.searchParams.set("browser", navigator.userAgent.slice(0, 240));
  if (whatHappened) url.searchParams.set("what-happened", whatHappened);
  return url.href;
}

function wireReport() {
  for (const link of document.querySelectorAll("a[data-report]")) {
    link.href = reportHref(link.dataset.reportText || "");
  }
}

/* ---------- guide feedback ---------- */

/* One question at the end of each section. An answer is an Umami count, not
   a form: the point is to find the sections that do not work, and a "no"
   offers the prefilled report for anyone with more to say. Each section asks
   once per browser. */

const FEEDBACK_KEY = "vrok-feedback:";

// Thumb icons, drawn rather than emoji so they take the button's colour.
const THUMB =
  '<svg viewBox="0 0 16 16" width="15" height="15" aria-hidden="true"><path fill="currentColor" d="M8.8 1.1a1 1 0 0 1 1.4.3c.5.8.6 1.8.3 2.7L10 5.5h3a1.6 1.6 0 0 1 1.6 1.9l-1 5.3A1.6 1.6 0 0 1 12 14H6.2a1 1 0 0 1-1-1V6.6c0-.3.1-.5.3-.7l3.3-4.8ZM2 6.5h1.6a.6.6 0 0 1 .6.6v6.3a.6.6 0 0 1-.6.6H2a.6.6 0 0 1-.6-.6V7.1a.6.6 0 0 1 .6-.6Z"/></svg>';

function wireFeedback() {
  for (const section of document.querySelectorAll(".guide__body section[id]")) {
    const id = section.id;
    if (storeGet(localStorage, FEEDBACK_KEY + id)) continue;
    const heading = section.querySelector("h2")?.textContent.trim() || id;

    const box = el("div", "helpful");
    const question = el("p", "helpful__q", "Did this section help?");
    question.id = `helpful-${id}`;
    question.setAttribute("aria-live", "polite");
    const answers = el("div", "helpful__seg");
    answers.setAttribute("role", "group");
    answers.setAttribute("aria-labelledby", question.id);
    for (const [value, label] of [
      ["yes", "Yes"],
      ["no", "No"]
    ]) {
      const button = el("button", "helpful__btn");
      button.type = "button";
      button.dataset.helpful = value;
      button.setAttribute("aria-pressed", "false");
      button.innerHTML = THUMB;
      button.append(label);
      answers.append(button);
    }
    box.append(question, answers);

    answers.addEventListener("click", (event) => {
      const button = event.target.closest("[data-helpful]");
      if (!button || box.dataset.answered) return;
      const helpful = button.dataset.helpful;
      track("guide-feedback", { section: id, helpful });
      storeSet(localStorage, FEEDBACK_KEY + id, helpful);

      // The choice stays on screen, lit, so the click visibly landed.
      box.dataset.answered = helpful;
      button.setAttribute("aria-pressed", "true");
      for (const b of answers.querySelectorAll("button")) b.disabled = true;

      if (helpful === "yes") {
        question.textContent = "Thanks, glad it helped.";
        return;
      }
      question.textContent = "Thanks for saying so.";
      const more = el("p", "helpful__more");
      const link = el("a", null, "Tell me what was missing");
      link.href = reportHref(
        `The "${heading}" section did not answer my question.\n\nWhat I was trying to do:\n`
      );
      link.target = "_blank";
      link.rel = "noopener";
      more.append(link, " — it opens a short GitHub issue.");
      box.append(more);
    });

    section.append(box);
  }
}

/* ---------- changelog ---------- */

/* Release notes come from GitHub's API as HTML GitHub has already rendered
   and sanitised, so the page shows exactly what the release page shows.
   Without JavaScript, or when the API is unreachable, the page keeps its
   link to the releases on GitHub. */

async function wireChangelog() {
  const root = document.querySelector("[data-changelog]");
  if (!root) return;
  const status = root.querySelector("[data-changelog-status]");
  const fallback = status?.innerHTML;
  if (status) status.textContent = "Loading releases…";

  let releases;
  try {
    const response = await fetch(
      `https://api.github.com/repos/${REPO}/releases?per_page=30`,
      { headers: { Accept: "application/vnd.github.html+json" } }
    );
    if (!response.ok) throw new Error(String(response.status));
    releases = (await response.json()).filter((r) => !r.draft && !r.prerelease);
  } catch {
    if (status) status.innerHTML = fallback;
    return;
  }
  if (!releases.length) {
    if (status) status.innerHTML = fallback;
    return;
  }
  status?.remove();

  const day = new Intl.DateTimeFormat(undefined, { dateStyle: "long" });
  for (const [i, release] of releases.entries()) {
    const article = el("article", "rel");
    article.id = release.tag_name;

    const title = el("h2", "rel__tag", release.tag_name);
    if (i === 0) title.append(" ", el("span", "rel__latest", "Latest"));

    const meta = el("p", "rel__meta");
    const published = Date.parse(release.published_at || "");
    if (Number.isFinite(published)) meta.append(day.format(published), " · ");
    const onGitHub = el("a", null, "on GitHub");
    onGitHub.href = release.html_url;
    meta.append(onGitHub);

    const notes = el("div", "rel__notes");
    notes.innerHTML = release.body_html || "<p>No notes for this release.</p>";
    // GitHub sometimes wraps a heading in div.markdown-heading, so cut from
    // the wrapper when there is one.
    const heading = (text) => {
      const h = [...notes.querySelectorAll("h1, h2, h3")].find((x) =>
        text.test(x.textContent.trim())
      );
      return h && (h.closest(".markdown-heading") || h);
    };
    // GoReleaser opens every body with a "Changelog" heading, which only
    // repeats the page title.
    heading(/^changelog$/i)?.remove();
    // Every release ends with the same install instructions. Once per page
    // is plenty, and the download page has them already.
    const install = heading(/^install/i);
    if (install) {
      while (install.nextSibling) install.nextSibling.remove();
      install.remove();
    }
    for (const link of notes.querySelectorAll("a[href^='http']")) {
      link.rel = "noopener";
    }

    article.append(title, meta, notes);
    root.append(article);
  }

  // A link to #v0.5.2 arrives before the release it names exists.
  if (location.hash) {
    document
      .getElementById(decodeURIComponent(location.hash.slice(1)))
      ?.scrollIntoView();
  }
}

/* ---------- version examples ---------- */

/* The guide's version examples follow the releases instead of going stale:
   "newest" is the current release, and "rollback" the newest older release
   that still has `vrok update`, which is what someone stepping back from a
   bad release would install. The markup holds a working value for when
   GitHub cannot be reached. */

// Mirrors update.FirstWithUpdate in internal/update.
const FIRST_WITH_UPDATE = [0, 6, 0];

function versionParts(tag) {
  const m = /^v?(\d+)\.(\d+)\.(\d+)$/.exec(tag || "");
  return m ? m.slice(1).map(Number) : null;
}

function compareVersions(a, b) {
  for (let i = 0; i < 3; i++) if (a[i] !== b[i]) return a[i] < b[i] ? -1 : 1;
  return 0;
}

async function wireVersionExamples() {
  const slots = document.querySelectorAll("[data-example]");
  if (!slots.length) return;

  let tags;
  try {
    const response = await fetch(
      `https://api.github.com/repos/${REPO}/releases?per_page=20`,
      { headers: { Accept: "application/vnd.github+json" } }
    );
    if (!response.ok) return;
    tags = (await response.json())
      .filter((r) => !r.draft && !r.prerelease && versionParts(r.tag_name))
      .map((r) => r.tag_name)
      .sort((a, b) => compareVersions(versionParts(b), versionParts(a)));
  } catch {
    return;
  }
  if (!tags.length) return;

  const newest = tags[0];
  const rollback =
    tags
      .slice(1)
      .find((t) => compareVersions(versionParts(t), FIRST_WITH_UPDATE) >= 0) ||
    newest;

  for (const slot of slots) {
    slot.textContent = slot.dataset.example === "rollback" ? rollback : newest;
  }
}

/* ---------- guide contents ---------- */

/* The contents list marks the section being read. "Being read" is the last
   section whose heading has scrolled past a line a third of the way down
   the screen, which matches where the eye is rather than whatever happens
   to touch the top edge. At the very bottom the last section wins, since a
   short final section may never reach that line. */

function wireToc() {
  const links = [...document.querySelectorAll(".guide__toc a[href^='#']")];
  const pairs = links
    .map((link) => [link, document.getElementById(link.hash.slice(1))])
    .filter(([, section]) => section);
  if (!pairs.length) return;

  let current = null;
  const mark = (link) => {
    if (link === current) return;
    current?.removeAttribute("aria-current");
    link?.setAttribute("aria-current", "true");
    current = link;
  };

  // After a click the page glides past other sections; the clicked link
  // holds until that scroll settles, and the next scroll resumes tracking.
  let held = false;
  let settle;

  let queued = false;
  const update = () => {
    queued = false;
    const line = innerHeight / 3;
    let active = null;
    for (const [link, section] of pairs) {
      if (section.getBoundingClientRect().top <= line) active = link;
    }
    const atBottom =
      innerHeight + scrollY >= document.documentElement.scrollHeight - 2;
    mark(atBottom ? pairs[pairs.length - 1][0] : active);
  };
  const schedule = () => {
    if (held) {
      clearTimeout(settle);
      // Released without re-measuring: the clicked link stays marked until
      // the reader scrolls on their own, even where the page bottoms out
      // before its section reaches the line.
      settle = setTimeout(() => {
        held = false;
      }, 150);
      return;
    }
    if (queued) return;
    queued = true;
    requestAnimationFrame(update);
  };

  addEventListener("scroll", schedule, { passive: true });
  addEventListener("resize", schedule);
  // A click marks its target at once, instead of after the scroll settles.
  for (const [link] of pairs) {
    link.addEventListener("click", () => {
      mark(link);
      held = true;
      // If the page cannot scroll that far, no scroll event will release it.
      clearTimeout(settle);
      settle = setTimeout(() => {
        held = false;
      }, 1000);
    });
  }
  update();
}

function wireNav() {
  const bar = document.querySelector(".bar");
  const toggle = document.querySelector("[data-nav]");
  const links = document.querySelector(".bar__links");
  if (!bar || !toggle || !links) return;

  toggle.addEventListener("click", () => {
    const open = bar.dataset.open === "true";
    bar.dataset.open = String(!open);
    toggle.setAttribute("aria-expanded", String(!open));
  });

  for (const link of links.querySelectorAll("a")) {
    link.addEventListener("click", () => {
      bar.dataset.open = "false";
      toggle.setAttribute("aria-expanded", "false");
    });
  }

  const here = location.pathname.split("/").pop() || "index.html";
  const page = here === "" || here === "/" ? "index.html" : here;
  for (const link of links.querySelectorAll("a")) {
    const href = (link.getAttribute("href") || "").split("#")[0];
    if (!href || href.startsWith("http") || href === "./") continue;
    if (href === page) link.setAttribute("aria-current", "page");
  }
}

/* ---------- watch it run ---------- */

/* The whole round trip, replayed: your terminal and the other person's
   browser. Terminal lines are the strings vrok prints, and bytes(),
   duration() and elapsed() mirror internal/humanize so every number is
   formatted exactly as the CLI formats it. */

const KEYS =
  "c copy · q QR code · p add password · e change expiry · 1 one-time link · x stop";
const PUBLIC = "anyone with the link";
const TOKEN = "Xr4kQ9mT2vLp8sWnBcYd7A";

function bytes(n) {
  if (n < 1024) return `${Math.round(n)} B`;
  let value = n;
  let unit = "";
  for (const u of ["KB", "MB", "GB", "TB"]) {
    value /= 1024;
    unit = u;
    if (value < 1024) break;
  }
  return value < 10
    ? `${value.toFixed(1)} ${unit}`
    : `${Math.round(value)} ${unit}`;
}

// Mirrors humanize.Duration, used for countdowns and time left.
function duration(seconds) {
  const s = Math.round(seconds);
  if (s <= 0) return "expired";
  const d = Math.floor(s / 86400);
  const h = Math.floor(s / 3600) % 24;
  const m = Math.floor(s / 60) % 60;
  if (d) return h ? `${d}d ${h}h` : `${d}d`;
  if (h) return m ? `${h}h ${m}m` : `${h}h`;
  if (m) return `${m}m`;
  return `${s % 60}s`;
}

// Mirrors humanize.Elapsed, used for how long a transfer took.
function elapsed(seconds) {
  const s = Math.round(seconds);
  const h = Math.floor(s / 3600);
  const m = Math.floor(s / 60) % 60;
  if (h) return `${h}h ${m}m`;
  if (m) return `${m}m ${s % 60}s`;
  return `${s}s`;
}

function clock() {
  return new Date().toTimeString().slice(0, 8);
}

function el(tag, cls, ...children) {
  const node = document.createElement(tag);
  if (cls) node.className = cls;
  for (const child of children) if (child != null) node.append(child);
  return node;
}

/* Thrown to unwind a scene when another one starts. */
class Cancelled extends Error {}

function makeTimer(instant, alive) {
  return async (ms) => {
    if (!alive()) throw new Cancelled();
    if (!instant) await new Promise((r) => setTimeout(r, ms));
    if (!alive()) throw new Cancelled();
  };
}

/* Your terminal. */
function makeTerminal(root, parts, instant, wait) {
  let live = null;
  const add = (node) => {
    root.append(node);
    root.scrollTop = root.scrollHeight;
    return node;
  };

  const term = {
    clear() {
      root.textContent = "";
      live = null;
    },
    speed(on) {
      parts.speed.hidden = !on || instant;
    },
    async key(k) {
      if (instant) return;
      parts.key.hidden = true;
      parts.key.textContent = k;
      // Restart the keycap animation for a repeated key.
      void parts.key.offsetWidth;
      parts.key.hidden = false;
      await wait(450);
    },
    async type(target, line, text) {
      const cursor = line.appendChild(el("span", "term__cursor"));
      if (instant) {
        target.textContent = text;
      } else {
        await wait(380);
        for (const ch of text) {
          target.textContent += ch;
          await wait(45 + Math.random() * 70);
        }
        await wait(320);
      }
      cursor.remove();
    },
    // A command typed at the prompt, a character at a time.
    async command(text) {
      const line = add(el("p", "term__cmd", el("span", "term__prompt", "$")));
      await term.type(line.appendChild(el("span", "term__typed")), line, text);
    },
    // Text typed into a prompt vrok is showing.
    async answer(prompt, text) {
      const line = add(el("p", null, prompt));
      await term.type(line.appendChild(el("span", "term__typed")), line, text);
    },
    head(mark, markClass, label, name, gap) {
      add(
        el(
          "p",
          `term__head${gap ? " term__gap" : ""}`,
          el("span", markClass, mark),
          `${label} `,
          el("strong", null, name)
        )
      );
    },
    rows(pairs) {
      const dl = add(el("dl", "term__rows"));
      for (const [label, value, cls] of pairs) {
        dl.append(el("dt", null, label));
        const dd = dl.appendChild(el("dd", cls || null));
        dd.append(...(Array.isArray(value) ? value : [value]));
      }
    },
    dim(text, cls) {
      add(el("p", `term__dim${cls ? ` ${cls}` : ""}`, text));
    },
    // The one line vrok redraws in place while downloads run.
    live(text) {
      if (!live) live = add(el("p", "term__live"));
      live.textContent = text;
    },
    endLive() {
      live?.remove();
      live = null;
    }
  };
  return term;
}

/* The other person's browser. */
function makeBrowser(parts, instant, wait) {
  const { view, address, loading, shelf, cursor } = parts;
  const shelfName = shelf.querySelector("[data-shelf-name]");
  const shelfStatus = shelf.querySelector("[data-shelf-status]");
  const shelfBar = shelf.querySelector("[data-shelf-bar]");
  const shelfTrack = shelfBar.parentElement;

  const browser = {
    blank() {
      address.textContent = "";
      view.replaceChildren(el("div", "browser__blank", "New tab"));
      shelf.hidden = true;
      cursor.hidden = true;
    },
    // Paste a URL, or reload when url is omitted, and show the page.
    async open(page, url) {
      if (url != null) {
        address.textContent = "";
        await wait(500);
        address.textContent = url;
        await wait(450);
      }
      if (!instant) {
        loading.hidden = true;
        void loading.offsetWidth;
        loading.hidden = false;
      }
      view.replaceChildren(el("div", "browser__blank", " "));
      await wait(650);
      loading.hidden = true;
      view.replaceChildren(page);
    },
    // Move the pointer to an element and click it.
    async click(selector) {
      const target = view.querySelector(selector);
      if (!target) return target;
      const box = parts.frame.getBoundingClientRect();
      const r = target.getBoundingClientRect();
      const x = r.left - box.left + r.width * 0.55;
      const y = r.top - box.top + r.height * 0.55;
      if (cursor.hidden) {
        cursor.style.transform = `translate(${x + 60}px, ${y + 90}px)`;
        cursor.hidden = false;
        void cursor.offsetWidth;
      }
      cursor.style.transform = `translate(${x}px, ${y}px)`;
      await wait(950);
      target.classList.add("is-pressed");
      cursor.removeAttribute("data-click");
      void cursor.offsetWidth;
      cursor.setAttribute("data-click", "");
      await wait(220);
      target.classList.remove("is-pressed");
      return target;
    },
    hideCursor() {
      cursor.hidden = true;
    },
    // The browser's own download bar, fed the same numbers as the terminal.
    download(name, sent, total, rate) {
      shelf.hidden = false;
      shelfName.textContent = name;
      if (total > 0) {
        delete shelfTrack.dataset.unknown;
        shelfBar.style.width = `${(sent / total) * 100}%`;
        shelfStatus.textContent =
          sent >= total
            ? `${bytes(total)} · Done`
            : `${bytes(sent)} of ${bytes(total)} · ${duration((total - sent) / rate)} left`;
      } else if (rate > 0) {
        shelfTrack.dataset.unknown = "";
        shelfStatus.textContent = `${bytes(sent)} · ${bytes(rate)}/s`;
      } else {
        delete shelfTrack.dataset.unknown;
        shelfBar.style.width = "100%";
        shelfStatus.textContent = `${bytes(sent)} · Done`;
      }
    }
  };
  return browser;
}

/* ----- pages the visitor sees, trimmed from the real viewer ----- */

function vrokBar(...pills) {
  const brand = el("span", "mock__brand");
  brand.innerHTML =
    '<img src="assets/mark.svg?v=b" alt="" width="24" height="24" /> vrok';
  return el(
    "div",
    "mock__bar",
    brand,
    pills.length
      ? el("span", "mock__pills", ...pills.map((p) => el("span", null, p)))
      : null
  );
}

function filePage() {
  const title = el(
    "div",
    "mock__title",
    el("span", "mock__icon", "▶"),
    el(
      "div",
      null,
      el("strong", null, "launch-cut.mov"),
      el("span", null, "4.0 GB")
    )
  );
  const sum = el("p", "browser__sum", el("b", null, "SHA-256"));
  sum.append(
    "3a7bd3e2360a3d29eea436fcfb7e44c735d117c42d1c1835420b6b9942dd4f1b"
  );
  return el(
    "div",
    null,
    vrokBar(),
    el("div", "mock__file", title, el("span", "mock__dl", "Download")),
    sum,
    el(
      "div",
      "mock__stage",
      el(
        "div",
        "mock__player",
        el("span", "mock__play"),
        el("span", null, "Watch here, or download the original")
      )
    )
  );
}

function listingPage() {
  const rows = [
    ["📁 brand/", "—"],
    ["🖼 hero.png", "2.1 MB"],
    ["▶ promo.mp4", "412 MB"],
    ["📄 deck.pdf", "18 MB"],
    ["🖼 logo.svg", "6.0 KB"]
  ].map(([name, size]) =>
    el("li", null, el("span", null, name), el("span", null, size))
  );
  return el(
    "div",
    null,
    vrokBar(),
    el(
      "div",
      "browser__listing",
      el(
        "div",
        "browser__listing-head",
        el("strong", null, "assets/"),
        el("span", "mock__dl browser__zip", "Download all (.zip)")
      ),
      el("ul", null, ...rows)
    )
  );
}

function appPage() {
  return el(
    "div",
    "browser__app",
    el(
      "div",
      "browser__app-bar",
      "acme · dashboard",
      el("span", null, "● live reload")
    ),
    el(
      "div",
      "browser__app-body",
      el(
        "div",
        "browser__app-card",
        el("b", null, "Hello from localhost:3000"),
        "Your app, served from your laptop."
      ),
      el(
        "div",
        "browser__app-card",
        el("b", null, "Signups this week"),
        "1,284 · up 12%"
      )
    )
  );
}

function gatePage() {
  return el(
    "div",
    null,
    vrokBar(),
    el(
      "div",
      "browser__gate",
      el("strong", null, "🔒 Password required"),
      el("p", null, "Ask the person who sent you the link."),
      el("div", "browser__input"),
      el("span", "mock__dl browser__unlock", "Unlock")
    )
  );
}

function unreachablePage(host) {
  return el(
    "div",
    "browser__error",
    el("strong", null, "This site can’t be reached"),
    `${host} stopped answering.`
  );
}

/* ----- the scenes ----- */

function urlFor(slug) {
  return `https://${slug}.trycloudflare.com/s/${TOKEN}/`;
}

function banner(t, name, slug) {
  t.head("✓", "term__ok", "Sharing", name);
  t.rows([
    [
      "URL:",
      [urlFor(slug), el("span", "term__copied", "(copied to clipboard)")],
      "term__url"
    ]
  ]);
  t.dim(`Expires when stopped · ${PUBLIC}`);
  t.dim(KEYS, "term__gap-s");
}

/* Simulates the CLI's once-a-second redraw. Each frame is one second of the
   real transfer, so the figures are the ones a real run would print. */
async function transfer({ t, b, wait }, name, total, rate, known) {
  let sent = 0;
  let shown = 0;
  let seconds = 0;
  while (sent < total) {
    const sample = rate * (0.9 + Math.random() * 0.2);
    sent = Math.min(total, sent + sample);
    shown = shown ? 0.3 * sample + 0.7 * shown : sample;
    seconds += 1;
    const parts = known
      ? [
          `↓ ${bytes(sent)} / ${bytes(total)}`,
          `${Math.floor((sent * 100) / total)}%`,
          `${bytes(shown)}/s`
        ]
      : [`↓ ${bytes(sent)}`, `${bytes(shown)}/s`];
    if (known && total > sent)
      parts.push(`${duration((total - sent) / shown)} left`);
    t.live(parts.join(" · "));
    b.download(name, sent, known ? total : 0, sent < total ? shown : 0);
    await wait(60);
  }
  b.download(name, total, known ? total : 0, 0);
  await wait(500);
  t.endLive();
  t.dim(
    `  ↓ Sent ${bytes(total)} in ${elapsed(seconds)} · ${bytes(total / seconds)}/s average`
  );
}

const SCENES = {
  async file(ctx) {
    const { t, b, say, wait } = ctx;
    const slug = "quiet-harbor-lamps-tuesday";
    say("You share a 4 GB video with one command.");
    await t.command("vrok ./launch-cut.mov");
    await wait(450);
    banner(t, "launch-cut.mov", slug);
    say(
      "The URL is already on your clipboard. You send it; they paste it into their browser."
    );
    await wait(1200);
    await b.open(filePage(), urlFor(slug).slice(8));

    say(
      "They see the file page: a preview, the size, a SHA-256 fingerprint to check against, and one Download button."
    );
    await wait(2400);
    await b.click(".mock__dl");
    say(
      "They click Download. Their browser and your terminal show the same transfer."
    );
    t.speed(true);
    await transfer(ctx, "launch-cut.mov", 4 * 1024 ** 3, 38 * 1024 ** 2, true);
    t.speed(false);
    b.hideCursor();
    say("Done. They have the file, and your terminal keeps a summary.");
    await wait(2400);

    say("You press x. The URL stops working the moment the process exits.");
    await t.key("x");
    t.head("■", "term__stop", "Stopped sharing", "launch-cut.mov", true);
    t.rows([
      ["Downloads:", "1"],
      ["Transferred:", "4.0 GB"],
      ["Last access:", clock()]
    ]);
    await wait(900);
    await b.open(unreachablePage(`${slug}.trycloudflare.com`));
  },

  async app(ctx) {
    const { t, b, say, wait } = ctx;
    const slug = "amber-tiles-orbit-cedar";
    say("Your dev server is running on port 3000.");
    await t.command("vrok localhost:3000");
    await wait(450);
    banner(t, "http://localhost:3000", slug);
    say(
      "They open the link and see your running app, live from your laptop. Hot reload reaches them too."
    );
    await wait(900);
    await b.open(appPage(), urlFor(slug).slice(8));
    await wait(2400);

    say("You press p and set a password. Same URL, nothing restarts.");
    await t.key("p");
    // vrok reads the password without echo, so nothing appears as it is typed.
    await t.answer("Password: ", "");
    t.dim(`Expires when stopped · ${PUBLIC} · password required · same URL`);
    t.dim(KEYS);
    await wait(900);

    say("When they reload, the page asks for the password first.");
    await b.open(gatePage());
    await wait(900);
    const input = b.view.querySelector(".browser__input");
    for (let i = 0; i < 8; i++) {
      input.textContent += "•";
      await wait(90);
    }
    await b.click(".browser__unlock");
    await b.open(appPage());
    b.hideCursor();
    await wait(1600);

    say(
      "You press e and give the link two hours. It closes itself after that."
    );
    await t.key("e");
    await t.answer("New lifetime (30m, 2h, 1d, 0): ", "2h");
    t.dim(`Expires in 2h · ${PUBLIC} · password required · same URL`);
    t.dim(KEYS);
  },

  async folder(ctx) {
    const { t, b, say, wait } = ctx;
    const slug = "silver-docks-meadow-relay";
    say("You share a whole folder.");
    await t.command("vrok ./assets");
    await wait(450);
    banner(t, "assets", slug);
    say("They open the link and get a listing of the folder.");
    await wait(900);
    await b.open(listingPage(), urlFor(slug).slice(8));
    await wait(2000);

    say(
      "They click Download all. The zip is built while it is sent, so neither side knows its final size."
    );
    await b.click(".browser__zip");
    t.speed(true);
    await transfer(ctx, "assets.zip", 438 * 1024 ** 2, 41 * 1024 ** 2, false);
    t.speed(false);
    b.hideCursor();
    say("One zip, one download. Your terminal keeps the summary.");
    await wait(2200);

    say("You press 1: one more download, then the link closes.");
    await t.key("1");
    t.dim(`Expires when stopped · ${PUBLIC} · one download left · same URL`);
    t.dim(KEYS);
  }
};

const ORDER = ["file", "app", "folder"];

function wireWatch() {
  const section = document.querySelector(".watch");
  const root = section?.querySelector("[data-screen]");
  if (!section || !root) return;

  const tabs = [...section.querySelectorAll("[data-scene]")];
  const narration = section.querySelector("[data-narration]");
  const replay = section.querySelector("[data-replay]");
  const termParts = {
    speed: section.querySelector("[data-speed]"),
    key: section.querySelector("[data-key]")
  };
  const browserParts = {
    frame: section.querySelector(".browser"),
    view: section.querySelector("[data-view]"),
    address: section.querySelector("[data-address]"),
    loading: section.querySelector("[data-loading]"),
    shelf: section.querySelector("[data-shelf]"),
    cursor: section.querySelector("[data-cursor]")
  };
  const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;

  let runId = 0;
  let current = "file";
  // Scenes rotate on their own until the visitor picks one.
  let chosen = false;
  let visible = false;

  const select = (name) => {
    current = name;
    for (const tab of tabs) {
      const on = tab.dataset.scene === name;
      tab.setAttribute("aria-selected", String(on));
      tab.tabIndex = on ? 0 : -1;
    }
  };

  const waitVisible = () =>
    new Promise((resolve) => {
      const check = () => (visible ? resolve() : setTimeout(check, 400));
      check();
    });

  async function play(name) {
    const id = ++runId;
    const alive = () => id === runId;
    const wait = makeTimer(reduced, alive);
    select(name);
    replay.hidden = true;
    termParts.key.hidden = true;

    const t = makeTerminal(root, termParts, reduced, wait);
    const b = makeBrowser(browserParts, reduced, wait);
    b.view = browserParts.view;
    t.clear();
    t.speed(false);
    b.blank();
    const say = (text) => {
      narration.textContent = text;
    };

    try {
      await SCENES[name]({ t, b, say, wait });
    } catch (err) {
      if (err instanceof Cancelled) return;
      throw err;
    }
    if (!alive()) return;
    replay.hidden = false;

    if (chosen || reduced) return;
    await new Promise((r) => setTimeout(r, 4000));
    await waitVisible();
    if (!alive() || chosen) return;
    play(ORDER[(ORDER.indexOf(name) + 1) % ORDER.length]);
  }

  const choose = (name) => {
    chosen = true;
    // Narration is announced only once the visitor is driving; an
    // auto-rotating demo would otherwise talk over the rest of the page.
    narration.setAttribute("aria-live", "polite");
    track("demo-played", { scene: name });
    play(name);
  };

  narration.setAttribute("aria-live", "off");
  for (const [i, tab] of tabs.entries()) {
    tab.addEventListener("click", () => choose(tab.dataset.scene));
    tab.addEventListener("keydown", (event) => {
      const step = { ArrowDown: 1, ArrowRight: 1, ArrowUp: -1, ArrowLeft: -1 }[
        event.key
      ];
      if (!step) return;
      event.preventDefault();
      const next = tabs[(i + step + tabs.length) % tabs.length];
      next.focus();
      choose(next.dataset.scene);
    });
  }
  replay.addEventListener("click", () => choose(current));

  if (reduced || !("IntersectionObserver" in window)) {
    visible = true;
    play("file");
    return;
  }
  let started = false;
  new IntersectionObserver(
    (entries) => {
      visible = entries.some((e) => e.isIntersecting);
      if (visible && !started) {
        started = true;
        play("file");
      }
    },
    { threshold: 0.3 }
  ).observe(section.querySelector("#watch-stage"));
}

wireCounts();
wireCopy();
wireOS();
wireRelease();
wireNav();
wireToc();
wireWatch();
wireStars();
wireStarAsk();
wireReport();
wireFeedback();
wireChangelog();
wireVersionExamples();
