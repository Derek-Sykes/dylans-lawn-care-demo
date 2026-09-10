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
