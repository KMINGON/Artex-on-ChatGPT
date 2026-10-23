#!/usr/bin/env node
// Resolve the MCP and browser from the same pinned runtime; never download at run time.
const path = require('node:path');
if (!process.argv.includes('--cdp-endpoint')) {
  if (!process.argv.includes('--executable-path')) {
    process.argv.push('--executable-path', process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH || require('playwright').chromium.executablePath());
  }
  if (typeof process.getuid === 'function' && process.getuid() === 0 && !process.argv.includes('--no-sandbox')) process.argv.push('--no-sandbox');
}
const root = path.dirname(require.resolve('@playwright/mcp/package.json'));
require(path.join(root, 'cli.js'));
