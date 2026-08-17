// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Everything this interface needs from the browser, and no more.
//
// Spec section 7 names htmx. This is a deliberate departure, made for the
// same reason the XLSX writer and the windows-1251 table were written by
// hand: htmx is about fifty kilobytes of third-party code vendored into a
// public AGPL repository to replace the eighty lines below. EventSource and
// fetch are native, the swap is four lines, and a reader auditing what this
// page does to their machine can read all of it in a minute.
//
// What it does: swaps fragments the server renders, opens and closes the
// settings dialog, follows a run over server-sent events, and streams a file
// upload without reading it into memory first.

(() => {
  "use strict";

  // ---- fragment swapping -------------------------------------------------
  //
  // The server renders HTML; this replaces a region with what it sent. No
  // client-side templating, so what a person sees in "view source" is what
  // the server decided, and there is one place where markup is produced.

  async function swap(target, url, init) {
    const el = document.querySelector(target);
    if (!el) return;
    el.setAttribute("aria-busy", "true");
    try {
      const res = await fetch(url, init);
      const html = await res.text();
      if (!res.ok) {
        el.innerHTML = `<div class="bt-alert bt-alert--error">${escapeHTML(html || res.statusText)}</div>`;
        return;
      }
      el.innerHTML = html;
      wire(el);
    } catch (err) {
      el.innerHTML = `<div class="bt-alert bt-alert--error">Сервер не отвечает: ${escapeHTML(String(err))}</div>`;
    } finally {
      el.removeAttribute("aria-busy");
    }
  }

  function escapeHTML(s) {
    const d = document.createElement("div");
    d.textContent = s;
    return d.innerHTML;
  }

  // ---- declarative wiring ------------------------------------------------
  //
  // Behaviour is declared on the element rather than bound by id in script,
  // so that a template author adds a working control without touching this
  // file — the one property that made htmx worth considering.

  let estimateTimer = null;

  function wire(root) {
    root.querySelectorAll("[data-get]").forEach((el) => {
      if (el.dataset.wired) return;
      el.dataset.wired = "1";
      el.addEventListener("click", (ev) => {
        ev.preventDefault();
        swap(el.dataset.target || "#main", el.dataset.get);
      });
    });

    root.querySelectorAll("form[data-post]").forEach((form) => {
      if (form.dataset.wired) return;
      form.dataset.wired = "1";
      form.addEventListener("submit", async (ev) => {
        ev.preventDefault();
        // FormData over a file input streams the file rather than reading it
        // in: a hundred thousand phrases must not have to fit in the tab's
        // memory before the upload starts.
        await swap(form.dataset.target || "#main", form.dataset.post, {
          method: "POST",
          body: new FormData(form),
        });
      });
    });

    // A button that posts the form it sits in without submitting it — the
    // cost estimate, which must not save anything.
    root.querySelectorAll("[data-post-form]").forEach((el) => {
      if (el.dataset.wired) return;
      el.dataset.wired = "1";
      el.addEventListener("click", (ev) => {
        ev.preventDefault();
        const form = el.closest("form");
        if (!form) return;
        swap(el.dataset.target || "#main", el.dataset.postForm, {
          method: "POST",
          body: new FormData(form),
        });
      });
    });

    // The file upload. The body is the File object itself, not its contents:
    // the browser streams it off disk, so a hundred-megabyte phrase list
    // never has to fit in the tab's memory — which is the whole point.
    root.querySelectorAll("[data-upload]").forEach((el) => {
      if (el.dataset.wired) return;
      el.dataset.wired = "1";
      el.addEventListener("click", async (ev) => {
        ev.preventDefault();
        const input = document.querySelector(el.dataset.file);
        if (!input || !input.files || !input.files.length) return;
        const body = new FormData();
        body.append("name", input.files[0].name);
        body.append("file", input.files[0]);
        await swap(el.dataset.target || "#main", el.dataset.upload, {
          method: "POST",
          body,
        });
      });
    });

    // Anything that changes the price re-asks for the price. Debounced,
    // because a person types a page count digit by digit and each keystroke
    // would otherwise be a request.
    root.querySelectorAll("[data-estimate]").forEach((el) => {
      if (el.dataset.wired) return;
      el.dataset.wired = "1";
      el.addEventListener("input", () => {
        const form = el.closest("form");
        if (!form) return;
        clearTimeout(estimateTimer);
        estimateTimer = setTimeout(() => {
          swap("#estimate", "/jobs/estimate", {
            method: "POST",
            body: new FormData(form),
          });
        }, 300);
      });
    });

    root.querySelectorAll("[data-tab]").forEach((el) => {
      if (el.dataset.wired) return;
      el.dataset.wired = "1";
      el.addEventListener("click", (ev) => {
        ev.preventDefault();
        document.querySelectorAll("[data-tab]").forEach((t) =>
          t.classList.toggle("bt-filter-tab--active", t === el)
        );
        swap("#main", el.dataset.tab);
      });
    });
  }

  // ---- settings dialog ---------------------------------------------------
  //
  // A native <dialog>: focus trapping, Escape and the backdrop come from the
  // browser rather than from code here that would get them subtly wrong.

  function wireDialog() {
    const dlg = document.getElementById("settings-dialog");
    if (!dlg) return;
    document.querySelectorAll("[data-open-settings]").forEach((el) =>
      el.addEventListener("click", async (ev) => {
        ev.preventDefault();
        await swap("#settings-body", "/settings");
        dlg.showModal();
      })
    );
    dlg.addEventListener("click", (ev) => {
      // Clicking the backdrop closes it. The dialog element itself is the
      // event target only when the click landed outside the panel.
      if (ev.target === dlg) dlg.close();
    });
    document.querySelectorAll("[data-close-settings]").forEach((el) =>
      el.addEventListener("click", (ev) => {
        ev.preventDefault();
        dlg.close();
      })
    );
  }

  // ---- live run ----------------------------------------------------------
  //
  // Server-sent events, not polling. The connection is closed when the run
  // ends and when the page goes away: a subscription nobody closed is a
  // subscriber the bus keeps feeding forever, and every reload would add one.

  let live = null;

  function follow(runID) {
    stopFollowing();
    live = new EventSource(`/live?run=${encodeURIComponent(runID)}`);
    live.addEventListener("progress", (ev) => {
      const el = document.querySelector("#run-progress");
      if (el) el.innerHTML = ev.data;
    });
    live.addEventListener("log", (ev) => {
      const el = document.querySelector("#run-log");
      if (!el) return;
      const line = document.createElement("div");
      line.className = "bt-code";
      line.textContent = ev.data;
      el.prepend(line);
      // Bounded on purpose: a run of a million items would otherwise grow the
      // page until the tab dies, which is a worse failure than losing the
      // oldest lines of a log nobody scrolls back through.
      while (el.childElementCount > 200) el.lastElementChild.remove();
    });
    live.addEventListener("done", () => stopFollowing());
    live.onerror = () => stopFollowing();
  }

  function stopFollowing() {
    if (live) {
      live.close();
      live = null;
    }
  }

  window.addEventListener("pagehide", stopFollowing);

  // ---- start -------------------------------------------------------------

  document.addEventListener("DOMContentLoaded", () => {
    wire(document);
    wireDialog();
    const run = document.body.dataset.followRun;
    if (run) follow(run);
  });

  // Exposed so a rendered fragment can start following a run it just began.
  window.btFollow = follow;
})();
