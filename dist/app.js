'use strict';
const menuToggle = document.querySelector('.menu-toggle');
const navigation = document.querySelector('#navigation');
const mobileMedia = window.matchMedia('(max-width: 800px)');
function setMenu(open, returnFocus = false) {
  menuToggle.setAttribute('aria-expanded', String(open));
  navigation.hidden = mobileMedia.matches && !open;
  if (returnFocus) menuToggle.focus();
}
function resetMenu() { setMenu(false); }
menuToggle.addEventListener('click', () => setMenu(menuToggle.getAttribute('aria-expanded') !== 'true'));
document.querySelector('.header').addEventListener('click', event => { if (event.target.closest('a')) setMenu(false); });
document.addEventListener('keydown', event => { if (event.key === 'Escape' && menuToggle.getAttribute('aria-expanded') === 'true') setMenu(false, true); });
document.addEventListener('click', event => { if (!event.target.closest('.header')) setMenu(false); });
mobileMedia.addEventListener('change', resetMenu);
resetMenu();

// Only defer elements below the viewport; content remains visible without JS.
const motionPreference = window.matchMedia('(prefers-reduced-motion: reduce)');
if (!motionPreference.matches && 'IntersectionObserver' in window) {
  const reveals = document.querySelectorAll('.reveal');
  const revealObserver = new IntersectionObserver(entries => {
    entries.forEach(entry => {
      if (!entry.isIntersecting) return;
      entry.target.classList.remove('is-pending');
      revealObserver.unobserve(entry.target);
    });
  }, { threshold: 0.08 });
  reveals.forEach(element => {
    if (element.getBoundingClientRect().top >= window.innerHeight) {
      element.classList.add('is-pending');
      revealObserver.observe(element);
    }
  });
  motionPreference.addEventListener('change', event => {
    if (event.matches) {
      reveals.forEach(element => element.classList.remove('is-pending'));
      revealObserver.disconnect();
    }
  });
}
