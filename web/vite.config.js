import { svelte } from '@sveltejs/vite-plugin-svelte';
import { svelteTesting } from '@testing-library/svelte/vite';
import { defineConfig } from 'vite';

export default defineConfig({
  // Relative addresses, so the app works in any folder of a static host.
  base: './',
  plugins: [svelte(), svelteTesting()],
  test: { environment: 'jsdom' },
});
