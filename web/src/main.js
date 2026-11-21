import { mount } from 'svelte';
import App from './App.svelte';
import './app.css';

mount(App, { target: document.getElementById('app') });

// The service worker makes the app installable and available offline. It
// is left out during development, where it would serve stale files.
if (import.meta.env.PROD && 'serviceWorker' in navigator) {
  navigator.serviceWorker.register(new URL('sw.js', document.baseURI)).catch(() => {});
}
