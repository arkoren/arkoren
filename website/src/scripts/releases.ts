/** Enhance the static release history with shareable module and version search. */
function setupReleaseHistory() {
  const archive = document.querySelector<HTMLElement>("[data-release-history]");
  const form = archive?.querySelector<HTMLFormElement>("[data-release-controls]");
  const search = form?.querySelector<HTMLInputElement>('[name="q"]');
  const module = form?.querySelector<HTMLSelectElement>('[name="module"]');
  const count = form?.querySelector<HTMLElement>("[data-release-count]");
  const reset = form?.querySelector<HTMLButtonElement>("[data-release-reset]");
  const empty = archive?.querySelector<HTMLElement>("[data-release-empty]");
  if (!archive || !form || !search || !module || !count || !reset || !empty) return;

  const entries = [...archive.querySelectorAll<HTMLElement>("[data-release-entry]")];
  const days = [...archive.querySelectorAll<HTMLElement>("[data-release-day]")];

  /** Restore controls from the URL and hide date groups without matching entries. */
  function renderHistory() {
    if (!search || !module || !count || !reset || !empty) return;
    const params = new URLSearchParams(location.search);
    search.value = params.get("q") ?? "";
    const selected = params.get("module") ?? "";
    module.value = [...module.options].some((option) => option.value === selected) ? selected : "";
    const terms = search.value.trim().toLowerCase().split(/\s+/).filter(Boolean);
    let matches = 0;
    for (const entry of entries) {
      const text = `${entry.dataset.module} ${entry.dataset.version}`.toLowerCase();
      const isMatch =
        (module.value === "" || entry.dataset.module === module.value) &&
        terms.every((term) => text.includes(term));
      entry.hidden = !isMatch;
      if (isMatch) matches++;
    }
    for (const day of days) {
      const visible = [...day.querySelectorAll<HTMLElement>("[data-release-entry]")].filter(
        (entry) => !entry.hidden,
      ).length;
      day.hidden = visible === 0;
      const dayCount = day.querySelector<HTMLElement>("[data-release-day-count]");
      if (dayCount)
        dayCount.textContent = `UTC · ${visible} ${visible === 1 ? "release" : "releases"}`;
    }
    count.textContent = `${matches} of ${entries.length} releases`;
    empty.hidden = matches !== 0 || entries.length === 0;
    reset.hidden = search.value === "" && module.value === "";
  }

  /** Keep edits reloadable without adding a browser-history entry per keystroke. */
  function commitControls(shouldPush = false) {
    if (!search || !module) return;
    const url = new URL(location.href);
    for (const [key, value] of [
      ["q", search.value],
      ["module", module.value],
    ]) {
      if (value !== "") url.searchParams.set(key, value);
      else url.searchParams.delete(key);
    }
    if (url.href !== location.href) {
      if (shouldPush) history.pushState(null, "", url);
      else history.replaceState(null, "", url);
    }
    renderHistory();
  }

  search.addEventListener("input", () => commitControls());
  search.addEventListener("search", () => commitControls());
  module.addEventListener("change", () => commitControls(true));
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    commitControls(true);
  });
  form.addEventListener("reset", (event) => {
    event.preventDefault();
    search.value = "";
    module.value = "";
    commitControls(true);
    search.focus();
  });
  window.addEventListener("popstate", renderHistory);
  window.addEventListener("pageshow", renderHistory);
  renderHistory();
  form.hidden = false;
}

setupReleaseHistory();
