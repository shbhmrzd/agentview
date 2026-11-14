import {defineConfig} from '@playwright/test';

export default defineConfig({
  testDir: './tests/browser',
  timeout: 30000,
  workers: 1,
  retries: 0,
  reporter: [['list'], ['html', {outputFolder: 'test-results/browser-report', open: 'never'}]],
  outputDir: 'test-results/browser',
  use: {browserName: 'chromium', viewport: {width: 1440, height: 1000}, trace: 'retain-on-failure'},
});
