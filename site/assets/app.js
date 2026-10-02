/* Four jobs: copy a command, preselect the visitor's OS, point the archive
   links at whatever the newest release actually is, and replay the CLI in
   the "Watch it run" section. Everything
   degrades to a working page with JavaScript off — the commands are in the
   markup and the archive links fall back to the release page. */

const REPO = "AliJabbar034/vrok";
const RELEASES = `https://github.com/${REPO}/releases`;

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
  const assets = document.querySelectorAll("[data-asset]");
  const banner = document.querySelector("[data-release]");
  if (!needsTag.length && !assets.length && !banner) return;

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
  return value < 10 ? `${value.toFixed(1)} ${unit}` : `${Math.round(value)} ${unit}`;
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
  brand.innerHTML = '<img src="assets/mark.svg?v=b" alt="" width="24" height="24" /> vrok';
  return el("div", "mock__bar", brand, pills.length ? el("span", "mock__pills", ...pills.map((p) => el("span", null, p))) : null);
}

function filePage() {
  const title = el(
    "div",
    "mock__title",
    el("span", "mock__icon", "▶"),
    el("div", null, el("strong", null, "launch-cut.mov"), el("span", null, "4.0 GB"))
  );
  const sum = el("p", "browser__sum", el("b", null, "SHA-256"));
  sum.append("3a7bd3e2360a3d29eea436fcfb7e44c735d117c42d1c1835420b6b9942dd4f1b");
  return el(
    "div",
    null,
    vrokBar(),
    el("div", "mock__file", title, el("span", "mock__dl", "Download")),
    sum,
    el(
      "div",
      "mock__stage",
      el("div", "mock__player", el("span", "mock__play"), el("span", null, "Watch here, or download the original"))
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
  ].map(([name, size]) => el("li", null, el("span", null, name), el("span", null, size)));
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
    el("div", "browser__app-bar", "acme · dashboard", el("span", null, "● live reload")),
    el(
      "div",
      "browser__app-body",
      el("div", "browser__app-card", el("b", null, "Hello from localhost:3000"), "Your app, served from your laptop."),
      el("div", "browser__app-card", el("b", null, "Signups this week"), "1,284 · up 12%")
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
  t.rows([["URL:", [urlFor(slug), el("span", "term__copied", "(copied to clipboard)")], "term__url"]]);
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
      ? [`↓ ${bytes(sent)} / ${bytes(total)}`, `${Math.floor((sent * 100) / total)}%`, `${bytes(shown)}/s`]
      : [`↓ ${bytes(sent)}`, `${bytes(shown)}/s`];
    if (known && total > sent) parts.push(`${duration((total - sent) / shown)} left`);
    t.live(parts.join(" · "));
    b.download(name, sent, known ? total : 0, sent < total ? shown : 0);
    await wait(60);
  }
  b.download(name, total, known ? total : 0, 0);
  await wait(500);
  t.endLive();
  t.dim(`  ↓ Sent ${bytes(total)} in ${elapsed(seconds)} · ${bytes(total / seconds)}/s average`);
}

const SCENES = {
  async file(ctx) {
    const { t, b, say, wait } = ctx;
    const slug = "quiet-harbor-lamps-tuesday";
    say("You share a 4 GB video with one command.");
    await t.command("vrok ./launch-cut.mov");
    await wait(450);
    banner(t, "launch-cut.mov", slug);
    say("The URL is already on your clipboard. You send it; they paste it into their browser.");
    await wait(1200);
    await b.open(filePage(), urlFor(slug).slice(8));

    say("They see the file page: a preview, the size, a SHA-256 fingerprint to check against, and one Download button.");
    await wait(2400);
    await b.click(".mock__dl");
    say("They click Download. Their browser and your terminal show the same transfer.");
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
    say("They open the link and see your running app, live from your laptop. Hot reload reaches them too.");
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

    say("You press e and give the link two hours. It closes itself after that.");
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

    say("They click Download all. The zip is built while it is sent, so neither side knows its final size.");
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
    play(name);
  };

  narration.setAttribute("aria-live", "off");
  for (const [i, tab] of tabs.entries()) {
    tab.addEventListener("click", () => choose(tab.dataset.scene));
    tab.addEventListener("keydown", (event) => {
      const step = { ArrowDown: 1, ArrowRight: 1, ArrowUp: -1, ArrowLeft: -1 }[event.key];
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

wireCopy();
wireOS();
wireRelease();
wireNav();
wireWatch();
