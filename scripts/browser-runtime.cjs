const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

// Chromium uses NSS, whereas NODE_EXTRA_CA_CERTS only changes Node's TLS trust.
function prepareBrowserHome(home, ca = process.env.NODE_EXTRA_CA_CERTS) {
  fs.mkdirSync(home, { recursive: true, mode: 0o700 });
  if (!ca) return;
  const nss = path.join(home, '.pki', 'nssdb');
  fs.mkdirSync(nss, { recursive: true, mode: 0o700 });
  if (!fs.existsSync(path.join(nss, 'cert9.db'))) {
    execFileSync('certutil', ['-N', '-d', `sql:${nss}`, '--empty-password'], { stdio: 'ignore' });
  }
  execFileSync('certutil', ['-A', '-d', `sql:${nss}`, '-n', 'ARTEX recording proxy', '-t', 'C,,', '-i', ca], { stdio: 'ignore' });
}
module.exports = { prepareBrowserHome };
