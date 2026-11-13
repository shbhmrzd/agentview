// Loaded before styles so a saved choice is applied before the first paint.
// Add future bundled themes here and provide the same tokens in themes.css.
(() => {
  const key = 'agentview-theme-v1';
  const themes = [{id:'light', label:'Light'}, {id:'dark', label:'Dark'}];
  const valid = value => themes.some(theme => theme.id === value);
  const system = window.matchMedia('(prefers-color-scheme: dark)');
  const read = () => {
    try { const value = localStorage.getItem(key); return valid(value) ? value : null; }
    catch { return null; }
  };
  let preference = read(), select;
  const apply = () => {
    const theme = preference || (system.matches ? 'dark' : 'light');
    document.documentElement.dataset.theme = theme;
    if (select) select.value = theme;
  };
  apply();
  system.addEventListener('change', () => { if (!preference) apply(); });
  window.addEventListener('storage', event => {
    if (event.key === key || event.key === null) { preference = read(); apply(); }
  });
  document.addEventListener('DOMContentLoaded', () => {
    select = document.getElementById('theme-select');
    if (!select) return;
    for (const theme of themes) {
      const option = document.createElement('option');
      option.value = theme.id; option.textContent = theme.label; select.append(option);
    }
    apply();
    select.addEventListener('change', () => {
      if (!valid(select.value)) return;
      preference = select.value;
      try { localStorage.setItem(key, preference); } catch { /* Keep the choice for this page. */ }
      apply();
    });
  }, {once:true});
})();
