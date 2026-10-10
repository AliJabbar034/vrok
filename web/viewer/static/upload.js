/* Sends files into a vrok receive share.

   Files go one at a time, in 8 MiB chunks: a Cloudflare tunnel refuses a
   request body over 100 MB, and a chunk that fails is retried on its own.
   After a dropped connection the page asks the server how much arrived and
   carries on from there, so a large upload from a phone survives a flaky
   network. */
"use strict";

(() => {
  const root = document.querySelector("[data-upload]");
  if (!root) return;

  const api = root.dataset.api;
  const offerApi = root.dataset.offer;
  const input = root.querySelector("[data-input]");
  const list = root.querySelector("[data-list]");
  const zone = root.querySelector("[data-zone]");
  const limit = root.querySelector("[data-left]");

  const CHUNK = 8 * 1024 * 1024;
  const RETRIES = 8;

  const queue = [];
  let running = false;
  let asking = 0;

  // Mirrors humanize.Bytes, so sizes read the same as in the terminal.
  function bytes(n) {
    if (n < 1024) return `${n} B`;
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

  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const backoff = (attempt) => Math.min(1000 * 2 ** attempt, 15000);

  function el(tag, cls, text) {
    const node = document.createElement(tag);
    if (cls) node.className = cls;
    if (text != null) node.textContent = text;
    return node;
  }

  /* One row in the list of files being sent. */
  function row(file) {
    const li = el("li", "upload");
    const name = el("span", "upload__name", file.name);
    const meta = el("span", "upload__meta", `${bytes(file.size)} · waiting`);
    const track = el("span", "upload__track");
    const bar = el("span", "upload__bar");
    track.append(bar);
    li.append(name, meta, track);
    list.append(li);

    let started = 0;
    let base = 0;
    return {
      begin(offset) {
        started = performance.now();
        base = offset;
      },
      progress(sent) {
        const pct = file.size ? Math.floor((sent / file.size) * 100) : 100;
        bar.style.transform = `scaleX(${pct / 100})`;
        const secs = (performance.now() - started) / 1000;
        const rate = secs > 0.5 ? (sent - base) / secs : 0;
        meta.textContent =
          `${bytes(sent)} of ${bytes(file.size)} · ${pct}%` +
          (rate > 0 ? ` · ${bytes(rate)}/s` : "");
      },
      status(text) {
        meta.textContent = text;
      },
      done() {
        li.classList.add("is-done");
        bar.style.transform = "scaleX(1)";
        meta.textContent = `${bytes(file.size)} · sent`;
      },
      fail(text) {
        li.classList.add("is-failed");
        meta.textContent = text;
      }
    };
  }

  /* Resolves with the status and parsed JSON body; rejects only when the
     request never got an answer. XHR rather than fetch, because only XHR
     reports upload progress. */
  function request(method, url, body, onProgress) {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open(method, url);
      xhr.setRequestHeader("X-Vrok-Upload", "1");
      if (typeof body === "string")
        xhr.setRequestHeader("Content-Type", "application/json");
      if (onProgress) xhr.upload.onprogress = (e) => onProgress(e.loaded);
      xhr.onload = () => {
        let data = null;
        try {
          data = JSON.parse(xhr.responseText);
        } catch {
          // A password page or an error page is HTML; the status says enough.
        }
        resolve({ status: xhr.status, data });
      };
      xhr.onerror = xhr.ontimeout = () => reject(new Error("network"));
      xhr.send(body ?? null);
    });
  }

  function refusal(r) {
    const known = {
      401: "This link needs its password. Reload the page and enter it.",
      403: "Reload this page and try again.",
      404: "This link is not accepting files any more.",
      410: "This link is not accepting more files.",
      413: "This file is larger than the link accepts.",
      507: "The receiving computer does not have enough free space for this file."
    };
    const err = new Error(
      r.data?.error || known[r.status] || "Something went wrong. Try again."
    );
    err.final = true;
    return err;
  }

  const hasOffset = (r) => r && r.data && typeof r.data.offset === "number";
  // A 5xx is worth retrying, except a full disk, which a retry cannot fix.
  const answered = (r) => r && (r.status < 500 || r.status === 507);

  /* Asks the receiver to accept a batch and waits for the answer. The
     receiver sees the names and sizes first; nothing is sent before a yes. */
  async function offer(batch) {
    const files = batch.map(({ file }) => ({
      name: file.name,
      size: file.size
    }));
    let r = null;
    for (let attempt = 0; ; attempt++) {
      try {
        r = await request("POST", offerApi, JSON.stringify({ files }));
      } catch {
        r = null;
      }
      if (answered(r)) break;
      if (attempt >= RETRIES) {
        if (r?.data?.error) throw refusal(r);
        throw new Error("Could not reach the receiving computer. Try again.");
      }
      await sleep(backoff(attempt));
    }
    if (r.status !== 201) throw refusal(r);

    const id = r.data.id;
    let state = r.data.state;
    let failures = 0;
    while (state === "pending") {
      try {
        r = await request("GET", `${offerApi}/${encodeURIComponent(id)}`);
      } catch {
        r = null;
      }
      if (r && r.status === 200) {
        state = r.data.state;
        failures = 0;
        continue;
      }
      if (r && r.status < 500) throw refusal(r);
      if (++failures > RETRIES)
        throw new Error("Lost touch with the receiving computer. Try again.");
      await sleep(backoff(failures));
    }
    if (state === "declined")
      throw new Error("The receiver declined these files.");
    if (state === "expired")
      throw new Error("The receiver did not answer in time. Try again.");
    return id;
  }

  async function send(file, ui, offerId) {
    let r = null;
    for (let attempt = 0; ; attempt++) {
      try {
        r = await request(
          "POST",
          api,
          JSON.stringify({ offer: offerId, name: file.name, size: file.size })
        );
      } catch {
        r = null;
      }
      if (answered(r)) break;
      if (attempt >= RETRIES)
        throw new Error("Could not reach the receiving computer. Try again.");
      ui.status("Connecting…");
      await sleep(backoff(attempt));
    }
    if (r.status !== 201) throw refusal(r);

    const url = `${api}/${encodeURIComponent(r.data.id)}`;
    let offset = 0;
    let failures = 0;
    ui.begin(0);

    while (offset < file.size) {
      const end = Math.min(offset + CHUNK, file.size);
      const from = offset;
      try {
        r = await request(
          "PUT",
          `${url}?offset=${from}`,
          file.slice(from, end),
          (loaded) => ui.progress(from + loaded)
        );
      } catch {
        r = null;
      }
      if (r && (r.status === 200 || r.status === 409) && hasOffset(r)) {
        offset = r.data.offset;
        failures = 0;
        ui.progress(offset);
        continue;
      }
      if (r && r.status < 500) throw refusal(r);

      if (++failures > RETRIES) {
        throw new Error(
          "The connection dropped and did not come back. Choose the file again to retry."
        );
      }
      ui.status("Connection lost. Retrying…");
      await sleep(backoff(failures));
      if (hasOffset(r)) {
        offset = r.data.offset;
        continue;
      }
      try {
        const s = await request("GET", url);
        if (s.status === 200 && hasOffset(s)) offset = s.data.offset;
        else if (s.status < 500) throw refusal(s);
      } catch (err) {
        if (err.final) throw err;
      }
      ui.begin(offset);
    }

    for (let attempt = 0; ; attempt++) {
      try {
        r = await request("POST", url);
      } catch {
        r = null;
      }
      if (r && r.status < 500) break;
      if (attempt >= RETRIES)
        throw new Error("Could not finish sending. Try again.");
      await sleep(backoff(attempt));
    }
    if (r.status !== 200) throw refusal(r);
    ui.done();
  }

  /* Counts down the files the link still accepts. Other people may be
     sending to the same link, so this is this page's view, and the server
     has the final word. */
  function counted() {
    if (!limit) return;
    const left = Math.max(Number(limit.dataset.left) - 1, 0);
    limit.dataset.left = String(left);
    limit.textContent =
      left === 0
        ? "This link has taken all the files it accepts."
        : `This link accepts ${left === 1 ? "one more file" : `${left} more files`}.`;
  }

  async function drain() {
    if (running) return;
    running = true;
    while (queue.length) {
      const { file, ui, offerId } = queue.shift();
      ui.status(`${bytes(file.size)} · starting`);
      try {
        await send(file, ui, offerId);
        counted();
      } catch (err) {
        ui.fail(err.message);
      }
    }
    running = false;
  }

  // Each choice of files is one question to the receiver. The files are
  // copied out before the first await, because the input is cleared after.
  async function add(files) {
    const batch = [...files].map((file) => ({ file, ui: row(file) }));
    if (!batch.length) return;
    for (const { file, ui } of batch)
      ui.status(`${bytes(file.size)} · waiting for the receiver to accept`);
    asking++;
    try {
      const offerId = await offer(batch);
      for (const item of batch) {
        item.ui.status(`${bytes(item.file.size)} · accepted`);
        queue.push({ ...item, offerId });
      }
      drain();
    } catch (err) {
      for (const { ui } of batch) ui.fail(err.message);
    } finally {
      asking--;
    }
  }

  input.addEventListener("change", () => {
    add(input.files);
    // Lets the same file be chosen again after a failure.
    input.value = "";
  });

  // Dropping anywhere on the page works; a near miss of the box should not
  // make the browser open the file instead.
  let depth = 0;
  document.addEventListener("dragenter", (e) => {
    e.preventDefault();
    depth++;
    zone.classList.add("is-over");
  });
  document.addEventListener("dragleave", () => {
    if (--depth <= 0) {
      depth = 0;
      zone.classList.remove("is-over");
    }
  });
  document.addEventListener("dragover", (e) => e.preventDefault());
  document.addEventListener("drop", (e) => {
    e.preventDefault();
    depth = 0;
    zone.classList.remove("is-over");
    if (e.dataTransfer?.files?.length) add(e.dataTransfer.files);
  });

  window.addEventListener("beforeunload", (e) => {
    if (running || asking) e.preventDefault();
  });
})();
