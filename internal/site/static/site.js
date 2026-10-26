// The site's only other script: the theme switch (without a page load;
// without JavaScript the same form posts to /theme), copy buttons and
// confirmations on destructive admin forms.
'use strict';

(function () {
  const root = document.documentElement;

  const themeForm = document.querySelector('form.theme-form');
  if (themeForm) {
    themeForm.addEventListener('submit', (ev) => {
      ev.preventDefault();
      const next = root.dataset.theme === 'light' ? 'dark' : 'light';
      root.dataset.theme = next;
      const secure = location.protocol === 'https:' ? '; Secure' : '';
      document.cookie = `theme=${next}; Path=/; Max-Age=31536000; SameSite=Lax${secure}`;
      themeForm.querySelector('input[name="theme"]').value = next === 'light' ? 'dark' : 'light';
      const button = themeForm.querySelector('button');
      button.textContent = next === 'light' ? 'Dark theme' : 'Light theme';
      button.setAttribute('aria-label', `Switch to the ${next === 'light' ? 'dark' : 'light'} theme`);
    });
  }

  for (const button of document.querySelectorAll('button[data-copy]')) {
    button.hidden = false;
    button.addEventListener('click', () => {
      const input = document.getElementById(button.dataset.copy);
      if (!input) return;
      const done = () => {
        const label = button.textContent;
        button.textContent = 'Copied';
        setTimeout(() => { button.textContent = label; }, 1500);
      };
      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(input.value).then(done, () => { input.select(); });
      } else {
        input.select();
        if (document.execCommand('copy')) done();
      }
    });
  }

  for (const form of document.querySelectorAll('form[data-confirm]')) {
    form.addEventListener('submit', (ev) => {
      if (!window.confirm(form.dataset.confirm)) ev.preventDefault();
    });
  }
}());
