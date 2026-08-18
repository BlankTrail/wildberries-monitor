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
// What it does: swaps fragments the server renders, shows the fields that
// belong to whatever a form's picker is set to, opens and closes the settings
// dialog, follows a run over server-sent events, and streams a file upload
// without reading it into memory first.

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

    // A form whose picker decides which of its own fields apply. The groups
    // for the choices not made are hidden and their inputs disabled —
    // disabled because a hidden field still posts: pick a brand after typing
    // a list of articles and the articles would ride along, invisible on the
    // screen that sent them.
    //
    // data-switch names the field to follow, data-when lists the values a
    // group belongs to. Nothing here knows what a job or a rule is, so a new
    // kind needs no line in this file. Without the script every group stays
    // visible and the server reads only what the choice uses, so the form
    // still works — it is just longer.
    root.querySelectorAll("form[data-switch]").forEach((form) => {
      if (form.dataset.switchWired) return;
      form.dataset.switchWired = "1";
      const name = form.dataset.switch;
      const apply = () => {
        const picked =
          form.querySelector(`[name="${name}"]:checked`) ||
          form.querySelector(`select[name="${name}"], input[name="${name}"]`);
        const value = picked ? picked.value : "";
        form.querySelectorAll("[data-when]").forEach((group) => {
          const applies = group.dataset.when.split(" ").includes(value);
          group.hidden = !applies;
          group
            .querySelectorAll("input, select, textarea")
            .forEach((c) => (c.disabled = !applies));
        });
      };
      form.addEventListener("change", (ev) => {
        if (ev.target.name === name) apply();
      });
      apply();
    });

    // A filter form that reads rather than writes: its fields go into the
    // query string, so the resulting view has a URL a person can bookmark or
    // send to somebody, which a POST would take away.
    root.querySelectorAll("form[data-get-form]").forEach((form) => {
      if (form.dataset.wired) return;
      form.dataset.wired = "1";
      form.addEventListener("submit", (ev) => {
        ev.preventDefault();
        const q = new URLSearchParams(new FormData(form)).toString();
        swap(form.dataset.target || "#main", form.dataset.getForm + "?" + q);
      });
    });

    // The tabs are left alone deliberately. They used to be swapped into
    // #main, and every screen behind them renders a whole page — so the
    // header, the nav and the footer arrived inside the region below the
    // header, twice over. Swapping a page into a part of itself is the bug;
    // the fix is not a fragment endpoint per screen but letting a link be a
    // link. A local server costs nothing to reload, and the address bar,
    // the back button and a bookmarkable URL come back for free.
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
      if (ev.target === dlg) {
        dlg.close();
        return;
      }
      // And so does the close button. Delegated to the dialog rather than
      // bound to the button: the dialog is empty at start and its contents
      // arrive from the server every time it opens, so a listener bound at
      // load time is bound to a button that does not exist yet — which is
      // why the panel could only be closed by clicking beside it.
      if (ev.target.closest("[data-close-settings]")) {
        ev.preventDefault();
        dlg.close();
      }
    });
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
