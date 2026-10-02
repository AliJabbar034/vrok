/* Three jobs: copy a command, preselect the visitor's OS, and point the
   archive links at whatever the newest release actually is. Everything
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

async function wireRelease() {
  const needsTag = document.querySelectorAll("[data-latest-tag]");
  const assets = document.querySelectorAll("[data-asset]");
  if (!needsTag.length && !assets.length) return;

  // Without a tag the asset links still have to go somewhere useful.
  for (const link of assets) link.href = `${RELEASES}/latest`;

  let tag;
  try {
    const response = await fetch(
      `https://api.github.com/repos/${REPO}/releases/latest`,
      {
        headers: { Accept: "application/vnd.github+json" }
      }
    );
    if (!response.ok) throw new Error(String(response.status));
    tag = (await response.json()).tag_name;
  } catch {
    for (const el of needsTag) el.textContent = "see releases";
    return;
  }
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

wireCopy();
wireOS();
wireRelease();
wireNav();
