// Runs before the stylesheets apply, so a viewer who chose a theme never sees
// the other one flash first. Storage can be unavailable (a locked-down profile,
// a private window); the page then simply follows the system theme.
(function () {
  'use strict';
  var theme;
  try {
    theme = window.localStorage.getItem('cia-monitor-theme');
  } catch (error) {
    theme = null;
  }
  if (theme === 'light' || theme === 'dark') {
    document.documentElement.setAttribute('data-theme', theme);
  }
})();
